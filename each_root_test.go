package casei

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

var rootBucketEnglishPatterns = []string{
	"Sherlock Holmes", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty",
}

var rootBucketLeipzigPatterns = []string{"Tom", "Sawyer", "Huckleberry", "Finn"}

var rootBucketRussianPatterns = []string{
	"Шерлок Холмс", "Джон Уотсон", "Ирен Адлер", "инспектор Лестрейд", "профессор Мориарти",
}

func collectRootBucketEach(matcher *Matcher, haystack string) ([]refEachResult, bool) {
	var results []refEachResult
	bucket := matcher.plan.tripleBucketFilter()
	complete := matcher.plan.eachRootBucket(haystack, bucket, func(match Match, width int) bool {
		results = append(results, refEachResult{match: match, width: width})
		return true
	})
	return results, complete
}

func checkRootBucketEach(t *testing.T, matcher *Matcher, haystack string) {
	t.Helper()
	want := refEach(haystack, matcher.patterns)
	for _, route := range []struct {
		name string
		call func(*Matcher, string) ([]refEachResult, bool)
	}{
		{"root iterator", collectRootBucketEach},
		{"public Each", func(m *Matcher, h string) ([]refEachResult, bool) {
			var results []refEachResult
			complete := m.Each(h, func(match Match, width int) bool {
				results = append(results, refEachResult{match: match, width: width})
				return true
			})
			return results, complete
		}},
	} {
		got, complete := route.call(matcher, haystack)
		if !complete || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s Each(%x) = %+v complete=%t, want %+v", route.name, haystack, got, complete, want)
		}
	}
}

func TestEachRootBucketMatchesReference(t *testing.T) {
	tiePatterns := []string{"Sherlock Holmes", "Sherlock", "Sherlock", "John Watson", "Irene Adler"}
	shortTiePatterns := []string{"Sherlock", "Sherlock Holmes", "Sherlock", "John Watson", "Irene Adler"}
	cases := []struct {
		name     string
		patterns []string
		haystack string
	}{
		{
			name:     "both width-changing prefix folds and every English root",
			patterns: rootBucketEnglishPatterns,
			haystack: strings.Repeat("x", 64) + "ſherlocK Holmes; JOHN WATSON; Irene Adler; INSPECTOR LESTRADE; Professor Moriarty;",
		},
		{
			name:     "source-order candidates and non-overlap",
			patterns: rootBucketEnglishPatterns,
			haystack: strings.Repeat("x", 97) + "John Watson; " + strings.Repeat("x", 13) + "Sherlock Holmes; Irene Adler;",
		},
		{
			name:     "malformed bytes before and through candidates",
			patterns: rootBucketEnglishPatterns,
			haystack: string(append(append([]byte(strings.Repeat("x", 63)), 0xff, 0x80), []byte("ſherlocK Holmes and Professor Moriarty")...)),
		},
		{
			name:     "Tom and Sawyer width-changing prefix",
			patterns: rootBucketLeipzigPatterns,
			haystack: strings.Repeat("!", 127) + "ſawyer Huckleberry TOM Finn tom",
		},
		{
			name:     "same-start longest and lowest-ID ties",
			patterns: tiePatterns,
			haystack: strings.Repeat("x", 70) + "ſherlock Holmes and John Watson",
		},
		{
			name:     "shorter lower-ID prefix width wins",
			patterns: shortTiePatterns,
			haystack: strings.Repeat("x", 64) + "ſherlock Holmes and John Watson",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matcher := NewMatcher(tc.patterns)
			if matcher.plan.patternCount < 4 || matcher.plan.patternCount > 5 ||
				matcher.plan.rootKind != rootGeneric || !matcher.plan.triplesComplete {
				t.Fatalf("test plan does not own a complete generic root filter: patterns=%q root=%d complete=%t",
					matcher.patterns, matcher.plan.rootKind, matcher.plan.triplesComplete)
			}
			checkRootBucketEach(t, matcher, tc.haystack)

			want := refEach(tc.haystack, tc.patterns)
			calls := 0
			var first refEachResult
			complete := matcher.Each(tc.haystack, func(match Match, width int) bool {
				calls++
				first = refEachResult{match: match, width: width}
				return false
			})
			if complete || calls != 1 || len(want) == 0 || first != want[0] {
				t.Fatalf("early stop=(%t,%d,%+v), want (false,1,%+v)", complete, calls, first, want[0])
			}
		})
	}
}

func TestMatcherEachRootBucketEligibility(t *testing.T) {
	if !asciiPairVBMIEnabled() {
		t.Skip("AVX-512 VBMI bucket path is disabled")
	}
	for _, patterns := range [][]string{rootBucketEnglishPatterns, rootBucketLeipzigPatterns} {
		plan := NewMatcher(patterns).plan
		if _, ok := plan.rootBucketEachFilter(strings.Repeat("x", rootBucketEachMinBytes-1)); ok {
			t.Fatalf("short input selected the root iterator for %q", patterns)
		}
		if _, ok := plan.rootBucketEachFilter(strings.Repeat("x", rootBucketEachMinBytes)); !ok {
			t.Fatalf("eligible input did not select the root iterator for %q", patterns)
		}
	}
	if _, ok := NewMatcher([]string{"Sherlock", "Holmes", "Watson"}).plan.rootBucketEachFilter(strings.Repeat("x", 1024)); ok {
		t.Fatal("three-pattern guard unexpectedly selected the four/five-literal iterator")
	}
	if _, ok := NewMatcher(rootBucketRussianPatterns).plan.rootBucketEachFilter(strings.Repeat("x", 1024)); ok {
		t.Fatal("Russian raw-byte plan unexpectedly selected the root iterator")
	}
}

func TestEachRootBucketRandomDifferential(t *testing.T) {
	sets := [][]string{rootBucketEnglishPatterns, rootBucketLeipzigPatterns}
	rng := rand.New(rand.NewPCG(20260929, 0x726f6f74))
	for _, patterns := range sets {
		matcher := NewMatcher(patterns)
		for iteration := 0; iteration < 1000; iteration++ {
			haystack := []byte(randomBytes(rng, 64+rng.IntN(160)))
			if iteration%2 == 0 {
				literal := patterns[rng.IntN(len(patterns))]
				if iteration%4 == 0 {
					literal = strings.Replace(literal, "S", "ſ", 1)
				}
				haystack = append(haystack, []byte(literal)...)
			}
			checkRootBucketEach(t, matcher, string(haystack))
		}
	}
}

func TestMatcherEachRootBucketThreshold(t *testing.T) {
	matcher := NewMatcher(rootBucketEnglishPatterns)
	for _, length := range []int{66, 67, 68, 127, 128, 129} {
		haystack := strings.Repeat("x", length-3) + "ABC"
		checkRootBucketEach(t, matcher, haystack)
	}
}
