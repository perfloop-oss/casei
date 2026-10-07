package casei

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var rootBucketEnglishPatterns = []string{
	"Sherlock Holmes", "John Watson", "Irene Adler", "Inspector Lestrade", "Professor Moriarty",
}

var rootBucketSherlockPatterns = []string{"Sherlock", "Holmes", "Watson"}

var rootBucketLeipzigPatterns = []string{"Tom", "Sawyer", "Huckleberry", "Finn"}

var rootBucketRussianPatterns = []string{
	"Шерлок Холмс", "Джон Уотсон", "Ирен Адлер", "инспектор Лестрейд", "профессор Мориарти",
}

func collectRootBucketEach(matcher *Matcher, haystack string, bucket tripleBucketFilter) ([]refEachResult, bool) {
	var results []refEachResult
	complete := matcher.plan.eachRootBucket(haystack, bucket, func(match Match, width int) bool {
		results = append(results, refEachResult{match: match, width: width})
		return true
	})
	return results, complete
}

func rootASCIIWordTestData(certs rootASCIIWordCerts) []byte {
	data := make([]byte, rootASCIIWordCertBytes(certs.count))
	certs.write(data)
	return data
}

func rootASCIIWordTestBucket(certs rootASCIIWordCerts) tripleBucketFilter {
	data := rootASCIIWordTestData(certs)
	bucket := make(tripleBucketFilter, tripleBucketFilterBytes+len(data))
	bucket[tripleBucketTableBytes] = 1
	bucket[tripleBucketFilterBytes-1] = tripleBucketValidMarker
	copy(bucket[tripleBucketFilterBytes:], data)
	return bucket
}

func hasCompleteRootBucketEachScreen(plan *searchPlan) bool {
	return plan != nil && plan.empty < 0 && plan.patternCount >= 3 && plan.patternCount <= 5 &&
		!plan.opaqueContinuation && plan.rootKind == rootGeneric && !plan.rawByteMulti.usable() &&
		plan.triplesComplete && plan.triples.shufti.usable()
}

func checkPublicEach(t *testing.T, matcher *Matcher, haystack string, want []refEachResult) {
	t.Helper()
	var got []refEachResult
	complete := matcher.Each(haystack, func(match Match, width int) bool {
		got = append(got, refEachResult{match: match, width: width})
		return true
	})
	if !complete || !reflect.DeepEqual(got, want) {
		t.Fatalf("public Each(%x) = %+v complete=%t, want %+v", haystack, got, complete, want)
	}
}

func checkRootBucketEach(t *testing.T, matcher *Matcher, haystack string) {
	t.Helper()
	want := refEach(haystack, matcher.patterns)
	got, complete := collectRootBucketEach(matcher, haystack, matcher.plan.tripleBucketFilter())
	if !complete || !reflect.DeepEqual(got, want) {
		t.Fatalf("root iterator Each(%x) = %+v complete=%t, want %+v", haystack, got, complete, want)
	}
	checkPublicEach(t, matcher, haystack, want)
}

func TestEachRootBucketMatchesReference(t *testing.T) {
	threeTiePatterns := []string{"Sherlock", "Sherlock Holmes", "Watson"}
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
			name:     "three-name roots include width-changing folds",
			patterns: rootBucketSherlockPatterns,
			haystack: strings.Repeat("x", 64) + "ſherlock; hOLMES; Watson!",
		},
		{
			name:     "three-name match contains long-s and Kelvin folds",
			patterns: rootBucketSherlockPatterns,
			haystack: strings.Repeat("x", 64) + "ſherlocK; Holmeſ; Watson!",
		},
		{
			name:     "three-name candidate reaches the haystack tail",
			patterns: rootBucketSherlockPatterns,
			haystack: strings.Repeat("x", 61) + "Watson",
		},
		{
			name:     "three-pattern lowest-ID prefix tie keeps its width",
			patterns: threeTiePatterns,
			haystack: strings.Repeat("x", 64) + "Sherlock Holmes; Watson",
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
			name:     "three-name route across malformed bytes",
			patterns: rootBucketSherlockPatterns,
			haystack: string(append(append([]byte(strings.Repeat("x", 64)), 0xff, 0x80), []byte("HOLMES and Watson")...)),
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
			name:     "ASCII same-start longest and lowest-ID ties",
			patterns: tiePatterns,
			haystack: strings.Repeat("x", 70) + "Sherlock Holmes and John Watson",
		},
		{
			name:     "shorter lower-ID prefix width wins",
			patterns: shortTiePatterns,
			haystack: strings.Repeat("x", 64) + "ſherlock Holmes and John Watson",
		},
		{
			name:     "ASCII shorter lower-ID prefix width wins",
			patterns: shortTiePatterns,
			haystack: strings.Repeat("x", 64) + "Sherlock Holmes and John Watson",
		},
		{
			name:     "short ASCII match at the end needs trie fallback",
			patterns: rootBucketLeipzigPatterns,
			haystack: strings.Repeat("x", 64) + "Tom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matcher := NewMatcher(tc.patterns)
			if matcher.plan.patternCount < 3 || matcher.plan.patternCount > 5 ||
				matcher.plan.rootKind != rootGeneric || !matcher.plan.triplesComplete {
				t.Fatalf("test plan does not own a complete generic root filter: patterns=%q root=%d complete=%t",
					matcher.patterns, matcher.plan.rootKind, matcher.plan.triplesComplete)
			}
			if hasCompleteRootBucketEachScreen(matcher.plan) {
				checkRootBucketEach(t, matcher, tc.haystack)
			} else {
				checkPublicEach(t, matcher, tc.haystack, refEach(tc.haystack, tc.patterns))
			}

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

func TestRootASCIIWordCertificatesMatchTrie(t *testing.T) {
	patternSets := []struct {
		name     string
		patterns []string
	}{
		{"English", rootBucketEnglishPatterns},
		{"Leipzig", rootBucketLeipzigPatterns},
		{"longer lower ID", []string{"Sherlock Holmes", "Sherlock", "Sherlock", "John Watson", "Irene Adler"}},
		{"shorter lower ID", []string{"Sherlock", "Sherlock Holmes", "Sherlock", "John Watson", "Irene Adler"}},
		{"Unicode fold mates with ASCII forms", []string{"ſherlock Holmes", "Sherlock Holmes", "John Watson", "Irene Adler", "Professor Moriarty"}},
	}
	rng := rand.New(rand.NewPCG(0x6173636969, 0x63657274))
	for _, tc := range patternSets {
		t.Run(tc.name, func(t *testing.T) {
			matcher := NewMatcher(tc.patterns)
			certs, ok := makeRootASCIIWordCerts(matcher.plan, matcher.patterns)
			if !ok {
				t.Fatal("ASCII-token plan did not compile word certificates")
			}
			data := rootASCIIWordTestData(certs)
			for sample := 0; sample < 64; sample++ {
				input := make([]byte, int(certs.maxUnits)+32)
				for i := range input {
					input[i] = byte(rng.IntN(utf8.RuneSelf))
				}
				if sample%2 == 0 {
					pattern := strings.ReplaceAll(tc.patterns[sample%len(tc.patterns)], "ſ", "s")
					copy(input[sample%16:], pattern)
				}
				haystack := string(input)
				for start := 0; start+int(certs.maxUnits) <= len(haystack); start++ {
					got, gotWidth, gotOK, known := matchRootASCIIWordCertData(data, haystack, start)
					if !known {
						t.Fatalf("ASCII window at %d was not certified", start)
					}
					want, wantWidth, wantOK := matcher.plan.matchAtStart(haystack, start)
					if got != want || gotWidth != wantWidth || gotOK != wantOK {
						t.Fatalf("certificate at %d = %+v/%d/%t, trie = %+v/%d/%t", start, got, gotWidth, gotOK, want, wantWidth, wantOK)
					}
				}
			}
		})
	}
}

func TestRootASCIIWordCertificatesThreeWordBoundary(t *testing.T) {
	patterns := []string{
		strings.Repeat("a", rootASCIIWordMaxUnits),
		strings.Repeat("b", rootASCIIWordMaxUnits),
		strings.Repeat("c", rootASCIIWordMaxUnits),
		strings.Repeat("d", rootASCIIWordMaxUnits),
		strings.Repeat("e", rootASCIIWordMaxUnits),
	}
	matcher := NewMatcher(patterns)
	certs, ok := makeRootASCIIWordCerts(matcher.plan, matcher.patterns)
	if !ok || int(certs.maxUnits) != rootASCIIWordMaxUnits {
		t.Fatalf("max-size certificate = %+v, want %d units", certs, rootASCIIWordMaxUnits)
	}
	data := rootASCIIWordTestData(certs)
	for patternID, pattern := range patterns {
		got, width, ok, known := matchRootASCIIWordCertData(data, pattern, 0)
		if !known || !ok || got != (Match{Pattern: patternID, Start: 0}) || width != rootASCIIWordMaxUnits {
			t.Fatalf("exact-boundary match %d = %+v/%d/%t known=%t", patternID, got, width, ok, known)
		}
	}
	short := strings.Repeat("a", rootASCIIWordMaxUnits-1)
	if _, _, _, known := matchRootASCIIWordCertData(data, short, 0); known {
		t.Fatal("short three-word tail did not fall back to the trie")
	}
}

func TestRootASCIIWordCertificatesFallback(t *testing.T) {
	patterns := []string{"ſherlock Holmes", "Sherlock Holmes", "John Watson", "Irene Adler", "Professor Moriarty"}
	matcher := NewMatcher(patterns)
	certs, ok := makeRootASCIIWordCerts(matcher.plan, matcher.patterns)
	if !ok {
		t.Fatal("fold mates with ASCII representatives did not compile word certificates")
	}
	data := rootASCIIWordTestData(certs)
	bucket := rootASCIIWordTestBucket(certs)
	p := matcher.plan
	compare := func(haystack string, start int) {
		t.Helper()
		got, gotWidth, gotOK := p.matchRootBucketCandidate(haystack, start, bucket)
		want, wantWidth, wantOK := p.matchAtStart(haystack, start)
		if got != want || gotWidth != wantWidth || gotOK != wantOK {
			t.Fatalf("candidate at %d = %+v/%d/%t, trie = %+v/%d/%t", start, got, gotWidth, gotOK, want, wantWidth, wantOK)
		}
	}

	ascii := "Sherlock Holmesabc"
	if _, _, _, known := matchRootASCIIWordCertData(data, ascii, 0); !known {
		t.Fatal("complete ASCII prefix did not use its certificate")
	}
	compare(ascii, 0)
	for _, haystack := range []string{"ſherlock Holmesxx", "SherlocK Holmesx"} {
		if _, _, _, known := matchRootASCIIWordCertData(data, haystack, 0); known {
			t.Fatalf("non-ASCII prefix %q was certified", haystack)
		}
		compare(haystack, 0)
	}
	got, width, ok := p.matchRootBucketCandidate("ſherlock Holmesxx", 0, bucket)
	if !ok || got != (Match{Pattern: 0, Start: 0}) || width != len("ſherlock Holmes") {
		t.Fatalf("unknown lower-ID spelling = %+v/%d/%t, want pattern 0 width %d", got, width, ok, len("ſherlock Holmes"))
	}

	for at := 0; at < int(certs.maxUnits); at++ {
		bytes := []byte(strings.Repeat("x", int(certs.maxUnits)))
		bytes[at] = 0xff
		if _, _, _, known := matchRootASCIIWordCertData(data, string(bytes), 0); known {
			t.Fatalf("high byte at %d did not force trie fallback", at)
		}
	}
	for length := 0; length < int(certs.maxUnits); length++ {
		haystack := strings.Repeat("x", length)
		if _, _, _, known := matchRootASCIIWordCertData(data, haystack, 0); known {
			t.Fatalf("%d-byte tail did not force trie fallback", length)
		}
	}

	leipzig := NewMatcher(rootBucketLeipzigPatterns)
	leipzigCerts, ok := makeRootASCIIWordCerts(leipzig.plan, leipzig.patterns)
	if !ok {
		t.Fatal("Leipzig ASCII plan did not compile word certificates")
	}
	leipzigBucket := rootASCIIWordTestBucket(leipzigCerts)
	if got, width, ok := leipzig.plan.matchRootBucketCandidate("Tom", 0, leipzigBucket); !ok || got != (Match{Pattern: 0, Start: 0}) || width != 3 {
		t.Fatalf("short end match = %+v/%d/%t, want pattern 0 width 3", got, width, ok)
	}
}

func TestRootASCIIWordCertificatesRejectUnknownTokens(t *testing.T) {
	patterns := []string{"Sherlock Holmes", "John Watson", "Irene Adler", "Professor Moriarty", "Δelta"}
	matcher := NewMatcher(patterns)
	if _, ok := makeRootASCIIWordCerts(matcher.plan, matcher.patterns); ok {
		t.Fatal("non-ASCII-only token unexpectedly received an ASCII certificate")
	}
}

func TestMatcherEachRootBucketEligibility(t *testing.T) {
	patternSets := [][]string{rootBucketSherlockPatterns, rootBucketLeipzigPatterns, rootBucketEnglishPatterns}
	for _, patterns := range patternSets {
		plan := NewMatcher(patterns).plan
		if !asciiPairVBMIEnabled() {
			if _, ok := plan.rootBucketEachFilter(strings.Repeat("x", 1024)); ok {
				t.Fatalf("feature-off plan selected the root iterator for %q", patterns)
			}
			continue
		}
		hasCerts := rootASCIIWordCertData(plan.tripleBucketFilter()) != nil
		if wantCerts := len(patterns) >= 4; hasCerts != wantCerts {
			t.Fatalf("root certificates = %t for %q, want %t", hasCerts, patterns, wantCerts)
		}
		if _, ok := plan.rootBucketEachFilter(strings.Repeat("x", rootBucketEachMinBytes-1)); ok {
			t.Fatalf("short input selected the root iterator for %q", patterns)
		}
		if _, ok := plan.rootBucketEachFilter(strings.Repeat("x", rootBucketEachMinBytes)); !ok {
			t.Fatalf("eligible input did not select the root iterator for %q", patterns)
		}
	}
	if !asciiPairVBMIEnabled() {
		matcher := NewMatcher(rootBucketSherlockPatterns)
		checkRootBucketEach(t, matcher, strings.Repeat("x", 61)+"Watson")
		return
	}
	sixPatterns := append(append([]string(nil), rootBucketEnglishPatterns...), "Mycroft Holmes")
	sixPlan := NewMatcher(sixPatterns).plan
	if sixPlan.patternCount != 6 || sixPlan.rootKind != rootGeneric || sixPlan.rawByteMulti.usable() ||
		!sixPlan.triplesComplete || !sixPlan.triples.shufti.usable() || !sixPlan.tripleBucketFilter().usable() {
		t.Fatal("six-pattern upper-bound plan does not satisfy the other root iterator gates")
	}
	if _, ok := sixPlan.rootBucketEachFilter(strings.Repeat("x", 1024)); ok {
		t.Fatal("six-pattern plan exceeded the root iterator's existing upper bound")
	}
	russian := NewMatcher(rootBucketRussianPatterns).plan
	if _, ok := russian.rootBucketEachFilter(strings.Repeat("x", 1024)); ok {
		t.Fatal("Russian raw-byte plan unexpectedly selected the root iterator")
	}
	if rootASCIIWordCertData(russian.tripleBucketFilter()) != nil {
		t.Fatal("Russian raw-byte plan unexpectedly compiled root certificates")
	}
}

func TestEachRootBucketRandomDifferential(t *testing.T) {
	sets := [][]string{rootBucketSherlockPatterns, rootBucketEnglishPatterns, rootBucketLeipzigPatterns}
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
	for _, patterns := range [][]string{rootBucketSherlockPatterns, rootBucketEnglishPatterns} {
		matcher := NewMatcher(patterns)
		for _, length := range []int{66, 67, 68, 127, 128, 129} {
			checkRootBucketEach(t, matcher, strings.Repeat("x", length-3)+"ABC")
			checkRootBucketEach(t, matcher, strings.Repeat("x", length-len("Watson"))+"Watson")
		}
	}
}
