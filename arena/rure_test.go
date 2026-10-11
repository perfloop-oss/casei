package arena_test

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/cpu"

	rure "github.com/tsenart/casei/arena/rure"
)

type rureRegex = rure.Regex

func newRureLiteral(pattern string) (*rureRegex, error) {
	return rure.CompileLiteral(pattern)
}

func newRureAlternation(patterns []string) (*rureRegex, error) {
	return rure.CompileAlternation(patterns)
}

func mustRureLiteral(pattern string) *rureRegex {
	re, err := newRureLiteral(pattern)
	if err != nil {
		panic(fmt.Sprintf("rure baseline unavailable: %v", err))
	}
	return re
}

var rureSingles = func() map[string]*rureRegex {
	out := make(map[string]*rureRegex)
	for _, s := range singleScenarios {
		if _, ok := out[s.needle]; !ok {
			out[s.needle] = mustRureLiteral(s.needle)
		}
	}
	return out
}()

func indexRure(haystack, needle string) int {
	re := rureSingles[needle]
	if re == nil {
		panic(fmt.Sprintf("rure baseline was not compiled for %q", needle))
	}
	return re.Index(haystack)
}

var rureAlts = func() []*rureRegex {
	out := make([]*rureRegex, len(multiScenarios))
	for i, s := range multiScenarios {
		re, err := newRureAlternation(s.patterns)
		if err != nil {
			panic(fmt.Sprintf("rure baseline unavailable for %s: %v", s.name, err))
		}
		out[i] = re
	}
	return out
}()

func TestRureDispatchIsObserved(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("rure audit probe requires an AVX2 host")
	}

	const haystackLen = 4096
	haystack := strings.Repeat("x", haystackLen)

	short, err := newRureLiteral("z")
	if err != nil {
		t.Fatal(err)
	}
	if got := short.Index(haystack); got != -1 {
		t.Fatalf("short literal match = %d, want miss", got)
	}
	if got := short.VectorBits(); got != 256 {
		t.Errorf("short literal dispatch = %d, want observed AVX2", got)
	}

	long, err := newRureLiteral("fatal panic")
	if err != nil {
		t.Fatal(err)
	}
	if got := long.Index(haystack); got != -1 {
		t.Fatalf("long literal match = %d, want miss", got)
	}
	// CPU support is deliberately not enough: this pinned Rust query uses its
	// scalar memmem path, so it must report no vector width.
	if got := long.VectorBits(); got != 0 {
		t.Errorf("long literal dispatch = %d, want no observed memchr vector path", got)
	}

	alternation, err := newRureAlternation([]string{"z", "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := alternation.Find(haystack); ok {
		t.Fatal("alternation matched, want miss")
	}
	// The alternation must record its own unobserved result, not retain the
	// prior literal's AVX2 observation.
	if got := alternation.VectorBits(); got != 0 {
		t.Errorf("alternation dispatch = %d, want no observed memchr vector path", got)
	}
}

func TestRureFoldHazardsAgree(t *testing.T) {
	// rust-regex must agree with the independent simple-fold oracle before it
	// can enter the field.
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
		re, err := newRureLiteral(tc.needle)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := re.Index(tc.haystack), reference(tc.haystack, tc.needle); got != want {
			t.Errorf("%s: rure = %d, oracle = %d", tc.name, got, want)
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
				re, err := newRureLiteral(needleForm)
				if err != nil {
					t.Fatalf("%q/%q: %v", haystackForm, needleForm, err)
				}
				if got, want := re.Index(haystack), reference(haystack, needleForm); got != want {
					t.Errorf("%q/%q: rure = %d, oracle = %d", haystackForm, needleForm, got, want)
				}
			}
		}
	}
}

func TestRureAlternationHazardsAgree(t *testing.T) {
	// rust-regex's leftmost-first choice must start where the arena's
	// lowest-pattern-index rule does; the oracle probes ties and differing
	// source starts before any query is timed.
	// The alternation reports no pattern ID.
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
		re, err := newRureAlternation(tc.patterns)
		if err != nil {
			t.Fatalf("%q: %v", tc.patterns, err)
		}
		start, pattern, ok := re.Find(tc.haystack)
		want, wantOK := refFind(tc.haystack, tc.patterns)
		if ok != wantOK || (ok && (start != want.Start || pattern != -1)) {
			t.Errorf("Find(%q, %q) = {%d %d},%v want %+v,%v", tc.haystack, tc.patterns, pattern, start, ok, want, wantOK)
		}
	}
}
