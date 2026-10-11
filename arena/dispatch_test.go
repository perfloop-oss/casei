package arena_test

import (
	"testing"

	"golang.org/x/sys/cpu"

	"github.com/tsenart/casei"
	pcre2jit "github.com/tsenart/casei/arena/pcre2"
	stringzilla "github.com/tsenart/casei/arena/stringzilla"
)

func expectedVectorscanBits() (bits int, vbmi bool) {
	if cpu.X86.HasAVX2 && cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512VBMI {
		return 512, true
	}
	if cpu.X86.HasAVX2 {
		return 256, false
	}
	return 0, false
}

// TestFieldDispatchWidths verifies the actual widths attached to the compiled
// entrants. In particular, Vectorscan reads hs_database_info rather than
// inferring a target from host capabilities, and the AVX-512-only StringZilla
// entrant is absent rather than silently downgraded in the control process.
func TestFieldDispatchWidths(t *testing.T) {
	wantVectorscan, wantVBMI := expectedVectorscanBits()
	if wantVectorscan == 0 {
		t.Skip("native x86 field requires AVX2 or AVX-512 VBMI")
	}
	for needle, matcher := range vectorscanSingles {
		if got := matcher.VectorBits(); got != wantVectorscan || matcher.HasVBMI() != wantVBMI {
			t.Errorf("Vectorscan literal %q dispatch = %d/vbmi=%t, want %d/vbmi=%t", needle, got, matcher.HasVBMI(), wantVectorscan, wantVBMI)
		}
	}
	for i, matcher := range vectorscanAlts {
		if got := matcher.VectorBits(); got != wantVectorscan || matcher.HasVBMI() != wantVBMI {
			t.Errorf("Vectorscan alternation %d dispatch = %d/vbmi=%t, want %d/vbmi=%t", i, got, matcher.HasVBMI(), wantVectorscan, wantVBMI)
		}
	}
	if got := pcre2jit.VectorBits(); got != 128 {
		t.Errorf("PCRE2 JIT dispatch = %d, want SSE2 128", got)
	}
	for i, s := range multiScenarios {
		if s.utf8 {
			continue
		}
		matcher := rustACAlts[i]
		_, _, _ = matcher.Find(s.haystack)
		if got := matcher.VectorBits(); got != 0 && got != 128 && got != 256 {
			t.Errorf("Rust Aho-Corasick %s audited dispatch = %d, want 0, 128, or 256", s.name, got)
		}
	}
	if got := velozVectorBits(); got != 256 {
		t.Errorf("veloz dispatch = %d, want AVX2 256", got)
	}
	if stringZillaAvailable {
		if got := stringzilla.VectorBits(); got != 512 {
			t.Errorf("StringZilla dispatch = %d, want AVX-512 512", got)
		}
	} else {
		if got := stringzilla.VectorBits(); got != 0 {
			t.Errorf("excluded StringZilla dispatch = %d, want 0", got)
		}
		if _, err := stringzilla.CompileLiteral("literal", false); err == nil {
			t.Error("excluded StringZilla quietly accepted a scalar fallback")
		}
	}
	// The candidate's telemetry is self-consistency here, not a width
	// requirement: this public gym ships the scalar reference, which honestly
	// reports 0, and demanding vector dispatch of it would keep the gym's own
	// CI red forever. The width demand -- a candidate should dispatch at the
	// machine's width -- is AGENTS.md's bar, enforced where candidates are
	// judged; what this test pins is that the telemetry cannot lie or
	// disagree with itself, because the dispatch report beside the field's
	// numbers is only worth printing if it is true.
	runtime := casei.RuntimeVectorBits()
	if runtime != 0 && runtime != 256 && runtime != 512 {
		t.Errorf("candidate runtime telemetry = %d, want 0, 256 or 512", runtime)
	}
	for _, scenario := range multiScenarios {
		matcher := casei.NewMatcher(scenario.patterns)
		if got := matcher.VectorBits(); got != runtime {
			t.Errorf("candidate %s plan dispatch = %d, disagrees with runtime telemetry %d", scenario.name, got, runtime)
		}
	}
}
