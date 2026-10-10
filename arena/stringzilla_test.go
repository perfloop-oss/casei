package arena_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/tsenart/casei/arena/board"
	stringzilla "github.com/tsenart/casei/arena/stringzilla"
)

type stringZillaLiteral = stringzilla.Matcher
type stringZillaAlternation = stringzilla.Alternation

// StringZilla is an optional AVX-512-only entrant. In the AVX-512-disabled
// control it is excluded before any native prepare call, never downgraded to a
// serial library path.
var stringZillaAvailable = stringzilla.Enabled()

func newStringZillaLiteral(pattern string) (*stringZillaLiteral, error) {
	return stringzilla.CompileLiteral(pattern)
}

func newStringZillaAlternation(patterns []string) (*stringZillaAlternation, error) {
	return stringzilla.CompileAlternation(patterns)
}

func mustStringZillaLiteral(pattern string) *stringZillaLiteral {
	m, err := newStringZillaLiteral(pattern)
	if err != nil {
		panic(fmt.Sprintf("StringZilla baseline unavailable: %v", err))
	}
	return m
}

var stringZillaSingles = func() map[string]*stringZillaLiteral {
	out := make(map[string]*stringZillaLiteral)
	if !stringZillaAvailable {
		return out
	}
	for _, s := range singleScenarios {
		if _, ok := out[s.needle]; !ok {
			out[s.needle] = mustStringZillaLiteral(s.needle)
		}
	}
	return out
}()

func indexStringZilla(haystack, needle string) int {
	m := stringZillaSingles[needle]
	if m == nil {
		panic(fmt.Sprintf("StringZilla baseline was not compiled for %q", needle))
	}
	return m.Index(haystack)
}

var stringZillaAlts = func() []*stringZillaAlternation {
	out := make([]*stringZillaAlternation, len(multiScenarios))
	if !stringZillaAvailable {
		return out
	}
	for i, s := range multiScenarios {
		m, err := newStringZillaAlternation(s.patterns)
		if err != nil {
			panic(fmt.Sprintf("StringZilla baseline unavailable for %s: %v", s.name, err))
		}
		out[i] = m
	}
	return out
}()

func TestStringZillaFoldHazardsAgree(t *testing.T) {
	if !stringZillaAvailable {
		t.Skip("StringZilla AVX-512 entrant is excluded on this process")
	}
	// StringZilla's native search uses full folding. The adapter must reject
	// full-fold expansions and agree with the independent simple-fold oracle
	// before its timing can enter the field.
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
		m, err := newStringZillaLiteral(tc.needle)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := m.Index(tc.haystack), reference(tc.haystack, tc.needle); got != want {
			t.Errorf("%s: StringZilla = %d, oracle = %d", tc.name, got, want)
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
				m, err := newStringZillaLiteral(needleForm)
				if err != nil {
					t.Fatalf("%q/%q: %v", haystackForm, needleForm, err)
				}
				if got, want := m.Index(haystack), reference(haystack, needleForm); got != want {
					t.Errorf("%q/%q: StringZilla = %d, oracle = %d", haystackForm, needleForm, got, want)
				}
			}
		}
	}
}

func TestStringZillaAlternationHazardsAgree(t *testing.T) {
	if !stringZillaAvailable {
		t.Skip("StringZilla AVX-512 entrant is excluded on this process")
	}
	// The reduction is deliberately tested with full-fold false positives,
	// differing starts, and equal-start ties before it enters the field.
	cases := []struct {
		haystack string
		patterns []string
	}{
		{"xxKelvin scale", []string{"scale", "kelvin"}},
		{"xxABCxx", []string{"abc", "ABC"}},
		{"τέλος", []string{"ΤΈΛΟΣ", "τέλος"}},
		{"große", []string{"GROSSE", "GROẞE"}},
		{"xxabc xx abc", []string{"abc", "xxabc"}},
		{"x", []string{"later", "", "x"}},
		{"x", []string{"x", "", "later"}},
	}
	for _, tc := range cases {
		m, err := newStringZillaAlternation(tc.patterns)
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

// TestStringZillaEachAgrees holds the batched Each to the board's pure-Go
// oracle, with full-fold expansions (ß and ss) that its candidates must reject
// and haystacks long enough to need several batches.
func TestStringZillaEachAgrees(t *testing.T) {
	if !stringZillaAvailable {
		t.Skip("StringZilla AVX-512 entrant is excluded on this process")
	}
	alphabet := []rune("sSßẞkKKσςΣaжЖ ")
	rng := rand.New(rand.NewPCG(20261010, 1))
	random := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for iteration := range 500 {
		haystack, patterns := random(rng.IntN(2000)), make([]string, 1+rng.IntN(5))
		for i := range patterns {
			patterns[i] = random(1 + rng.IntN(3))
		}
		m, err := stringzilla.CompileAlternation(patterns)
		if err != nil {
			t.Fatal(err)
		}
		var got []board.Hit
		m.Each(haystack, func(start, pattern, width int) bool {
			got = append(got, board.Hit{Start: start, Pattern: pattern, Width: width})
			return true
		})
		if want := board.Matches(haystack, patterns, false); !slices.Equal(got, want) {
			t.Fatalf("iteration %d: Each(%q, %q) = %v, oracle %v", iteration, haystack, patterns, got, want)
		}
	}
}
