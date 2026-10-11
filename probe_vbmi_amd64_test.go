//go:build amd64 && linux

package casei

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/cpu"
	"golang.org/x/sys/unix"
)

func testVBMIProbe(t *testing.T) *asciiProbe {
	t.Helper()
	if !cpu.X86.HasAVX512F || !cpu.X86.HasAVX512BW || !cpu.X86.HasAVX512VBMI {
		t.Skip("AVX-512 VBMI probe path is disabled")
	}
	plan := newSearchPlan([]string{"goto retryLabel"}, false)
	if !plan.asciiProbe.usable() || plan.asciiProbe.vbmi.valid == 0 {
		t.Fatalf("did not compile a VBMI probe: %+v", plan.asciiProbe)
	}
	return &plan.asciiProbe
}

func maxVBMIProbeOffset(probe *asciiVBMIProbe) int {
	max := probe.firstAt
	if probe.secondAt > max {
		max = probe.secondAt
	}
	if probe.thirdAt > max {
		max = probe.thirdAt
	}
	return max
}

func makeVBMIProbeInput(probe *asciiVBMIProbe, candidates int) []byte {
	input := []byte(strings.Repeat("x", candidates+maxVBMIProbeOffset(probe)))
	return input
}

func putVBMIProbe(input []byte, at int, probe *asciiProbe) {
	input[at+probe.vbmi.firstAt] = probe.first
	input[at+probe.vbmi.secondAt] = probe.second
	input[at+probe.vbmi.thirdAt] = probe.third
}

// scalarVBMIProbeSkip models the table predicate itself, including VPERMB's
// low-six-bit index. It is deliberately separate from asciiProbeAt: aliases
// are valid filter survivors but not necessarily exact literal matches.
func scalarVBMIProbeSkip(s string, candidates int, probe *asciiVBMIProbe) int {
	for at := 0; at < candidates; at++ {
		if probe.first[s[at+probe.firstAt]&0x3f]&
			probe.second[s[at+probe.secondAt]&0x3f]&
			probe.third[s[at+probe.thirdAt]&0x3f] != 0 {
			return at
		}
	}
	return candidates
}

func TestProbeVBMISkip64QuadEarliest(t *testing.T) {
	probe := testVBMIProbe(t)
	const candidates = 512

	for _, tc := range []struct {
		name   string
		starts []int
	}{
		{"miss", nil},
		{"first lane", []int{0}},
		{"first block end", []int{63}},
		{"second block start", []int{64}},
		{"second block end", []int{127}},
		{"third block start", []int{128}},
		{"third block end", []int{191}},
		{"fourth block start", []int{192}},
		{"fourth block end", []int{255}},
		{"second quad start", []int{256}},
		{"second quad end", []int{511}},
		{"first pair of masks", []int{63, 64}},
		{"second pair of masks", []int{191, 192}},
		{"quad boundary", []int{255, 256}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := makeVBMIProbeInput(&probe.vbmi, candidates)
			for _, at := range tc.starts {
				putVBMIProbe(input, at, probe)
			}
			haystack := string(input)
			want := scalarVBMIProbeSkip(haystack, candidates, &probe.vbmi)
			got := probeVBMISkip64(unsafe.StringData(haystack), candidates, &probe.vbmi)
			if got != want {
				t.Fatalf("probeVBMISkip64 = %d, want earliest table survivor %d", got, want)
			}
		})
	}
}

func TestProbeVBMISkip64QuadBounds(t *testing.T) {
	probe := testVBMIProbe(t)
	const candidates = 256
	length := candidates + maxVBMIProbeOffset(&probe.vbmi)
	page := unix.Getpagesize()
	if length > page {
		t.Fatalf("probe input %d exceeds page size %d", length, page)
	}

	mapping, err := unix.Mmap(-1, 0, 2*page, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Munmap(mapping); err != nil {
			t.Error(err)
		}
	}()
	if err := unix.Mprotect(mapping[page:], unix.PROT_NONE); err != nil {
		t.Fatal(err)
	}

	input := mapping[page-length : page]
	for i := range input {
		input[i] = 'x'
	}
	haystack := unsafe.String(unsafe.SliceData(input), len(input))
	for _, tc := range []struct {
		name string
		at   int
	}{
		{"miss", -1},
		{"final lane", candidates - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range input {
				input[i] = 'x'
			}
			if tc.at >= 0 {
				putVBMIProbe(input, tc.at, probe)
			}
			want := scalarVBMIProbeSkip(haystack, candidates, &probe.vbmi)
			got := probeVBMISkip64(unsafe.StringData(haystack), candidates, &probe.vbmi)
			if got != want {
				t.Fatalf("probeVBMISkip64 = %d, want %d", got, want)
			}
		})
	}
	runtime.KeepAlive(mapping)
}

func testVBMIPairProbe(t *testing.T) *asciiPairProbe {
	t.Helper()
	if !cpu.X86.HasAVX512F || !cpu.X86.HasAVX512BW || !cpu.X86.HasAVX512VBMI {
		t.Skip("AVX-512 VBMI pair path is disabled")
	}
	if got := unsafe.Offsetof(asciiPairVBMIProbe{}.secondAt); got != 128 {
		t.Fatalf("asciiPairVBMIProbe.secondAt offset = %d, want 128", got)
	}
	plan := newSearchPlan([]string{"goto retryLabel"}, false)
	if !plan.asciiPairVBMIDisplaced() {
		t.Fatalf("did not compile a displaced VBMI pair: %+v", plan.asciiPair.vbmi)
	}
	return &plan.asciiPair
}

func makeVBMIPairInput(probe *asciiPairProbe, candidates int) []byte {
	return []byte(strings.Repeat("x", candidates+int(probe.vbmi.secondAt)))
}

func putVBMIPair(input []byte, at int, probe *asciiPairProbe) {
	input[at] = probe.first
	input[at+int(probe.vbmi.secondAt)] = 'y'
}

// scalarVBMIPairSkip models the two-table VPERMB predicate. It deliberately
// retains low-six-bit aliases because the assembly is only a conservative
// filter before the common plan confirms a survivor.
func scalarVBMIPairSkip(s string, candidates int, probe *asciiPairVBMIProbe) int {
	for at := 0; at < candidates; at++ {
		if probe.first[s[at]&0x3f]&probe.second[s[at+int(probe.secondAt)]&0x3f] != 0 {
			return at
		}
	}
	return candidates
}

func TestASCIIPairDirectVBMISkip64QuadEarliest(t *testing.T) {
	probe := testVBMIPairProbe(t)
	const candidates = 512

	for _, tc := range []struct {
		name   string
		starts []int
	}{
		{"miss", nil},
		{"first lane", []int{0}},
		{"first block end", []int{63}},
		{"second block start", []int{64}},
		{"second block end", []int{127}},
		{"third block start", []int{128}},
		{"third block end", []int{191}},
		{"fourth block start", []int{192}},
		{"fourth block end", []int{255}},
		{"second quad start", []int{256}},
		{"second quad end", []int{511}},
		{"first pair of masks", []int{63, 64}},
		{"second pair of masks", []int{191, 192}},
		{"quad boundary", []int{255, 256}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := makeVBMIPairInput(probe, candidates)
			for _, at := range tc.starts {
				putVBMIPair(input, at, probe)
			}
			haystack := string(input)
			want := scalarVBMIPairSkip(haystack, candidates, &probe.vbmi)
			got := asciiPairDirectVBMISkip64(unsafe.StringData(haystack), candidates, &probe.vbmi)
			if got != want {
				t.Fatalf("asciiPairDirectVBMISkip64 = %d, want earliest table survivor %d", got, want)
			}
		})
	}
}

func TestASCIIPairDirectVBMISkip64Bounds(t *testing.T) {
	probe := testVBMIPairProbe(t)
	const candidates = 256
	length := candidates + int(probe.vbmi.secondAt)
	page := unix.Getpagesize()
	if length > page {
		t.Fatalf("pair input %d exceeds page size %d", length, page)
	}

	mapping, err := unix.Mmap(-1, 0, 2*page, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Munmap(mapping); err != nil {
			t.Error(err)
		}
	}()
	if err := unix.Mprotect(mapping[page:], unix.PROT_NONE); err != nil {
		t.Fatal(err)
	}

	input := mapping[page-length : page]
	for i := range input {
		input[i] = 'x'
	}
	haystack := unsafe.String(unsafe.SliceData(input), len(input))
	for _, tc := range []struct {
		name string
		at   int
	}{
		{"miss", -1},
		{"final lane", candidates - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range input {
				input[i] = 'x'
			}
			if tc.at >= 0 {
				putVBMIPair(input, tc.at, probe)
			}
			want := scalarVBMIPairSkip(haystack, candidates, &probe.vbmi)
			got := asciiPairDirectVBMISkip64(unsafe.StringData(haystack), candidates, &probe.vbmi)
			if got != want {
				t.Fatalf("asciiPairDirectVBMISkip64 = %d, want %d", got, want)
			}
		})
	}
	runtime.KeepAlive(mapping)
}
