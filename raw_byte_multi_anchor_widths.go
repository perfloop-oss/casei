package casei

import (
	"unicode"
	"unicode/utf8"
)

// rawByteMultiAnchorAddRuneWidths extends an ordered, deduplicated set of
// source-byte widths by every spelling in one rune's simple-fold orbit. A
// failed extension leaves the input state intact; callers reject only ranges
// that include this rune.
func rawByteMultiAnchorAddRuneWidths(widths *rawByteMultiAnchorWidthSet, r rune) bool {
	var next rawByteMultiAnchorWidthSet
	for member := r; ; member = unicode.SimpleFold(member) {
		var encoded [utf8.UTFMax]byte
		width := utf8.EncodeRune(encoded[:], member)
		for i := 0; i < int(widths.n); i++ {
			value := int(widths.offsets[i]) + width
			if value > int(^uint8(0)) {
				return false
			}
			duplicate := false
			for j := 0; j < int(next.n); j++ {
				duplicate = duplicate || next.offsets[j] == uint8(value)
			}
			if !duplicate {
				if int(next.n) == len(next.offsets) {
					return false
				}
				next.offsets[next.n] = uint8(value)
				next.n++
			}
		}
		if unicode.SimpleFold(member) == r {
			break
		}
	}
	*widths = next
	return true
}
