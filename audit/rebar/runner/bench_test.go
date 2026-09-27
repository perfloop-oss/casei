//go:build go1.24

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsenart/casei"
)

// rebarRow is one of the 18 Rebar performance rows in REBAR.md, copied from
// the definitions at Rebar commit 463d00f31887e84c38467805b9e3122c314b9521.
// count is Rebar's expected answer: matches for the count model, and the sum
// of match widths in bytes for the count-spans model.
type rebarRow struct {
	id       string
	regex    string
	spans    bool
	haystack string
	count    int
}

var rebarRows = []rebarRow{
	{"curated/01-literal/sherlock-casei-en", "Sherlock Holmes", false, "opensubtitles/en-sampled.txt", 522},
	{"curated/01-literal/sherlock-casei-ru", "Шерлок Холмс", false, "opensubtitles/ru-sampled.txt", 746},
	{"curated/02-literal-alternate/sherlock-casei-en", "Sherlock Holmes|John Watson|Irene Adler|Inspector Lestrade|Professor Moriarty", false, "opensubtitles/en-sampled.txt", 725},
	{"curated/02-literal-alternate/sherlock-casei-ru", "Шерлок Холмс|Джон Уотсон|Ирен Адлер|инспектор Лестрейд|профессор Мориарти", false, "opensubtitles/ru-sampled.txt", 971},
	{"hyperscan/literal-casei-english-nosom", "Sherlock Holmes", false, "opensubtitles/en-huge.txt", 1},
	{"hyperscan/literal-casei-english-som", "Sherlock Holmes", true, "opensubtitles/en-huge.txt", 15},
	{"hyperscan/literal-casei-russian-nosom", "Шерлок Холмс", false, "opensubtitles/ru-huge.txt", 1},
	{"hyperscan/literal-casei-russian-som", "Шерлок Холмс", true, "opensubtitles/ru-huge.txt", 23},
	{"imported/leipzig/twain-insensitive", "Twain", false, "imported/leipzig-3200.txt", 965},
	{"imported/leipzig/tom-sawyer-huckle-fin-insensitive", "Tom|Sawyer|Huckleberry|Finn", false, "imported/leipzig-3200.txt", 4152},
	{"imported/sherlock/name-sherlock-casei", "Sherlock", true, "sherlock.txt", 816},
	{"imported/sherlock/name-holmes-casei", "Holmes", true, "sherlock.txt", 2802},
	{"imported/sherlock/name-sherlock-holmes-casei", "Sherlock Holmes", true, "sherlock.txt", 1440},
	{"imported/sherlock/name-alt3-casei", "Sherlock|Holmes|Watson|Irene|Adler|John|Baker", true, "sherlock.txt", 4593},
	{"imported/sherlock/name-alt5-casei", "Sherlock|Holmes|Watson", true, "sherlock.txt", 4104},
	{"imported/sherlock/the-casei", "the", true, "sherlock.txt", 23961},
	{"opt/prefilter/literal-casei-english", "Sherlock Holmes", false, "opensubtitles/en-huge.txt", 1},
	{"opt/prefilter/literal-casei-russian", "Шерлок Холмс", false, "opensubtitles/ru-huge.txt", 1},
}

// benchSink keeps the timed result live.
var benchSink int

// BenchmarkRebar times the operation the Rebar runner times on each row:
// countMatches over the whole haystack with a Matcher compiled once. Each row
// first passes the runner's untimed verifyEnumeration and must reproduce
// Rebar's expected count, so a wrong engine cannot report a time.
//
// Haystacks come from audit/rebar/haystacks.sh. CASEI_REBAR_HAYSTACKS names
// their directory; the default is ../haystacks relative to this package.
func BenchmarkRebar(b *testing.B) {
	dir := os.Getenv("CASEI_REBAR_HAYSTACKS")
	if dir == "" {
		dir = filepath.Join("..", "haystacks")
	}
	for _, row := range rebarRows {
		b.Run(strings.ReplaceAll(row.id, "/", "-"), func(b *testing.B) {
			path := filepath.Join(dir, row.haystack)
			raw, err := os.ReadFile(path)
			if err != nil {
				b.Fatalf("haystack for %s: %v (fetch with audit/rebar/haystacks.sh and set CASEI_REBAR_HAYSTACKS)", row.id, err)
			}
			haystack := string(raw)
			patterns, err := literalAlternation([]string{row.regex})
			if err != nil {
				b.Fatal(err)
			}
			matcher := casei.NewMatcher(patterns)
			got, err := verifyEnumeration(haystack, patterns, matcher, row.spans)
			if err != nil {
				b.Fatalf("%s: %v", row.id, err)
			}
			if got != row.count {
				b.Fatalf("%s: verified count %d, Rebar expects %d", row.id, got, row.count)
			}
			if got := countMatches(haystack, matcher, row.spans); got != row.count {
				b.Fatalf("%s: countMatches = %d, Rebar expects %d", row.id, got, row.count)
			}
			b.SetBytes(int64(len(haystack)))
			for b.Loop() {
				benchSink = countMatches(haystack, matcher, row.spans)
			}
		})
	}
}
