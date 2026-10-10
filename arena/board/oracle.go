package board

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FoldKey maps a rune to the smallest member of its simple-fold orbit, so
// fold-equality becomes plain equality of keys. It is the arena's definition of
// Unicode simple case folding.
func FoldKey(r rune) rune {
	m := r
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		if x < m {
			m = x
		}
	}
	return m
}

// FoldString rewrites every valid rune of s as its FoldKey. Invalid bytes stay
// as they are. On valid UTF-8, two strings are fold-equal exactly when their
// folded forms are byte-equal.
func FoldString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(s[i])
		} else {
			b.WriteRune(FoldKey(r))
		}
		i += size
	}
	return b.String()
}

// Hit is one match: the matched pattern's index, the match start in the
// haystack, and the haystack bytes it spans. Under simple folding the width can
// differ from the pattern's byte length.
type Hit struct {
	Start, Pattern, Width int
}

// Matches is the board's answer key. It returns the non-overlapping matches of
// Matcher.Each: leftmost start first, ties to the lowest pattern index, the
// next search starting where the last match ended. Find and IndexFold answer
// with its first hit.
//
// It is structurally unlike the candidate: it folds the whole haystack once,
// then runs an exact substring search per pattern and keeps each pattern's next
// occurrence. Matching on folded valid UTF-8 is rune-aligned, because a folded
// pattern starts with a lead byte that no continuation byte can equal. The
// haystack and patterns must be valid UTF-8 and the patterns non-empty; the
// generator draws only such cells.
func Matches(haystack string, patterns []string, sensitive bool) []Hit {
	if !utf8.ValidString(haystack) {
		panic("board oracle needs valid UTF-8")
	}
	text, offset := haystack, func(i int) int { return i }
	keys := patterns
	if !sensitive {
		var origin []int32
		text, origin = foldWithOrigin(haystack)
		offset = func(i int) int { return int(origin[i]) }
		keys = make([]string, len(patterns))
		for i, p := range patterns {
			keys[i] = FoldString(p)
		}
	}
	for _, k := range keys {
		if k == "" || !utf8.ValidString(k) {
			panic("board oracle needs non-empty valid UTF-8 patterns")
		}
	}

	index := func(key string, from int) int {
		j := strings.Index(text[from:], key)
		if j < 0 {
			return -1
		}
		return from + j
	}
	next := make([]int, len(keys))
	for i, k := range keys {
		next[i] = index(k, 0)
	}
	var hits []Hit
	for at := 0; ; {
		best := -1
		for i, k := range keys {
			if next[i] >= 0 && next[i] < at {
				next[i] = index(k, at)
			}
			if next[i] >= 0 && (best < 0 || next[i] < next[best]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		start, end := next[best], next[best]+len(keys[best])
		hits = append(hits, Hit{Start: offset(start), Pattern: best, Width: offset(end) - offset(start)})
		at = end
	}
	return hits
}

// foldWithOrigin folds s and records, for each folded byte, the offset in s of
// the rune it came from, plus a final entry for len(s).
func foldWithOrigin(s string) (string, []int32) {
	var b strings.Builder
	b.Grow(len(s))
	origin := make([]int32, 0, len(s)+1)
	var buf [utf8.UTFMax]byte
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		n := utf8.EncodeRune(buf[:], FoldKey(r))
		b.Write(buf[:n])
		for range n {
			origin = append(origin, int32(i))
		}
		i += size
	}
	origin = append(origin, int32(len(s)))
	return b.String(), origin
}
