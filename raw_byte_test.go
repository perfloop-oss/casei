package casei

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRawByteTokenPlanDirectTransitions(t *testing.T) {
	plan := newSearchPlan(rawByteCyrillicPatterns)
	if !plan.hasRawByteTokenPlan() {
		t.Fatal("eligible Cyrillic plan did not retain a compact raw-byte map")
	}
	for state := range plan.nodes {
		for value := range utf8.RuneSelf {
			haystack := string([]byte{byte(value)})
			got, size, ok := plan.rawByteAdvance(haystack, 0, state)
			wantToken, wantSize := plan.haystackToken(haystack, 0)
			want := plan.advance(state, wantToken)
			if !ok || size != wantSize || got != want {
				t.Fatalf("state %d ASCII %02x: raw=(%d,%d,%t), decoded=(%d,%d)",
					state, value, got, size, ok, want, wantSize)
			}
		}
		for lead := 0xc2; lead <= 0xdf; lead++ {
			for trail := 0x80; trail <= 0xbf; trail++ {
				haystack := string([]byte{byte(lead), byte(trail)})
				got, size, ok := plan.rawByteAdvance(haystack, 0, state)
				wantToken, wantSize := plan.haystackToken(haystack, 0)
				want := plan.advance(state, wantToken)
				if !ok || size != wantSize || got != want {
					t.Fatalf("state %d UTF-8 %02x%02x: raw=(%d,%d,%t), decoded=(%d,%d)",
						state, lead, trail, got, size, ok, want, wantSize)
				}
			}
		}
	}

	for _, haystack := range []string{"\x80", "\xc2x", "€", "K"} {
		if _, _, ok := plan.rawByteAdvance(haystack, 0, 0); ok {
			t.Fatalf("unsupported input %x entered the raw-byte map", haystack)
		}
	}
}

// rawByteFilteredDirectTransitions mirrors the non-skipped portion of
// findFiltered for a no-match stream. It verifies that the public filtered
// route reaches the compact raw map rather than merely compiling it.
func rawByteFilteredDirectTransitions(t *testing.T, plan *searchPlan, haystack string) int {
	t.Helper()
	if !plan.hasRawByteTokenPlan() {
		t.Fatal("plan has no compact raw-byte map")
	}

	state, transitions := 0, 0
	for at := 0; at < len(haystack); {
		if state == 0 {
			skipped := len(haystack) - at
			if plan.pairSecond {
				skipped = pairSecondSkipBytes(haystack, at, &plan.filter)
			} else if plan.filter.usable() {
				skipped = filterSkipBytes(haystack, at, &plan.filter)
			}
			if plan.triples.usable() {
				if tripleSkipped := tripleSkipBytes(haystack, at, &plan.triples); tripleSkipped < skipped {
					skipped = tripleSkipped
				}
			}
			at += skipped
			if at == len(haystack) {
				break
			}
		}

		rawState, rawSize, rawOK := plan.rawByteAdvance(haystack, at, state)
		token, decodedSize := plan.haystackToken(haystack, at)
		decodedState := plan.advance(state, token)
		if !rawOK || rawSize != decodedSize || rawState != decodedState {
			t.Fatalf("transition at byte %d: raw=(state=%d,size=%d,ok=%t), decoded=(state=%d,size=%d)",
				at, rawState, rawSize, rawOK, decodedState, decodedSize)
		}
		state = rawState
		transitions++
		at += rawSize
	}
	return transitions
}

func rawByteSharedPrefixPatterns(n int) []string {
	patterns := make([]string, n)
	for i := range patterns {
		patterns[i] = "щупальце" + string(rune('0'+i))
	}
	return patterns
}

func TestRawByteMultiAnchorRequiresTagDiversity(t *testing.T) {
	for _, n := range []int{2, 4, 8} {
		patterns := rawByteSharedPrefixPatterns(n)
		plan := newSearchPlan(patterns)
		if !plan.hasRawByteTokenPlan() {
			t.Fatalf("N=%d shared-prefix plan lost the compact raw-byte map", n)
		}
		if plan.rawByteMulti.usable() {
			t.Fatalf("N=%d indistinguishable shared-prefix anchors entered rawByteMulti", n)
		}
		matcher := NewMatcher(patterns)
		for _, haystack := range []string{
			strings.Repeat("щ", 63) + "упальцеx",
			"ᲇупальце7", // width-changing fold spelling before the shared suffix.
			strings.Repeat("x", 71) + "\xff" + patterns[n-1],
		} {
			got, gotOK := matcher.Find(haystack)
			want, wantOK := refFind(haystack, patterns)
			if gotOK != wantOK || gotOK && got != want {
				t.Fatalf("N=%d Find(%x) = %+v,%t; want %+v,%t", n, haystack, got, gotOK, want, wantOK)
			}
		}
	}

	diverse := []string{
		"абвгде0", "ёжзийк1", "лмнопр2", "стуфхц3",
		"чшщьыъ4", "ыьэюяа5", "бвгдеж6", "зийклм7",
	}
	for _, patterns := range [][]string{rawByteCyrillicPatterns, diverse} {
		plan := newSearchPlan(patterns)
		if !plan.rawByteMulti.usable() || !plan.rawByteMulti.tagDiverse() {
			t.Fatalf("diverse anchors were not admitted: patterns=%q usable=%t diverse=%t", patterns, plan.rawByteMulti.usable(), plan.rawByteMulti.tagDiverse())
		}
	}
}

func TestRawByteFilteredDensityUsesDirectTransitions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		period   int
		groups   int
	}{
		{"two_one_in_32", rawByteCyrillicPatterns[:2], 32, 4096 / 32},
		{"five_one_in_256", rawByteCyrillicPatterns, 256, rawBytePublicationCorpusBytes / 256},
		{"five_one_in_4", rawByteCyrillicPatterns, 4, rawByteBenchmarkCorpusBytes / 16 / 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			haystack := rawByteFalseCandidates(tc.period, tc.groups)
			plan := newSearchPlan(tc.patterns)
			if plan.unicodePairN != 0 || plan.unicodeAnchor.n != 0 || !plan.filter.usable() {
				t.Fatalf("fixture does not select generic filtered search: pairs=%d anchor=%d filter=%t",
					plan.unicodePairN, plan.unicodeAnchor.n, plan.filter.usable())
			}
			if got := rawByteFilteredDirectTransitions(t, plan, haystack); got != 2*tc.groups {
				t.Fatalf("direct non-skipped transitions = %d, want %d", got, 2*tc.groups)
			}

			matcher := NewMatcher(tc.patterns)
			if got, ok := matcher.Find(haystack); ok || got != (Match{}) {
				t.Fatalf("Find = %+v,%t, want no match", got, ok)
			}
			if !matcher.plan.hasRawByteTokenPlan() {
				t.Fatal("public Matcher.Find did not retain the compact raw-byte map")
			}
		})
	}
}

func TestRawByteTokenPlanPreservesFallbackOffsetsAndTies(t *testing.T) {
	patterns := append(append([]string(nil), rawByteCyrillicPatterns...), "ДЖОН УОТСОН")
	matcher := NewMatcher(patterns)
	if !matcher.plan.hasRawByteTokenPlan() {
		t.Fatal("eligible multi-pattern plan did not retain a raw-byte map")
	}

	for _, haystack := range []string{
		"ᲁЖОН УОТСОН", // U+1C81 is a three-byte simple-fold spelling of Д.
		strings.Repeat("x", 31) + "\xff" + strings.Repeat("x", 17) + "ДЖОН УОТСОН",
		strings.Repeat("x", 29) + "K" + strings.Repeat("x", 11) + "ШЕРЛОК ХОЛМС",
		strings.Repeat("x", 23) + "ДЖОН УОТСОН",
	} {
		got, gotOK := matcher.Find(haystack)
		want, wantOK := refFind(haystack, patterns)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("Find(%x) = %+v,%t; want %+v,%t", haystack, got, gotOK, want, wantOK)
		}
	}

	rng := rand.New(rand.NewPCG(20260824, 1))
	units := []string{"x", " ", "Д", "д", "ᲁ", "Ж", "ж", "Ш", "ш", "K", "ſ", "\xff", "\x80", "€"}
	for iteration := 0; iteration < 1000; iteration++ {
		var haystack strings.Builder
		for range 96 {
			haystack.WriteString(units[rng.IntN(len(units))])
		}
		if iteration%3 == 0 {
			haystack.WriteString(patterns[rng.IntN(len(rawByteCyrillicPatterns))])
		}
		input := haystack.String()
		got, gotOK := matcher.Find(input)
		want, wantOK := refFind(input, patterns)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("iteration %d Find(%x) = %+v,%t; want %+v,%t", iteration, input, got, gotOK, want, wantOK)
		}
	}
}

func TestRawByteOriginDoesNotEnterASCIIOnlyPartition(t *testing.T) {
	plan := newSearchPlan(rawByteCyrillicPatterns)
	if plan.patternCount != len(rawByteCyrillicPatterns) || plan.asciiOnly {
		t.Fatalf("raw transition plan changed ASCII-only admission: patterns=%d asciiOnly=%t", plan.patternCount, plan.asciiOnly)
	}
	if plan.asciiOnlyPartitionUsable() {
		t.Fatal("raw transition plan entered the N=1 ASCII-only partition route")
	}
	if !plan.rawByteMulti.usable() || !plan.rawByteOrigin.usable() {
		t.Fatal("eligible plan did not compile the tagged filter and origin gate")
	}
}

func TestRawByteOriginGatePreservesFind(t *testing.T) {
	plan := newSearchPlan(rawByteCyrillicPatterns)
	if !plan.rawByteMulti.usable() || !plan.rawByteOrigin.usable() {
		t.Fatal("eligible plan did not compile the tagged filter and origin gate")
	}
	matcher := NewMatcher(rawByteCyrillicPatterns)
	check := func(name, haystack string) {
		t.Helper()
		want, wantOK := refFind(haystack, rawByteCyrillicPatterns)
		for _, route := range []struct {
			name string
			find func(string) (Match, bool)
		}{
			{"direct", plan.findRawByteOrigin},
			{"public", matcher.Find},
		} {
			got, gotOK := route.find(haystack)
			if gotOK != wantOK || gotOK && got != want {
				t.Fatalf("%s/%s: Find(%x) = %+v,%t; want %+v,%t", name, route.name, haystack, got, gotOK, want, wantOK)
			}
		}
	}

	check("absent", strings.Repeat("x", 5<<10))
	check("unrelated-earlier-gate", strings.Repeat("x", 97)+" "+strings.Repeat("x", 5<<10)+rawByteCyrillicPatterns[3])
	check("opaque-before-match", strings.Repeat("x", 5<<10)+"\xff"+rawByteCyrillicPatterns[2])
	for alignment := 0; alignment < 64; alignment++ {
		// U+1C81 is a three-byte rendering of the pattern's initial Д. Its
		// varying source width exercises the gate's maximum-prefix lookback.
		check("width-changing-prefix", strings.Repeat("x", 4096+alignment)+"ᲁЖОН УОТСОН")
	}

	if gate := rawByteOriginGateFor([]string{"абв", "где"}); gate.usable() {
		t.Fatalf("patterns with no common fold-invariant ASCII byte compiled gate %+v", gate)
	}
	if gate := rawByteOriginGateFor([]string{"абв ", "где \xff"}); gate.usable() {
		t.Fatalf("malformed pattern compiled gate %+v", gate)
	}
}

type rawByteEachResult struct {
	match Match
	width int
}

// rawByteReferenceEach is deliberately independent from Matcher.Find and the
// compiled transition plan. It reduces the canonical fold reference used by
// the package tests to the non-overlapping enumeration contract.
func rawByteReferenceEach(haystack string, patterns []string) []rawByteEachResult {
	var out []rawByteEachResult
	for at := 0; at <= len(haystack); {
		match, ok := refFind(haystack[at:], patterns)
		if !ok {
			return out
		}
		match.Start += at
		canon, _ := canonFold(patterns[match.Pattern])
		_, offsets := canonFold(haystack[match.Start:])
		width := offsets[len(canon)]
		out = append(out, rawByteEachResult{match, width})
		at = match.Start + width
	}
	return out
}

func rawByteCheckEach(t *testing.T, matcher *Matcher, haystack string) {
	t.Helper()
	want := rawByteReferenceEach(haystack, matcher.patterns)
	var got []rawByteEachResult
	if complete := matcher.Each(haystack, func(match Match, width int) bool {
		got = append(got, rawByteEachResult{match, width})
		return true
	}); !complete {
		t.Fatal("Each stopped before completing enumeration")
	}
	if len(got) != len(want) {
		t.Fatalf("Each(%x) returned %d matches, want %d: got=%+v want=%+v", haystack, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Each(%x) match %d = %+v, want %+v", haystack, i, got[i], want[i])
		}
	}
}

// TestRawByteMultiAnchorFindOrderingAndTail exercises the first-result view of
// the shared tagged scan. It keeps a duplicate fold-equivalent literal so the
// exact replay, rather than tag iteration order, must select the lowest ID.
func TestRawByteMultiAnchorFindOrderingAndTail(t *testing.T) {
	patterns := append(append([]string(nil), rawByteCyrillicPatterns...), strings.ToLower(rawByteCyrillicPatterns[0]))
	matcher := NewMatcher(patterns)
	if !matcher.plan.rawByteMulti.usable() {
		t.Fatal("eligible Find plan did not compile a raw multi-anchor filter")
	}

	check := func(name, haystack string) {
		t.Helper()
		got, gotOK := matcher.Find(haystack)
		want, wantOK := refFind(haystack, patterns)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("%s: Find(%x) = %+v,%t; want %+v,%t", name, haystack, got, gotOK, want, wantOK)
		}
	}

	// Shift the final literal through every vector-tail alignment. The prefix
	// is long enough to take the VBMI scan before its scalar tail reaches the
	// candidate; the duplicate pattern checks the lowest-ID tie at that tail.
	for alignment := 0; alignment < 64; alignment++ {
		check("tail", strings.Repeat("x", 128+alignment)+rawByteCyrillicPatterns[0])
	}

	check("earlier-start-wins", rawByteCyrillicPatterns[3]+" x "+rawByteCyrillicPatterns[0])
	check("opaque-before-match", strings.Repeat("x", 91)+"\xff"+rawByteCyrillicPatterns[2])
	// U+1C81 is the three-byte simple-fold spelling of the initial Д. The
	// compiled start offsets may nominate extras, but exact raw-plan replay must
	// still return the same ordinary byte offset as the reference.
	check("width-changing-before-anchor", strings.Repeat("x", 77)+"ᲁжон уотсон")
}

func TestRawByteMultiAnchorEnumeration(t *testing.T) {
	matcher := NewMatcher(rawByteCyrillicPatterns)
	if !matcher.plan.hasRawByteTokenPlan() || !matcher.plan.rawByteMulti.usable() {
		t.Fatalf("eligible plan did not compile raw multi-anchor state: raw=%t multi=%t", matcher.plan.hasRawByteTokenPlan(), matcher.plan.rawByteMulti.usable())
	}

	// Sweep every vector tail alignment. The second occurrence forces a resume
	// after a nonzero width, while the last one makes the selected anchor cross
	// from a vector block into the scalar tail.
	for alignment := 0; alignment < 64; alignment++ {
		haystack := rawByteCyrillicPatterns[0] + strings.Repeat("x", alignment) + rawByteCyrillicPatterns[2]
		rawByteCheckEach(t, matcher, haystack)
	}

	for _, haystack := range []string{
		"ᲁжон уотсон " + rawByteCyrillicPatterns[2], // width-changing Д form before an anchor.
		strings.Repeat("x", 71) + "\xff" + rawByteCyrillicPatterns[3],
		strings.Repeat("ж", 83) + rawByteCyrillicPatterns[4],
		strings.Repeat("x", 19) + rawByteCyrillicPatterns[1] + rawByteCyrillicPatterns[0],
	} {
		rawByteCheckEach(t, matcher, haystack)
	}

	rng := rand.New(rand.NewPCG(20260826, 3))
	units := []string{"x", " ", "Д", "д", "ᲁ", "Ж", "ж", "Ш", "ш", "О", "ᲂ", "о", "Н", "н", "И", "и", "€", "\xff", "\x80"}
	for iteration := 0; iteration < 512; iteration++ {
		var haystack strings.Builder
		for range 96 {
			haystack.WriteString(units[rng.IntN(len(units))])
		}
		for i := 0; i < iteration%5; i++ {
			haystack.WriteString(rawByteCyrillicPatterns[rng.IntN(len(rawByteCyrillicPatterns))])
			haystack.WriteString(" xx ")
		}
		rawByteCheckEach(t, matcher, haystack.String())
	}
}

func checkRawByteMultiReference(t *testing.T, matcher *Matcher, patterns []string, haystack string) {
	t.Helper()
	want, wantOK := refFind(haystack, patterns)
	got, gotOK := matcher.Find(haystack)
	if gotOK != wantOK || gotOK && got != want {
		t.Fatalf("Find mismatch: Find(%x, %q) = %+v,%t; want %+v,%t", haystack, patterns, got, gotOK, want, wantOK)
	}

	wantEach := refEach(haystack, patterns)
	var gotEach []refEachResult
	if complete := matcher.Each(haystack, func(match Match, width int) bool {
		gotEach = append(gotEach, refEachResult{match: match, width: width})
		return true
	}); !complete {
		t.Fatalf("Each mismatch: stopped before completing %x", haystack)
	}
	if len(gotEach) != len(wantEach) {
		t.Fatalf("Each mismatch: Each(%x, %q) returned %d results, want %d: got=%+v want=%+v",
			haystack, patterns, len(gotEach), len(wantEach), gotEach, wantEach)
	}
	for i := range wantEach {
		if gotEach[i] != wantEach[i] {
			t.Fatalf("Each mismatch: Each(%x, %q) result %d = %+v, want %+v",
				haystack, patterns, i, gotEach[i], wantEach[i])
		}
	}
}

// TestRawByteMultiSharedPrefixDifferential checks the public shared-plan
// routes against the independent per-pattern reference for tied prefixes,
// complete non-overlapping enumeration, and seeded shared-prefix plans.
func TestRawByteMultiSharedPrefixDifferential(t *testing.T) {
	patterns := []string{"σοφος", "σοφο"}
	matcher := NewMatcher(patterns)
	if !matcher.plan.rawByteMulti.usable() {
		t.Fatal("test precondition: Greek tied-prefix plan did not select rawByteMulti")
	}
	if got, ok := refFind("σοφος", patterns); !ok || got != (Match{Pattern: 0, Start: 0}) {
		t.Fatalf("reference precondition: Find = %+v,%t, want pattern 0 at byte 0", got, ok)
	}
	if want := refEach("σοφος", patterns); len(want) != 1 ||
		want[0] != (refEachResult{match: Match{Pattern: 0, Start: 0}, width: len("σοφος")}) {
		t.Fatalf("reference precondition: Each = %+v, want pattern 0 at byte 0 with width %d", want, len("σοφος"))
	}
	checkRawByteMultiReference(t, matcher, patterns, "σοφος")

	// Kelvin's three-byte fold spelling must keep the selected match's source
	// width, not the shorter terminal's or the pattern's byte length.
	widthPatterns := []string{"σοφοkα", "σοφο"}
	widthHaystack := "σοφοKα"
	widthMatcher := NewMatcher(widthPatterns)
	if !widthMatcher.plan.rawByteMulti.usable() {
		t.Fatal("test precondition: width-changing tie plan did not select rawByteMulti")
	}
	if want := refEach(widthHaystack, widthPatterns); len(want) != 1 ||
		want[0] != (refEachResult{match: Match{Pattern: 0, Start: 0}, width: len(widthHaystack)}) {
		t.Fatalf("reference precondition: Each = %+v, want pattern 0 with source width %d", want, len(widthHaystack))
	}
	checkRawByteMultiReference(t, widthMatcher, widthPatterns, widthHaystack)

	// The long Find route uses the exact common-byte origin gate, while Each
	// continues to use the shared tagged scan. Both must keep the same tie.
	originPatterns := []string{"σοφο!ς", "σοφο!"}
	originHaystack := strings.Repeat("x", 4096) + "σοφο!ς"
	originMatcher := NewMatcher(originPatterns)
	if !originMatcher.plan.rawByteMulti.usable() || !originMatcher.plan.rawByteOrigin.usable() {
		t.Fatal("test precondition: long tied-prefix plan did not select both raw-byte gates")
	}
	if got, ok := refFind(originHaystack, originPatterns); !ok || got != (Match{Pattern: 0, Start: 4096}) {
		t.Fatalf("reference precondition: long Find = %+v,%t, want pattern 0 at byte 4096", got, ok)
	}
	if want := refEach(originHaystack, originPatterns); len(want) != 1 ||
		want[0] != (refEachResult{match: Match{Pattern: 0, Start: 4096}, width: len("σοφο!ς")}) {
		t.Fatalf("reference precondition: long Each = %+v, want pattern 0 with source width %d", want, len("σοφο!ς"))
	}
	checkRawByteMultiReference(t, originMatcher, originPatterns, originHaystack)

	// A pending early match must not hide a later non-overlapping result when
	// maxOffset keeps the shared scan open until it reaches the haystack tail.
	tailPatterns := []string{"σοφος", "σοφο", "σοφοαβγδεζηθλν"}
	tailMatcher := NewMatcher(tailPatterns)
	if !tailMatcher.plan.rawByteMulti.usable() || int(tailMatcher.plan.rawByteMulti.maxOffset) <= len("σοφο") {
		t.Fatal("test precondition: tail plan did not retain a wide rawByteMulti lookahead")
	}
	checkRawByteMultiReference(t, tailMatcher, tailPatterns, "σοφοxσοφος")

	pool := []string{"σοφος", "σοφο", "σοφοα", "σοφοβ", "σοφογ", "σοφοδε", "σοφοζκ", "σοφολμ"}
	units := []string{"x", " ", "σ", "Σ", "ς", "ο", "Ο", "φ", "Φ", "α", "β", "γ", "δ", "ζ", "λ", "μ", "ᲇ", "ᲂ", "ᲁ", "€", "\xff", "\x80"}
	rng := rand.New(rand.NewPCG(20260615, 83))
	accepted := 0
	for attempt := 0; attempt < 1024 && accepted < 128; attempt++ {
		patterns := []string{pool[0], pool[1]}
		for extra := rng.IntN(4); extra > 0; extra-- {
			candidate := pool[2+rng.IntN(len(pool)-2)]
			found := false
			for _, pattern := range patterns {
				found = found || pattern == candidate
			}
			if !found {
				patterns = append(patterns, candidate)
			}
		}
		rng.Shuffle(len(patterns), func(i, j int) { patterns[i], patterns[j] = patterns[j], patterns[i] })
		matcher := NewMatcher(patterns)
		if !matcher.plan.rawByteMulti.usable() {
			continue
		}
		accepted++

		var haystack strings.Builder
		for range rng.IntN(8) {
			haystack.WriteString(units[rng.IntN(len(units))])
		}
		if attempt%4 == 0 {
			haystack.WriteString(pool[rng.IntN(len(pool))])
		} else {
			haystack.WriteString(pool[0])
		}
		if attempt%3 == 0 {
			haystack.WriteString("x")
			haystack.WriteString(pool[rng.IntN(len(pool))])
		}
		checkRawByteMultiReference(t, matcher, patterns, haystack.String())
	}
	if accepted < 128 {
		t.Fatalf("test precondition: generated only %d usable shared-prefix rawByteMulti plans", accepted)
	}
}

// TestRawByteMultiVariableWidthEOFDifferential checks that alternative fold
// offsets near EOF do not discard a usable shorter spelling before exact replay.
func TestRawByteMultiVariableWidthEOFDifferential(t *testing.T) {
	check := func(name string, patterns []string, haystack string, origin bool) {
		t.Run(name, func(t *testing.T) {
			matcher := NewMatcher(patterns)
			if !matcher.plan.rawByteMulti.usable() {
				t.Fatalf("test precondition: patterns did not select rawByteMulti: %q", patterns)
			}
			if origin && !matcher.plan.rawByteOrigin.usable() {
				t.Fatalf("test precondition: patterns did not select rawByteOrigin: %q", patterns)
			}
			want, wantOK := refFind(haystack, patterns)
			got, gotOK := matcher.Find(haystack)
			if gotOK != wantOK || gotOK && got != want {
				t.Errorf("Find mismatch: case %s patterns %q gave %+v,%t; want %+v,%t", name, patterns, got, gotOK, want, wantOK)
			}

			wantEach := refEach(haystack, patterns)
			var gotEach []refEachResult
			complete := matcher.Each(haystack, func(match Match, width int) bool {
				gotEach = append(gotEach, refEachResult{match: match, width: width})
				return true
			})
			if !complete || len(gotEach) != len(wantEach) {
				t.Errorf("Each mismatch: case %s patterns %q gave %+v, complete=%t; want %+v", name, patterns, gotEach, complete, wantEach)
			} else {
				for i := range wantEach {
					if gotEach[i] != wantEach[i] {
						t.Errorf("Each mismatch: case %s patterns %q result %d = %+v; want %+v", name, patterns, i, gotEach[i], wantEach[i])
					}
				}
			}
		})
	}

	variablePatterns := []string{"φφφαφ!kβ", "φφφαφ!kβο"}
	check("short-eof", variablePatterns, "φφφαφ!kβ", false)
	check("two-byte-tail-control", variablePatterns, "φφφαφ!kβxx", false)
	check("long-origin-eof", variablePatterns, strings.Repeat("x", 4096)+"φφφαφ!kβ", true)
	check("truncated-final-pair", variablePatterns, "φφφαφ!k", false)
	check("opaque-middle", variablePatterns, "φφφαφ!\xffβ", false)
	check("opaque-prefix", variablePatterns, strings.Repeat("x", 64)+"\xffφφφαφ!kβ", false)

	// Fold-equivalent patterns at one start still choose the lowest ID when
	// alternate confirmation and guard widths extend past EOF.
	tiedPatterns := []string{"φφφαφ!kβ", "ΦΦΦΑΦ!KΒ", "φφφαφ!kβο"}
	check("same-start-tie", tiedPatterns, "φφφαφ!kβ", false)

	// A shorter later-start candidate must not hide the anchored literal whose
	// final guard alternative is outside the input.
	earlierPatterns := []string{"φφφαφςο", "φφφαφ!", "xφφφαφ!kβ", "φφφαφ"}
	check("earlier-start", earlierPatterns, "xφφφαφ!kβ", false)
	check("earlier-start-control", []string{"φφφαφ!kβ", "φφαφ"}, "φφφαφ!kβ", false)

	// Exercise independent Kelvin-width choices at both k positions.
	mixedPatterns := []string{"φαkφkβ", "φαkφkβο"}
	for _, first := range []string{"k", "K", "K"} {
		for _, second := range []string{"k", "K", "K"} {
			check("mixed-k-"+first+"-"+second, mixedPatterns, "φα"+first+"φ"+second+"β", false)
		}
	}

	// Long single-byte gaps put a usable alternative guard near EOF while other
	// offsets extend beyond it. Sweep vector and scalar-tail alignments.
	longPattern := "φφφαφ" + strings.Repeat("!", 64) + "kβ"
	longPatterns := []string{longPattern, longPattern + "ο"}
	check("vector-block-at-zero", longPatterns, longPattern, false)
	for _, alignment := range []int{0, 1, 31, 63} {
		haystack := strings.Repeat("x", 64+alignment) + longPattern
		check("vector-tail", longPatterns, haystack, false)
	}
}

func TestRawByteMultiAnchorSkipNeverPassesAConfirmedTag(t *testing.T) {
	plan := newSearchPlan(rawByteCyrillicPatterns)
	filter := &plan.rawByteMulti
	if !filter.usable() {
		t.Fatal("eligible plan did not compile a raw multi-anchor filter")
	}
	for alignment := 0; alignment < 64; alignment++ {
		haystack := strings.Repeat("x", alignment+128) + rawByteCyrillicPatterns[alignment%len(rawByteCyrillicPatterns)] + strings.Repeat("x", 96)
		for at := range haystack {
			skipped, _ := rawByteMultiAnchorSkipBytes(haystack, at, filter)
			if skipped < 0 || at+skipped > len(haystack) {
				t.Fatalf("alignment %d at %d: invalid skip %d", alignment, at, skipped)
			}
			for candidate := at; candidate < at+skipped; candidate++ {
				if tags := filter.tagsAt(haystack, candidate, 0xff); tags != 0 {
					t.Fatalf("alignment %d at %d: skip %d passed confirmed tag %08b at %d", alignment, at, skipped, tags, candidate)
				}
			}
		}
	}
}

func TestRawByteMultiAnchorScalarScreenUnionsConfirmationGroups(t *testing.T) {
	pair := func(first, second byte) rawByteMultiAnchorPairSet {
		return rawByteMultiAnchorPairSet{
			pairs: [rawByteMultiAnchorForms]uint16{uint16(first) | uint16(second)<<8},
			n:     1,
		}
	}
	const (
		aliasTag = byte(1 << iota)
		matchTag
	)
	aliasPrimary := pair(0x01, 0x02) // Low six bits alias "AB".
	matchPrimary := pair('A', 'B')
	aliasConfirm := pair('C', 'D')
	matchConfirm := pair('E', 'F')
	guard := pair('G', 'H')
	filter := rawByteMultiAnchorFilter{
		confirmOffset: [rawByteMultiAnchorConfirmGroups]uint8{2, 4},
		confirmN:      2,
		valid:         1,
	}
	rawByteMultiAnchorAddTable(&filter.first, aliasPrimary, false, aliasTag)
	rawByteMultiAnchorAddTable(&filter.second, aliasPrimary, true, aliasTag)
	rawByteMultiAnchorAddTable(&filter.first, matchPrimary, false, matchTag)
	rawByteMultiAnchorAddTable(&filter.second, matchPrimary, true, matchTag)
	rawByteMultiAnchorAddTable(&filter.confirmFirst[0], aliasConfirm, false, aliasTag)
	rawByteMultiAnchorAddTable(&filter.confirmSecond[0], aliasConfirm, true, aliasTag)
	rawByteMultiAnchorAddTable(&filter.confirmFirst[1], matchConfirm, false, matchTag)
	rawByteMultiAnchorAddTable(&filter.confirmSecond[1], matchConfirm, true, matchTag)
	filter.anchors[0] = rawByteMultiAnchor{
		primary:       aliasPrimary,
		confirm:       aliasConfirm,
		guard:         guard,
		starts:        [rawByteMultiAnchorStartOffsets]uint8{0},
		confirmOffset: [rawByteMultiAnchorConfirmGroups]uint8{2},
		guardOffset:   [rawByteMultiAnchorStartOffsets]uint8{6},
		startN:        1,
		confirmN:      1,
		guardN:        1,
	}
	filter.anchors[1] = rawByteMultiAnchor{
		primary:       matchPrimary,
		confirm:       matchConfirm,
		guard:         guard,
		starts:        [rawByteMultiAnchorStartOffsets]uint8{0},
		confirmOffset: [rawByteMultiAnchorConfirmGroups]uint8{4},
		guardOffset:   [rawByteMultiAnchorStartOffsets]uint8{6},
		startN:        1,
		confirmN:      1,
		guardN:        1,
	}

	haystack := "ABCDEFGH"
	skipped, candidates := rawByteMultiAnchorSkipScalar(haystack, 0, &filter)
	if skipped != 0 || candidates != aliasTag|matchTag {
		t.Fatalf("scalar screen = (%d, %08b), want (0, %08b)", skipped, candidates, aliasTag|matchTag)
	}
	if tags := filter.tagsAt(haystack, 0, candidates); tags != matchTag {
		t.Fatalf("exact tags = %08b, want %08b", tags, matchTag)
	}
}

// TestRawByteMultiAnchorScalarScreenNeverPassesConfirmedTag proves the table
// screen used after a vector tail (and on portable hosts) can stop early on an
// alias but never skips an exact tagged anchor. The later tagsAt replay remains
// the match authority.
func TestRawByteMultiAnchorScalarScreenNeverPassesConfirmedTag(t *testing.T) {
	plan := newSearchPlan(rawByteCyrillicPatterns)
	filter := &plan.rawByteMulti
	if !filter.usable() {
		t.Fatal("eligible plan did not compile a raw multi-anchor filter")
	}
	inputs := []string{
		strings.Repeat("x", 257) + rawByteCyrillicPatterns[0],
		strings.Repeat("x", 63) + "\xff\x80" + rawByteCyrillicPatterns[1],
		strings.Repeat("x", 17) + "ᲁжон уотсон",
	}
	rng := rand.New(rand.NewPCG(20260907, 8))
	for i := 0; i < 256; i++ {
		buf := make([]byte, i)
		for j := range buf {
			buf[j] = byte(rng.Uint32())
		}
		inputs = append(inputs, string(buf))
	}
	for inputIndex, haystack := range inputs {
		for at := range haystack {
			skipped, _ := rawByteMultiAnchorSkipScalar(haystack, at, filter)
			if skipped < 0 || at+skipped > len(haystack) {
				t.Fatalf("input %d at %d: invalid scalar skip %d", inputIndex, at, skipped)
			}
			for candidate := at; candidate < at+skipped; candidate++ {
				if tags := filter.tagsAt(haystack, candidate, 0xff); tags != 0 {
					t.Fatalf("input %d at %d: scalar skip %d passed confirmed tag %08b at %d", inputIndex, at, skipped, tags, candidate)
				}
			}
		}
	}
}

func TestRawByteTokenPlanFallsBackForUnsupportedPlans(t *testing.T) {
	for _, patterns := range [][]string{
		{"Шерлок"},
		{"Шерлок", "Δelta", "éclair"},
		{"\xffШерлок", "Джон"},
	} {
		matcher := NewMatcher(patterns)
		if matcher.plan.hasRawByteTokenPlan() {
			t.Fatalf("unsupported plan %q retained raw-byte tokens", patterns)
		}
		input := "… δELTA … ÉCLAIR … ШЕРЛОК \xffДЖОН"
		got, gotOK := matcher.Find(input)
		want, wantOK := refFind(input, patterns)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("Find(%q, %x) = %+v,%t; want %+v,%t", patterns, input, got, gotOK, want, wantOK)
		}
	}
}

func TestRawByteTokenPlanFindAllocatesNothingAfterConstruction(t *testing.T) {
	matcher := NewMatcher(rawByteCyrillicPatterns)
	haystack := rawByteFalseCandidatesAtLeast(64, 64<<10)
	if got, ok := matcher.Find(haystack); ok || got != (Match{}) {
		t.Fatalf("setup Find = %+v,%t", got, ok)
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = matcher.Find(haystack) }); allocs != 0 {
		t.Fatalf("reused raw-byte Find allocations = %g, want 0", allocs)
	}
}
