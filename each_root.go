package casei

import (
	"encoding/binary"
	"unicode/utf8"
)

const (
	// rootBucketEachMinBytes is the first length where the existing four-byte
	// bucket kernel can examine a full 64-start block (the last prefix uses bytes
	// 63 through 66). Shorter input keeps the ordinary Each route.
	rootBucketEachMinBytes = 64 + 3

	rootASCIIWordMaxUnits    = 24
	rootASCIIWordMaxWords    = (rootASCIIWordMaxUnits + 7) / 8
	rootASCIIWordMaxPatterns = 5
)

const (
	rootASCIIHighBits        uint64 = 0x8080808080808080
	rootASCIIWordHeaderBytes        = 2
	rootASCIIWordEntryBytes         = 1 + rootASCIIWordMaxWords*2*8
)

type rootASCIIWordCert struct {
	units uint8
	words [rootASCIIWordMaxWords]uint64
	fold  [rootASCIIWordMaxWords]uint64
}

type rootASCIIWordCerts struct {
	maxUnits uint8
	count    uint8
	entries  [rootASCIIWordMaxPatterns]rootASCIIWordCert
}

func rootASCIIWordCertBytes(count uint8) int {
	return rootASCIIWordHeaderBytes + int(count)*rootASCIIWordEntryBytes
}

// write stores the bounded certificates after the existing bucket table.
func (c rootASCIIWordCerts) write(dst []byte) {
	dst[0], dst[1] = c.maxUnits, c.count
	at := rootASCIIWordHeaderBytes
	for i := 0; i < int(c.count); i++ {
		cert := &c.entries[i]
		dst[at] = cert.units
		at++
		for _, word := range cert.words {
			binary.LittleEndian.PutUint64(dst[at:at+8], word)
			at += 8
		}
		for _, word := range cert.fold {
			binary.LittleEndian.PutUint64(dst[at:at+8], word)
			at += 8
		}
	}
}

// makeRootASCIIWordCerts compiles every pattern token into its complete ASCII
// fold class, retaining entries in pattern-ID order. A token with no exact ASCII
// representation makes this plan use the trie for all candidates; the bounded
// representation is only for short words.
func makeRootASCIIWordCerts(p *searchPlan, patterns []string) (rootASCIIWordCerts, bool) {
	var certs rootASCIIWordCerts
	if p.patternCount < 4 || p.patternCount > rootASCIIWordMaxPatterns || len(patterns) != p.patternCount ||
		p.empty >= 0 || p.maxUnits == 0 || p.maxUnits > rootASCIIWordMaxUnits {
		return certs, false
	}

	certs.maxUnits = uint8(p.maxUnits)
	for patternID, pattern := range patterns {
		if pattern == "" {
			return rootASCIIWordCerts{}, false
		}
		cert := &certs.entries[patternID]
		units := 0
		for at := 0; at < len(pattern); units++ {
			r, size := utf8.DecodeRuneInString(pattern[at:])
			var token uint32
			if r == utf8.RuneError && size == 1 {
				token = p.opaque[pattern[at]]
			} else {
				token = p.runes[r]
			}
			if token == 0 {
				return rootASCIIWordCerts{}, false
			}
			kind, value := p.asciiTokenKind(token)
			if kind == rootGeneric {
				return rootASCIIWordCerts{}, false
			}
			word, shift := units/8, uint((units%8)*8)
			cert.words[word] |= uint64(value) << shift
			if kind == rootASCIIFold {
				cert.fold[word] |= uint64(0x20) << shift
			}
			at += size
		}
		if units == 0 || units > rootASCIIWordMaxUnits || units > p.maxUnits {
			return rootASCIIWordCerts{}, false
		}
		cert.units = uint8(units)
	}
	certs.count = uint8(len(patterns))
	return certs, true
}

// rootASCIIWordCertData returns the optional plan-owned tail after the verified
// bucket table.
func rootASCIIWordCertData(bucket tripleBucketFilter) []byte {
	if len(bucket) < tripleBucketFilterBytes+rootASCIIWordHeaderBytes ||
		bucket[tripleBucketTableBytes] == 0 || bucket[tripleBucketFilterBytes-1] != tripleBucketValidMarker {
		return nil
	}
	data := bucket[tripleBucketFilterBytes:]
	count := data[1]
	if count < 4 || count > rootASCIIWordMaxPatterns || data[0] == 0 ||
		data[0] > rootASCIIWordMaxUnits || len(data) < rootASCIIWordCertBytes(count) {
		return nil
	}
	return data[:rootASCIIWordCertBytes(count)]
}

// matchRootASCIIWordCertData checks the complete maxUnits-byte prefix before
// using ASCII words. known is false when the prefix is unknown or incomplete
// and needs trie replay; otherwise ok and match contain the ordered result.
func matchRootASCIIWordCertData(data []byte, haystack string, start int) (match Match, width int, ok, known bool) {
	if len(data) < rootASCIIWordHeaderBytes {
		return Match{}, 0, false, false
	}
	maxUnits, count := int(data[0]), int(data[1])
	if maxUnits == 0 || maxUnits > rootASCIIWordMaxUnits || count < 4 || count > rootASCIIWordMaxPatterns ||
		len(data) < rootASCIIWordCertBytes(uint8(count)) || start < 0 || start > len(haystack) ||
		len(haystack)-start < maxUnits {
		return Match{}, 0, false, false
	}

	var words [rootASCIIWordMaxWords]uint64
	fullWords := maxUnits / 8
	for word := 0; word < fullWords; word++ {
		at := start + word*8
		value := binary.LittleEndian.Uint64([]byte(haystack[at : at+8]))
		if value&rootASCIIHighBits != 0 {
			return Match{}, 0, false, false
		}
		words[word] = value
	}
	for unit := fullWords * 8; unit < maxUnits; unit++ {
		value := haystack[start+unit]
		if value >= utf8.RuneSelf {
			return Match{}, 0, false, false
		}
		words[unit/8] |= uint64(value) << uint((unit%8)*8)
	}

	entryAt := rootASCIIWordHeaderBytes
	for patternID := 0; patternID < count; patternID++ {
		units := int(data[entryAt])
		entryAt++
		if units == 0 || units > maxUnits {
			return Match{}, 0, false, false
		}
		wordsAt := entryAt
		foldAt := wordsAt + rootASCIIWordMaxWords*8
		entryAt = foldAt + rootASCIIWordMaxWords*8
		wordCount := (units + 7) / 8
		matched := true
		for word := 0; word < wordCount; word++ {
			valueAt := wordsAt + word*8
			foldWordAt := foldAt + word*8
			value := words[word] | binary.LittleEndian.Uint64(data[foldWordAt:foldWordAt+8])
			if word == wordCount-1 && units%8 != 0 {
				validBits := uint(units%8) * 8
				value &= (uint64(1) << validBits) - 1
			}
			if value != binary.LittleEndian.Uint64(data[valueAt:valueAt+8]) {
				matched = false
				break
			}
		}
		if matched {
			return Match{Pattern: patternID, Start: start}, units, true, true
		}
	}
	return Match{}, 0, false, true
}

// rootBucketEachFilter limits the alternate iterator to complete generic
// three-to-five-literal root plans with conservative Shufti coverage and a
// compiled bucket. Covered roots make p.filter unusable for this shape; the
// existing bucket/Shufti wrapper is the active candidate screen.
func (p *searchPlan) rootBucketEachFilter(haystack string) (tripleBucketFilter, bool) {
	if p.empty >= 0 || p.patternCount < 3 || p.patternCount > 5 || p.opaqueContinuation ||
		p.rootKind != rootGeneric || p.rawByteMulti.usable() || !p.triplesComplete ||
		!p.triples.shufti.usable() || !asciiPairVBMIEnabled() || len(haystack) < rootBucketEachMinBytes {
		return nil, false
	}
	bucket := p.tripleBucketFilter()
	return bucket, bucket.usable()
}

// matchAtStart replays one byte-boundary candidate through the shared trie. It
// considers every terminal at exactly start and chooses the lowest pattern ID,
// retaining the consumed source width of that pattern.
func (p *searchPlan) matchAtStart(haystack string, start int) (Match, int, bool) {
	if start < 0 || start >= len(haystack) {
		return Match{}, 0, false
	}
	state, at := 0, start
	best := Match{Pattern: -1, Start: -1}
	bestWidth := 0
	for units := 1; units <= p.maxUnits && at < len(haystack); units++ {
		token, size := p.haystackToken(haystack, at)
		if token == 0 {
			break
		}
		next, ok := p.nodes[state].edges[token]
		if !ok {
			break
		}
		state = next
		at += size
		// finish propagates shorter failure-link outputs into each node. An
		// output whose unit count equals the anchored prefix length belongs to
		// this start; shorter suffix outputs begin after it and must not win.
		if output := p.nodes[state].output; output.pattern >= 0 && output.units == units &&
			(best.Pattern < 0 || output.pattern < best.Pattern) {
			best = Match{Pattern: output.pattern, Start: start}
			bestWidth = at - start
		}
	}
	if best.Pattern < 0 {
		return Match{}, 0, false
	}
	return best, bestWidth, true
}

func (p *searchPlan) matchRootBucketCandidate(haystack string, start int, bucket tripleBucketFilter) (Match, int, bool) {
	if data := rootASCIIWordCertData(bucket); data != nil {
		match, width, ok, known := matchRootASCIIWordCertData(data, haystack, start)
		if known {
			return match, width, ok
		}
	}
	return p.matchAtStart(haystack, start)
}

// eachRootBucket enumerates candidate starts in source order through the
// complete plan's existing root-triple filter. A complete ASCII word
// certificate confirms bounded ASCII prefixes in pattern-ID order; unknown or
// incomplete prefixes retain the exact trie replay. The bucket or Shufti
// survivor is still only a nomination. The chosen source width owns non-overlap
// advancement. This route replaces general suffix search but may rescan
// unconsumed lanes, so it does not claim a one-load root screen.
func (p *searchPlan) eachRootBucket(haystack string, bucket tripleBucketFilter, yield func(Match, int) bool) bool {
	at := 0
	for at+2 < len(haystack) {
		at += tripleBucketSkipBytes(haystack, at, bucket, &p.triples.shufti)
		if at+2 >= len(haystack) {
			break
		}
		match, width, ok := p.matchRootBucketCandidate(haystack, at, bucket)
		if !ok {
			at++
			continue
		}
		if !yield(match, width) {
			return false
		}
		at = match.Start + width
	}
	return true
}
