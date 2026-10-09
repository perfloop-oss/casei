package arena_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/tsenart/casei/arena/board"
	vectorscan "github.com/tsenart/casei/arena/vectorscan"
)

type vectorscanMatcher = vectorscan.Matcher

func newVectorscanLiteral(pattern string) (*vectorscanMatcher, error) {
	return vectorscan.CompileLiteral(pattern)
}

func newVectorscanAlternation(patterns []string) (*vectorscanMatcher, error) {
	return vectorscan.CompileAlternation(patterns)
}

func mustVectorscanLiteral(pattern string) *vectorscanMatcher {
	m, err := newVectorscanLiteral(pattern)
	if err != nil {
		panic(fmt.Sprintf("Vectorscan baseline unavailable: %v", err))
	}
	return m
}

var vectorscanSingles = func() map[string]*vectorscanMatcher {
	out := make(map[string]*vectorscanMatcher)
	for _, s := range singleScenarios {
		if _, ok := out[s.needle]; !ok {
			out[s.needle] = mustVectorscanLiteral(s.needle)
		}
	}
	return out
}()

func indexVectorscan(haystack, needle string) int {
	m := vectorscanSingles[needle]
	if m == nil {
		panic(fmt.Sprintf("Vectorscan baseline was not compiled for %q", needle))
	}
	return m.Index(haystack)
}

var vectorscanAlts = func() []*vectorscanMatcher {
	out := make([]*vectorscanMatcher, len(multiScenarios))
	for i, s := range multiScenarios {
		m, err := newVectorscanAlternation(s.patterns)
		if err != nil {
			panic(fmt.Sprintf("Vectorscan baseline unavailable for %s: %v", s.name, err))
		}
		out[i] = m
	}
	return out
}()

func TestVectorscanFoldHazardsAgree(t *testing.T) {
	// Vectorscan does not enter the field until its caseless UTF-8 mode agrees
	// with the independent simple-fold oracle on unequal-width orbits.
	cases := []struct {
		name     string
		haystack string
		needle   string
	}{
		{"Kelvin sign in haystack", "xxKelvin", "kelvin"},
		{"Kelvin sign in needle", "5 kelvin", "Kelvin"},
		{"long s width change", "ſecret", "SECRET"},
		{"sigma trio", "τέλος", "ΤΈΛΟΣ"},
		{"angstrom trio", "1Å", "1å"},
		{"micro trio", "5µs", "5μS"},
		{"sharp s simple fold", "große", "GROẞE"},
		{"sharp s is not expansion", "große", "GROSSE"},
		{"dotted I remains distinct", "İstanbul", "istanbul"},
		{"dotless I remains distinct", "ı", "I"},
		{"literal metacharacters", "x[a-z]+y", "[a-z]+"},
	}
	for _, tc := range cases {
		m, err := newVectorscanLiteral(tc.needle)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := m.Index(tc.haystack), reference(tc.haystack, tc.needle); got != want {
			t.Errorf("%s: Vectorscan = %d, oracle = %d", tc.name, got, want)
		}
	}

	for _, orbit := range [][]string{
		{"k", "K", "K"},
		{"s", "S", "ſ"},
		{"σ", "ς", "Σ"},
		{"å", "Å", "Å"},
		{"µ", "μ", "Μ"},
		{"ß", "ẞ"},
	} {
		for _, haystackForm := range orbit {
			for _, needleForm := range orbit {
				haystack := "x" + haystackForm + "y"
				m, err := newVectorscanLiteral(needleForm)
				if err != nil {
					t.Fatalf("%q/%q: %v", haystackForm, needleForm, err)
				}
				if got, want := m.Index(haystack), reference(haystack, needleForm); got != want {
					t.Errorf("%q/%q: Vectorscan = %d, oracle = %d", haystackForm, needleForm, got, want)
				}
			}
		}
	}
}

func TestVectorscanAlternationHazardsAgree(t *testing.T) {
	// hs_scan reports every pattern event. These cases prove that the adapter,
	// not callback order, implements the arena's leftmost/lowest-ID contract.
	cases := []struct {
		haystack string
		patterns []string
	}{
		{"xxKelvin scale", []string{"scale", "kelvin"}},
		{"xxABCxx", []string{"abc", "ABC"}},
		{"τέλος", []string{"ΤΈΛΟΣ", "τέλος"}},
		{"große", []string{"GROSSE", "GROẞE"}},
		{"xxabc xx abc", []string{"abc", "xxabc"}},
	}
	for _, tc := range cases {
		m, err := newVectorscanAlternation(tc.patterns)
		if err != nil {
			t.Fatalf("%q: %v", tc.patterns, err)
		}
		start, pattern, ok := m.Find(tc.haystack)
		want, wantOK := refFind(tc.haystack, tc.patterns)
		if ok != wantOK || (ok && (start != want.Start || pattern != want.Pattern)) {
			t.Errorf("Find(%q, %q) = {%d %d},%v want %+v,%v", tc.haystack, tc.patterns, pattern, start, ok, want, wantOK)
		}
	}
}

func TestVectorscanEmptyPatternTie(t *testing.T) {
	for _, tc := range []struct {
		patterns []string
		haystack string
	}{
		{nil, "anything"},
		{[]string{"later", "", "x"}, "x"},
		{[]string{"x", "", "later"}, "x"},
	} {
		m, err := newVectorscanAlternation(tc.patterns)
		if err != nil {
			t.Fatalf("CompileAlternation(%q): %v", tc.patterns, err)
		}
		start, pattern, ok := m.Find(tc.haystack)
		want, wantOK := refFind(tc.haystack, tc.patterns)
		if ok != wantOK || (ok && (start != want.Start || pattern != want.Pattern)) {
			t.Errorf("Find(%q, %q) = {%d %d},%v want %+v,%v", tc.haystack, tc.patterns, pattern, start, ok, want, wantOK)
		}
	}
}

// TestVectorscanEachAgrees holds the report-collecting Each to the board's
// oracle. Vectorscan reports matches in no guaranteed order, overlapping and
// across fold forms of unequal width, so the reduction is where it can go wrong.
func TestVectorscanEachAgrees(t *testing.T) {
	alphabet := []rune("kKKsSſσςΣaAbB ßẞåÅÅжЖ")
	rng := rand.New(rand.NewPCG(20261009, 1))
	random := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for iteration := range 2000 {
		haystack := random(rng.IntN(200))
		patterns := make([]string, 1+rng.IntN(6))
		for i := range patterns {
			patterns[i] = random(1 + rng.IntN(4))
		}
		m, err := newVectorscanAlternation(patterns)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		var got []board.Hit
		m.Each(haystack, func(start, pattern, width int) bool {
			got = append(got, board.Hit{Start: start, Pattern: pattern, Width: width})
			return true
		})
		if want := board.Matches(haystack, patterns, false, -1); !slices.Equal(got, want) {
			t.Fatalf("iteration %d: Each(%q, %q) = %v, oracle %v", iteration, haystack, patterns, got, want)
		}
		if len(got) > 1 {
			calls := 0
			if m.Each(haystack, func(int, int, int) bool { calls++; return false }) || calls != 1 {
				t.Fatalf("iteration %d: Each did not stop when yield returned false", iteration)
			}
		}
	}
}
