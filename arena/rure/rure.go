// Package rure provides the arena's rust-regex C API baseline.
package rure

/*
#cgo pkg-config: rure
#include <rure.h>
#include <stddef.h>
#include <stdint.h>

// These are private symbols added by the pinned arena audit patch. Keeping
// reset, search, and readback in one native call preserves thread-local Rust
// telemetry when cgo moves a Go goroutine between OS threads.
extern bool rure_casei_find(const rure *re, const uint8_t *haystack,
	size_t length, size_t start, rure_match *match, uint32_t *dispatch_bits);
extern bool rure_casei_find_captures(const rure *re, const uint8_t *haystack,
	size_t length, size_t start, rure_captures *captures, uint32_t *dispatch_bits);

static rure *casei_rure_compile(const uint8_t *pattern, size_t length,
		rure_error *error) {
	return rure_compile(pattern, length, RURE_FLAG_UNICODE, NULL, error);
}

static int casei_rure_find(rure *re, const uint8_t *haystack, size_t length,
		rure_match *match, uint32_t *dispatch_bits) {
	return rure_casei_find(re, haystack, length, 0, match, dispatch_bits) ? 1 : 0;
}

static int casei_rure_find_captures(rure *re, const uint8_t *haystack,
		size_t length, rure_captures *captures, uint32_t *dispatch_bits) {
	return rure_casei_find_captures(re, haystack, length, 0, captures, dispatch_bits) ? 1 : 0;
}

static int casei_rure_captures_at(rure_captures *captures, size_t index,
		rure_match *match) {
	return rure_captures_at(captures, index, match) ? 1 : 0;
}

// casei_rure_each writes up to cap (start, end, pattern) triples of
// consecutive non-overlapping matches from start into out, in one native call,
// and returns how many it wrote. A capture set selects the matched branch.
static size_t casei_rure_each(rure *re, const uint8_t *haystack, size_t length,
		size_t start, rure_captures *captures, size_t groups, size_t *out, size_t cap) {
	size_t n = 0;
	rure_match match;
	while (n < cap && start <= length) {
		size_t pattern = 0;
		if (captures == NULL) {
			if (!rure_find(re, haystack, length, start, &match)) {
				break;
			}
		} else {
			if (!rure_find_captures(re, haystack, length, start, captures)) {
				break;
			}
			rure_captures_at(captures, 0, &match);
			rure_match branch;
			for (size_t group = 1; group <= groups; group++) {
				if (rure_captures_at(captures, group, &branch)) {
					pattern = group - 1;
					break;
				}
			}
		}
		out[3 * n] = match.start;
		out[3 * n + 1] = match.end;
		out[3 * n + 2] = pattern;
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

// Regex is a rust-regex C API expression compiled with Unicode mode. Ordered
// alternations retain a capture for each branch so Find can expose the arena's
// lowest-pattern-index tie rule.
type Regex struct {
	re           *C.rure
	captures     int
	dispatchBits atomic.Int32
	pool         sync.Pool // *capture
}

type capture struct {
	ptr *C.rure_captures
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

func (re *Regex) recordDispatch(bits C.uint32_t) {
	re.dispatchBits.Store(int32(bits))
}

// Compile builds pattern with Unicode mode enabled. Callers add (?i) to select
// rust-regex's Unicode simple-case-fold behavior. Errors are returned rather
// than replaced by an interpreted or scalar baseline.
func Compile(pattern string, captures int) (*Regex, error) {
	if captures < 0 {
		return nil, fmt.Errorf("rure capture count must not be negative")
	}
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

	compiled := &Regex{re: re, captures: captures}
	if captures > 0 {
		compiled.pool.New = func() any {
			ptr := C.rure_captures_new(re)
			if ptr == nil {
				panic("rure capture allocation failed")
			}
			return &capture{ptr: ptr}
		}
		// Seed allocation before arena timing begins. Compiled expressions and
		// their capture storage intentionally live for the benchmark process.
		compiled.pool.Put(compiled.pool.New())
	}
	return compiled, nil
}

// CompileLiteral builds a case-insensitive Unicode literal.
func CompileLiteral(pattern string) (*Regex, error) {
	return Compile("(?i)"+QuoteMeta(pattern), 0)
}

// CompileAlternation builds an ordered literal alternation. Capture branches
// let Find report the leftmost branch selected by rust-regex and its pattern ID.
func CompileAlternation(patterns []string) (*Regex, error) {
	if len(patterns) == 0 {
		return &Regex{}, nil
	}
	branches := make([]string, len(patterns))
	for i, pattern := range patterns {
		branches[i] = "(" + QuoteMeta(pattern) + ")"
	}
	return Compile("(?i)(?:"+strings.Join(branches, "|")+")", len(patterns))
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

var eachBuffers = sync.Pool{New: func() any { return new([3 * eachBatch]C.size_t) }}

// Each calls yield with the start, pattern index and byte width of each
// consecutive non-overlapping match, as casei's Matcher.Each orders them. The
// matching loop runs in C and returns matches in batches, so enumeration costs
// one native call per batch rather than one per match. It returns false when
// yield stops it.
func (re *Regex) Each(haystack string, yield func(start, pattern, width int) bool) bool {
	if re == nil || re.re == nil {
		return true
	}
	out := eachBuffers.Get().(*[3 * eachBatch]C.size_t)
	defer eachBuffers.Put(out)
	var captures *C.rure_captures
	if re.captures > 0 {
		c := re.pool.Get().(*capture)
		defer re.pool.Put(c)
		captures = c.ptr
	}
	for at := 0; ; {
		n := int(C.casei_rure_each(re.re, pointer(haystack), C.size_t(len(haystack)), C.size_t(at),
			captures, C.size_t(re.captures), &out[0], eachBatch))
		runtime.KeepAlive(haystack)
		for i := range n {
			start, end := int(out[3*i]), int(out[3*i+1])
			if !yield(start, int(out[3*i+2]), end-start) {
				return false
			}
			at = end
		}
		if n < eachBatch {
			return true
		}
	}
}

// Find returns the leftmost byte start and, for an alternation, the selected
// pattern index. It records the audited Rust backend for this exact query; the
// scoreboard excludes any query that did not observe memchr AVX2.
func (re *Regex) Find(haystack string) (start, pattern int, ok bool) {
	if re == nil || re.re == nil {
		return 0, 0, false
	}
	if re.captures == 0 {
		var (
			match        C.rure_match
			dispatchBits C.uint32_t
		)
		if C.casei_rure_find(re.re, pointer(haystack), C.size_t(len(haystack)), &match, &dispatchBits) == 0 {
			re.recordDispatch(dispatchBits)
			runtime.KeepAlive(haystack)
			return 0, 0, false
		}
		re.recordDispatch(dispatchBits)
		runtime.KeepAlive(haystack)
		return int(C.casei_rure_match_start(&match)), 0, true
	}

	captures := re.pool.Get().(*capture)
	var dispatchBits C.uint32_t
	matched := C.casei_rure_find_captures(
		re.re, pointer(haystack), C.size_t(len(haystack)), captures.ptr, &dispatchBits,
	)
	re.recordDispatch(dispatchBits)
	runtime.KeepAlive(haystack)
	if matched == 0 {
		re.pool.Put(captures)
		return 0, 0, false
	}
	for group := 1; group <= re.captures; group++ {
		var match C.rure_match
		if C.casei_rure_captures_at(captures.ptr, C.size_t(group), &match) != 0 {
			re.pool.Put(captures)
			return int(C.casei_rure_match_start(&match)), group - 1, true
		}
	}
	re.pool.Put(captures)
	panic("rure alternation matched without a selected branch")
}

// Index returns the byte start of the first match, or -1 when there is none.
func (re *Regex) Index(haystack string) int {
	start, _, ok := re.Find(haystack)
	if !ok {
		return -1
	}
	return start
}
