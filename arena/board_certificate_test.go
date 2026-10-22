package arena_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/tsenart/casei/arena/board"
)

var expectedVelozUTF8DirectCells = map[string]struct{}{
	"length/3-4/03":     {},
	"case/ci/09":        {},
	"corpus/russian/02": {},
	"size/1MiB-4MiB/00": {},
	"op/indexfold/00":   {},
	"op/indexfold/04":   {},
}

func expectedVelozDirect(name, tier string, patternCount int) bool {
	if patternCount != 1 {
		return false
	}
	if tier == "ascii" {
		return true
	}
	if tier != "utf8" {
		return false
	}
	_, ok := expectedVelozUTF8DirectCells[pinnedCellID(name)]
	return ok
}

func pinnedCellID(name string) string {
	if i := strings.Index(name, "/n="); i >= 0 {
		return name[:i]
	}
	return name
}

// sourceSimpleFoldOrbit enumerates one complete Unicode simple-fold cycle.
func sourceSimpleFoldOrbit(r rune) []rune {
	orbit := []rune{r}
	for member := unicode.SimpleFold(r); member != r; member = unicode.SimpleFold(member) {
		if slices.Contains(orbit, member) {
			panic("unicode.SimpleFold orbit did not return to its start")
		}
		orbit = append(orbit, member)
	}
	return orbit
}

func completeSimpleFoldSpellings(pattern string) []string {
	spellings := []string{""}
	for _, r := range pattern {
		orbit := sourceSimpleFoldOrbit(r)
		next := make([]string, 0, len(spellings)*len(orbit))
		for _, prefix := range spellings {
			for _, member := range orbit {
				next = append(next, prefix+string(member))
			}
		}
		spellings = next
	}
	return spellings
}

func checkVelozBoardOracle(t *testing.T, engine boardEngine, pattern, haystack string) {
	t.Helper()
	want := board.Matches(haystack, []string{pattern}, false)
	findWant := want
	if len(findWant) > 1 {
		findWant = findWant[:1]
	}
	if err := engine.bind("veloz", "find", haystack).agree(findWant); err != nil {
		t.Errorf("Veloz Find for %q: %v", pattern, err)
	}
	if err := engine.bind("veloz", "each", haystack).agree(want); err != nil {
		t.Errorf("Veloz Each for %q: %v", pattern, err)
	}
}

func TestVelozBoardCertifiedLanguageSelection(t *testing.T) {
	if !board.VelozAvailable() {
		t.Skip("pinned Veloz field entry requires AVX2")
	}
	pinned := board.Pinned()
	seen := make(map[string]bool, len(expectedVelozUTF8DirectCells))
	folded, exact, direct, view := 0, 0, 0, 0
	for _, spec := range board.Cells() {
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("%s digest = %s, want %s", spec.Name(), got, want)
		}
		tier := pinned[spec.Name()][0]
		engine, supported, err := boardEntrant("veloz", cell.Patterns, cell.ASCII, spec.Sensitive)
		if err != nil {
			t.Fatalf("%s Veloz selection: %v", spec.Name(), err)
		}
		if spec.Sensitive {
			exact++
			if supported {
				t.Errorf("%s: Veloz entered an exact cell", spec.Name())
			}
			continue
		}
		folded++
		if !supported {
			t.Errorf("%s: Veloz does not support a folded cell", spec.Name())
			continue
		}

		wantDirect := expectedVelozDirect(spec.Name(), tier, len(cell.Patterns))
		wantMode := "view"
		if wantDirect {
			wantMode = "direct"
		}
		if engine.mode != wantMode {
			t.Errorf("%s: Veloz mode = %q, want %q", spec.Name(), engine.mode, wantMode)
		}
		if engine.mode == "direct" {
			direct++
		} else if engine.mode == "view" {
			view++
		}

		id := pinnedCellID(spec.Name())
		if tier == "utf8" && len(cell.Patterns) == 1 && wantDirect {
			seen[id] = true
			if cell.ASCII {
				t.Errorf("%s: certified UTF-8 cell unexpectedly has ASCII input", spec.Name())
			}
			spellings := completeSimpleFoldSpellings(cell.Patterns[0])
			for _, spelling := range spellings {
				if len(spelling) != len(cell.Patterns[0]) {
					t.Errorf("%s fold spelling %q changes byte length from %d to %d", spec.Name(), spelling, len(cell.Patterns[0]), len(spelling))
				}
			}
			haystack := strings.Join(spellings, "\x00")
			checkVelozBoardOracle(t, engine, cell.Patterns[0], haystack)
		}
	}
	if folded != 112 || exact != 88 {
		t.Fatalf("board has %d folded and %d exact cells, want 112 and 88", folded, exact)
	}
	for id := range expectedVelozUTF8DirectCells {
		if !seen[id] {
			t.Fatalf("pinned Veloz direct cell %s is absent", id)
		}
	}
	if direct != 15 || view != 97 {
		t.Errorf("Veloz mode census is %d direct/%d view, want 15/97", direct, view)
	}

	for _, pattern := range []string{"k", "K", "K", "s", "S", "ſ", "aK", "aſ", "aЖ"} {
		engine, supported, err := boardEntrant("veloz", []string{pattern}, false, false)
		if err != nil || !supported {
			t.Fatalf("control %q Veloz selection = %t, error %v", pattern, supported, err)
		}
		if engine.mode != "view" {
			t.Errorf("width-changing control %q selected %q, want view", pattern, engine.mode)
		}
		haystack := strings.Join(completeSimpleFoldSpellings(pattern), "\x00")
		checkVelozBoardOracle(t, engine, pattern, haystack)
	}

	for _, pattern := range []string{"¢", "一", "😀", "a¢", "a😀"} {
		engine, supported, err := boardEntrant("veloz", []string{pattern}, false, false)
		if err != nil || !supported {
			t.Fatalf("positive control %q Veloz selection = %t, error %v", pattern, supported, err)
		}
		if engine.mode != "direct" {
			t.Errorf("safe single pattern %q selected %q, want direct", pattern, engine.mode)
		}
		checkVelozBoardOracle(t, engine, pattern, strings.Join(completeSimpleFoldSpellings(pattern), "\x00"))
	}

	for _, pair := range [][2]string{{"一", "丠"}, {"丠", "一"}} {
		pattern, nearMiss := pair[0], pair[1]
		engine, supported, err := boardEntrant("veloz", []string{pattern}, false, false)
		if err != nil || !supported {
			t.Fatalf("bit-five control Veloz selection = %t, error %v", supported, err)
		}
		if engine.mode != "direct" {
			t.Errorf("invariant non-ASCII pattern %q selected %q, want direct", pattern, engine.mode)
		}
		checkVelozBoardOracle(t, engine, pattern, nearMiss+"\x00"+pattern)
	}
}
