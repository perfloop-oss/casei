// Package pcre2 provides the arena's PCRE2 JIT baseline.
package pcre2

/*
#cgo pkg-config: libpcre2-8
#define PCRE2_CODE_UNIT_WIDTH 8
#include <pcre2.h>
#include <stddef.h>

static pcre2_code_8 *casei_pcre2_compile_8(const char *pattern, size_t length,
	int *error_code, size_t *error_offset) {
	PCRE2_SIZE offset = 0;
	pcre2_code_8 *code = pcre2_compile_8(
		(PCRE2_SPTR8)pattern, (PCRE2_SIZE)length,
		PCRE2_CASELESS | PCRE2_UTF, error_code, &offset, NULL);
	*error_offset = (size_t)offset;
	return code;
}

static int casei_pcre2_jit_compile_8(pcre2_code_8 *code) {
	return pcre2_jit_compile_8(code, PCRE2_JIT_COMPLETE);
}

static void casei_pcre2_code_free_8(pcre2_code_8 *code) {
	pcre2_code_free_8(code);
}

static pcre2_match_data_8 *casei_pcre2_match_data_create_8(const pcre2_code_8 *code) {
	return pcre2_match_data_create_from_pattern_8(code, NULL);
}

static int casei_pcre2_jit_match_8(const pcre2_code_8 *code, const char *subject,
	size_t length, pcre2_match_data_8 *match_data) {
	return pcre2_jit_match_8(code, (PCRE2_SPTR8)subject, (PCRE2_SIZE)length,
		0, 0, match_data, NULL);
}

// casei_pcre2_each_8 writes up to cap (start, end, pattern) triples of
// consecutive non-overlapping matches from start into out, in one native call,
// and returns how many it wrote, or a negative PCRE2 error.
static long casei_pcre2_each_8(const pcre2_code_8 *code, const char *subject,
	size_t length, size_t start, int captures, pcre2_match_data_8 *match_data,
	size_t *out, size_t cap) {
	size_t n = 0;
	while (n < cap && start <= length) {
		int result = pcre2_jit_match_8(code, (PCRE2_SPTR8)subject, (PCRE2_SIZE)length,
			(PCRE2_SIZE)start, 0, match_data, NULL);
		if (result == PCRE2_ERROR_NOMATCH) {
			break;
		}
		if (result < 0) {
			return result;
		}
		PCRE2_SIZE *ovector = pcre2_get_ovector_pointer_8(match_data);
		size_t pattern = 0;
		for (int group = 1; group <= captures; group++) {
			if (ovector[2 * group] != PCRE2_UNSET) {
				pattern = (size_t)(group - 1);
				break;
			}
		}
		out[3 * n] = ovector[0];
		out[3 * n + 1] = ovector[1];
		out[3 * n + 2] = pattern;
		n++;
		start = ovector[1] > ovector[0] ? ovector[1] : ovector[1] + 1;
	}
	return (long)n;
}

static int casei_pcre2_is_nomatch_8(int result) {
	return result == PCRE2_ERROR_NOMATCH;
}

static int casei_pcre2_group_matched_8(pcre2_match_data_8 *match_data, int group) {
	PCRE2_SIZE *ovector = pcre2_get_ovector_pointer_8(match_data);
	return ovector[2 * group] != PCRE2_UNSET;
}

static size_t casei_pcre2_group_start_8(pcre2_match_data_8 *match_data, int group) {
	PCRE2_SIZE *ovector = pcre2_get_ovector_pointer_8(match_data);
	return (size_t)ovector[2 * group];
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// Regex is a PCRE2_UTF|PCRE2_CASELESS pattern whose PCRE2 JIT compilation
// succeeded. Match data is pooled because PCRE2 writes its ovector per match.
type Regex struct {
	code     *C.pcre2_code_8
	captures int
	matches  sync.Pool // *matchData
}

type matchData struct {
	ptr *C.pcre2_match_data_8
	out [3 * eachBatch]C.size_t // Each's match buffer
}

// eachBatch is how many matches Each collects per native call.
const eachBatch = 256

var empty = [1]byte{}

// VectorBits reports the generic x86-64 PCRE2 JIT width. In the pinned
// PCRE2 10.47 JIT source, the fast-forward AVX2 path is explicitly disabled;
// its x86 vector baseline is SSE2, which is mandatory on this target. It is
// therefore not counted as an AVX2 or AVX-512 entrant.
func VectorBits() int { return 128 }

func pointer(s string) *C.char {
	if len(s) == 0 {
		return (*C.char)(unsafe.Pointer(&empty[0]))
	}
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s)))
}

// Compile builds and JIT-compiles pattern. captures is the number of ordered
// branch captures that Find should inspect to identify the selected branch.
// It returns an error instead of falling back to interpreted PCRE2.
func Compile(pattern string, captures int) (*Regex, error) {
	var compileError C.int
	var errorOffset C.size_t
	code := C.casei_pcre2_compile_8(
		pointer(pattern), C.size_t(len(pattern)), &compileError, &errorOffset,
	)
	runtime.KeepAlive(pattern)
	if code == nil {
		return nil, fmt.Errorf("PCRE2 compile failed: code %d at byte %d", int(compileError), int(errorOffset))
	}
	if result := C.casei_pcre2_jit_compile_8(code); result != 0 {
		C.casei_pcre2_code_free_8(code)
		return nil, fmt.Errorf("PCRE2 JIT compilation declined: code %d", int(result))
	}

	re := &Regex{code: code, captures: captures}
	re.matches.New = func() any {
		data := C.casei_pcre2_match_data_create_8(code)
		if data == nil {
			panic("PCRE2 match-data allocation failed")
		}
		return &matchData{ptr: data}
	}
	// Seed the pool before arena timing begins. Compiled code and its pooled
	// match data intentionally live for the benchmark process.
	re.matches.Put(re.matches.New())
	return re, nil
}

// QuoteMeta returns a PCRE2 literal fragment for s.
func QuoteMeta(s string) string {
	return `\Q` + strings.ReplaceAll(s, `\E`, `\E\\E\Q`) + `\E`
}

// CompileLiteral compiles one UTF-8 simple-fold literal with PCRE2 JIT.
func CompileLiteral(pattern string) (*Regex, error) {
	return Compile(QuoteMeta(pattern), 0)
}

// CompileAlternation compiles patterns in their supplied order. A capture per
// branch lets Find expose PCRE2's leftmost-first branch selection as its index.
func CompileAlternation(patterns []string) (*Regex, error) {
	if len(patterns) == 0 {
		return Compile(`(?!)`, 0)
	}
	branches := make([]string, len(patterns))
	for i, pattern := range patterns {
		branches[i] = "(" + QuoteMeta(pattern) + ")"
	}
	return Compile("(?:"+strings.Join(branches, "|")+")", len(patterns))
}

// Find returns the byte start and, for an ordered alternation, the selected
// branch index. PCRE2 JIT errors panic because a baseline must not silently
// switch to a weaker execution mode while it is being timed.
func (re *Regex) Find(haystack string) (start, pattern int, ok bool) {
	data := re.matches.Get().(*matchData)
	result := C.casei_pcre2_jit_match_8(
		re.code, pointer(haystack), C.size_t(len(haystack)), data.ptr,
	)
	runtime.KeepAlive(haystack)
	if C.casei_pcre2_is_nomatch_8(result) != 0 {
		re.matches.Put(data)
		return 0, 0, false
	}
	if result < 0 {
		re.matches.Put(data)
		panic(fmt.Sprintf("PCRE2 JIT match failed: code %d", int(result)))
	}

	start = int(C.casei_pcre2_group_start_8(data.ptr, 0))
	pattern = 0
	if re.captures > 0 {
		pattern = -1
		for group := 1; group <= re.captures; group++ {
			if C.casei_pcre2_group_matched_8(data.ptr, C.int(group)) != 0 {
				pattern = group - 1
				break
			}
		}
		if pattern < 0 {
			re.matches.Put(data)
			panic("PCRE2 alternation matched without a selected branch")
		}
	}
	re.matches.Put(data)
	return start, pattern, true
}

// Index returns the byte start of the first match, or -1 when there is none.
func (re *Regex) Index(haystack string) int {
	start, _, ok := re.Find(haystack)
	if !ok {
		return -1
	}
	return start
}

// Each calls yield with the start, pattern index and byte width of each
// consecutive non-overlapping match, as casei's Matcher.Each orders them. The
// matching loop runs in C and returns matches in batches, so enumeration costs
// one native call per batch rather than one per match. It returns false when
// yield stops it.
func (re *Regex) Each(haystack string, yield func(start, pattern, width int) bool) bool {
	data := re.matches.Get().(*matchData)
	defer re.matches.Put(data)
	for at := 0; ; {
		n := int(C.casei_pcre2_each_8(re.code, pointer(haystack), C.size_t(len(haystack)), C.size_t(at),
			C.int(re.captures), data.ptr, &data.out[0], eachBatch))
		runtime.KeepAlive(haystack)
		if n < 0 {
			panic(fmt.Sprintf("PCRE2 JIT match failed: code %d", n))
		}
		for i := range n {
			start, end := int(data.out[3*i]), int(data.out[3*i+1])
			if !yield(start, int(data.out[3*i+2]), end-start) {
				return false
			}
			at = end
		}
		if n < eachBatch {
			return true
		}
	}
}
