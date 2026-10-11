// Package rure provides the arena's rust-regex C API baseline.
package rure

/*
#cgo pkg-config: rure
#include <rure.h>
#include <stddef.h>
#include <stdint.h>

// This is a private symbol added by the pinned arena audit patch. Keeping
// reset, search, and readback in one native call preserves thread-local Rust
// telemetry when cgo moves a Go goroutine between OS threads.
extern bool rure_casei_find(const rure *re, const uint8_t *haystack,
	size_t length, size_t start, rure_match *match, uint32_t *dispatch_bits);

static rure *casei_rure_compile(const uint8_t *pattern, size_t length,
		rure_error *error) {
	return rure_compile(pattern, length, RURE_FLAG_UNICODE, NULL, error);
}

static int casei_rure_find(rure *re, const uint8_t *haystack, size_t length,
		rure_match *match, uint32_t *dispatch_bits) {
	return rure_casei_find(re, haystack, length, 0, match, dispatch_bits) ? 1 : 0;
}

// casei_rure_each writes up to cap (start, end) pairs of consecutive
// non-overlapping matches from start into out, in one native call, and returns
// how many it wrote.
static size_t casei_rure_each(rure *re, const uint8_t *haystack, size_t length,
		size_t start, size_t *out, size_t cap) {
	size_t n = 0;
	rure_match match;
	while (n < cap && start <= length && rure_find(re, haystack, length, start, &match)) {
		out[2 * n] = match.start;
		out[2 * n + 1] = match.end;
		n++;
		start = match.end > match.start ? match.end : match.end + 1;
	}
	return n;
}

static size_t casei_rure_match_start(const rure_match *match) {
	return match->start;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Regex is a rust-regex C API expression compiled with Unicode mode. It has no
// capture groups: explicit captures turn off rust-regex's literal-set fast
// path, so an alternation reports pattern ID -1, as Go's regexp does.
type Regex struct {
	re           *C.rure
	pattern      int // the pattern ID Find and Each report: 0, or -1 for an alternation
	dispatchBits atomic.Int32
}

var empty = [1]byte{}

// VectorBits returns the widest memchr backend observed during the most
// recent Find on re. It is an observation of the Rust query, not a Go CPU
// capability: zero means that query did not reach an audited vector prefilter.
func (re *Regex) VectorBits() int {
	if re == nil {
		return 0
	}
	return int(re.dispatchBits.Load())
}

// Compile builds pattern with Unicode mode enabled. Callers add (?i) to select
// rust-regex's Unicode simple-case-fold behavior; without it matching is
// exact. Errors are returned rather than replaced by an interpreted or scalar
// baseline.
func Compile(pattern string) (*Regex, error) {
	error := C.rure_error_new()
	if error == nil {
		return nil, fmt.Errorf("rure error allocation failed")
	}
	defer C.rure_error_free(error)

	re := C.casei_rure_compile(pointer(pattern), C.size_t(len(pattern)), error)
	runtime.KeepAlive(pattern)
	if re == nil {
		message := C.rure_error_message(error)
		if message == nil {
			return nil, fmt.Errorf("rure compile failed")
		}
		return nil, fmt.Errorf("rure compile failed: %s", C.GoString(message))
	}
	// Compiled expressions intentionally live for the benchmark process.
	return &Regex{re: re}, nil
}

// caseFlag is the expression prefix for a case mode: (?i) folds, and exact
// matching is rust-regex's default, case-sensitive mode.
func caseFlag(exact bool) string {
	if exact {
		return ""
	}
	return "(?i)"
}

// CompileLiteral builds a Unicode literal, case-insensitive unless exact.
func CompileLiteral(pattern string, exact bool) (*Regex, error) {
	return Compile(caseFlag(exact) + QuoteMeta(pattern))
}

// CompileAlternation builds an ordered literal alternation, case-insensitive
// unless exact. rust-regex's leftmost-first semantics choose the same match
// as the arena's lowest-pattern-index tie rule; Find and Each report its
// pattern ID as -1.
func CompileAlternation(patterns []string, exact bool) (*Regex, error) {
	if len(patterns) == 0 {
		return &Regex{pattern: -1}, nil
	}
	branches := make([]string, len(patterns))
	for i, pattern := range patterns {
		branches[i] = QuoteMeta(pattern)
	}
	re, err := Compile(caseFlag(exact) + "(?:" + strings.Join(branches, "|") + ")")
	if err != nil {
		return nil, err
	}
	re.pattern = -1
	return re, nil
}

// QuoteMeta quotes s as one rust-regex literal. The expression syntax accepts
// these escapes and \x00 keeps embedded NUL bytes representable even though the
// C API's escaped-string helper itself accepts only NUL-terminated input.
func QuoteMeta(s string) string {
	const special = `\.+*?()|[]{}^$`
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 {
			b.WriteString(`\x00`)
			continue
		}
		if strings.IndexByte(special, c) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func pointer(s string) *C.uint8_t {
	if len(s) == 0 {
		return (*C.uint8_t)(unsafe.Pointer(&empty[0]))
	}
	return (*C.uint8_t)(unsafe.Pointer(unsafe.StringData(s)))
}

// eachBatch is how many matches Each collects per native call.
const eachBatch = 256

var eachBuffers = sync.Pool{New: func() any { return new([2 * eachBatch]C.size_t) }}

// Each calls yield with the start, pattern ID (as Find reports it) and byte
// width of each consecutive non-overlapping match, as casei's Matcher.Each
// orders them. The matching loop runs in C and returns matches in batches, so
// enumeration costs one native call per batch rather than one per match. It
// returns false when yield stops it.
func (re *Regex) Each(haystack string, yield func(start, pattern, width int) bool) bool {
	if re == nil || re.re == nil {
		return true
	}
	out := eachBuffers.Get().(*[2 * eachBatch]C.size_t)
	defer eachBuffers.Put(out)
	for at := 0; ; {
		n := int(C.casei_rure_each(re.re, pointer(haystack), C.size_t(len(haystack)), C.size_t(at),
			&out[0], eachBatch))
		runtime.KeepAlive(haystack)
		for i := range n {
			start, end := int(out[2*i]), int(out[2*i+1])
			if !yield(start, re.pattern, end-start) {
				return false
			}
			at = end
		}
		if n < eachBatch {
			return true
		}
	}
}

// Find returns the leftmost byte start and the pattern ID: 0 for a literal,
// -1 for an alternation. It records the audited Rust backend for this exact
// query as a diagnostic; the width never removes the entrant from the field.
func (re *Regex) Find(haystack string) (start, pattern int, ok bool) {
	if re == nil || re.re == nil {
		return 0, 0, false
	}
	var (
		match        C.rure_match
		dispatchBits C.uint32_t
	)
	found := C.casei_rure_find(re.re, pointer(haystack), C.size_t(len(haystack)), &match, &dispatchBits)
	re.dispatchBits.Store(int32(dispatchBits))
	runtime.KeepAlive(haystack)
	if found == 0 {
		return 0, 0, false
	}
	return int(C.casei_rure_match_start(&match)), re.pattern, true
}

// Index returns the byte start of the first match, or -1 when there is none.
func (re *Regex) Index(haystack string) int {
	start, _, ok := re.Find(haystack)
	if !ok {
		return -1
	}
	return start
}
