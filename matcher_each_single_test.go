package casei

import (
	"reflect"
	"strings"
	"testing"
)

func TestMatcherEachASCIIProbeWidthChangingFolds(t *testing.T) {
	patterns := []string{"Sherlock Holmes"}
	matcher := NewMatcher(patterns)
	if !matcher.plan.asciiProbe.usable() || matcher.plan.asciiProbe.firstAt == 0 {
		t.Fatalf("Sherlock plan does not exercise the interior ASCII probe: %+v", matcher.plan.asciiProbe)
	}

	prefix := strings.Repeat("x", 5000)
	haystacks := []string{
		prefix + "ſherlocK Holmes",
		prefix + "Sherlock Holmes" + " and " + "ſherlocK holmeſ",
		prefix + "\xffſherlocK Holmes" + "\x84" + prefix,
		prefix + "ſ\xffherlocK Holmes",
		prefix + "\x84herlocK Holmes",
	}
	for i, haystack := range haystacks {
		var got []refEachResult
		if !matcher.Each(haystack, func(match Match, width int) bool {
			got = append(got, refEachResult{match: match, width: width})
			return true
		}) {
			t.Fatalf("case %d stopped early", i)
		}
		want := refEach(haystack, patterns)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("case %d: Each returned %+v, want %+v", i, got, want)
		}
	}
}

func TestMatcherEachRareProbeIsEachOnly(t *testing.T) {
	matcher := NewMatcher([]string{"Twain"})
	if matcher.asciiEachProbe == nil {
		t.Fatal("fixed-width Twain literal has no Each-only probe")
	}
	if matcher.plan.asciiProbe.firstAt != 0 || matcher.plan.asciiProbe.secondAt != 2 || matcher.plan.asciiProbe.thirdAt != 4 {
		t.Fatalf("Find probe = %+v, want the original [0,2,4] filter", matcher.plan.asciiProbe)
	}
	if matcher.asciiEachProbe.firstAt != 0 || matcher.asciiEachProbe.secondAt != 1 || matcher.asciiEachProbe.thirdAt != 3 {
		t.Fatalf("Each probe = %+v, want [0,1,3]", matcher.asciiEachProbe)
	}

	for _, patterns := range [][]string{
		{"the"},
		{"Sherlock Holmes"},
		{"Twain", "the"},
	} {
		if matcher := NewMatcher(patterns); matcher.asciiEachProbe != nil {
			t.Errorf("patterns %q unexpectedly got an Each-only probe", patterns)
		}
	}
}

func TestMatcherEachASCIIProbeNonOverlapAndEarlyStop(t *testing.T) {
	prefix := strings.Repeat("x", 5000)
	for _, tc := range []struct {
		pattern  string
		haystack string
	}{
		{pattern: "Twain", haystack: prefix + "TWAINTwainTWAIN"},
		{pattern: "Twain", haystack: prefix + "\xffTWAINTwain\x84tWaIn"},
		{pattern: "aba", haystack: prefix + "abababa"},
	} {
		matcher := NewMatcher([]string{tc.pattern})
		if !matcher.plan.asciiProbe.usable() {
			t.Fatalf("pattern %q does not compile an ASCII probe", tc.pattern)
		}
		if tc.pattern == "Twain" && (matcher.asciiEachProbe == nil ||
			matcher.plan.asciiProbe.secondAt != 2 || matcher.plan.asciiProbe.thirdAt != 4 ||
			matcher.asciiEachProbe.firstAt != 0 || matcher.asciiEachProbe.secondAt != 1 ||
			matcher.asciiEachProbe.thirdAt != 3) {
			t.Fatalf("Twain probes = Find:%+v Each:%+v, want unchanged Find and rare-prefix Each probe", matcher.plan.asciiProbe, matcher.asciiEachProbe)
		}
		want := refEach(tc.haystack, []string{tc.pattern})
		if len(want) == 0 {
			t.Fatalf("pattern %q has no reference matches", tc.pattern)
		}
		var got []refEachResult
		if !matcher.Each(tc.haystack, func(match Match, width int) bool {
			got = append(got, refEachResult{match: match, width: width})
			return true
		}) {
			t.Fatalf("pattern %q stopped during full enumeration", tc.pattern)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pattern %q: Each returned %+v, want %+v", tc.pattern, got, want)
		}
		if match, ok := matcher.Find(tc.haystack); !ok || match != want[0].match {
			t.Errorf("pattern %q: Find = %+v,%t, want %+v,true", tc.pattern, match, ok, want[0].match)
		}

		calls := 0
		var first refEachResult
		complete := matcher.Each(tc.haystack, func(match Match, width int) bool {
			calls++
			first = refEachResult{match: match, width: width}
			return false
		})
		if complete || calls != 1 || first != want[0] {
			t.Errorf("pattern %q: stopped Each = (%v, %d calls, %+v), want (false, 1 call, %+v)", tc.pattern, complete, calls, first, want[0])
		}
	}
}
