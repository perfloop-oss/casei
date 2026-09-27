package casei

import "unicode/utf8"

// makeASCIIInteriorAnchor uses one fixed safe window only when the existing
// leading window cannot be compiled. Original byte offsets equal unit offsets
// because admission requires an ASCII pattern. They are not source offsets:
// a matching prefix may contain the wider long-s or Kelvin spellings.
func (p *searchPlan) makeASCIIInteriorAnchor(pattern string) {
	if !p.asciiOnly || len(pattern) > 64 {
		return
	}
	bestStart, bestEnd := 0, 0
	for at := 0; at < len(pattern); {
		if maxFoldRuneWidth(rune(pattern[at])) != 1 {
			at++
			continue
		}
		start := at
		for at < len(pattern) && maxFoldRuneWidth(rune(pattern[at])) == 1 {
			at++
		}
		if at-start > bestEnd-bestStart {
			bestStart, bestEnd = start, at
		}
	}
	// Keep the route for a leading width-changing unit followed immediately
	// by the safe window. Punctuation or another width-changing unit before
	// the window belongs to the existing decoded route; dense code-shaped
	// literals otherwise pay recovery and confirmation cost without the
	// long clean prefix this transition is meant to exploit.
	if bestStart != 1 || bestEnd-bestStart < 3 {
		return
	}
	probe := &p.asciiProbe
	probe.firstAt = bestStart
	probe.secondAt = bestStart + (bestEnd-bestStart)/2
	probe.thirdAt = bestEnd - 1
	values := [3]*byte{
		&probe.first, &probe.second, &probe.third,
	}
	for i, at := range [3]int{probe.firstAt, probe.secondAt, probe.thirdAt} {
		value := pattern[at]
		if isASCIILetter(value) {
			value |= 0x20
			probe.fold |= 1 << i
		}
		*values[i] = value
	}
	makeASCIIVBMIProbe(probe)
	p.asciiNeedle = pattern
	p.asciiVerifyTokens = true
}

// The ASCII window starts at a proven boundary. Walking back a fixed count
// of decoded units gives monotonically ordered candidate starts as windows
// advance. Malformed units cannot match this ASCII-only pattern prefix.
func recoverASCIIInteriorStart(haystack string, window, units int) int {
	start := window
	for units > 0 {
		if start == 0 {
			return -1
		}
		size := 1
		if haystack[start-1] >= utf8.RuneSelf {
			r, decoded := utf8.DecodeLastRuneInString(haystack[:start])
			if r == utf8.RuneError && decoded == 1 {
				return -1
			}
			size = decoded
		}
		start -= size
		units--
	}
	return start
}
