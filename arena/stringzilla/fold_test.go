package stringzilla

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// slowFoldMatchAt is simpleFoldMatchAt without the ASCII fast path.
func slowFoldMatchAt(haystack, needle string, start int) bool {
	haystack = haystack[start:]
	for _, n := range needle {
		h, size := utf8.DecodeRuneInString(haystack)
		if len(haystack) == 0 || !simpleFoldEqual(h, n) {
			return false
		}
		haystack = haystack[size:]
	}
	return true
}

// TestSimpleFoldMatchAt pins the ASCII fast path to the orbit walk: ASCII
// letters fold, the 0x20-apart punctuation pairs do not, and the Kelvin sign
// and long s still match k and s through the non-ASCII path.
func TestSimpleFoldMatchAt(t *testing.T) {
	for _, tc := range []struct {
		haystack, needle string
		want             bool
	}{
		{"xHeLLo", "hello", true}, {"x[", "{", false}, {"x@", "`", false}, {"x^", "~", false},
		{"xKelvin", "kelvin", true}, {"xſecret", "SECRET", true}, {"xτέλος", "ΤΈΛΟΣ", true},
		{"xgroße", "GROSSE", false}, {"xab", "abc", false},
	} {
		if got := simpleFoldMatchAt(tc.haystack, tc.needle, 1); got != tc.want {
			t.Errorf("simpleFoldMatchAt(%q, %q) = %v, want %v", tc.haystack, tc.needle, got, tc.want)
		}
	}
	alphabet := []rune("aAzZ[{@`^~09 KkKsſSσς")
	rng := rand.New(rand.NewPCG(1, 2))
	random := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 100000 {
		haystack, needle := random(rng.IntN(6)), random(rng.IntN(4))
		if got, want := simpleFoldMatchAt(haystack, needle, 0), slowFoldMatchAt(haystack, needle, 0); got != want {
			t.Fatalf("simpleFoldMatchAt(%q, %q) = %v, orbit walk %v", haystack, needle, got, want)
		}
	}
}

// BenchmarkSimpleFoldMatchAtASCII times verifying one 16-byte ASCII match.
func BenchmarkSimpleFoldMatchAtASCII(b *testing.B) {
	haystack, needle := "Sherlock Holmes!", "sherlock holmes!"
	for b.Loop() {
		if !simpleFoldMatchAt(haystack, needle, 0) {
			b.Fatal("no match")
		}
	}
}
