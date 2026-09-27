package casei

import (
	"strings"
	"testing"
)

func collectASCIIInteriorEach(m *Matcher, haystack string) []refEachResult {
	var got []refEachResult
	m.Each(haystack, func(match Match, width int) bool {
		got = append(got, refEachResult{match: match, width: width})
		return true
	})
	return got
}

func TestASCIIInteriorAdmissionAndOffsets(t *testing.T) {
	patterns := []string{"Sherlock", "Sherlock Holmes", "KabcS"}
	for _, pattern := range patterns {
		m := NewMatcher([]string{pattern})
		if m.plan.asciiProbe.firstAt == 0 {
			t.Fatalf("%q has no interior probe: %+v", pattern, m.plan.asciiProbe)
		}
		if m.plan.asciiProbe.thirdAt-m.plan.asciiProbe.firstAt < 2 {
			t.Fatalf("%q interior probe is shorter than three bytes: %+v", pattern, m.plan.asciiProbe)
		}
		for _, rendered := range []string{
			pattern,
			strings.ToUpper(pattern),
			strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(pattern), "s", "ſ"), "k", "K"),
		} {
			for padding := 0; padding < 130; padding++ {
				input := strings.Repeat("x", padding) + rendered + " " + rendered
				got := collectASCIIInteriorEach(m, input)
				want := refEach(input, []string{pattern})
				if index := reference(input, pattern); IndexFold(input, pattern) != index {
					t.Fatalf("IndexFold(%q, %q) = %d, want %d", input, pattern, IndexFold(input, pattern), index)
				}
				if len(got) != len(want) {
					t.Fatalf("%q in %q: got %+v want %+v", pattern, input, got, want)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%q in %q: got %+v want %+v", pattern, input, got, want)
					}
				}
			}
		}
	}
	for _, pattern := range []string{
		"ab", "sss", "kss", "skherlo", "ssabcdef", "[keys[i%", "abcdef", "яabcdef", "\xffabcdef", "s" + strings.Repeat("a", 64),
	} {
		if p := newSearchPlan([]string{pattern}); p.asciiProbe.firstAt != 0 {
			t.Fatalf("%q unexpectedly admitted to interior probe: %+v", pattern, p.asciiProbe)
		}
	}
}

func TestASCIIInteriorRecoversSourceUnits(t *testing.T) {
	m := NewMatcher([]string{"Sherlock"})
	cases := []struct {
		name     string
		pattern  string
		haystack string
		start    int
		width    int
	}{
		{"long-s-prefix", "Sherlock", "ſherlock tail", 0, len("ſherlock")},
		{"kelvin-prefix", "KabcS", "Kabcſ tail", 0, len("Kabcſ")},
		{"ascii-prefix", "Sherlock", "xSherlock tail", 1, len("Sherlock")},
	}
	for _, tc := range cases {
		matcher := NewMatcher([]string{tc.pattern})
		got := collectASCIIInteriorEach(matcher, tc.haystack)
		want := []refEachResult{{match: Match{Pattern: 0, Start: tc.start}, width: tc.width}}
		if len(got) != len(want) || len(got) == 1 && got[0] != want[0] {
			t.Fatalf("%s: Each(%q) = %+v, want %+v", tc.name, tc.haystack, got, want)
		}
	}

	// The interior byte is not a source offset. A byte subtraction would
	// return one for the long-s occurrence instead of the true start zero.
	prefixUnits := m.plan.asciiProbe.firstAt
	window := len("ſ")
	if got := recoverASCIIInteriorStart("ſherlock", window, prefixUnits); got != 0 {
		t.Fatalf("recovered start = %d, want 0", got)
	}
}

func TestASCIIInteriorRejectsMalformedPrefixesAndKeepsTails(t *testing.T) {
	m := NewMatcher([]string{"Sherlock Holmes"})
	for _, input := range []string{
		"\xffherlock Holmes",
		"\xc3herlock Holmes",
		"\xe2\x82herlock Holmes",
		"x\xffherlock Holmes",
	} {
		got, want := collectASCIIInteriorEach(m, input), refEach(input, []string{"Sherlock Holmes"})
		if len(got) != len(want) {
			t.Fatalf("malformed %x: got %+v want %+v", input, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("malformed %x: got %+v want %+v", input, got, want)
			}
		}
	}
	for length := 0; length <= m.plan.asciiProbe.thirdAt+2; length++ {
		input := strings.Repeat("x", length)
		if got, want := IndexFold(input, "Sherlock Holmes"), reference(input, "Sherlock Holmes"); got != want {
			t.Fatalf("tail length %d: IndexFold = %d, want %d", length, got, want)
		}
	}
}

func TestASCIIInteriorRejectsFalseSurvivorsBeforeHit(t *testing.T) {
	matcher := NewMatcher([]string{"Sherlock"})
	prefix := "\xffherlock xherlock "
	input := prefix + strings.Repeat("x", 64-len(prefix)) + "ſherlock"
	want := refEach(input, []string{"Sherlock"})
	if len(want) != 1 || want[0].match.Start != 64 {
		t.Fatalf("reference = %+v, want one hit at byte 64", want)
	}
	if got, ok := matcher.plan.findASCIIAnchor(input); !ok || got != want[0].match {
		t.Fatalf("direct interior route = %+v,%t, want %+v,true", got, ok, want[0].match)
	}
	if got, ok := matcher.Find(input); !ok || got != want[0].match {
		t.Fatalf("Find = %+v,%t, want %+v,true", got, ok, want[0].match)
	}
	got := collectASCIIInteriorEach(matcher, input)
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Each = %+v, want %+v", got, want)
	}
}

func TestASCIIInteriorFindAndEachOrder(t *testing.T) {
	patterns := []string{"Sherlock Holmes"}
	m := NewMatcher(patterns)
	haystack := strings.Repeat("x", 63) + "ſherlocK Holmes" +
		strings.Repeat("x", 71) + "SHERLOCK HOLMES" +
		strings.Repeat("x", 67) + "Kherlocſ Holmes"
	got := collectASCIIInteriorEach(m, haystack)
	want := refEach(haystack, patterns)
	if len(got) != len(want) {
		t.Fatalf("Each returned %d matches, want %d: got=%+v want=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, ok := m.Find(haystack); !ok || got != want[0].match {
		t.Fatalf("Find = %+v,%t; want %+v,true", got, ok, want[0].match)
	}
}

// FuzzASCIIInteriorSource keeps arbitrary bytes on the admitted N=1 route.
// It is intentionally independent of the regexp fuzz target: malformed bytes
// are opaque in this package and must not be delegated to a Unicode oracle.
func FuzzASCIIInteriorSource(f *testing.F) {
	patterns := []string{"Sherlock", "Sherlock Holmes", "KabcS"}
	matchers := make([]*Matcher, len(patterns))
	for i, pattern := range patterns {
		matchers[i] = NewMatcher([]string{pattern})
		if matchers[i].plan.asciiProbe.firstAt == 0 {
			f.Fatalf("fixture %q was not admitted", pattern)
		}
		f.Add(pattern, uint8(i))
		f.Add(strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(pattern), "s", "ſ"), "k", "K"), uint8(i))
	}
	f.Add("\xffherlock Holmes", uint8(1))
	f.Add("ſherlocK Holmes\x80", uint8(1))
	f.Fuzz(func(t *testing.T, input string, choice uint8) {
		if len(input) > 4096 {
			t.Skip()
		}
		i := int(choice) % len(patterns)
		got, want := collectASCIIInteriorEach(matchers[i], input), refEach(input, []string{patterns[i]})
		if len(got) != len(want) {
			t.Fatalf("%q in %x: got %+v want %+v", patterns[i], input, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("%q in %x: got %+v want %+v", patterns[i], input, got, want)
			}
		}
	})
}
