// Package rustac provides the arena's direct Rust aho-corasick DFA entrant.
package rustac

/*
#cgo pkg-config: casei-rustac
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct casei_ac_matcher casei_ac_matcher;
casei_ac_matcher *casei_ac_compile(const uint8_t *const *patterns,
    const size_t *lengths, size_t count, char **error);
void casei_ac_free(casei_ac_matcher *matcher);
void casei_ac_error_free(char *error);
int casei_ac_find(const casei_ac_matcher *matcher, const uint8_t *haystack,
    size_t length, size_t *start, size_t *pattern, uint32_t *dispatch_bits);
long casei_ac_each(const casei_ac_matcher *matcher, const uint8_t *haystack,
    size_t length, size_t start, size_t *out, size_t cap);
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Matcher owns a Rust aho-corasick DFA. It is deliberately ASCII-only because
// the arena uses it on its exact ASCII tier. Its enabled prefilter is audited
// for each query instead of being labeled from Go's CPU feature flags.
type Matcher struct {
	ptr          *C.casei_ac_matcher
	dispatchBits atomic.Int32
}

var empty = [1]byte{}

// VectorBits returns the widest audited memchr backend reached during the most
// recent Find. It is an observation of that Rust query: zero means its
// prefilter did not reach an audited vector backend.
func (m *Matcher) VectorBits() int {
	if m == nil {
		return 0
	}
	return int(m.dispatchBits.Load())
}

func (m *Matcher) recordDispatch(bits C.uint32_t) {
	m.dispatchBits.Store(int32(bits))
}

// Compile builds a case-insensitive, LeftmostFirst DFA from ASCII patterns.
// Construction is outside Find so the benchmark measures only native search.
func Compile(patterns []string) (*Matcher, error) {
	count := len(patterns)
	var pointers []*C.uint8_t
	var lengths []C.size_t
	if count != 0 {
		pointers = make([]*C.uint8_t, count)
		lengths = make([]C.size_t, count)
		defer func() {
			for _, pointer := range pointers {
				C.free(unsafe.Pointer(pointer))
			}
		}()
		for i, pattern := range patterns {
			bytes := []byte(pattern)
			for _, b := range bytes {
				if b >= 0x80 {
					return nil, fmt.Errorf("Rust Aho-Corasick entrant is ASCII-only")
				}
			}
			pointers[i] = (*C.uint8_t)(C.CBytes(bytes))
			lengths[i] = C.size_t(len(bytes))
		}
	}

	var errorText *C.char
	var raw *C.casei_ac_matcher
	if count == 0 {
		raw = C.casei_ac_compile(nil, nil, 0, &errorText)
	} else {
		raw = C.casei_ac_compile(
			(**C.uint8_t)(unsafe.Pointer(&pointers[0])),
			(*C.size_t)(unsafe.Pointer(&lengths[0])), C.size_t(count), &errorText,
		)
	}
	if raw == nil {
		if errorText == nil {
			return nil, fmt.Errorf("Rust Aho-Corasick compile failed")
		}
		defer C.casei_ac_error_free(errorText)
		return nil, fmt.Errorf("Rust Aho-Corasick compile failed: %s", C.GoString(errorText))
	}
	return &Matcher{ptr: raw}, nil
}

// CompileAlternation builds a matcher in the supplied pattern order.
func CompileAlternation(patterns []string) (*Matcher, error) {
	return Compile(patterns)
}

func pointer(s string) *C.uint8_t {
	if len(s) == 0 {
		return (*C.uint8_t)(unsafe.Pointer(&empty[0]))
	}
	return (*C.uint8_t)(unsafe.Pointer(unsafe.StringData(s)))
}

// Find returns the leftmost match and ties it to the lowest original pattern
// index. The Rust adapter preserves original IDs even when empty patterns are
// omitted from the DFA.
func (m *Matcher) Find(haystack string) (start, pattern int, ok bool) {
	if m == nil || m.ptr == nil {
		return 0, 0, false
	}
	var (
		foundStart, foundPattern C.size_t
		dispatchBits             C.uint32_t
	)
	status := C.casei_ac_find(
		m.ptr, pointer(haystack), C.size_t(len(haystack)), &foundStart, &foundPattern, &dispatchBits,
	)
	m.recordDispatch(dispatchBits)
	runtime.KeepAlive(haystack)
	switch status {
	case 0:
		return 0, 0, false
	case 1:
		return int(foundStart), int(foundPattern), true
	default:
		panic("Rust Aho-Corasick scan failed")
	}
}

// eachBatch is how many matches Each collects per native call.
const eachBatch = 256

var eachBuffers = sync.Pool{New: func() any { return new([3 * eachBatch]C.size_t) }}

// Each calls yield with the start, pattern index and byte width of each
// consecutive non-overlapping match, from aho-corasick's own find_iter. Matches
// come back in batches, so enumeration costs one native call per batch rather
// than one per match. It returns false when yield stops it. Empty patterns are
// outside its contract.
func (m *Matcher) Each(haystack string, yield func(start, pattern, width int) bool) bool {
	out := eachBuffers.Get().(*[3 * eachBatch]C.size_t)
	defer eachBuffers.Put(out)
	for at := 0; ; {
		n := int(C.casei_ac_each(m.ptr, pointer(haystack), C.size_t(len(haystack)), C.size_t(at), &out[0], eachBatch))
		runtime.KeepAlive(haystack)
		if n < 0 {
			panic("Rust Aho-Corasick Each failed or has an empty pattern")
		}
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
