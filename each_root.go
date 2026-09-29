package casei

// rootBucketEachMinBytes is the first length where the existing four-byte
// bucket kernel can examine a full 64-start block (the last prefix uses bytes
// 63 through 66). Shorter input keeps the ordinary Each route.
const rootBucketEachMinBytes = 64 + 3

// rootBucketEachFilter limits the alternate iterator to complete generic
// multi-literal root plans with conservative Shufti coverage and a compiled
// bucket. Covered roots make p.filter unusable for this shape; the existing
// bucket/Shufti wrapper is the active candidate screen.
func (p *searchPlan) rootBucketEachFilter(haystack string) (tripleBucketFilter, bool) {
	if p.empty >= 0 || p.patternCount < 4 || p.patternCount > 5 || p.opaqueContinuation ||
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

// eachRootBucket enumerates candidate starts in source order through the
// complete plan's existing root-triple filter. Exact trie replay makes a bucket
// or Shufti survivor only a nomination; the plan still owns ID ties, width, and
// non-overlap advancement. It replaces the general findWithWidth suffix search
// with an anchored replay; the vector filter may rescan unconsumed lanes after a
// match, so this route does not remove or claim a one-load root screen.
func (p *searchPlan) eachRootBucket(haystack string, bucket tripleBucketFilter, yield func(Match, int) bool) bool {
	at := 0
	for at+2 < len(haystack) {
		at += tripleBucketSkipBytes(haystack, at, bucket, &p.triples.shufti)
		if at+2 >= len(haystack) {
			break
		}
		match, width, ok := p.matchAtStart(haystack, at)
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
