package casei

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestASCIIOnlyMatchWidthUsesPlanTokens(t *testing.T) {
	literal := make([]byte, utf8.RuneSelf)
	for i := range literal {
		literal[i] = byte(i)
	}
	plan := newSearchPlan([]string{string(literal)})
	if !plan.asciiOnly || !plan.asciiVerifyTokens || len(plan.singleTokens) != len(literal) {
		t.Fatalf("ASCII token plan = asciiOnly:%t verify:%t tokens:%d; want an all-ASCII token-verified literal", plan.asciiOnly, plan.asciiVerifyTokens, len(plan.singleTokens))
	}

	candidate := append([]byte(nil), literal...)
	for pos := range literal {
		for value := range utf8.RuneSelf {
			candidate[pos] = byte(value)
			haystack := string(candidate)
			width, fast := plan.asciiOnlyMatchWidth(haystack, 0)
			want := plan.matchesSingleAt(haystack, 0)
			if fast != want {
				t.Fatalf("ASCII byte %02x at pattern byte %d: fast match %t, token plan match %t", value, pos, fast, want)
			}
			if fast && width != len(literal) {
				t.Fatalf("ASCII byte %02x at pattern byte %d: width %d, want %d", value, pos, width, len(literal))
			}
		}
		candidate[pos] = literal[pos]
	}
}

func TestASCIIOnlyMatchWidthBoundsAndUnicodeFallback(t *testing.T) {
	matcher := NewMatcher([]string{"Sherlock Holmes"})
	plan := matcher.plan
	for end := 0; end < len(plan.asciiNeedle); end++ {
		if _, ok := plan.asciiOnlyMatchWidth(plan.asciiNeedle[:end], 0); ok {
			t.Fatalf("truncated ASCII window of %d bytes was confirmed", end)
		}
	}
	width, ok := plan.asciiOnlyMatchWidth("x"+plan.asciiNeedle, 1)
	if !ok || width != len(plan.asciiNeedle) {
		t.Fatalf("complete unaligned ASCII window = (%d,%t), want (%d,true)", width, ok, len(plan.asciiNeedle))
	}

	unicodePlan := newSearchPlan([]string{"abcK"})
	if !unicodePlan.asciiVerifyTokens || unicodePlan.asciiOnly {
		t.Fatalf("Unicode token plan = verify:%t asciiOnly:%t", unicodePlan.asciiVerifyTokens, unicodePlan.asciiOnly)
	}
	// asciiPatternAt visits rune starts, so the leading bytes alone can appear
	// to confirm a Unicode pattern. The token matcher must own this candidate.
	if !unicodePlan.asciiOnlyPatternAt("abc\xe2xx", 0, unicodePlan.asciiNeedle) {
		t.Fatal("fixture no longer exposes the non-ASCII pattern alias")
	}
	if _, ok := unicodePlan.asciiOnlyMatchWidth("abc\xe2xx", 0); ok {
		t.Fatal("non-ASCII pattern was accepted by the ASCII width check")
	}

	input := strings.Repeat("x", 5000) + "abc\xe2xx"
	got := collectASCIIInteriorEach(NewMatcher([]string{"abcK"}), input)
	want := refEach(input, []string{"abcK"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Each(%x) = %+v, want Unicode fallback %+v", input, got, want)
	}
}

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
