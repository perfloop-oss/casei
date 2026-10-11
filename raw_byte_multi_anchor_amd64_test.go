//go:build amd64

package casei

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/cpu"
)

// rawByteMultiAnchorVectorResult models only the conservative table predicate
// implemented by rawByteMultiAnchorSkip64. Exact primary/guard checks belong
// to tagsAt and intentionally occur after this candidate screen.
func rawByteMultiAnchorVectorResult(s string, at int, filter *rawByteMultiAnchorFilter) (int, byte) {
	start := at
	for len(s)-at >= 65+int(filter.maxConfirmOffset) {
		for lane := 0; lane < 64; lane++ {
			primary := filter.first[s[at+lane]&0x3f] & filter.second[s[at+lane+1]&0x3f]
			var tags byte
			for group := 0; group < rawByteMultiAnchorConfirmGroups; group++ {
				offset := int(filter.confirmOffset[group])
				confirm := filter.confirmFirst[group][s[at+lane+offset]&0x3f] &
					filter.confirmSecond[group][s[at+lane+offset+1]&0x3f]
				tags |= primary & confirm
			}
			if tags != 0 {
				return at - start + lane, tags
			}
		}
		at += 64
	}
	return at - start, 0
}

// rawByteMultiAnchorDenseNoConfirmPrefix constructs a fixed counterexample to
// an unbounded dense schedule: two of the first four primary masks are set,
// but no vector-confirmed lane occurs before the sparse suffix.
func rawByteMultiAnchorDenseNoConfirmPrefix(t *testing.T, filter *rawByteMultiAnchorFilter) string {
	t.Helper()
	buf := make([]byte, 512)
	seed := uint32(1)
	for attempt := 0; attempt < 10000; attempt++ {
		for i := range buf {
			seed = seed*1664525 + 1013904223
			buf[i] = byte(seed >> 24)
		}
		occupied := 0
		for block := 0; block < 4; block++ {
			for lane := 0; lane < 64; lane++ {
				at := block*64 + lane
				if filter.first[buf[at]&0x3f]&filter.second[buf[at+1]&0x3f] != 0 {
					occupied++
					break
				}
			}
		}
		candidate := string(buf[:256])
		skipped, _ := rawByteMultiAnchorVectorResult(candidate, 0, filter)
		if occupied >= 2 && skipped >= 192 {
			return candidate
		}
	}
	t.Fatal("could not construct a dense/no-confirm prefix")
	return ""
}

func TestRawByteMultiAnchorDenseEpochMatchesTableModel(t *testing.T) {
	if !cpu.X86.HasAVX512F || !cpu.X86.HasAVX512BW || !cpu.X86.HasAVX512VBMI {
		t.Skip("AVX-512 VBMI multi-anchor path is disabled")
	}
	plan := newSearchPlan(rawByteCyrillicPatterns, false)
	filter := &plan.rawByteMulti
	if !filter.usable() {
		t.Fatal("eligible plan did not compile a raw multi-anchor filter")
	}
	prefix := rawByteMultiAnchorDenseNoConfirmPrefix(t, filter)
	for _, tc := range []struct {
		name     string
		haystack string
	}{
		{"dense_prefix_sparse_suffix", prefix + strings.Repeat("x", 5<<20)},
		{"uniform_dense_no_confirm", strings.Repeat(prefix, 1<<14)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, wantTags := rawByteMultiAnchorVectorResult(tc.haystack, 0, filter)
			if want < len(tc.haystack)-128 {
				t.Fatalf("fixture unexpectedly has a vector tag at %d", want)
			}
			got, gotTags := rawByteMultiAnchorSkip64(unsafe.StringData(tc.haystack), len(tc.haystack), filter)
			if got != want || gotTags != wantTags {
				t.Fatalf("rawByteMultiAnchorSkip64 = %d,%02x, want %d,%02x", got, gotTags, want, wantTags)
			}
		})
	}
}

func TestRawByteMultiAnchorScalarFallbackPreservesMatches(t *testing.T) {
	if !cpu.X86.HasAVX512F || !cpu.X86.HasAVX512BW || !cpu.X86.HasAVX512VBMI {
		t.Skip("AVX-512 VBMI multi-anchor path is disabled")
	}
	prior := cpu.X86.HasAVX512VBMI
	cpu.X86.HasAVX512VBMI = false
	defer func() { cpu.X86.HasAVX512VBMI = prior }()

	patterns := append(append([]string(nil), rawByteCyrillicPatterns...), strings.ToLower(rawByteCyrillicPatterns[0]))
	matcher := NewMatcher(patterns)
	if !matcher.plan.rawByteMulti.usable() {
		t.Fatal("eligible scalar-fallback plan did not compile a multi-anchor filter")
	}
	inputs := []string{
		strings.Repeat("x", 129) + rawByteCyrillicPatterns[0],
		strings.Repeat("x", 71) + "\xff" + rawByteCyrillicPatterns[3],
		"ᲁжон уотсон " + rawByteCyrillicPatterns[2],
		strings.Repeat("x", 63) + rawByteCyrillicPatterns[1] + strings.Repeat("x", 17) + rawByteCyrillicPatterns[0],
	}
	rng := rand.New(rand.NewPCG(20260908, 10))
	units := []string{"x", "Д", "д", "ᲁ", "Ж", "ж", "Ш", "ш", "K", "ſ", "\xff", "\x80", "€"}
	for iteration := 0; iteration < 128; iteration++ {
		var haystack strings.Builder
		for range 64 {
			haystack.WriteString(units[rng.IntN(len(units))])
		}
		if iteration%3 == 0 {
			haystack.WriteString(patterns[rng.IntN(len(patterns))])
		}
		inputs = append(inputs, haystack.String())
	}
	for inputIndex, haystack := range inputs {
		got, gotOK := matcher.Find(haystack)
		want, wantOK := refFind(haystack, patterns)
		if gotOK != wantOK || gotOK && got != want {
			t.Fatalf("input %d Find(%x) = %+v,%t; want %+v,%t", inputIndex, haystack, got, gotOK, want, wantOK)
		}
		rawByteCheckEach(t, matcher, haystack)
	}
}

func TestRawByteMultiAnchorVBMISkip64MatchesTableModel(t *testing.T) {
	if !cpu.X86.HasAVX512F || !cpu.X86.HasAVX512BW || !cpu.X86.HasAVX512VBMI {
		t.Skip("AVX-512 VBMI multi-anchor path is disabled")
	}
	plan := newSearchPlan(rawByteCyrillicPatterns, false)
	filter := &plan.rawByteMulti
	if !filter.usable() {
		t.Fatal("eligible plan did not compile a raw multi-anchor filter")
	}

	rng := rand.New(rand.NewPCG(20260827, 4))
	// Cross the 512-byte sparse aggregate boundary as well as every tail
	// alignment below it. The assembly must replay its four-block dispatcher
	// without changing the earliest conservative table survivor.
	for length := 0; length < 768; length++ {
		input := make([]byte, length)
		for i := range input {
			input[i] = byte(rng.Uint32())
		}
		haystack := string(input)
		for at := range haystack {
			want, wantTags := rawByteMultiAnchorVectorResult(haystack, at, filter)
			got, gotTags := rawByteMultiAnchorSkip64((*byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(haystack)), at)), len(haystack)-at, filter)
			if got != want || gotTags != wantTags {
				t.Fatalf("length %d at %d: rawByteMultiAnchorSkip64 = %d,%02x, want %d,%02x", length, at, got, gotTags, want, wantTags)
			}
		}
	}
}
