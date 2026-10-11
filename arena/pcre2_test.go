package arena_test

import (
	"fmt"
	"testing"

	pcre2jit "github.com/tsenart/casei/arena/pcre2"
)

type pcre2Regex = pcre2jit.Regex

func newPCRE2Regex(pattern string) (*pcre2Regex, error) {
	return pcre2jit.Compile(pattern, false)
}

func pcre2QuoteMeta(s string) string { return pcre2jit.QuoteMeta(s) }

func newPCRE2Alternation(patterns []string) (*pcre2Regex, error) {
	return pcre2jit.CompileAlternation(patterns, false)
}

func mustPCRE2Regex(pattern string) *pcre2Regex {
	re, err := newPCRE2Regex(pattern)
	if err != nil {
		panic(fmt.Sprintf("PCRE2 JIT baseline unavailable: %v", err))
	}
	return re
}

var pcre2Singles = func() map[string]*pcre2Regex {
	out := make(map[string]*pcre2Regex)
	for _, s := range singleScenarios {
		if _, ok := out[s.needle]; !ok {
			out[s.needle] = mustPCRE2Regex(pcre2QuoteMeta(s.needle))
		}
	}
	return out
}()

func indexPCRE2(haystack, needle string) int {
	re := pcre2Singles[needle]
	if re == nil {
		panic(fmt.Sprintf("PCRE2 baseline was not compiled for %q", needle))
	}
	return re.Index(haystack)
}

var pcre2Alts = func() []*pcre2Regex {
	out := make([]*pcre2Regex, len(multiScenarios))
	for i, s := range multiScenarios {
		re, err := newPCRE2Alternation(s.patterns)
		if err != nil {
			panic(fmt.Sprintf("PCRE2 JIT baseline unavailable for %s: %v", s.name, err))
		}
		out[i] = re
	}
	return out
}()

func TestPCRE2FoldHazardsAgree(t *testing.T) {
	// Every row is checked against the arena's independent canonical-fold
	// oracle. The first four cover the unequal UTF-8-width forms PCRE2 must
	// handle before it can enter the UTF-8 field.
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
		{"literal quote terminator", "x\\Ey", "\\E"},
	}
	for _, tc := range cases {
		re, err := newPCRE2Regex(pcre2QuoteMeta(tc.needle))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := re.Index(tc.haystack), reference(tc.haystack, tc.needle); got != want {
			t.Errorf("%s: PCRE2 = %d, oracle = %d", tc.name, got, want)
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
				re, err := newPCRE2Regex(pcre2QuoteMeta(needleForm))
				if err != nil {
					t.Fatalf("%q/%q: %v", haystackForm, needleForm, err)
				}
				if got, want := re.Index(haystack), reference(haystack, needleForm); got != want {
					t.Errorf("%q/%q: PCRE2 = %d, oracle = %d", haystackForm, needleForm, got, want)
				}
			}
		}
	}
}

func TestPCRE2AlternationHazardsAgree(t *testing.T) {
	cases := []struct {
		haystack string
		patterns []string
	}{
		{"xxKelvin scale", []string{"scale", "kelvin"}},
		{"xxABCxx", []string{"abc", "ABC"}},
		{"τέλος", []string{"ΤΈΛΟΣ", "τέλος"}},
		{"große", []string{"GROSSE", "GROẞE"}},
	}
	for _, tc := range cases {
		re, err := newPCRE2Alternation(tc.patterns)
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
