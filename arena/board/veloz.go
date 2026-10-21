package board

import (
	"fmt"
	"unicode/utf8"

	veloz "github.com/mhr3/veloz/ascii"
	"golang.org/x/sys/cpu"
)

// VelozView adapts Veloz's ASCII-byte IndexFold to the board's Unicode
// simple-fold relation. Construction retains one canonical needle per pattern.
// Each call rebuilds the canonical haystack and its per-byte int32 source map;
// Each additionally allocates one machine-int pending start per pattern. The
// first and later calls pay the same conversion cost, and no call scratch is
// retained or pooled.
type VelozView struct {
	needles []string
}

// VelozAvailable reports whether the pinned AVX2 field route is enabled.
// Veloz's SSE/scalar fallback is intentionally not counted as this entrant.
func VelozAvailable() bool { return cpu.X86.HasAVX2 }

// NewVelozView canonicalizes one literal per original pattern ID. The board
// supplies non-empty, valid UTF-8 patterns. BenchmarkBoard constructs views
// before its retained-search timing, like its other compiled entrants.
func NewVelozView(patterns []string) (*VelozView, error) {
	if !VelozAvailable() {
		return nil, fmt.Errorf("pinned Veloz AVX2 route is unavailable")
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("Veloz view needs at least one pattern")
	}
	view := &VelozView{needles: make([]string, len(patterns))}
	for i, pattern := range patterns {
		if pattern == "" {
			return nil, fmt.Errorf("Veloz view pattern %d is empty", i)
		}
		if !utf8.ValidString(pattern) {
			return nil, fmt.Errorf("Veloz view pattern %d is not valid UTF-8", i)
		}
		view.needles[i] = FoldString(pattern)
	}
	return view, nil
}

// Find returns the earliest source-byte hit, breaking ties by original pattern
// ID. Width is projected from the canonical endpoint to the original input.
func (view *VelozView) Find(haystack string) (Hit, bool) {
	if view == nil || len(view.needles) == 0 {
		return Hit{}, false
	}
	if haystack == "" {
		panic("Veloz view requires nonempty valid UTF-8 input")
	}
	canonical, origin := foldWithOrigin(haystack)
	bestStart, bestPattern := -1, -1
	for pattern, needle := range view.needles {
		start := veloz.IndexFold(canonical, needle)
		if start >= 0 && (bestStart < 0 || start < bestStart || start == bestStart && pattern < bestPattern) {
			bestStart, bestPattern = start, pattern
		}
	}
	if bestPattern < 0 {
		return Hit{}, false
	}
	return view.project(canonical, origin, bestStart, bestPattern), true
}

// Each calls yield with every leftmost, lowest-ID, non-overlapping source hit.
// Pending starts and restart offsets stay in canonical byte coordinates; all
// search state is local to this invocation, so a callback may re-enter view.
func (view *VelozView) Each(haystack string, yield func(Hit) bool) bool {
	if view == nil || len(view.needles) == 0 {
		return true
	}
	if haystack == "" {
		panic("Veloz view requires nonempty valid UTF-8 input")
	}
	canonical, origin := foldWithOrigin(haystack)
	next := make([]int, len(view.needles))
	for pattern, needle := range view.needles {
		next[pattern] = view.searchFrom(canonical, needle, 0)
	}

	for cursor := 0; ; {
		best := -1
		for pattern, start := range next {
			if start >= 0 && (best < 0 || start < next[best] || start == next[best] && pattern < best) {
				best = pattern
			}
		}
		if best < 0 {
			return true
		}

		start := next[best]
		end := start + len(view.needles[best])
		hit := view.project(canonical, origin, start, best)
		if !yield(hit) {
			return false
		}

		cursor = end
		for pattern, pending := range next {
			if pending >= 0 && pending < cursor {
				next[pattern] = view.searchFrom(canonical, view.needles[pattern], cursor)
			}
		}
	}
}

// searchFrom returns an absolute canonical byte offset, or -1 when absent.
// Callers restart only at canonical rune boundaries.
func (view *VelozView) searchFrom(canonical, needle string, from int) int {
	if from < 0 || from > len(canonical) || !canonicalBoundary(canonical, from) {
		panic("Veloz view restart is not a canonical rune boundary")
	}
	if relative := veloz.IndexFold(canonical[from:], needle); relative >= 0 {
		return from + relative
	}
	return -1
}

func (view *VelozView) project(canonical string, origin []int32, start, pattern int) Hit {
	end := start + len(view.needles[pattern])
	if len(origin) != len(canonical)+1 || !canonicalBoundary(canonical, start) || !canonicalBoundary(canonical, end) {
		panic("Veloz view match is not bounded by canonical rune boundaries")
	}
	sourceStart, sourceEnd := int(origin[start]), int(origin[end])
	if sourceStart < 0 || sourceEnd < sourceStart {
		panic("Veloz view source-offset map is not monotone at a match")
	}
	return projectFoldedOffsets(sourceStart, sourceEnd, pattern)
}
