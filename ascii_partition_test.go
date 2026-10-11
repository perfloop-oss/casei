package casei

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// asciiPartitionPatterns deliberately leave the root filter too broad while
// retaining a complete, bounded ASCII triple projection. Nine ASCII literals
// disable the eight-entry pair projection; the Cyrillic roots make the regular
// mixed-byte filter unusable. This is the shape where one high byte otherwise
// poisons the remainder of an otherwise block-friendly scan.
func asciiPartitionPatterns() []string {
	return []string{
		"abc0", "abc1", "def0", "def1", "ghi0", "ghi1", "jkl0", "jkl1", "mno0",
		"абв0", "где1", "жзи2", "йкл3", "мно4", "прс5", "туф6", "хцч7", "шщъ8", "ыьэ9",
	}
}

func decodedPlanFind(p *searchPlan, haystack string) (Match, bool) {
	var inlineStarts [256]int
	starts := inlineStarts[:]
	if p.maxUnits > len(starts) {
		starts = make([]int, p.maxUnits)
	}
	return p.findUnfilteredWithStarts(haystack, starts)
}

func TestASCIIPartitionPlanAdmission(t *testing.T) {
	plan := newSearchPlan(asciiPartitionPatterns(), false)
	if runtimeVectorBits() != 512 {
		t.Skipf("ASCII partition is runtime-gated to AVX-512; vector width %d", runtimeVectorBits())
	}
	if !plan.asciiPartitionUsable() {
		t.Fatalf("plan did not retain the partition route: triples=%d complete=%t shufti=%t pair=%t filter=%t",
			plan.asciiTriples.n, plan.asciiTriplesComplete, plan.asciiTriples.shufti.usable(),
			plan.asciiPairAnchors.usable(), plan.filter.usable())
	}
	if plan.maxBytes == 0 || plan.maxBytes < plan.maxUnits {
		t.Fatalf("maximum source width = %d for %d units", plan.maxBytes, plan.maxUnits)
	}
	if plan.filter.usable() || plan.asciiPairAnchors.usable() || plan.rawByteMulti.usable() {
		t.Fatalf("fixture entered a route outside the intended gap: filter=%t pair=%t raw=%t", plan.filter.usable(), plan.asciiPairAnchors.usable(), plan.rawByteMulti.usable())
	}
}

func TestASCIIPartitionDifferential(t *testing.T) {
	patterns := asciiPartitionPatterns()
	plan := newSearchPlan(patterns, false)
	matcher := NewMatcher(patterns)
	rng := rand.New(rand.NewPCG(20260829, 17))
	units := []string{
		"x", " ", "a", "A", "b", "c", "0", "Д", "д", "Ж", "ж", "€", "ᲁ",
		"\x00", "\xff", "\x80", "\xc2\x80", "\xe2\x84\xaa",
	}
	for iteration := 0; iteration < 2000; iteration++ {
		var input strings.Builder
		for range 1 + rng.IntN(160) {
			input.WriteString(units[rng.IntN(len(units))])
		}
		if iteration%3 == 0 {
			input.WriteString(patterns[rng.IntN(len(patterns))])
		}
		haystack := input.String()
		want, wantOK := decodedPlanFind(plan, haystack)
		got, gotOK := matcher.Find(haystack)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("iteration %d Find(%x) = %+v,%t; decoded = %+v,%t", iteration, haystack, got, gotOK, want, wantOK)
		}
		ref, refOK := refFind(haystack, patterns)
		if gotOK != refOK || gotOK && got != ref {
			t.Fatalf("iteration %d Find(%x) = %+v,%t; reference = %+v,%t", iteration, haystack, got, gotOK, ref, refOK)
		}
	}
}

func TestASCIIPartitionPositionDensityAndBoundaries(t *testing.T) {
	patterns := asciiPartitionPatterns()
	plan := newSearchPlan(patterns, false)
	matcher := NewMatcher(patterns)
	for _, size := range []int{1, 63, 64, 127, 1024, 8192} {
		for _, gap := range []int{1, 7, 31, 127, 511} {
			var input strings.Builder
			for at := 0; at < size; at += gap {
				input.WriteString(strings.Repeat("x", min(gap, size-at)))
				input.WriteString("€")
			}
			haystack := input.String()
			want, wantOK := refFind(haystack, patterns)
			if got, gotOK := matcher.Find(haystack); gotOK != wantOK || gotOK && got != want {
				t.Fatalf("size %d gap %d Find = %+v,%t; want %+v,%t", size, gap, got, gotOK, want, wantOK)
			}
		}
	}

	highAt := 64
	spanEnd := highAt + len("€")
	boundaryStart := spanEnd + plan.maxBytes - 1
	boundary := []byte(strings.Repeat("x", boundaryStart+len("abc0")+32))
	copy(boundary[highAt:], "€")
	copy(boundary[boundaryStart:], "abc0")
	secondHighAt := 128
	interWindowStart := secondHighAt - plan.maxBytes - 1
	interWindow := []byte(strings.Repeat("x", 256))
	copy(interWindow[highAt:], "€")
	copy(interWindow[secondHighAt:], "€")
	copy(interWindow[interWindowStart:], "abc0")

	for _, haystack := range []string{
		"abc0" + "€" + strings.Repeat("x", 64),
		strings.Repeat("x", 64) + "€" + "abc0",
		strings.Repeat("x", 64) + "ᲁ" + "abc0",
		strings.Repeat("x", 64) + "\xff" + "abc0",
		strings.Repeat("x", 64) + "\x80" + "abc0",
		strings.Repeat("x", 64) + "\xc2\x80" + "abc0",
		strings.Repeat("x", 64) + "\x00" + "abc0",
		strings.Repeat("x", 64) + "abc0" + "€" + "def1",
		strings.Repeat("x", 64) + "€" + strings.Repeat("x", 64) + "\xff" + "ghi2",
		strings.Repeat("x", 76) + "jkl0" + strings.Repeat("x", 5) + "\xff" + strings.Repeat("x", 512),
		string(boundary),
		string(interWindow),
	} {
		want, wantOK := refFind(haystack, patterns)
		if got, gotOK := matcher.Find(haystack); gotOK != wantOK || gotOK && got != want {
			t.Fatalf("boundary Find(%x) = %+v,%t; want %+v,%t", haystack, got, gotOK, want, wantOK)
		}
	}
}

func TestASCIIPartitionWidthChangingBoundaries(t *testing.T) {
	patterns := append(asciiPartitionPatterns(), "абвkK0", "гдеsſ1")
	plan := newSearchPlan(patterns, false)
	if !plan.asciiPartitionUsable() {
		t.Skipf("width-changing fixture is not admitted on vector width %d", runtimeVectorBits())
	}
	if plan.maxBytes <= plan.maxUnits {
		t.Fatalf("width-changing fixture did not widen windows: maxBytes=%d maxUnits=%d", plan.maxBytes, plan.maxUnits)
	}
	matcher := NewMatcher(patterns)
	for _, haystack := range []string{
		strings.Repeat("x", 73) + "абвKK0" + strings.Repeat("x", 73),
		strings.Repeat("x", 73) + "гдеSſ1" + strings.Repeat("x", 73),
		strings.Repeat("x", 73) + "абвkK0" + "€" + "гдеsſ1",
	} {
		want, wantOK := refFind(haystack, patterns)
		if got, gotOK := matcher.Find(haystack); gotOK != wantOK || gotOK && got != want {
			t.Fatalf("width-changing Find(%x) = %+v,%t; want %+v,%t; maxBytes=%d", haystack, got, gotOK, want, wantOK, plan.maxBytes)
		}
	}
}

func TestASCIIPartitionEachContract(t *testing.T) {
	patterns := append(asciiPartitionPatterns(), "abc0")
	matcher := NewMatcher(patterns)
	for _, haystack := range []string{
		"abc0xx€ABC1xxdef0",
		strings.Repeat("x", 91) + "€" + "ghi1" + strings.Repeat("x", 73) + "\xff" + "jkl0",
		strings.Repeat("x", 512) + "ᲁ" + "mno0" + strings.Repeat("x", 512) + "abc1",
	} {
		want := refEach(haystack, patterns)
		var got []refEachResult
		if complete := matcher.Each(haystack, func(match Match, width int) bool {
			got = append(got, refEachResult{match: match, width: width})
			return true
		}); !complete {
			t.Fatalf("Each(%x) stopped early", haystack)
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
}

func TestASCIIPartitionReportsWorkSplit(t *testing.T) {
	patterns := asciiPartitionPatterns()
	plan := newSearchPlan(patterns, false)
	if !plan.asciiPartitionUsable() {
		t.Skipf("ASCII partition is runtime-gated to AVX-512; vector width %d", runtimeVectorBits())
	}
	var input strings.Builder
	for input.Len()+512+len("€") <= 1<<16 {
		input.WriteString(strings.Repeat("x", 512))
		input.WriteString("€")
	}
	input.WriteString(strings.Repeat("x", 1<<16-input.Len()))
	haystack := input.String()
	var stats asciiPartitionStats
	if _, ok := plan.findUnfilteredWithStats(haystack, &stats); ok {
		t.Fatal("setup unexpectedly matched")
	}
	if stats.fallbackEntries != 0 || stats.decodedWindows == 0 || stats.decodedWindowBytes == 0 {
		t.Fatalf("partition diagnostics = %+v", stats)
	}
	if stats.highBytes == 0 || stats.firstExceptional <= 0 || stats.asciiCandidateBytes <= stats.decodedWindowBytes {
		t.Fatalf("partition did not expose sparse ASCII work: %+v", stats)
	}

	fallbackHaystack := strings.Repeat("x", 256) + "€"
	stats = asciiPartitionStats{}
	if _, ok := plan.findUnfilteredWithStats(fallbackHaystack, &stats); ok {
		t.Fatal("fallback setup unexpectedly matched")
	}
	if stats.fallbackEntries != 1 || stats.firstExceptional < 0 || stats.decodedWindows != 0 {
		t.Fatalf("tail fallback diagnostics = %+v", stats)
	}
}

func TestASCIIPartitionRejectsDenseExceptionalInput(t *testing.T) {
	if runtimeVectorBits() != 512 {
		t.Skipf("ASCII partition is runtime-gated to AVX-512; vector width %d", runtimeVectorBits())
	}
	plan := newSearchPlan(asciiPartitionPatterns(), false)
	for _, tc := range []struct {
		name     string
		interval int
		value    byte
	}{
		{name: "high", interval: 32, value: 0xe2},
		{name: "nul", interval: 128, value: 0},
	} {
		input := []byte(strings.Repeat("x", 1<<20))
		for at := 0; at < len(input); at += tc.interval {
			input[at] = tc.value
		}
		var stats asciiPartitionStats
		if _, ok := plan.findUnfilteredWithStats(string(input), &stats); ok {
			t.Fatalf("%s setup unexpectedly matched", tc.name)
		}
		if stats.fallbackEntries != 1 || stats.decodedWindows != 0 {
			t.Fatalf("%s dense input was partitioned: %+v", tc.name, stats)
		}
	}

	lateDense := []byte(strings.Repeat("x", 1<<20))
	copy(lateDense[0:3], "€")
	for at := 4096; at < len(lateDense)-2; at += 32 {
		copy(lateDense[at:at+3], "€")
	}
	var stats asciiPartitionStats
	if _, ok := plan.findUnfilteredWithStats(string(lateDense), &stats); ok {
		t.Fatal("late dense setup unexpectedly matched")
	}
	if stats.fallbackEntries != 1 {
		t.Fatalf("late dense input was not rejected while discovering spans: %+v", stats)
	}

	contiguous := []byte(strings.Repeat("x", 1<<20))
	copy(contiguous[0:3], "€")
	copy(contiguous[4096:], strings.Repeat("€", 1<<16))
	stats = asciiPartitionStats{}
	if _, ok := plan.findUnfilteredWithStats(string(contiguous), &stats); ok {
		t.Fatal("contiguous dense setup unexpectedly matched")
	}
	if stats.fallbackEntries != 1 {
		t.Fatalf("contiguous dense input was not rejected early: %+v", stats)
	}

	// A sparse first span must not make the partitioner spend a decoded window
	// before it discovers a later contiguous valid-UTF-8 region is dense.
	sparsePrefixDenseSuffix := []byte(strings.Repeat("x", 1<<20))
	copy(sparsePrefixDenseSuffix[64:], "€")
	copy(sparsePrefixDenseSuffix[4096:], strings.Repeat("€", 1<<16))
	stats = asciiPartitionStats{}
	if _, ok := plan.findUnfilteredWithStats(string(sparsePrefixDenseSuffix), &stats); ok {
		t.Fatal("sparse-prefix setup unexpectedly matched")
	}
	if stats.fallbackEntries != 1 || stats.decodedWindows != 0 {
		t.Fatalf("sparse-prefix dense input was partitioned before fallback: %+v", stats)
	}

	sampledDenseRegion := []byte(strings.Repeat("x", 1<<20))
	copy(sampledDenseRegion[64:], "€")
	for at := 200000; at+3 < 700000; at += 8 {
		copy(sampledDenseRegion[at:], "€")
	}
	if asciiPartitionSparseEnough(string(sampledDenseRegion), 64) {
		t.Fatal("sampled dense region passed admission")
	}
	stats = asciiPartitionStats{}
	if _, ok := plan.findUnfilteredWithStats(string(sampledDenseRegion), &stats); ok {
		t.Fatal("sampled dense setup unexpectedly matched")
	}
	if stats.fallbackEntries != 1 || stats.decodedWindows != 0 {
		t.Fatalf("sampled dense input was admitted before fallback: %+v", stats)
	}

	widePatterns := asciiPartitionPatterns()
	widePatterns[0] = strings.Repeat("q", 100000) + "0"
	widePlan := newSearchPlan(widePatterns, false)
	if !widePlan.asciiPartitionUsable() || widePlan.maxBytes < 100000 {
		t.Fatalf("wide pattern did not retain the partition shape: usable=%t maxBytes=%d", widePlan.asciiPartitionUsable(), widePlan.maxBytes)
	}
	for _, tc := range []struct {
		length int
		highAt int
	}{{length: 256, highAt: 189}, {length: 131072, highAt: 64}} {
		wideHaystack := []byte(strings.Repeat("x", tc.length))
		copy(wideHaystack[tc.highAt:], "€")
		stats = asciiPartitionStats{}
		if _, ok := widePlan.findUnfilteredWithStats(string(wideHaystack), &stats); ok {
			t.Fatalf("wide-pattern setup unexpectedly matched at length %d", tc.length)
		}
		if stats.fallbackEntries != 1 || stats.decodedWindows != 0 {
			t.Fatalf("wide pattern admitted a whole-input decoded window at length %d: %+v", tc.length, stats)
		}
	}

	lateMalformed := []byte(strings.Repeat("x", 1<<16))
	copy(lateMalformed[0:3], "€")
	lateMalformed[4096] = 0xff
	copy(lateMalformed[4100:], "jkl0")
	matcher := NewMatcher(asciiPartitionPatterns())
	want, wantOK := refFind(string(lateMalformed), asciiPartitionPatterns())
	if got, gotOK := matcher.Find(string(lateMalformed)); gotOK != wantOK || gotOK && got != want {
		t.Fatalf("late malformed Find = %+v,%t; want %+v,%t", got, gotOK, want, wantOK)
	}

}

func TestASCIIPartitionKeepsCrossSpanMatchLeftmost(t *testing.T) {
	patterns := append(asciiPartitionPatterns(), "abcdefgh€", "defg")
	matcher := NewMatcher(patterns)
	haystack := strings.Repeat("x", 70) + "abcdefgh€" + strings.Repeat("x", 512)
	want := Match{Pattern: len(asciiPartitionPatterns()), Start: 70}
	if got, ok := matcher.Find(haystack); !ok || got != want {
		t.Fatalf("cross-span Find = %+v,%t; want %+v,true", got, ok, want)
	}
}

func TestASCIIPartitionFindAllocatesNothing(t *testing.T) {
	matcher := NewMatcher(asciiPartitionPatterns())
	haystack := strings.Repeat("x", 64) + "€" + strings.Repeat("x", 64<<10)
	if got, ok := matcher.Find(haystack); ok {
		t.Fatalf("setup Find = %+v,%t", got, ok)
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = matcher.Find(haystack) }); allocs != 0 {
		t.Fatalf("partitioned Find allocations = %g, want 0", allocs)
	}
}

func TestASCIIPartitionRejectsOpaqueContinuationPlans(t *testing.T) {
	plan := newSearchPlan([]string{"\x80abc", "def0", "абв1", "где2", "жзи3", "йкл4", "мно5", "прс6", "туф7", "хцч8"}, false)
	if plan.asciiPartitionUsable() {
		t.Fatal("opaque continuation plan entered the ASCII partition route")
	}
}

func TestASCIIOnlyPartitionAdmission(t *testing.T) {
	plan := newSearchPlan([]string{"Sherlock Holmes"}, false)
	if !plan.asciiOnly || plan.asciiOnlyLong {
		t.Fatalf("letter-only ASCII plan = asciiOnly:%t long:%t", plan.asciiOnly, plan.asciiOnlyLong)
	}
	if plan.maxBytes <= len(plan.singlePayload) {
		t.Fatalf("width-changing ASCII plan did not widen: maxBytes=%d payload=%d", plan.maxBytes, len(plan.singlePayload))
	}
	if runtimeVectorBits() != 512 {
		t.Skipf("ASCII-only partition is runtime-gated to AVX-512; vector width %d", runtimeVectorBits())
	}
	if !plan.asciiOnlyPartitionUsable() {
		t.Fatal("long N=1 ASCII-only partition was not admitted")
	}
	structured := newSearchPlan([]string{"[keys[i%"}, false)
	if !structured.asciiOnly || !structured.asciiOnlyLong || structured.asciiOnlyPartitionUsable() {
		t.Fatalf("structured ASCII plan changed admission: asciiOnly=%t long=%t usable=%t", structured.asciiOnly, structured.asciiOnlyLong, structured.asciiOnlyPartitionUsable())
	}
}

func TestASCIIOnlyPartitionDifferential(t *testing.T) {
	patterns := []string{"Sherlock Holmes"}
	plan := newSearchPlan(patterns, false)
	matcher := NewMatcher(patterns)
	maxBytes := plan.maxBytes
	cases := []string{
		strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "ſherlocK Holmeſ" + strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "SherlocK Holmes" + strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "€ſK" + strings.Repeat("x", 5000) + "sHERLOCK hOLMES",
		strings.Repeat("x", 5000) + "€" + strings.Repeat("x", maxBytes) + "ſherlocK Holmeſ" + strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "€" + strings.Repeat("x", 2*maxBytes) + "K" + strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "€" + strings.Repeat("x", 5000) + "SHERLOCK HOLMES" + strings.Repeat("x", 5000) + "€",
		strings.Repeat("x", 5000) + "€" + strings.Repeat("x", 5000) + "\xff" + strings.Repeat("x", 5000),
		strings.Repeat("x", 5000) + "€" + strings.Repeat("x", 5000) + "\x00" + strings.Repeat("x", 5000),
		strings.Repeat("x", 1023) + "€" + strings.Repeat("x", 5000),
		strings.Repeat("x", 1024) + "€" + strings.Repeat("x", 5000),
	}
	for i, haystack := range cases {
		want, wantOK := decodedPlanFind(plan, haystack)
		got, gotOK := matcher.Find(haystack)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("case %d Find = %+v,%t; decoded = %+v,%t", i, got, gotOK, want, wantOK)
		}
		ref, refOK := refFind(haystack, patterns)
		if gotOK != refOK || gotOK && got != ref {
			t.Fatalf("case %d Find = %+v,%t; reference = %+v,%t", i, got, gotOK, ref, refOK)
		}
	}
}

func TestASCIIOnlyPartitionWorkSplitAndFallback(t *testing.T) {
	if runtimeVectorBits() != 512 {
		t.Skipf("ASCII-only partition is runtime-gated to AVX-512; vector width %d", runtimeVectorBits())
	}
	plan := newSearchPlan([]string{"Sherlock Holmes"}, false)
	input := []byte(strings.Repeat("x", 1<<16))
	copy(input[4096:], "ſK")
	haystack := string(input)
	_, _, firstHigh := plan.findASCIIOnlyAnchorWithHigh(haystack, plan.singlePayload)
	if firstHigh != 4096 {
		t.Fatalf("first exceptional byte = %d, want 4096", firstHigh)
	}
	var stats asciiPartitionStats
	if _, ok := plan.findPartitionedASCIIWithStats(haystack, firstHigh, &stats); ok {
		t.Fatal("partition setup unexpectedly matched")
	}
	if stats.decodedWindows == 0 || stats.decodedWindowBytes == 0 || stats.asciiCandidateBytes <= stats.decodedWindowBytes {
		t.Fatalf("partition work split = %+v", stats)
	}

	for _, haystack := range []string{
		"€" + strings.Repeat("x", (1<<20)-3),
		strings.Repeat("x", (1<<20)-3) + "€",
	} {
		if _, _, handled := plan.findASCIIOnlyPartitioned(haystack); handled {
			t.Fatalf("unprofitable input entered partition route: len=%d", len(haystack))
		}
	}

	denseSuffix := []byte(strings.Repeat("x", 1<<16))
	copy(denseSuffix[4096:], "€")
	for at := 8192; at+len("€") <= len(denseSuffix)-64; at += 32 {
		copy(denseSuffix[at:], "€")
	}
	matcher := NewMatcher([]string{"Sherlock Holmes"})
	want, wantOK := refFind(string(denseSuffix), []string{"Sherlock Holmes"})
	if got, gotOK := matcher.Find(string(denseSuffix)); gotOK != wantOK || gotOK && got != want {
		t.Fatalf("dense-suffix Find = %+v,%t; want %+v,%t", got, gotOK, want, wantOK)
	}
}

func TestASCIIOnlyPartitionEachWidth(t *testing.T) {
	patterns := []string{"Sherlock Holmes"}
	matcher := NewMatcher(patterns)
	haystack := strings.Repeat("x", 5000) + "ſherlocK Holmeſ" +
		strings.Repeat("x", 5000) + "SHERLOCK HOLMES" + strings.Repeat("x", 5000)
	want := refEach(haystack, patterns)
	var got []refEachResult
	if complete := matcher.Each(haystack, func(match Match, width int) bool {
		got = append(got, refEachResult{match: match, width: width})
		return true
	}); !complete {
		t.Fatal("Each stopped early")
	}
	if len(got) != len(want) {
		t.Fatalf("Each returned %d matches, want %d: got=%+v want=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Each match %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(got) != 2 || got[0].width != len("ſherlocK Holmeſ") || got[1].width != len("SHERLOCK HOLMES") {
		t.Fatalf("unexpected source widths: %+v", got)
	}
}

func TestASCIIOnlyPartitionRandomDifferential(t *testing.T) {
	patterns := []string{"Sherlock Holmes"}
	plan := newSearchPlan(patterns, false)
	matcher := NewMatcher(patterns)
	rng := rand.New(rand.NewPCG(20260829, 31))
	units := []string{"x", "X", " ", "q", "€", "ſ", "K", "Ж", "\xff"}
	for iteration := 0; iteration < 120; iteration++ {
		var input strings.Builder
		input.Grow(8192)
		for input.Len() < 8192 {
			if iteration%4 == 0 && input.Len() == 4096 {
				input.WriteString("ſherlocK Holmes")
				continue
			}
			input.WriteString(units[rng.IntN(len(units))])
		}
		haystack := input.String()
		want, wantOK := decodedPlanFind(plan, haystack)
		got, gotOK := matcher.Find(haystack)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("iteration %d Find = %+v,%t; decoded = %+v,%t", iteration, got, gotOK, want, wantOK)
		}
	}
}
