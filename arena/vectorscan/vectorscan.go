// Package vectorscan provides the arena's Vectorscan baseline.
package vectorscan

/*
#cgo pkg-config: libhs
#include <hs.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	unsigned long long start;
	unsigned int id;
	int found;
} casei_hs_result;

static char *casei_hs_copy_message(const char *message) {
	if (message == NULL) {
		message = "unknown Vectorscan compiler error";
	}
	size_t size = strlen(message) + 1;
	char *copy = (char *)malloc(size);
	if (copy != NULL) {
		memcpy(copy, message, size);
	}
	return copy;
}

static hs_database_t *casei_hs_compile(char *const *expressions,
		const unsigned int *ids, unsigned int count, unsigned int vector_bits,
		char **message) {
	unsigned int *flags = (unsigned int *)malloc((size_t)count * sizeof(*flags));
	if (flags == NULL) {
		*message = casei_hs_copy_message("Vectorscan flag allocation failed");
		return NULL;
	}
	for (unsigned int i = 0; i < count; i++) {
		flags[i] = HS_FLAG_CASELESS | HS_FLAG_UTF8 | HS_FLAG_SOM_LEFTMOST;
	}

	hs_platform_info_t platform;
	hs_error_t status = hs_populate_platform(&platform);
	if (status != HS_SUCCESS) {
		free(flags);
		*message = casei_hs_copy_message("Vectorscan platform query failed");
		return NULL;
	}
	// The public feature bits are cumulative in the compiler input: a VBMI
	// database must carry its AVX2 and AVX-512 prerequisites as well, even
	// though hs_database_info summarizes the result as AVX512VBMI.
	platform.cpu_features = vector_bits == 512 ?
		(HS_CPU_FEATURES_AVX2 | HS_CPU_FEATURES_AVX512 | HS_CPU_FEATURES_AVX512VBMI) :
		HS_CPU_FEATURES_AVX2;

	hs_database_t *database = NULL;
	hs_compile_error_t *error = NULL;
	status = hs_compile_multi((const char *const *)expressions,
		flags, ids, count, HS_MODE_BLOCK, &platform, &database, &error);
	free(flags);
	if (status != HS_SUCCESS) {
		*message = casei_hs_copy_message(error == NULL ? NULL : error->message);
		if (error != NULL) {
			hs_free_compile_error(error);
		}
		return NULL;
	}
	return database;
}

static void casei_hs_free_message(char *message) {
	free(message);
}

static char *casei_hs_database_info(const hs_database_t *database) {
	char *info = NULL;
	if (hs_database_info(database, &info) != HS_SUCCESS) {
		return NULL;
	}
	return info;
}

static void casei_hs_free_database_info(char *info) {
	free(info);
}

static int casei_hs_on_match(unsigned int id, unsigned long long from,
		unsigned long long to, unsigned int flags, void *context) {
	(void)to;
	(void)flags;
	casei_hs_result *result = (casei_hs_result *)context;
	if (!result->found || from < result->start ||
		(from == result->start && id < result->id)) {
		result->start = from;
		result->id = id;
		result->found = 1;
	}
	return 0;
}

static int casei_hs_scan(const hs_database_t *database, const char *subject,
		unsigned int length, hs_scratch_t *scratch, casei_hs_result *result) {
	result->start = 0;
	result->id = 0;
	result->found = 0;
	return hs_scan(database, subject, length, 0, scratch, casei_hs_on_match, result);
}

// casei_hs_events collects every report of one scan for Each. The buffer
// grows by doubling and is kept with its scratch, so repeated scans reuse it.
typedef struct {
	unsigned long long start;
	unsigned long long end;
	unsigned int id;
} casei_hs_event;

typedef struct {
	casei_hs_event *events;
	size_t len;
	size_t cap;
	int failed;
} casei_hs_events;

static casei_hs_events *casei_hs_events_new(void) {
	return (casei_hs_events *)calloc(1, sizeof(casei_hs_events));
}

static void casei_hs_events_free(casei_hs_events *list) {
	if (list != NULL) {
		free(list->events);
		free(list);
	}
}

static int casei_hs_on_event(unsigned int id, unsigned long long from,
		unsigned long long to, unsigned int flags, void *context) {
	(void)flags;
	casei_hs_events *list = (casei_hs_events *)context;
	if (list->len == list->cap) {
		size_t cap = list->cap == 0 ? 1024 : list->cap * 2;
		casei_hs_event *grown = (casei_hs_event *)realloc(list->events, cap * sizeof(*grown));
		if (grown == NULL) {
			list->failed = 1;
			return 1;
		}
		list->events = grown;
		list->cap = cap;
	}
	list->events[list->len].start = from;
	list->events[list->len].end = to;
	list->events[list->len].id = id;
	list->len++;
	return 0;
}

static int casei_hs_scan_all(const hs_database_t *database, const char *subject,
		unsigned int length, hs_scratch_t *scratch, casei_hs_events *list) {
	list->len = 0;
	list->failed = 0;
	return hs_scan(database, subject, length, 0, scratch, casei_hs_on_event, list);
}

static int casei_hs_result_found(const casei_hs_result *result) {
	return result->found;
}

static unsigned long long casei_hs_result_start(const casei_hs_result *result) {
	return result->start;
}

static unsigned int casei_hs_result_id(const casei_hs_result *result) {
	return result->id;
}
*/
import "C"

import (
	"cmp"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// Matcher is a Vectorscan block-mode database compiled with Unicode caseless
// literal patterns. Its scratch space is pooled because Vectorscan requires a
// distinct scratch allocation for concurrent scans.
type Matcher struct {
	database   *C.hs_database_t
	scratch    sync.Pool // *scratch
	empty      int
	vectorBits int
	vbmi       bool
}

type scratch struct {
	ptr    *C.hs_scratch_t
	events *C.casei_hs_events // Each's report buffer, in C memory
}

// scratchMemory is what a dropped scratch leaves to free. It is separate from
// scratch so the cleanup does not keep its own scratch reachable.
type scratchMemory struct {
	ptr    *C.hs_scratch_t
	events *C.casei_hs_events
}

var emptySubject = [1]byte{}

func requestedVectorBits() int {
	if cpu.X86.HasAVX2 && cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512VBMI {
		return 512
	}
	if cpu.X86.HasAVX2 {
		return 256
	}
	return 0
}

// VectorBits reports the database target that Compile audited. A zero value
// means that this matcher has no native database (for example, all patterns
// were empty).
func (m *Matcher) VectorBits() int {
	if m == nil {
		return 0
	}
	return m.vectorBits
}

// HasVBMI reports whether this database was compiled for the AVX-512 VBMI
// target. It is meaningful only when VectorBits returns 512.
func (m *Matcher) HasVBMI() bool {
	return m != nil && m.vbmi
}

// Compile builds one Vectorscan database for patterns. Compilation is
// deliberately separate from Find so the arena times only the search and its
// leftmost/lowest-pattern adapter. An unavailable compiler is returned as an
// error rather than replaced by a weaker matcher.
func Compile(patterns []string) (*Matcher, error) {
	m := &Matcher{empty: -1}
	for i, pattern := range patterns {
		if pattern == "" && (m.empty < 0 || i < m.empty) {
			m.empty = i
		}
	}

	count := 0
	for _, pattern := range patterns {
		if pattern != "" {
			count++
		}
	}
	if count == 0 {
		return m, nil
	}

	requested := requestedVectorBits()
	if requested == 0 {
		return nil, fmt.Errorf("Vectorscan entrant requires AVX2 or AVX-512 VBMI")
	}

	expressions := C.malloc(C.size_t(count) * C.size_t(unsafe.Sizeof(uintptr(0))))
	if expressions == nil {
		return nil, fmt.Errorf("Vectorscan expression allocation failed")
	}
	defer C.free(expressions)
	ids := C.malloc(C.size_t(count) * C.size_t(unsafe.Sizeof(C.uint(0))))
	if ids == nil {
		return nil, fmt.Errorf("Vectorscan ID allocation failed")
	}
	defer C.free(ids)

	expressionSlice := unsafe.Slice((**C.char)(expressions), count)
	idSlice := unsafe.Slice((*C.uint)(ids), count)
	defer func() {
		for _, expression := range expressionSlice {
			C.free(unsafe.Pointer(expression))
		}
	}()

	at := 0
	for i, pattern := range patterns {
		if pattern == "" {
			continue
		}
		expressionSlice[at] = C.CString(QuoteMeta(pattern))
		idSlice[at] = C.uint(i)
		at++
	}

	var message *C.char
	database := C.casei_hs_compile(
		(**C.char)(expressions), (*C.uint)(ids), C.uint(count), C.uint(requested), &message,
	)
	if database == nil {
		if message == nil {
			return nil, fmt.Errorf("Vectorscan compile failed")
		}
		defer C.casei_hs_free_message(message)
		return nil, fmt.Errorf("Vectorscan compile failed: %s", C.GoString(message))
	}
	info := C.casei_hs_database_info(database)
	if info == nil {
		C.hs_free_database(database)
		return nil, fmt.Errorf("Vectorscan database-info query failed")
	}
	infoText := C.GoString(info)
	C.casei_hs_free_database_info(info)
	m.vectorBits = 256
	if strings.Contains(infoText, "Features: AVX512VBMI") {
		m.vectorBits, m.vbmi = 512, true
	} else if strings.Contains(infoText, "Features: AVX512") {
		m.vectorBits = 512
	} else if !strings.Contains(infoText, "Features: AVX2") {
		C.hs_free_database(database)
		return nil, fmt.Errorf("Vectorscan database has no audited ISA target: %q", infoText)
	}
	if m.vectorBits != requested || requested == 512 && !m.vbmi {
		C.hs_free_database(database)
		return nil, fmt.Errorf("Vectorscan database target %q does not match requested %d-bit dispatch", infoText, requested)
	}

	m.database = database
	m.scratch.New = func() any {
		var ptr *C.hs_scratch_t
		if status := C.hs_alloc_scratch(database, &ptr); status != 0 {
			panic(fmt.Sprintf("Vectorscan scratch allocation failed: code %d", int(status)))
		}
		events := C.casei_hs_events_new()
		if events == nil {
			panic("Vectorscan report buffer allocation failed")
		}
		s := &scratch{ptr: ptr, events: events}
		// The pool drops idle scratch at GC; free its C memory with it. The
		// board compiles a database per cell, so this would otherwise grow.
		runtime.AddCleanup(s, func(m scratchMemory) {
			C.casei_hs_events_free(m.events)
			C.hs_free_scratch(m.ptr)
		}, scratchMemory{ptr, events})
		return s
	}
	// Allocate one scratch area before timing begins. The arena holds compiled
	// databases for its process lifetime, just as it does the PCRE2 baseline.
	m.scratch.Put(m.scratch.New())
	return m, nil
}

// CompileLiteral builds a one-pattern database.
func CompileLiteral(pattern string) (*Matcher, error) {
	return Compile([]string{pattern})
}

// CompileAlternation builds a literal set in its supplied pattern order.
func CompileAlternation(patterns []string) (*Matcher, error) {
	return Compile(patterns)
}

// QuoteMeta quotes s as one Vectorscan literal. Vectorscan accepts PCRE-style
// \Q...\E quoting; embedded terminators and NUL bytes are split out so the
// C-string compiler interface receives a faithful literal expression.
func QuoteMeta(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	b.WriteString(`\Q`)
	for len(s) > 0 {
		switch {
		case s[0] == 0:
			b.WriteString(`\E\x00\Q`)
			s = s[1:]
		case strings.HasPrefix(s, `\E`):
			b.WriteString(`\E\\E\Q`)
			s = s[2:]
		default:
			b.WriteByte(s[0])
			s = s[1:]
		}
	}
	b.WriteString(`\E`)
	return b.String()
}

func pointer(s string) *C.char {
	if len(s) == 0 {
		return (*C.char)(unsafe.Pointer(&emptySubject[0]))
	}
	return (*C.char)(unsafe.Pointer(unsafe.StringData(s)))
}

// Find scans the whole subject because Vectorscan reports every match. The
// callback reduces those reports to this package's contract: leftmost start,
// then lowest supplied pattern index. It panics on a scan failure so an
// accelerated baseline cannot silently turn into a slow fallback.
func (m *Matcher) Find(haystack string) (start, pattern int, ok bool) {
	if m == nil {
		return 0, 0, false
	}
	bestStart, bestPattern, found := 0, m.empty, m.empty >= 0
	if m.database == nil {
		return bestStart, bestPattern, found
	}
	if len(haystack) > math.MaxUint32 {
		panic("Vectorscan subject exceeds its block-mode length limit")
	}

	scratch := m.scratch.Get().(*scratch)
	var result C.casei_hs_result
	status := C.casei_hs_scan(
		m.database, pointer(haystack), C.uint(len(haystack)), scratch.ptr, &result,
	)
	runtime.KeepAlive(haystack)
	m.scratch.Put(scratch)
	if status != 0 {
		panic(fmt.Sprintf("Vectorscan scan failed: code %d", int(status)))
	}
	if C.casei_hs_result_found(&result) != 0 {
		candidateStart := int(C.casei_hs_result_start(&result))
		candidatePattern := int(C.casei_hs_result_id(&result))
		if !found || candidateStart < bestStart ||
			(candidateStart == bestStart && candidatePattern < bestPattern) {
			bestStart, bestPattern, found = candidateStart, candidatePattern, true
		}
	}
	return bestStart, bestPattern, found
}

// Index returns the byte start of the first match, or -1 when no pattern
// matches.
func (m *Matcher) Index(haystack string) int {
	start, _, ok := m.Find(haystack)
	if !ok {
		return -1
	}
	return start
}

// Each calls yield for the non-overlapping matches of casei's Matcher.Each:
// leftmost start first, ties to the lowest pattern index, the next match
// starting at or after the previous match's end. Vectorscan reports every
// match in no guaranteed order, so one scan collects all reports and the
// adapter sorts and reduces them. Both are part of the timed baseline. Each
// returns false when yield stops it. Empty patterns are outside its contract.
func (m *Matcher) Each(haystack string, yield func(start, pattern, width int) bool) bool {
	if m == nil || m.database == nil {
		return true
	}
	if m.empty >= 0 {
		panic("Vectorscan Each does not enumerate empty patterns")
	}
	if len(haystack) > math.MaxUint32 {
		panic("Vectorscan subject exceeds its block-mode length limit")
	}
	scratch := m.scratch.Get().(*scratch)
	defer m.scratch.Put(scratch)
	status := C.casei_hs_scan_all(
		m.database, pointer(haystack), C.uint(len(haystack)), scratch.ptr, scratch.events,
	)
	runtime.KeepAlive(haystack)
	if status != 0 || scratch.events.failed != 0 {
		panic(fmt.Sprintf("Vectorscan scan failed: code %d", int(status)))
	}
	events := unsafe.Slice(scratch.events.events, int(scratch.events.len))
	slices.SortFunc(events, func(a, b C.casei_hs_event) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	var at C.ulonglong
	for _, e := range events {
		if e.start < at {
			continue
		}
		if !yield(int(e.start), int(e.id), int(e.end-e.start)) {
			return false
		}
		at = e.end
	}
	return true
}
