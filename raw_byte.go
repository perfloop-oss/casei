package casei

import (
	"math/bits"
	"unicode"
	"unicode/utf8"
)

// A raw-byte token plan keeps the two-byte UTF-8 spellings needed by a small
// dense multi-pattern plan in the plan's already-reserved opaque-token array.
// The normal opaque table is unused for an eligible plan: it has no malformed
// pattern bytes. Reusing that storage makes the replacement transition
// allocation-free for a fresh Matcher as well as for a reused one.
const (
	rawByteMaxLeadRows       = 2
	rawByteTrailClasses      = 0x40
	rawByteTokenClasses      = rawByteMaxLeadRows * rawByteTrailClasses
	rawByteTransitionStride  = utf8.RuneSelf + rawByteTokenClasses
	rawByteMaxTransitionSize = 48 << 10
)

// rawByteTokenConfig maps the two-byte UTF-8 spellings in the existing fold
// token map to their dense token. ASCII already has p.ascii, so it needs no
// duplicate entries. A lead row is deliberately correlated with its following
// continuation byte; no independent byte masks can make a two-byte rune.
type rawByteTokenConfig struct {
	leads  [rawByteMaxLeadRows]byte
	leadN  uint8
	tokens [rawByteTokenClasses]uint32
}

// rawByteTokenConfigFor proves that every direct class is the same token the
// decoded path would select. The compact path is intentionally limited to the
// small plans for which its dense transition table remains cache-resident.
func (p *searchPlan) rawByteTokenConfigFor() (rawByteTokenConfig, bool) {
	var config rawByteTokenConfig
	if p.patternCount < 2 || p.dense == nil ||
		len(p.nodes) > rawByteMaxTransitionSize/(2*rawByteTransitionStride) ||
		len(p.dense) > rawByteMaxTransitionSize/4 {
		return config, false
	}
	for _, token := range p.opaque {
		if token != 0 {
			return config, false
		}
	}

	var haveLead [256]bool
	for r := range p.runes {
		if r < utf8.RuneSelf {
			continue
		}
		var encoded [utf8.UTFMax]byte
		if utf8.EncodeRune(encoded[:], r) == 2 {
			haveLead[encoded[0]] = true
		}
	}
	for lead := range haveLead {
		if !haveLead[lead] {
			continue
		}
		if int(config.leadN) == len(config.leads) {
			return rawByteTokenConfig{}, false
		}
		config.leads[config.leadN] = byte(lead)
		config.leadN++
	}
	if config.leadN == 0 {
		return rawByteTokenConfig{}, false
	}

	for r, token := range p.runes {
		if r < utf8.RuneSelf {
			continue
		}
		var encoded [utf8.UTFMax]byte
		if utf8.EncodeRune(encoded[:], r) != 2 || encoded[1] < 0x80 || encoded[1] >= 0xc0 {
			continue
		}
		row := -1
		for i := 0; i < int(config.leadN); i++ {
			if config.leads[i] == encoded[0] {
				row = i
				break
			}
		}
		if row < 0 {
			return rawByteTokenConfig{}, false
		}
		class := row*rawByteTrailClasses + int(encoded[1]-0x80)
		if prior := config.tokens[class]; prior != 0 && prior != token {
			return rawByteTokenConfig{}, false
		}
		config.tokens[class] = token
	}
	return config, true
}

// makeRawByteTokenPlan records a proven compact raw map during normal plan
// construction. singleTokens is otherwise used only by one-pattern plans; an
// empty, non-nil slice marks this multi-pattern plan while pointing at storage
// it already owns. No lazy publication, allocation, or caller history is
// involved.
func (p *searchPlan) makeRawByteTokenPlan(patterns []string) {
	config, ok := p.rawByteTokenConfigFor()
	if !ok {
		return
	}
	copy(p.opaque[:rawByteTokenClasses], config.tokens[:])
	for row := 0; row < int(config.leadN); row++ {
		p.opaque[config.leads[row]] = uint32(row + 1)
	}
	p.singleTokens = p.opaque[:0]
	p.makeRawByteMultiAnchorFilter(patterns)
	if p.rawByteMulti.usable() {
		p.rawByteOrigin = rawByteOriginGateFor(patterns)
	}
}

func (p *searchPlan) hasRawByteTokenPlan() bool {
	return p.patternCount > 1 && p.dense != nil && p.singleTokens != nil && len(p.singleTokens) == 0
}

func (p *searchPlan) rawByteAdvanceToken(state int, token uint32) int {
	if token == 0 {
		return 0
	}
	return int(p.dense[state*p.stride+int(token)])
}

// rawByteAdvance consumes exactly one ASCII byte or one complete two-byte UTF-8
// sequence. Unsupported widths and malformed encodings return ok=false so the
// decoded path retains its full Unicode and opaque-byte authority.
func (p *searchPlan) rawByteAdvance(haystack string, at, state int) (next, size int, ok bool) {
	value := haystack[at]
	if value < utf8.RuneSelf {
		return p.rawByteAdvanceToken(state, p.ascii[value]), 1, true
	}
	if value < 0xc2 || value > 0xdf || at+1 == len(haystack) {
		return 0, 0, false
	}
	trail := haystack[at+1]
	if trail < 0x80 || trail >= 0xc0 {
		return 0, 0, false
	}
	row := p.opaque[value]
	if row == 0 {
		// This valid two-byte rune is absent from every pattern orbit, so its
		// decoded token is zero and the shared state machine resets.
		return 0, 2, true
	}
	token := p.opaque[(int(row)-1)*rawByteTrailClasses+int(trail-0x80)]
	return p.rawByteAdvanceToken(state, token), 2, true
}

// rawByteMultiAnchorGroups is bounded by the eight tag bits carried through
// the VBMI tables. Limiting the projection is a plan property, not a workload
// admission rule: plans outside it keep the ordinary decoded enumeration.
const (
	rawByteMultiAnchorGroups        = 8
	rawByteMultiAnchorForms         = 4
	rawByteMultiAnchorStartOffsets  = 4
	rawByteMultiAnchorConfirmGroups = 3
)

// rawByteMultiAnchorPairSet lists the exact two-byte spellings of one selected
// fold orbit. The vector tables retain only low-six-bit membership, so this
// exact representation removes aliases before the shared plan is replayed.
type rawByteMultiAnchorPairSet struct {
	pairs [rawByteMultiAnchorForms]uint16
	n     uint8
}

func (pairs rawByteMultiAnchorPairSet) matches(s string, at int) bool {
	if at < 0 || at+1 >= len(s) {
		return false
	}
	value := uint16(s[at]) | uint16(s[at+1])<<8
	for i := 0; i < int(pairs.n); i++ {
		if pairs.pairs[i] == value {
			return true
		}
	}
	return false
}

// rawByteMultiAnchor records one literal's three correlated interior UTF-8
// pairs. starts includes every possible source-byte prefix width before the
// primary pair, so a width-changing fold before an anchor can only create a
// replayed extra candidate, never hide the true match.
type rawByteMultiAnchor struct {
	primary, confirm, guard  rawByteMultiAnchorPairSet
	starts                   [rawByteMultiAnchorStartOffsets]uint8
	confirmOffset            [rawByteMultiAnchorConfirmGroups]uint8
	guardOffset              [rawByteMultiAnchorStartOffsets]uint8
	startN, confirmN, guardN uint8
}

// rawByteMultiAnchorFilter is the compact vector-facing representation of one
// tagged pair for every literal. A bit identifies the literal group. The first
// confirmation table for an offset retains that bit only when the same literal
// owns both pairs; the exact third pair is checked in Go before the shared raw
// transition replay. The first 512 bytes have a fixed table layout consumed by
// rawByteMultiAnchorSkip64.
type rawByteMultiAnchorFilter struct {
	first, second              [64]byte
	confirmFirst               [rawByteMultiAnchorConfirmGroups][64]byte
	confirmSecond              [rawByteMultiAnchorConfirmGroups][64]byte
	confirmOffset              [rawByteMultiAnchorConfirmGroups]uint8
	confirmN, maxConfirmOffset uint8
	maxOffset, valid           uint8
	anchors                    [rawByteMultiAnchorGroups]rawByteMultiAnchor
}

// rawByteOriginGate records an exact ASCII byte found in every rendering of
// every raw-filter literal. maxPrefix bounds its latest folded source offset;
// a Find start before the first byte minus that bound is impossible.
type rawByteOriginGate struct {
	byte      byte
	maxPrefix uint16
	valid     uint8
}

func (gate rawByteOriginGate) usable() bool { return gate.valid != 0 }

func (filter *rawByteMultiAnchorFilter) usable() bool {
	return filter != nil && filter.valid != 0
}

// tagDiverse reports whether the compiled tagged anchors can distinguish at
// least two literals before replay. A shared prefix whose primary,
// confirmation, guard, and source-width sets are identical for every tag pays
// the vector filter and then replays every pattern at the same survivor; it has
// no multi-pattern selectivity and must retain the ordinary Unicode route.
func (filter *rawByteMultiAnchorFilter) tagDiverse() bool {
	var first rawByteMultiAnchor
	haveFirst := false
	for _, anchor := range filter.anchors {
		if anchor.startN == 0 {
			continue
		}
		if !haveFirst {
			first, haveFirst = anchor, true
			continue
		}
		if anchor != first {
			return true
		}
	}
	return false
}

// rawByteOriginGateFor intersects fixed ASCII runes across the literals. A
// chosen rune encodes as the same byte in every simple-fold spelling. For each
// literal the earliest such occurrence minimizes its worst-case prefix; the
// global maximum is the safe Find lookback bound.
func rawByteOriginGateFor(patterns []string) rawByteOriginGate {
	var common [utf8.RuneSelf]bool
	for i := range common {
		common[i] = true
	}
	var maxPrefix [utf8.RuneSelf]uint16
	for _, pattern := range patterns {
		var seen [utf8.RuneSelf]bool
		var localPrefix [utf8.RuneSelf]uint16
		prefix := 0
		for at := 0; at < len(pattern); {
			r, size := utf8.DecodeRuneInString(pattern[at:])
			if r == utf8.RuneError && size == 1 {
				return rawByteOriginGate{}
			}
			if r < utf8.RuneSelf && unicode.SimpleFold(r) == r {
				value := byte(r)
				if !seen[value] || prefix < int(localPrefix[value]) {
					seen[value] = true
					localPrefix[value] = uint16(prefix)
				}
			}
			maxWidth := 0
			for member := r; ; member = unicode.SimpleFold(member) {
				var encoded [utf8.UTFMax]byte
				if width := utf8.EncodeRune(encoded[:], member); width > maxWidth {
					maxWidth = width
				}
				if unicode.SimpleFold(member) == r {
					break
				}
			}
			prefix += maxWidth
			if prefix > int(^uint16(0)) {
				return rawByteOriginGate{}
			}
			at += size
		}
		for value := range common {
			if !seen[value] {
				common[value] = false
				continue
			}
			if localPrefix[value] > maxPrefix[value] {
				maxPrefix[value] = localPrefix[value]
			}
		}
	}

	frequency := rawByteMultiAnchorFrequency(patterns)
	best, bestScore := -1, uint16(^uint16(0))
	for value, present := range common {
		if !present {
			continue
		}
		score := frequency[value]
		if best < 0 || score < bestScore || score == bestScore &&
			(maxPrefix[value] < maxPrefix[best] || maxPrefix[value] == maxPrefix[best] && value < best) {
			best, bestScore = value, score
		}
	}
	if best < 0 {
		return rawByteOriginGate{}
	}
	return rawByteOriginGate{byte: byte(best), maxPrefix: maxPrefix[best], valid: 1}
}

func rawByteMultiAnchorPairSetFor(r rune) (rawByteMultiAnchorPairSet, bool) {
	var out rawByteMultiAnchorPairSet
	for member := r; ; member = unicode.SimpleFold(member) {
		var encoded [utf8.UTFMax]byte
		if utf8.EncodeRune(encoded[:], member) != 2 {
			return rawByteMultiAnchorPairSet{}, false
		}
		pair := uint16(encoded[0]) | uint16(encoded[1])<<8
		duplicate := false
		for i := 0; i < int(out.n); i++ {
			duplicate = duplicate || out.pairs[i] == pair
		}
		if !duplicate {
			if int(out.n) == len(out.pairs) {
				return rawByteMultiAnchorPairSet{}, false
			}
			out.pairs[out.n] = pair
			out.n++
		}
		if unicode.SimpleFold(member) == r {
			break
		}
	}
	return out, out.n != 0
}

// rawByteMultiAnchorPrefixWidths records the bounded set of possible byte
// widths before an anchor. The cross product is deliberately collapsed by
// width: it is only used to nominate replay starts, whose shared plan decides
// whether that spelling actually matches.
func rawByteMultiAnchorPrefixWidths(pattern string, end int, out *[rawByteMultiAnchorStartOffsets]uint8) (int, bool) {
	out[0] = 0
	n := 1
	for at := 0; at < end; {
		r, size := utf8.DecodeRuneInString(pattern[at:])
		if r == utf8.RuneError && size == 1 {
			return 0, false
		}
		var next [rawByteMultiAnchorStartOffsets]uint8
		nextN := 0
		for member := r; ; member = unicode.SimpleFold(member) {
			var encoded [utf8.UTFMax]byte
			width := utf8.EncodeRune(encoded[:], member)
			for i := 0; i < n; i++ {
				value := int(out[i]) + width
				if value > int(^uint8(0)) {
					return 0, false
				}
				duplicate := false
				for j := 0; j < nextN; j++ {
					duplicate = duplicate || next[j] == uint8(value)
				}
				if !duplicate {
					if nextN == len(next) {
						return 0, false
					}
					next[nextN] = uint8(value)
					nextN++
				}
			}
			if unicode.SimpleFold(member) == r {
				break
			}
		}
		*out = next
		n = nextN
		at += size
	}
	return n, true
}

// rawByteMultiAnchorFrequency estimates selectivity only from the immutable
// literal set. It intentionally never samples a haystack, so plan selection
// cannot become benchmark- or caller-dependent.
func rawByteMultiAnchorFrequency(patterns []string) (out [256]uint16) {
	for _, pattern := range patterns {
		for at := 0; at < len(pattern); {
			r, size := utf8.DecodeRuneInString(pattern[at:])
			if r == utf8.RuneError && size == 1 {
				break
			}
			for member := r; ; member = unicode.SimpleFold(member) {
				var encoded [utf8.UTFMax]byte
				for _, value := range encoded[:utf8.EncodeRune(encoded[:], member)] {
					if out[value] != ^uint16(0) {
						out[value]++
					}
				}
				if unicode.SimpleFold(member) == r {
					break
				}
			}
			at += size
		}
	}
	return out
}

func rawByteMultiAnchorPairCost(pairs rawByteMultiAnchorPairSet, frequency *[256]uint16) uint64 {
	var cost uint64
	for i := 0; i < int(pairs.n); i++ {
		pair := pairs.pairs[i]
		first, second := byte(pair), byte(pair>>8)
		firstCost, secondCost := uint64(frequency[first]), uint64(frequency[second])
		if firstCost == 0 {
			firstCost = 1
		}
		if secondCost == 0 {
			secondCost = 1
		}
		cost += firstCost * secondCost
	}
	return cost
}

type rawByteMultiAnchorCandidate struct {
	anchor rawByteMultiAnchor
	score  uint64
	at     int
}

// rawByteMultiAnchorRelativeWidths records all possible byte displacements
// from start to end. Independent width choices may yield a crossed offset, but
// that only creates a scalar-guarded replay; it cannot remove the true one.
func rawByteMultiAnchorRelativeWidths(pattern string, start, end int, out *[rawByteMultiAnchorStartOffsets]uint8) (int, bool) {
	return rawByteMultiAnchorPrefixWidths(pattern[start:end], end-start, out)
}

type rawByteMultiAnchorUnit struct {
	at    int
	pairs rawByteMultiAnchorPairSet
}

// rawByteMultiAnchorCandidateFor chooses three width-stable two-byte interior
// pairs. Variable-width forms between pairs are represented by every bounded
// displacement; this is why the vector filter has three confirmation groups.
func rawByteMultiAnchorCandidateFor(pattern string, primary, confirm, guard rawByteMultiAnchorUnit, frequency *[256]uint16) (rawByteMultiAnchorCandidate, bool) {
	if primary.at == 0 {
		return rawByteMultiAnchorCandidate{}, false
	}
	var starts, guardOffsets [rawByteMultiAnchorStartOffsets]uint8
	var possibleConfirmOffsets [rawByteMultiAnchorStartOffsets]uint8
	startN, ok := rawByteMultiAnchorPrefixWidths(pattern, primary.at, &starts)
	if !ok || startN == 0 {
		return rawByteMultiAnchorCandidate{}, false
	}
	confirmN, ok := rawByteMultiAnchorRelativeWidths(pattern, primary.at, confirm.at, &possibleConfirmOffsets)
	if !ok || confirmN == 0 || confirmN > rawByteMultiAnchorConfirmGroups {
		return rawByteMultiAnchorCandidate{}, false
	}
	var confirmOffsets [rawByteMultiAnchorConfirmGroups]uint8
	copy(confirmOffsets[:], possibleConfirmOffsets[:confirmN])
	guardN, ok := rawByteMultiAnchorRelativeWidths(pattern, primary.at, guard.at, &guardOffsets)
	if !ok || guardN == 0 {
		return rawByteMultiAnchorCandidate{}, false
	}
	return rawByteMultiAnchorCandidate{
		anchor: rawByteMultiAnchor{
			primary:       primary.pairs,
			confirm:       confirm.pairs,
			guard:         guard.pairs,
			starts:        starts,
			confirmOffset: confirmOffsets,
			guardOffset:   guardOffsets,
			startN:        uint8(startN),
			confirmN:      uint8(confirmN),
			guardN:        uint8(guardN),
		},
		score: rawByteMultiAnchorPairCost(primary.pairs, frequency) +
			rawByteMultiAnchorPairCost(confirm.pairs, frequency) +
			rawByteMultiAnchorPairCost(guard.pairs, frequency),
		at: primary.at,
	}, true
}

func rawByteMultiAnchorAddTable(table *[64]byte, pairs rawByteMultiAnchorPairSet, second bool, bit byte) {
	for i := 0; i < int(pairs.n); i++ {
		value := byte(pairs.pairs[i])
		if second {
			value = byte(pairs.pairs[i] >> 8)
		}
		table[value&0x3f] |= bit
	}
}

func (filter *rawByteMultiAnchorFilter) confirmationGroup(offset uint8) (int, bool) {
	for i := 0; i < int(filter.confirmN); i++ {
		if filter.confirmOffset[i] == offset {
			return i, true
		}
	}
	if int(filter.confirmN) == len(filter.confirmOffset) {
		return 0, false
	}
	group := int(filter.confirmN)
	filter.confirmOffset[group] = offset
	filter.confirmN++
	if offset > filter.maxConfirmOffset {
		filter.maxConfirmOffset = offset
	}
	return group, true
}

// makeRawByteMultiAnchorFilter compiles the shared tagged interior-pair screen
// at plan construction. It is intentionally available only after the compact
// direct raw map is proven. Rejected shapes leave Matcher.Each on the existing
// decoded enumerator without lazy state, allocations, or history thresholds.
func (p *searchPlan) makeRawByteMultiAnchorFilter(patterns []string) {
	p.rawByteMulti = rawByteMultiAnchorFilter{}
	if len(patterns) < 2 || len(patterns) > rawByteMultiAnchorGroups {
		return
	}
	frequency := rawByteMultiAnchorFrequency(patterns)
	for patternID, pattern := range patterns {
		var units [rawByteMultiAnchorStartOffsets * rawByteMultiAnchorStartOffsets]rawByteMultiAnchorUnit
		unitN := 0
		for at := 0; at < len(pattern); {
			r, size := utf8.DecodeRuneInString(pattern[at:])
			if r == utf8.RuneError && size == 1 {
				return
			}
			if pairs, ok := rawByteMultiAnchorPairSetFor(r); ok {
				if unitN == len(units) {
					return
				}
				units[unitN] = rawByteMultiAnchorUnit{at: at, pairs: pairs}
				unitN++
			}
			at += size
		}
		var best rawByteMultiAnchorCandidate
		found := false
		for primary := 0; primary < unitN; primary++ {
			for confirm := primary + 1; confirm < unitN; confirm++ {
				for guard := confirm + 1; guard < unitN; guard++ {
					candidate, ok := rawByteMultiAnchorCandidateFor(pattern, units[primary], units[confirm], units[guard], &frequency)
					if !ok || !p.rawByteMulti.canAddConfirmationOffsets(candidate.anchor.confirmOffset[:candidate.anchor.confirmN]) {
						continue
					}
					if !found || candidate.anchor.confirmN < best.anchor.confirmN ||
						candidate.anchor.confirmN == best.anchor.confirmN && (candidate.score < best.score || candidate.score == best.score && candidate.at > best.at) {
						best, found = candidate, true
					}
				}
			}
		}
		if !found {
			p.rawByteMulti = rawByteMultiAnchorFilter{}
			return
		}
		bit := byte(1 << uint(patternID))
		rawByteMultiAnchorAddTable(&p.rawByteMulti.first, best.anchor.primary, false, bit)
		rawByteMultiAnchorAddTable(&p.rawByteMulti.second, best.anchor.primary, true, bit)
		for i := 0; i < int(best.anchor.confirmN); i++ {
			group, ok := p.rawByteMulti.confirmationGroup(best.anchor.confirmOffset[i])
			if !ok {
				p.rawByteMulti = rawByteMultiAnchorFilter{}
				return
			}
			rawByteMultiAnchorAddTable(&p.rawByteMulti.confirmFirst[group], best.anchor.confirm, false, bit)
			rawByteMultiAnchorAddTable(&p.rawByteMulti.confirmSecond[group], best.anchor.confirm, true, bit)
			if best.anchor.confirmOffset[i] > p.rawByteMulti.maxOffset {
				p.rawByteMulti.maxOffset = best.anchor.confirmOffset[i]
			}
		}
		p.rawByteMulti.anchors[patternID] = best.anchor
		for i := 0; i < int(best.anchor.startN); i++ {
			if best.anchor.starts[i] > p.rawByteMulti.maxOffset {
				p.rawByteMulti.maxOffset = best.anchor.starts[i]
			}
		}
		for i := 0; i < int(best.anchor.guardN); i++ {
			if best.anchor.guardOffset[i] > p.rawByteMulti.maxOffset {
				p.rawByteMulti.maxOffset = best.anchor.guardOffset[i]
			}
		}
	}
	// A table with one effective tag signature is not a selective multi-anchor
	// filter. It cannot eliminate any shared-prefix alternative before the
	// expensive raw-plan replays, so leave this plan on its existing fallback.
	if !p.rawByteMulti.tagDiverse() {
		p.rawByteMulti = rawByteMultiAnchorFilter{}
		return
	}
	p.rawByteMulti.valid = 1
}

func (filter *rawByteMultiAnchorFilter) canAddConfirmationOffsets(offsets []uint8) bool {
	n := int(filter.confirmN)
	var pending [rawByteMultiAnchorConfirmGroups]uint8
	for _, offset := range offsets {
		found := false
		for i := 0; i < int(filter.confirmN); i++ {
			found = found || filter.confirmOffset[i] == offset
		}
		for i := 0; i < n; i++ {
			found = found || pending[i] == offset
		}
		if !found {
			if n == len(pending) {
				return false
			}
			pending[n] = offset
			n++
		}
	}
	return true
}

// tagsAt applies exact pair checks only for tags retained by the vector or
// scalar table screen. It tests each fold-width alternative independently so an
// unavailable longer offset cannot suppress an in-bounds spelling near EOF.
func (filter *rawByteMultiAnchorFilter) tagsAt(s string, at int, candidates byte) byte {
	if !filter.usable() {
		return 0
	}
	var tags byte
	for candidates != 0 {
		i := bits.TrailingZeros8(candidates)
		candidates &= candidates - 1
		anchor := filter.anchors[i]
		if anchor.startN == 0 || !anchor.primary.matches(s, at) {
			continue
		}
		confirmed := false
		for j := 0; j < int(anchor.confirmN); j++ {
			confirmed = confirmed || anchor.confirm.matches(s, at+int(anchor.confirmOffset[j]))
		}
		if !confirmed {
			continue
		}
		guarded := false
		for j := 0; j < int(anchor.guardN); j++ {
			guarded = guarded || anchor.guard.matches(s, at+int(anchor.guardOffset[j]))
		}
		if guarded {
			tags |= 1 << uint(i)
		}
	}
	return tags
}

func rawByteMultiAnchorSkipScalar(s string, at int, filter *rawByteMultiAnchorFilter) (int, byte) {
	start := at
	for at+1 < len(s) {
		// The vector loop has already reduced these same low-six-bit tables.
		// Keep its conservative predicate in the scalar tail, then let the
		// caller run tagsAt's exact pair and guard checks only at a survivor.
		tags := filter.first[s[at]&0x3f]
		if tags != 0 {
			tags &= filter.second[s[at+1]&0x3f]
			var candidates byte
			for group := 0; tags != 0 && group < int(filter.confirmN); group++ {
				offset := int(filter.confirmOffset[group])
				if at+offset+1 >= len(s) {
					continue
				}
				confirmed := filter.confirmFirst[group][s[at+offset]&0x3f] &
					filter.confirmSecond[group][s[at+offset+1]&0x3f]
				candidates |= tags & confirmed
			}
			if candidates != 0 {
				return at - start, candidates
			}
		}
		at++
	}
	return at - start, 0
}

// rawByteMatchAt confirms only an anchored start. It retains the same raw
// two-byte map and decoded fallback as findFiltered, but does not let a later
// root found through a failure link impersonate a match at start. It keeps the
// lowest-ID terminal across different end positions and saves that terminal's
// width, because a shorter higher-ID prefix can complete first.
func (p *searchPlan) rawByteMatchAt(haystack string, start int) (Match, int, bool) {
	state, at := 0, start
	best := Match{Pattern: -1, Start: start}
	bestWidth := 0
	for units := 1; units <= p.maxUnits && at < len(haystack); units++ {
		next, size, raw := p.rawByteAdvance(haystack, at, state)
		if !raw {
			token, decodedSize := p.haystackToken(haystack, at)
			next, size = p.advance(state, token), decodedSize
		}
		state = next
		at += size
		if output := p.nodes[state].output; output.pattern >= 0 && output.units == units {
			if best.Pattern < 0 || output.pattern < best.Pattern {
				best = Match{Pattern: output.pattern, Start: start}
				bestWidth = at - start
			}
			if best.Pattern == 0 || len(p.nodes[state].edges) == 0 {
				return best, bestWidth, true
			}
		}
		if state == 0 {
			break
		}
	}
	if best.Pattern < 0 {
		return Match{}, 0, false
	}
	return best, bestWidth, true
}

// findRawByteFixedAnchored is the first-result specialization of the same
// tagged scan used by Matcher.Each. Stopping its callback leaves the shared
// scan only after it has waited through every compiled primary-start offset, so
// it preserves Find's leftmost and lowest-ID contract without a second engine.
func (p *searchPlan) findRawByteFixedAnchored(haystack string) (Match, bool) {
	var result Match
	found := false
	p.eachRawByteFixedAnchored(haystack, func(match Match, _ int) bool {
		result, found = match, true
		return false
	})
	return result, found
}

// findRawByteOrigin begins the tagged scan at the only suffix that can contain
// a match. The exact-byte scan and the tagged/raw-plan replay retain authority
// over malformed input, fold spelling, leftmost order, and pattern-ID ties.
func (p *searchPlan) findRawByteOrigin(haystack string) (Match, bool) {
	gate := p.rawByteOrigin
	at := literalSkipExactASCII(haystack, 0, gate.byte)
	if at == len(haystack) {
		return Match{}, false
	}
	from := at - int(gate.maxPrefix)
	if from < 0 {
		from = 0
	}
	match, ok := p.findRawByteFixedAnchored(haystack[from:])
	if ok {
		match.Start += from
	}
	return match, ok
}

// eachRawByteFixedAnchored enumerates with one shared tagged interior-pair
// scan. The table only nominates starts; exact pair checks and raw-plan replay
// keep Unicode folding, malformed bytes, leftmost order, and lowest-ID ties in
// the existing state machine. When lookahead reaches EOF with a pending match,
// it resumes from that match's end to find later non-overlapping results.
func (p *searchPlan) eachRawByteFixedAnchored(haystack string, yield func(Match, int) bool) bool {
	filter := &p.rawByteMulti
	if !filter.usable() {
		return true
	}

	at, from := 0, 0
	best := Match{Pattern: -1, Start: -1}
	bestEnd := 0
	emit := func() bool {
		if !yield(best, bestEnd-best.Start) {
			return false
		}
		at, from = bestEnd, bestEnd
		best, bestEnd = Match{Pattern: -1, Start: -1}, 0
		return true
	}

	for {
		for at+1 < len(haystack) {
			if best.Pattern >= 0 && at > best.Start+int(filter.maxOffset) {
				if !emit() {
					return false
				}
				continue
			}
			skipped, candidates := rawByteMultiAnchorSkipBytes(haystack, at, filter)
			at += skipped
			if at+1 >= len(haystack) {
				break
			}
			for tags := filter.tagsAt(haystack, at, candidates); tags != 0; tags &= tags - 1 {
				patternID := bits.TrailingZeros8(tags)
				anchor := filter.anchors[patternID]
				for i := 0; i < int(anchor.startN); i++ {
					start := at - int(anchor.starts[i])
					if start < from || best.Pattern >= 0 && start > best.Start {
						continue
					}
					match, width, ok := p.rawByteMatchAt(haystack, start)
					if !ok || best.Pattern >= 0 && match.Start > best.Start {
						continue
					}
					if best.Pattern < 0 || match.Start < best.Start ||
						match.Start == best.Start && match.Pattern < best.Pattern {
						best, bestEnd = match, match.Start+width
					}
				}
			}
			at++
		}
		if best.Pattern < 0 {
			return true
		}
		if !emit() {
			return false
		}
	}
}
