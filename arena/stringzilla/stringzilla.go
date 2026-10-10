// Package stringzilla provides the arena's StringZilla full-fold baseline with
// a timed simple-fold verification adapter.
package stringzilla

/*
#cgo pkg-config: stringzilla
#include <stringzilla/stringzilla.h>

static int casei_stringzilla_has_ice(void) {
	return (sz_capabilities() & sz_cap_icelake_k) != 0;
}

static int casei_stringzilla_prepare(const char *needle, size_t needle_length,
		sz_utf8_uncased_needle_metadata_t *metadata) {
	sz_size_t matched_length = 0;
	return sz_utf8_uncased_search(needle, needle_length, needle,
		needle_length, metadata, &matched_length) != NULL;
}

static int casei_stringzilla_find(const char *haystack, size_t haystack_length,
		const char *needle, size_t needle_length,
		sz_utf8_uncased_needle_metadata_t *metadata, size_t *offset) {
	sz_size_t matched_length = 0;
	sz_cptr_t found = sz_utf8_uncased_search(haystack, haystack_length,
		needle, needle_length, metadata, &matched_length);
	if (found == NULL) {
		return 0;
	}
	*offset = (size_t)(found - haystack);
	return 1;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// Matcher reuses StringZilla's full-fold needle metadata, then verifies every
// full-fold hit with Unicode simple folding. The verification is intentionally
// in Find: it is the adaptation required to make StringZilla comparable to the
// arena contract, so it is part of the timed baseline.
type Matcher struct {
	needle   string
	runes    int // the needle's runes; a match spans at most utf8.UTFMax bytes each
	metadata C.sz_utf8_uncased_needle_metadata_t
}

// Alternation is a reduction over independently compiled StringZilla needles.
// StringZilla exposes a single-needle API, so this adapter runs each native
// scan and reduces starts and IDs in the timed region.
type Alternation struct {
	matchers []*Matcher
}

var empty = [1]byte{}

// Enabled reports whether this process can safely enter StringZilla's Ice
// Lake AVX-512 implementation. The Go feature gate is checked before the C
// capability query so an AVX-512-disabled control process neither executes nor
// silently substitutes the library's serial path.
func Enabled() bool {
	return cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512VBMI &&
		C.casei_stringzilla_has_ice() != 0
}

// VectorBits reports the width of the admitted StringZilla path. Zero means
// this optional AVX-512-only entrant is excluded from the current process.
func VectorBits() int {
	if Enabled() {
		return 512
	}
	return 0
}

// CompileLiteral prepares one valid UTF-8 needle. StringZilla's dynamic
// library must expose its Ice Lake AVX-512 implementation; using a quiet
// serial fallback would make a native competitor's timing misleading.
func CompileLiteral(needle string) (*Matcher, error) {
	if !utf8.ValidString(needle) {
		return nil, fmt.Errorf("StringZilla requires a valid UTF-8 needle")
	}
	if VectorBits() != 512 {
		return nil, fmt.Errorf("StringZilla Ice Lake AVX-512 path is unavailable")
	}

	m := &Matcher{needle: needle, runes: utf8.RuneCountInString(needle)}
	if C.casei_stringzilla_prepare(pointer(needle), C.size_t(len(needle)), &m.metadata) == 0 {
		runtime.KeepAlive(needle)
		return nil, fmt.Errorf("StringZilla failed to prepare needle metadata")
	}
	runtime.KeepAlive(needle)
	return m, nil
}

// CompileAlternation prepares each literal once, outside timed searches.
func CompileAlternation(patterns []string) (*Alternation, error) {
	alternation := &Alternation{matchers: make([]*Matcher, len(patterns))}
	for i, pattern := range patterns {
		m, err := CompileLiteral(pattern)
		if err != nil {
			return nil, err
		}
		alternation.matchers[i] = m
	}
	return alternation, nil
}

func pointer(s string) *C.char {
	if len(s) == 0 {
		return (*C.char)(unsafe.Pointer(&empty[0]))
	}
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s)))
}

func pointerAt(s string, offset int) *C.char {
	if offset == len(s) {
		return (*C.char)(unsafe.Pointer(&empty[0]))
	}
	return (*C.char)(unsafe.Add(unsafe.Pointer(unsafe.StringData(s)), offset))
}

// Index returns the first simple-fold match. Full folding is a conservative
// candidate generator: it may report an expansion such as ß for "ss", which
// simpleFoldMatchAt rejects before the scan resumes at the next source rune.
//
// StringZilla requires valid UTF-8. The arena's count helper begins each next
// overlap probe one byte after the previous start, which can leave a slice at a
// continuation byte. That prefix cannot begin a valid pattern, so discard it
// before the native call and retain its byte offset in the result. Other
// malformed input is outside this baseline's declared valid-UTF-8 domain.
func (m *Matcher) Index(haystack string) int {
	if m == nil {
		return -1
	}
	if len(m.needle) == 0 {
		return 0
	}
	base := 0
	for base < len(haystack) && haystack[base]&0xc0 == 0x80 {
		base++
	}
	haystack = haystack[base:]

	metadata := m.metadata
	for offset := 0; offset <= len(haystack); {
		var found C.size_t
		if C.casei_stringzilla_find(
			pointerAt(haystack, offset), C.size_t(len(haystack)-offset),
			pointer(m.needle), C.size_t(len(m.needle)), &metadata, &found,
		) == 0 {
			runtime.KeepAlive(haystack)
			runtime.KeepAlive(m.needle)
			return -1
		}
		runtime.KeepAlive(haystack)
		runtime.KeepAlive(m.needle)
		candidate := offset + int(found)
		if simpleFoldMatchAt(haystack, m.needle, candidate) {
			return base + candidate
		}
		if candidate == len(haystack) {
			return -1
		}
		_, size := utf8.DecodeRuneInString(haystack[candidate:])
		offset = candidate + size
	}
	return -1
}

// Find returns the leftmost match from this alternation, ties to the lowest
// pattern ID. All native scans and this reduction are timed as the adapter.
// Once a literal has matched, a later literal can only win by starting before
// that match, so its scan ends where such a match must end.
func (a *Alternation) Find(haystack string) (start, pattern int, ok bool) {
	if a == nil {
		return 0, 0, false
	}
	bestStart, bestPattern := -1, -1
	for pattern, m := range a.matchers {
		scope := haystack
		if bestStart >= 0 && bestStart+utf8.UTFMax*m.runes < len(haystack) {
			scope = haystack[:bestStart+utf8.UTFMax*m.runes]
		}
		start := m.Index(scope)
		if start >= 0 && (bestStart < 0 || start < bestStart || (start == bestStart && pattern < bestPattern)) {
			bestStart, bestPattern = start, pattern
		}
	}
	if bestStart < 0 {
		return 0, 0, false
	}
	return bestStart, bestPattern, true
}

func simpleFoldMatchAt(haystack, needle string, start int) bool {
	haystack = haystack[start:]
	for len(needle) != 0 {
		if len(haystack) == 0 {
			return false
		}
		h, hSize := utf8.DecodeRuneInString(haystack)
		n, nSize := utf8.DecodeRuneInString(needle)
		if !simpleFoldEqual(h, n) {
			return false
		}
		haystack = haystack[hSize:]
		needle = needle[nSize:]
	}
	return true
}

func simpleFoldEqual(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
