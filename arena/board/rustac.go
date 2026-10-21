package board

import (
	"fmt"
	"unicode/utf8"

	"github.com/tsenart/casei/arena/rustac"
)

// RustACView adapts RustAC's exact-byte API to the board's Unicode
// simple-fold relation. It retains one canonical literal per original
// pattern, in original order. Each operation owns its canonical text and
// origin map; neither is cached or shared with another call.
type RustACView struct {
	matcher        *rustac.Matcher
	patternLengths []int
}

// NewRustACView compiles one FoldString representative per nonempty, valid
// UTF-8 pattern through RustAC's unchanged exact API. Canonical duplicates
// remain in their original positions so LeftmostFirst keeps original-ID order.
func NewRustACView(patterns []string) (*RustACView, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("simple-fold RustAC view requires at least one pattern")
	}
	canonical := make([]string, len(patterns))
	lengths := make([]int, len(patterns))
	for i, pattern := range patterns {
		if pattern == "" {
			return nil, fmt.Errorf("pattern %d: empty patterns are outside the translated view", i)
		}
		if !utf8.ValidString(pattern) {
			return nil, fmt.Errorf("pattern %d: pattern is not valid UTF-8", i)
		}
		canonical[i] = FoldString(pattern)
		lengths[i] = len(canonical[i])
		if lengths[i] == 0 {
			return nil, fmt.Errorf("pattern %d: canonical pattern is empty", i)
		}
	}
	matcher, err := rustac.CompileAlternation(canonical, true)
	if err != nil {
		return nil, fmt.Errorf("compile canonical RustAC patterns: %w", err)
	}
	return &RustACView{matcher: matcher, patternLengths: lengths}, nil
}

// Close releases the native matcher. The owner must call it only after all
// Find and Each calls have returned.
func (view *RustACView) Close() {
	if view == nil || view.matcher == nil {
		return
	}
	view.matcher.Close()
	view.matcher = nil
	view.patternLengths = nil
}

// Find returns the leftmost hit with its original pattern ID and the number of
// source bytes consumed at that occurrence.
func (view *RustACView) Find(haystack string) (start, pattern, width int, ok bool, err error) {
	if view == nil || view.matcher == nil {
		return 0, 0, 0, false, fmt.Errorf("simple-fold RustAC view is closed")
	}
	text, origin, err := foldWithOriginChecked(haystack)
	if err != nil {
		return 0, 0, 0, false, err
	}
	canonicalStart, pattern, ok, err := view.matcher.FindErr(text)
	if err != nil || !ok {
		return 0, 0, 0, ok, err
	}
	if pattern < 0 || pattern >= len(view.patternLengths) {
		return 0, 0, 0, false, fmt.Errorf("RustAC returned pattern ID %d outside %d originals", pattern, len(view.patternLengths))
	}
	canonicalWidth := view.patternLengths[pattern]
	if canonicalWidth <= 0 || canonicalStart < 0 || canonicalStart > len(text)-canonicalWidth {
		return 0, 0, 0, false, fmt.Errorf("RustAC returned invalid canonical span start=%d width=%d length=%d", canonicalStart, canonicalWidth, len(text))
	}
	canonicalEnd := canonicalStart + canonicalWidth
	if !validCanonicalSpan(text, origin, canonicalStart, canonicalEnd) {
		return 0, 0, 0, false, fmt.Errorf("RustAC returned an unprojectable canonical span")
	}
	sourceStart, sourceEnd := int(origin[canonicalStart]), int(origin[canonicalEnd])
	if sourceStart < 0 || sourceEnd < sourceStart || sourceEnd > len(haystack) {
		return 0, 0, 0, false, fmt.Errorf("RustAC projected an invalid source span [%d,%d) for length %d", sourceStart, sourceEnd, len(haystack))
	}
	hit := projectFoldedOffsets(sourceStart, sourceEnd, pattern)
	return hit.Start, hit.Pattern, hit.Width, true, nil
}

// Each emits source-byte spans in RustAC's non-overlapping LeftmostFirst order.
// Native restart positions remain canonical; only callback spans are projected
// to source coordinates. A callback stop is returned as incomplete, not error.
func (view *RustACView) Each(haystack string, yield func(start, pattern, width int) bool) (bool, error) {
	if view == nil || view.matcher == nil {
		return false, fmt.Errorf("simple-fold RustAC view is closed")
	}
	if yield == nil {
		return false, fmt.Errorf("simple-fold RustAC view requires a callback")
	}
	text, origin, err := foldWithOriginChecked(haystack)
	if err != nil {
		return false, err
	}
	var projectionErr error
	complete, err := view.matcher.EachErr(text, func(canonicalStart, pattern, canonicalWidth int) bool {
		if pattern < 0 || pattern >= len(view.patternLengths) {
			projectionErr = fmt.Errorf("RustAC returned pattern ID %d outside %d originals", pattern, len(view.patternLengths))
			return false
		}
		if canonicalWidth != view.patternLengths[pattern] {
			projectionErr = fmt.Errorf("RustAC pattern %d returned width %d, canonical width %d", pattern, canonicalWidth, view.patternLengths[pattern])
			return false
		}
		canonicalEnd := canonicalStart + canonicalWidth
		if !validCanonicalSpan(text, origin, canonicalStart, canonicalEnd) {
			projectionErr = fmt.Errorf("RustAC returned an unprojectable canonical span")
			return false
		}
		sourceStart, sourceEnd := int(origin[canonicalStart]), int(origin[canonicalEnd])
		if sourceStart < 0 || sourceEnd < sourceStart || sourceEnd > len(haystack) {
			projectionErr = fmt.Errorf("RustAC projected an invalid source span [%d,%d) for length %d", sourceStart, sourceEnd, len(haystack))
			return false
		}
		hit := projectFoldedOffsets(sourceStart, sourceEnd, pattern)
		return yield(hit.Start, hit.Pattern, hit.Width)
	})
	if projectionErr != nil {
		return false, projectionErr
	}
	return complete, err
}
