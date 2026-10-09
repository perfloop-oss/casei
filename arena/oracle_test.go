package arena_test

import (
	"unicode/utf8"

	"github.com/tsenart/casei"
	"github.com/tsenart/casei/arena/board"
)

// ---- the arena's own semantic oracle ----------------------------------------
//
// Copied deliberately rather than shared with the candidate's tests. This is
// the definition of "correct" that every entrant, candidate and baseline
// alike, is held to. A candidate that could edit it could move the field it is
// being measured against.

// canonFold decodes s into canonical fold form. Opaque (invalid-encoding)
// bytes become distinct negative sentinels so they compare byte-exactly and
// can never collide with a real rune. offs[i] is the byte offset in s of
// canonical element i; a final entry holds len(s).
func canonFold(s string) (canon []rune, offs []int) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			canon = append(canon, -rune(s[i])-1)
		} else {
			canon = append(canon, board.FoldKey(r))
		}
		offs = append(offs, i)
		i += size
	}
	offs = append(offs, len(s))
	return canon, offs
}

// reference is a second, structurally different implementation: canonical
// fold both strings, then exact slice search. Every implementation in this
// repository must agree with it on every input.
func reference(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	ch, offs := canonFold(haystack)
	cn, _ := canonFold(needle)
	for i := 0; i+len(cn) <= len(ch); i++ {
		match := true
		for j := range cn {
			if ch[i+j] != cn[j] {
				match = false
				break
			}
		}
		if match {
			return offs[i]
		}
	}
	return -1
}

// fold reference, leftmost start, ties to the lowest pattern index.
func refFind(haystack string, patterns []string) (casei.Match, bool) {
	best := casei.Match{Pattern: -1, Start: -1}
	for i, p := range patterns {
		pos := reference(haystack, p)
		if pos < 0 {
			continue
		}
		if best.Pattern < 0 || pos < best.Start {
			best = casei.Match{Pattern: i, Start: pos}
		}
	}
	if best.Pattern < 0 {
		return casei.Match{}, false
	}
	return best, true
}
