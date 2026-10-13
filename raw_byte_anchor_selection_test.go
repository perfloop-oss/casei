package casei

import (
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func pinnedCyrillicAlternationPatterns(tb testing.TB) ([]string, *Matcher) {
	tb.Helper()
	raw, err := os.ReadFile("audit/rebar/runner/testdata/rows.tsv")
	if err != nil {
		tb.Fatal(err)
	}
	for _, row := range strings.Split(string(raw), "\n") {
		fields := strings.Split(row, "\t")
		if len(fields) == 5 && fields[0] == "curated/02-literal-alternate/sherlock-casei-ru" {
			patterns := strings.Split(fields[4], "|")
			matcher := NewMatcher(patterns)
			if !matcher.plan.hasRawByteTokenPlan() || !matcher.plan.rawByteMulti.usable() {
				tb.Fatal("pinned Russian alternation did not compile the raw multi-anchor filter")
			}
			return patterns, matcher
		}
	}
	tb.Fatal("pinned Russian alternation row is missing")
	return nil, nil
}

const referenceRawByteMultiAnchorUnitCapacity = rawByteMultiAnchorStartOffsets * rawByteMultiAnchorStartOffsets

func referenceRawByteMultiAnchorPrefixWidths(pattern string, end int, out *[rawByteMultiAnchorStartOffsets]uint8) (int, bool) {
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

func referenceRawByteMultiAnchorRelativeWidths(pattern string, start, end int, out *[rawByteMultiAnchorStartOffsets]uint8) (int, bool) {
	return referenceRawByteMultiAnchorPrefixWidths(pattern[start:end], end-start, out)
}

func referenceRawByteMultiAnchorCandidateFor(pattern string, primary, confirm, guard rawByteMultiAnchorUnit, frequency *[256]uint16) (rawByteMultiAnchorCandidate, bool) {
	if primary.at == 0 {
		return rawByteMultiAnchorCandidate{}, false
	}
	var starts, guardOffsets [rawByteMultiAnchorStartOffsets]uint8
	var possibleConfirmOffsets [rawByteMultiAnchorStartOffsets]uint8
	startN, ok := referenceRawByteMultiAnchorPrefixWidths(pattern, primary.at, &starts)
	if !ok || startN == 0 {
		return rawByteMultiAnchorCandidate{}, false
	}
	confirmN, ok := referenceRawByteMultiAnchorRelativeWidths(pattern, primary.at, confirm.at, &possibleConfirmOffsets)
	if !ok || confirmN == 0 || confirmN > rawByteMultiAnchorConfirmGroups {
		return rawByteMultiAnchorCandidate{}, false
	}
	var confirmOffsets [rawByteMultiAnchorConfirmGroups]uint8
	copy(confirmOffsets[:], possibleConfirmOffsets[:confirmN])
	guardN, ok := referenceRawByteMultiAnchorRelativeWidths(pattern, primary.at, guard.at, &guardOffsets)
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

// referenceRawByteMultiAnchorFilter retains the former exhaustive triple
// selector as a test oracle. Keeping it here avoids shipping a second, slow
// production selector while allowing the optimized compiler's entire filter
// state to be compared byte-for-byte against the previous selection semantics.
func referenceRawByteMultiAnchorFilter(patterns []string) (filter rawByteMultiAnchorFilter) {
	if len(patterns) < 2 || len(patterns) > rawByteMultiAnchorGroups {
		return filter
	}
	frequency := rawByteMultiAnchorFrequency(patterns)
	for patternID, pattern := range patterns {
		var units [referenceRawByteMultiAnchorUnitCapacity]rawByteMultiAnchorUnit
		unitN := 0
		for at := 0; at < len(pattern); {
			r, size := utf8.DecodeRuneInString(pattern[at:])
			if r == utf8.RuneError && size == 1 {
				return filter
			}
			if pairs, ok := rawByteMultiAnchorPairSetFor(r); ok {
				if unitN == len(units) {
					return filter
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
					candidate, ok := referenceRawByteMultiAnchorCandidateFor(pattern, units[primary], units[confirm], units[guard], &frequency)
					if !ok || !filter.canAddConfirmationOffsets(candidate.anchor.confirmOffset[:candidate.anchor.confirmN]) {
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
			return rawByteMultiAnchorFilter{}
		}
		bit := byte(1 << uint(patternID))
		rawByteMultiAnchorAddTable(&filter.first, best.anchor.primary, false, bit)
		rawByteMultiAnchorAddTable(&filter.second, best.anchor.primary, true, bit)
		for i := 0; i < int(best.anchor.confirmN); i++ {
			group, ok := filter.confirmationGroup(best.anchor.confirmOffset[i])
			if !ok {
				return rawByteMultiAnchorFilter{}
			}
			rawByteMultiAnchorAddTable(&filter.confirmFirst[group], best.anchor.confirm, false, bit)
			rawByteMultiAnchorAddTable(&filter.confirmSecond[group], best.anchor.confirm, true, bit)
			if best.anchor.confirmOffset[i] > filter.maxOffset {
				filter.maxOffset = best.anchor.confirmOffset[i]
			}
		}
		filter.anchors[patternID] = best.anchor
		for i := 0; i < int(best.anchor.startN); i++ {
			if best.anchor.starts[i] > filter.maxOffset {
				filter.maxOffset = best.anchor.starts[i]
			}
		}
		for i := 0; i < int(best.anchor.guardN); i++ {
			if best.anchor.guardOffset[i] > filter.maxOffset {
				filter.maxOffset = best.anchor.guardOffset[i]
			}
		}
	}
	if !filter.tagDiverse() {
		return rawByteMultiAnchorFilter{}
	}
	filter.valid = 1
	return filter
}

func TestRawByteMultiAnchorFilterMatchesExhaustiveSelection(t *testing.T) {
	long := strings.Repeat("😀", 62)
	prefix := long + "xxxxx"
	foldBeforeStart := long + "xxxxxk"
	partial := []string{"!αβγδ", "!" + strings.Repeat("α", referenceRawByteMultiAnchorUnitCapacity+1) + "βγ"}
	confirmationOverflow := []string{"!αkβγ", "!δkkεζ"}
	pinned, pinnedMatcher := pinnedCyrillicAlternationPatterns(t)
	cases := []struct {
		name     string
		patterns []string
	}{
		{"pinned-row", pinned},
		{"fold-width-order", []string{"!Kſαγδ", "!Kſαδγ"}},
		{"equal-cost-guard-tie", []string{"kkkαkγδζ", "kkkηkγδζ"}},
		{"four-guard-widths", []string{"!αγkkkδkkkkε", "!αγkkkζkkkkη"}},
		{"later-interval-failure", []string{"!αβγkkkk", "!αβδkkkk"}},
		{"absolute-prefix-overflow-locality", []string{long + "αβγδ", long + "εζηθ"}},
		{"folded-width-boundary", []string{prefix + "αkβγ", prefix + "δkεζ"}},
		{"overflowing-prefix-width-set", []string{foldBeforeStart + "αβγ", foldBeforeStart + "δζη"}},
		{"prefix-width-set-cap", []string{"!" + strings.Repeat("k", 4) + "αβγ", "!" + strings.Repeat("k", 4) + "δζη"}},
		{"primary-at-zero", []string{"αβγ", "δεζ"}},
		{"partial-unit-cap", partial},
		{"shared-confirmation-cap", confirmationOverflow},
		{"indistinguishable-tags", rawByteSharedPrefixPatterns(2)},
	}
	var foldWidthOrderMatcher, equalCostGuardMatcher, fourGuardWidthsMatcher *Matcher
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var matcher *Matcher
			if tc.name == "pinned-row" {
				matcher = pinnedMatcher
			} else {
				matcher = NewMatcher(tc.patterns)
			}
			switch tc.name {
			case "fold-width-order":
				foldWidthOrderMatcher = matcher
			case "equal-cost-guard-tie":
				equalCostGuardMatcher = matcher
			case "four-guard-widths":
				fourGuardWidthsMatcher = matcher
			}
			if !matcher.plan.hasRawByteTokenPlan() {
				t.Fatal("test precondition: pattern set did not select the raw-byte token plan")
			}
			want := referenceRawByteMultiAnchorFilter(tc.patterns)
			if got := matcher.plan.rawByteMulti; got != want {
				t.Fatalf("compiled filter differs from exhaustive selection:\n got: %+v\nwant: %+v", got, want)
			}
			for _, haystack := range []string{tc.patterns[0], "x" + tc.patterns[len(tc.patterns)-1]} {
				checkRawByteMultiReference(t, matcher, tc.patterns, haystack)
			}
		})
	}

	ordered := foldWidthOrderMatcher.plan.rawByteMulti.anchors[0]
	if ordered.startN != 4 || ordered.starts != [rawByteMultiAnchorStartOffsets]uint8{6, 4, 5, 3} {
		t.Fatalf("folded start widths = %v/%d, want [6 4 5 3]/4", ordered.starts, ordered.startN)
	}
	pairwise := equalCostGuardMatcher.plan.rawByteMulti.anchors[0]
	if pairwise.starts != [rawByteMultiAnchorStartOffsets]uint8{3, 5, 7, 9} ||
		pairwise.confirmOffset != [rawByteMultiAnchorConfirmGroups]uint8{3, 5} ||
		pairwise.guardOffset != [rawByteMultiAnchorStartOffsets]uint8{5, 7} {
		t.Fatalf("equal-cost selection = starts %v, confirms %v, guards %v", pairwise.starts, pairwise.confirmOffset, pairwise.guardOffset)
	}
	guards := fourGuardWidthsMatcher.plan.rawByteMulti.anchors[0]
	if guards.guardN != 4 || guards.guardOffset != [rawByteMultiAnchorStartOffsets]uint8{7, 9, 11, 13} {
		t.Fatalf("four feasible guard widths = %v/%d, want [7 9 11 13]/4", guards.guardOffset, guards.guardN)
	}

	m := pinnedMatcher
	haystack := strings.Join(m.patterns, " x ")
	for stopAt := 1; stopAt <= 2; stopAt++ {
		var got []refEachResult
		complete := m.Each(haystack, func(match Match, width int) bool {
			got = append(got, refEachResult{match: match, width: width})
			return len(got) < stopAt
		})
		want := refEach(haystack, m.patterns)
		if stopAt < len(want) {
			want = want[:stopAt]
		}
		if complete || len(got) != len(want) {
			t.Fatalf("Each stop at %d returned %+v, complete=%t; want %+v, false", stopAt, got, complete, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("Each stop at %d result %d = %+v, want %+v", stopAt, i, got[i], want[i])
			}
		}
	}
}
