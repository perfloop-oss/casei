//go:build amd64

package casei

import "unsafe"

func (f tripleBucketFilter) usable() bool {
	return len(f) >= tripleBucketFilterBytes && f[tripleBucketTableBytes] != 0 &&
		f[tripleBucketFilterBytes-1] == tripleBucketValidMarker
}

func (f tripleBucketFilter) prefixCount() int {
	if !f.usable() {
		return 0
	}
	return int(f[tripleBucketTableBytes])
}

func (p *searchPlan) tripleBucketFilter() tripleBucketFilter {
	if len(p.tripleRoots) < tripleBucketFilterBytes || p.tripleRoots[tripleBucketFilterBytes-1] != tripleBucketValidMarker {
		return nil
	}
	return tripleBucketFilter(p.tripleRoots)
}

// tripleBucketPrefix records one bounded ASCII path from the plan trie.
type tripleBucketPrefix struct {
	tokens [tripleBucketPrefixBytes]uint32
	length uint8
}

func (p *searchPlan) hasTripleBucketASCIIToken(token uint32) bool {
	for _, got := range p.ascii {
		if got == token {
			return true
		}
	}
	return false
}

func addTripleBucketPrefix(prefixes *[tripleShuftiSlots]tripleBucketPrefix, n *int, tokens [tripleBucketPrefixBytes]uint32, length uint8) bool {
	candidate := tripleBucketPrefix{tokens: tokens, length: length}
	for i := 0; i < *n; i++ {
		if prefixes[i] == candidate {
			return true
		}
	}
	if *n == len(prefixes) {
		return false
	}
	prefixes[*n] = candidate
	(*n)++
	return true
}

// makeTripleBucketFilter compiles ASCII-only prefixes for plans whose complete
// triple union already has a Shufti projection for high-byte blocks. Each bucket
// bit names one trie prefix. A three-unit terminal leaves the fourth byte
// unconstrained; any live fourth-unit prefix is retained even when later units
// require Unicode. The block kernel uses this table only when the four
// overlapping input vectors are all ASCII; other blocks use the Shufti
// projection. The shared plan remains the only match authority.
func (p *searchPlan) makeTripleBucketFilter() bool {
	if p.patternCount <= 1 || p.rootKind != rootGeneric || p.rawByteMulti.usable() ||
		!p.triples.shufti.usable() || !asciiPairVBMIEnabled() {
		return false
	}

	var prefixes [tripleShuftiSlots]tripleBucketPrefix
	n := 0
	var path [tripleBucketPrefixBytes]uint32
	for token0, state0 := range p.nodes[0].edges {
		if !p.hasTripleBucketASCIIToken(token0) {
			continue
		}
		path[0] = token0
		for token1, state1 := range p.nodes[state0].edges {
			if !p.hasTripleBucketASCIIToken(token1) {
				continue
			}
			path[1] = token1
			for token2, state2 := range p.nodes[state1].edges {
				if !p.hasTripleBucketASCIIToken(token2) {
					continue
				}
				path[2], path[3] = token2, 0
				node2 := &p.nodes[state2]
				if node2.output.pattern >= 0 && node2.output.units == 3 {
					if !addTripleBucketPrefix(&prefixes, &n, path, 3) {
						return false
					}
				}
				for token3, state3 := range node2.edges {
					if !p.hasTripleBucketASCIIToken(token3) {
						continue
					}
					node3 := &p.nodes[state3]
					if (node3.output.pattern >= 0 && node3.output.units == 4) || len(node3.edges) != 0 {
						path[3] = token3
						if !addTripleBucketPrefix(&prefixes, &n, path, 4) {
							return false
						}
					}
				}
			}
		}
	}
	if n == 0 {
		return false
	}

	storage := make([]byte, tripleBucketFilterBytes)
	out := tripleBucketFilter(storage)
	for slot := 0; slot < n; slot++ {
		bit := byte(1 << uint(slot))
		for position := 0; position < int(prefixes[slot].length); position++ {
			for value, token := range p.ascii {
				if token == prefixes[slot].tokens[position] {
					out[position*128+value] |= bit
				}
			}
		}
		if prefixes[slot].length == 3 {
			for value := range 128 {
				out[3*128+value] |= bit
			}
		}
	}
	out[tripleBucketTableBytes] = byte(n)
	out[tripleBucketFilterBytes-1] = tripleBucketValidMarker
	p.tripleRoots = storage
	return true
}

// tripleBucketSkipBytes uses the compiled ASCII prefix buckets only for blocks
// whose four overlapping loads prove every byte ASCII. A high byte sends that
// 64-start block through the existing conservative Shufti filter; the scalar
// tail is unchanged.
func tripleBucketSkipBytes(s string, at int, bucket tripleBucketFilter, shufti *tripleShuftiFilter) int {
	if !bucket.usable() || !shufti.usable() || !asciiPairVBMIEnabled() {
		return tripleShuftiSkipBytes(s, at, shufti)
	}
	start := at
	remaining := len(s) - at
	if remaining >= 67 {
		full := ((remaining - 3) / 64) * 64
		ptr := (*byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), at))
		skipped := tripleBucketSkip64(ptr, remaining, unsafe.SliceData(bucket[:tripleBucketTableBytes]), shufti)
		at += skipped
		if skipped < full {
			return at - start
		}
	}
	return at - start + tripleShuftiSkipBytes(s, at, shufti)
}
