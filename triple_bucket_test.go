//go:build amd64

package casei

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/cpu"
)

var tripleBucketTestPatterns = []string{
	"Sherlock Holmes",
	"Sherlock Holmes",
	"Sherlock",
	"John Watson",
	"Irene Adler",
	"Inspector Lestrade",
	"Professor Moriarty",
}

func tripleBucketTestMatcher(patterns []string, enabled bool) *Matcher {
	base := NewMatcher(patterns)
	if base.plan.rawByteMulti.usable() {
		panic("bucket test pattern set selected the independent tagged-anchor route")
	}
	plan := *base.plan
	if !enabled {
		plan.tripleRoots = nil
	}
	return &Matcher{patterns: base.patterns, plan: &plan}
}

func tripleBucketAt(s []byte, at int, filter tripleBucketFilter) bool {
	if at+3 >= len(s) || s[at] >= 128 || s[at+1] >= 128 || s[at+2] >= 128 || s[at+3] >= 128 {
		return false
	}
	mask := filter[int(s[at])] & filter[128+int(s[at+1])] &
		filter[256+int(s[at+2])] & filter[384+int(s[at+3])]
	return mask != 0
}

func tripleBucketPrefixModel(p *searchPlan, s []byte, at int) bool {
	state := 0
	for depth := 1; depth <= tripleBucketPrefixBytes; depth++ {
		token := p.ascii[s[at+depth-1]]
		child, ok := p.nodes[state].edges[token]
		if token == 0 || !ok {
			return false
		}
		state = child
		node := &p.nodes[state]
		if depth == 3 && node.output.pattern >= 0 && node.output.units == 3 {
			return true
		}
		if depth == tripleBucketPrefixBytes {
			return (node.output.pattern >= 0 && node.output.units == depth) || len(node.edges) != 0
		}
	}
	return false
}

func TestTripleBucketCompilerMatchesTrieModel(t *testing.T) {
	plans := []*searchPlan{
		NewMatcher([]string{"Sherlock Holmes", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty"}).plan,
		NewMatcher([]string{"Tom", "Sawyer", "Huckleberry", "Finn"}).plan,
	}
	if !asciiPairVBMIEnabled() {
		t.Skip("AVX-512 VBMI bucket path is disabled")
	}
	rng := rand.New(rand.NewSource(0x4b17))
	for planIndex, p := range plans {
		filter := p.tripleBucketFilter()
		if !filter.usable() {
			t.Fatalf("plan %d has no bucket", planIndex)
		}
		for sample := 0; sample < 250_000; sample++ {
			var bytes [tripleBucketPrefixBytes]byte
			for i := range bytes {
				bytes[i] = byte(rng.Intn(utf8.RuneSelf))
			}
			want := tripleBucketPrefixModel(p, bytes[:], 0)
			if got := tripleBucketAt(bytes[:], 0, filter); got != want {
				t.Fatalf("plan %d sample %d bytes=%q: bucket=%v trie=%v", planIndex, sample, bytes, got, want)
			}
		}
	}
}

// tripleBucketSkip64Model is the lane-for-lane specification of the assembly
// block scan, including its Shufti handoff for blocks containing high bytes.
func tripleBucketSkip64Model(s []byte, filter tripleBucketFilter, shufti *tripleShuftiFilter) int {
	offset := 0
	for len(s)-offset >= 67 {
		ascii := true
		for i := offset; i < offset+67; i++ {
			if s[i] >= 128 {
				ascii = false
				break
			}
		}
		for lane := 0; lane < 64; lane++ {
			at := offset + lane
			matched := tripleBucketAt(s, at, filter)
			if !ascii {
				matched = tripleShuftiAt(s[at], s[at+1], s[at+2], shufti)
			}
			if matched {
				return at
			}
		}
		offset += 64
	}
	return offset
}

func TestTripleBucketPlanEligibility(t *testing.T) {
	english := NewMatcher([]string{
		"Sherlock Holmes", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty",
	})
	leipzig := NewMatcher([]string{"Tom", "Sawyer", "Huckleberry", "Finn"})
	russian := NewMatcher([]string{"Шерлок Холмс", "Джон Уотсон", "Ирен Адлер", "инспектор Лестрейд", "профессор Мориарти"})
	single := NewMatcher([]string{"Sherlock Holmes"})

	if !english.plan.triplesComplete || !english.plan.triples.shufti.usable() ||
		!leipzig.plan.triplesComplete || !leipzig.plan.triples.shufti.usable() {
		t.Fatal("target plans no longer use the complete triple-Shufti owner")
	}
	for _, plan := range []*searchPlan{english.plan, leipzig.plan} {
		if plan.rawByteMulti.usable() || plan.asciiPairAnchors.usable() || plan.unicodePairN != 0 ||
			plan.unicodeAnchor.n != 0 || plan.asciiProbe.usable() || plan.asciiOnly || plan.asciiRun {
			t.Fatal("target consumer no longer reaches the complete multi-triple plan route")
		}
	}
	if !russian.plan.rawByteMulti.usable() {
		t.Fatal("Russian Rebar counterpart no longer uses its retained raw multi-anchor owner")
	}
	if !asciiPairVBMIEnabled() {
		if english.plan.tripleBucketFilter() != nil || leipzig.plan.tripleBucketFilter() != nil {
			t.Fatal("bucket compiled without runtime AVX-512 VBMI")
		}
		t.Skip("plan-owned bucket is runtime-gated to AVX-512 VBMI")
	}
	englishBucket := english.plan.tripleBucketFilter()
	if !englishBucket.usable() || englishBucket.prefixCount() != 5 {
		t.Fatalf("English bucket prefixes=%d, want five", englishBucket.prefixCount())
	}
	leipzigBucket := leipzig.plan.tripleBucketFilter()
	if !leipzigBucket.usable() || leipzigBucket.prefixCount() != 4 {
		t.Fatalf("Leipzig bucket prefixes=%d, want four", leipzigBucket.prefixCount())
	}
	if russian.plan.tripleBucketFilter() != nil || single.plan.tripleBucketFilter() != nil {
		t.Fatal("bucket escaped the complete multi-literal plan boundary")
	}
}

func TestTripleBucketSkip64MatchesModel(t *testing.T) {
	if !asciiPairVBMIEnabled() {
		t.Skip("AVX-512 VBMI bucket path is disabled")
	}
	plan := NewMatcher([]string{
		"Sherlock Holmes", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty",
	}).plan
	filter := plan.tripleBucketFilter()
	if !filter.usable() {
		t.Fatal("eligible plan has no bucket")
	}

	rng := rand.New(rand.NewSource(0x5f7a))
	lengths := []int{67, 68, 127, 128, 129, 130, 131, 191, 192, 193, 255, 256, 257, 513, 1025}
	for _, n := range lengths {
		for _, alignment := range []int{0, 1, 31, 63} {
			for _, ascii := range []bool{true, false} {
				backing := make([]byte, alignment+n)
				input := backing[alignment:]
				for i := range input {
					if ascii {
						input[i] = byte('!' + rng.Intn('~'-'!'+1))
					} else {
						input[i] = byte(rng.Intn(256))
					}
				}
				for _, at := range []int{0, 1, 62, 63, 64, 65, 126, 127, 128, 190, 191, 192, 255} {
					if at+4 <= len(input) {
						copy(input[at:], "Sher")
					}
				}
				want := tripleBucketSkip64Model(input, filter, &plan.triples.shufti)
				got := tripleBucketSkip64(unsafe.SliceData(input), len(input), unsafe.SliceData(filter[:tripleBucketTableBytes]), &plan.triples.shufti)
				if got != want {
					t.Fatalf("n=%d align=%d ascii=%v: skip=%d want %d", n, alignment, ascii, got, want)
				}
			}
		}
	}

	// Every location in a three-block horizon must hand its containing block to
	// Shufti when any of its four overlapping source windows sees a high byte.
	for high := 0; high < 3*64+67; high++ {
		input := []byte(strings.Repeat("x", 3*64+67))
		input[high] = 0x80
		want := tripleBucketSkip64Model(input, filter, &plan.triples.shufti)
		if got := tripleBucketSkip64(unsafe.SliceData(input), len(input), unsafe.SliceData(filter[:tripleBucketTableBytes]), &plan.triples.shufti); got != want {
			t.Fatalf("high byte at %d: skip=%d want %d", high, got, want)
		}
	}
}

func collectTripleBucketEach(m *Matcher, haystack string) ([]Match, []int, bool) {
	var matches []Match
	var widths []int
	complete := m.Each(haystack, func(match Match, width int) bool {
		matches = append(matches, match)
		widths = append(widths, width)
		return true
	})
	return matches, widths, complete
}

func TestTripleBucketPreservesPlanResultsAndWidths(t *testing.T) {
	fast := tripleBucketTestMatcher(tripleBucketTestPatterns, true)
	shufti := tripleBucketTestMatcher(tripleBucketTestPatterns, false)
	if !fast.plan.tripleBucketFilter().usable() {
		t.Skip("AVX-512 VBMI bucket path is disabled")
	}
	if fast.plan.rawByteMulti.usable() {
		t.Fatal("test plan unexpectedly bypasses the triple filter")
	}

	malformed := append([]byte(strings.Repeat("x", 63)), 0xff)
	malformed = append(malformed, strings.Repeat("x", 96)...)
	malformed = append(malformed, []byte("Sherlock Holmes and John Watson")...)
	haystacks := []string{
		strings.Repeat("x", 63) + "Sherlock Holmes and John Watson",
		strings.Repeat("x", 61) + "ſherlock Holmes",
		strings.Repeat("x", 59) + "SherlocK Holmes",
		string(malformed),
		strings.Repeat("x", 64) + "Sherlock Sherlock Holmes John Watson",
		strings.Repeat("z", 257),
		"\x00" + strings.Repeat("x", 63) + "Sherlock Holmes",
	}
	for _, haystack := range haystacks {
		gotFind, gotOK := fast.Find(haystack)
		wantFind, wantOK := shufti.Find(haystack)
		if gotFind != wantFind || gotOK != wantOK {
			t.Errorf("Find(%q): bucket=%+v,%v Shufti=%+v,%v", haystack, gotFind, gotOK, wantFind, wantOK)
		}
		gotMatches, gotWidths, gotComplete := collectTripleBucketEach(fast, haystack)
		wantMatches, wantWidths, wantComplete := collectTripleBucketEach(shufti, haystack)
		if gotComplete != wantComplete || !reflect.DeepEqual(gotMatches, wantMatches) || !reflect.DeepEqual(gotWidths, wantWidths) {
			t.Errorf("Each(%q): bucket=%+v/%v complete=%v Shufti=%+v/%v complete=%v", haystack, gotMatches, gotWidths, gotComplete, wantMatches, wantWidths, wantComplete)
		}
	}

	match, ok := fast.Find(strings.Repeat("x", 64) + "sHerlock Holmes")
	if !ok || match != (Match{Pattern: 0, Start: 64}) {
		t.Fatalf("leftmost/lowest-ID tie = %+v,%v, want pattern 0 at 64", match, ok)
	}
	var first Match
	var firstWidth int
	fast.Each(strings.Repeat("x", 63)+"ſherlock Holmes", func(match Match, width int) bool {
		first, firstWidth = match, width
		return false
	})
	if first != (Match{Pattern: 0, Start: 63}) || firstWidth != len("ſherlock Holmes") {
		t.Fatalf("width-changing simple fold = %+v width %d, want byte width %d", first, firstWidth, len("ſherlock Holmes"))
	}
	kelvin := strings.Repeat("x", 59) + "SherlocK Holmes"
	fast.Each(kelvin, func(match Match, width int) bool {
		if match != (Match{Pattern: 0, Start: 59}) || width != len("SherlocK Holmes") {
			t.Fatalf("Kelvin source width = %+v width %d, want byte width %d", match, width, len("SherlocK Holmes"))
		}
		return false
	})
}

func TestTripleBucketNonASCIIRootByteSeams(t *testing.T) {
	if !asciiPairVBMIEnabled() {
		t.Skip("AVX-512 VBMI bucket path is disabled")
	}
	patterns := []string{"AbcdЖ", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty"}
	for _, needle := range []string{
		"AbcdЖ", string([]byte{'A', 'b', 'c', 'd', 0xff}),
		"AbcЖ", string([]byte{'A', 'b', 'c', 0xff}),
	} {
		patterns[0] = needle
		fast := tripleBucketTestMatcher(patterns, true)
		shufti := tripleBucketTestMatcher(patterns, false)
		if !fast.plan.tripleBucketFilter().usable() || !fast.plan.triples.shufti.usable() || fast.plan.rawByteMulti.usable() {
			t.Fatalf("mixed non-ASCII root-byte plan does not reach bucket+Shufti: bucket=%v shufti=%v rawMulti=%v", fast.plan.tripleBucketFilter().usable(), fast.plan.triples.shufti.usable(), fast.plan.rawByteMulti.usable())
		}
		for _, start := range []int{62, 63, 64, 126, 127, 128} {
			haystack := strings.Repeat("x", start) + needle + strings.Repeat("x", 80)
			want := Match{Pattern: 0, Start: start}
			if got, ok := fast.Find(haystack); !ok || got != want {
				t.Fatalf("needle %q at %d: bucket Find=%+v,%v want %+v", needle, start, got, ok, want)
			}
			if got, ok := shufti.Find(haystack); !ok || got != want {
				t.Fatalf("needle %q at %d: Shufti control Find=%+v,%v want %+v", needle, start, got, ok, want)
			}
			var match Match
			var width int
			fast.Each(haystack, func(m Match, w int) bool { match, width = m, w; return false })
			if match != want || width != len(needle) {
				t.Fatalf("needle %q at %d: bucket Each=%+v width %d, want width %d", needle, start, match, width, len(needle))
			}
		}
	}
}

func TestTripleBucketFallsBackWithoutVBMI(t *testing.T) {
	if !asciiPairVBMIEnabled() {
		t.Skip("the runtime has no AVX-512 VBMI to disable")
	}
	fast := tripleBucketTestMatcher(tripleBucketTestPatterns, true)
	if !fast.plan.tripleBucketFilter().usable() {
		t.Fatal("eligible plan did not compile its bucket")
	}
	want := tripleBucketTestMatcher(tripleBucketTestPatterns, false)
	hadVBMI := cpu.X86.HasAVX512VBMI
	cpu.X86.HasAVX512VBMI = false
	defer func() { cpu.X86.HasAVX512VBMI = hadVBMI }()
	for _, haystack := range []string{
		strings.Repeat("x", 1<<12) + "Sherlock Holmes",
		strings.Repeat("x", 63) + "ſherlock Holmes",
		strings.Repeat("x", 67) + "SherlocK Holmes",
		string([]byte{0xff, 'x', 'x'}) + strings.Repeat("x", 96) + "John Watson",
	} {
		got, gotOK := fast.Find(haystack)
		wantMatch, wantOK := want.Find(haystack)
		if got != wantMatch || gotOK != wantOK {
			t.Fatalf("VBMI-disabled Find(%q) = %+v,%v, want %+v,%v", haystack, got, gotOK, wantMatch, wantOK)
		}
		gotMatches, gotWidths, gotComplete := collectTripleBucketEach(fast, haystack)
		wantMatches, wantWidths, wantComplete := collectTripleBucketEach(want, haystack)
		if gotComplete != wantComplete || !reflect.DeepEqual(gotMatches, wantMatches) || !reflect.DeepEqual(gotWidths, wantWidths) {
			t.Fatalf("VBMI-disabled Each(%q) differs: bucket=%+v/%v Shufti=%+v/%v", haystack, gotMatches, gotWidths, wantMatches, wantWidths)
		}
	}
}
