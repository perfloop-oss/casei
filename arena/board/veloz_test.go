package board_test

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/cpu"

	"github.com/tsenart/casei/arena/board"
)

// originalSourceOrbitMatches is deliberately independent of the canonical
// view: it walks source rune boundaries and compares each rune by its
// unicode.SimpleFold orbit.
func originalSourceOrbitMatches(haystack string, patterns []string) []board.Hit {
	if !utf8.ValidString(haystack) || haystack == "" {
		panic("source orbit oracle needs nonempty valid UTF-8 input")
	}
	first := make(map[rune][]int)
	for pattern, needle := range patterns {
		if needle == "" || !utf8.ValidString(needle) {
			panic("source orbit oracle needs nonempty valid UTF-8 patterns")
		}
		r, _ := utf8.DecodeRuneInString(needle)
		for member := r; ; member = unicode.SimpleFold(member) {
			first[member] = append(first[member], pattern)
			if unicode.SimpleFold(member) == r {
				break
			}
		}
	}

	var hits []board.Hit
	for at := 0; at < len(haystack); {
		r, size := utf8.DecodeRuneInString(haystack[at:])
		matched := false
		for _, pattern := range first[r] {
			if end, ok := originalSourceMatchAt(haystack, at, patterns[pattern]); ok {
				hits = append(hits, board.Hit{Start: at, Pattern: pattern, Width: end - at})
				at = end
				matched = true
				break
			}
		}
		if !matched {
			at += size
		}
	}
	return hits
}

func originalSourceMatchAt(haystack string, start int, pattern string) (int, bool) {
	at := start
	for _, want := range pattern {
		if at >= len(haystack) {
			return 0, false
		}
		got, size := utf8.DecodeRuneInString(haystack[at:])
		if !sameSimpleFoldOrbit(got, want) {
			return 0, false
		}
		at += size
	}
	return at, true
}

func sameSimpleFoldOrbit(a, b rune) bool {
	if a == b {
		return true
	}
	for member := unicode.SimpleFold(a); member != a; member = unicode.SimpleFold(member) {
		if member == b {
			return true
		}
	}
	return false
}

func TestVelozCanonicalKeysCoverSimpleFoldOrbits(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		key := board.FoldKey(r)
		minimum := r
		for member := unicode.SimpleFold(r); member != r; member = unicode.SimpleFold(member) {
			minimum = min(minimum, member)
			if got := board.FoldKey(member); got != key {
				t.Fatalf("FoldKey(%U) = %U, FoldKey(%U) = %U", r, key, member, got)
			}
		}
		if key != minimum {
			t.Fatalf("FoldKey(%U) = %U, minimum orbit member is %U", r, key, minimum)
		}
		if 'a' <= key && key <= 'z' {
			t.Fatalf("canonical key %U is lowercase ASCII and Veloz could change it", key)
		}
	}
}

func TestVelozViewMatchesEveryFoldedBoardCell(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("pinned Veloz field entry requires its AVX2 route")
	}
	pinned := board.Pinned()
	folded, exact := 0, 0
	operations := map[string]int{}
	for _, spec := range board.Cells() {
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("%s digest = %s, want %s", spec.Name(), got, want)
		}
		if spec.Sensitive {
			exact++
			continue
		}
		folded++
		operations[spec.Op]++
		if cell.Haystack == "" || !utf8.ValidString(cell.Haystack) {
			t.Fatalf("%s has empty or invalid UTF-8 input", spec.Name())
		}
		for i, pattern := range cell.Patterns {
			if pattern == "" || !utf8.ValidString(pattern) {
				t.Fatalf("%s pattern %d is empty or invalid UTF-8", spec.Name(), i)
			}
		}

		want := originalSourceOrbitMatches(cell.Haystack, cell.Patterns)
		if got := board.Matches(cell.Haystack, cell.Patterns, false); !slices.Equal(got, want) {
			t.Fatalf("%s board oracle = %v, original-source orbit oracle = %v", spec.Name(), got, want)
		}
		view, err := board.NewVelozView(cell.Patterns)
		if err != nil {
			t.Fatalf("%s: %v", spec.Name(), err)
		}
		gotFind, found := view.Find(cell.Haystack)
		if len(want) == 0 {
			if found {
				t.Errorf("%s Find = %+v, want no match", spec.Name(), gotFind)
			}
		} else if !found || gotFind != want[0] {
			t.Errorf("%s Find = %+v/%t, want %+v", spec.Name(), gotFind, found, want[0])
		}
		var gotEach []board.Hit
		if complete := view.Each(cell.Haystack, func(hit board.Hit) bool {
			gotEach = append(gotEach, hit)
			return true
		}); !complete {
			t.Errorf("%s Each stopped without a stop request", spec.Name())
		}
		if !slices.Equal(gotEach, want) {
			t.Errorf("%s Each = %v, original-source orbit oracle = %v", spec.Name(), gotEach, want)
		}
		for i, hit := range gotEach {
			end := hit.Start + hit.Width
			if hit.Start < 0 || end > len(cell.Haystack) || hit.Width <= 0 ||
				(hit.Start < len(cell.Haystack) && !utf8.RuneStart(cell.Haystack[hit.Start])) ||
				(end < len(cell.Haystack) && !utf8.RuneStart(cell.Haystack[end])) {
				t.Errorf("%s hit %d has invalid projected source span %+v", spec.Name(), i, hit)
			}
		}
	}
	if folded != 112 || exact != 88 {
		t.Fatalf("pinned board has %d folded and %d exact cells, want 112 and 88", folded, exact)
	}
	for _, op := range []string{"find", "each", "indexfold"} {
		if operations[op] == 0 {
			t.Errorf("no folded %s cell was exercised", op)
		}
	}
}

func TestVelozViewFixedStreams(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("pinned Veloz field entry requires its AVX2 route")
	}
	cases := []struct {
		name     string
		patterns []string
		haystack string
		want     []board.Hit
	}{
		{"reversed Kelvin-long-s prefix", []string{"ks", "k", "s"}, "Kſk", []board.Hit{{Start: 0, Pattern: 0, Width: 5}, {Start: 5, Pattern: 1, Width: 1}}},
		{"reversed Kelvin-long-s priority", []string{"k", "ks", "s"}, "Kſk", []board.Hit{{Start: 0, Pattern: 0, Width: 3}, {Start: 3, Pattern: 2, Width: 2}, {Start: 5, Pattern: 0, Width: 1}}},
		{"overlapping prefixes", []string{"ab", "ba", "a"}, "aba", []board.Hit{{Start: 0, Pattern: 0, Width: 2}, {Start: 2, Pattern: 2, Width: 1}}},
		{"simple fold does not expand sharp-s", []string{"ss", "ß"}, "ßssẞ", []board.Hit{{Start: 0, Pattern: 1, Width: 2}, {Start: 2, Pattern: 0, Width: 2}, {Start: 4, Pattern: 1, Width: 3}}},
		{"canonical duplicates", []string{"k", "K", "K"}, "Kk", []board.Hit{{Start: 0, Pattern: 0, Width: 3}, {Start: 3, Pattern: 0, Width: 1}}},
		{"sigma orbit", []string{"Σ", "ς", "σ"}, "ςΣσ", []board.Hit{{Start: 0, Pattern: 0, Width: 2}, {Start: 2, Pattern: 0, Width: 2}, {Start: 4, Pattern: 0, Width: 2}}},
		{"four-byte fold", []string{"𐐀", "𐐨"}, "𐐨𐐀", []board.Hit{{Start: 0, Pattern: 0, Width: 4}, {Start: 4, Pattern: 0, Width: 4}}},
		{"refresh pending hit crossed by selected endpoint", []string{"ab", "b"}, "abxb", []board.Hit{{Start: 0, Pattern: 0, Width: 2}, {Start: 3, Pattern: 1, Width: 1}}},
		{"dense", []string{"a"}, "aaaa", []board.Hit{{Start: 0, Pattern: 0, Width: 1}, {Start: 1, Pattern: 0, Width: 1}, {Start: 2, Pattern: 0, Width: 1}, {Start: 3, Pattern: 0, Width: 1}}},
		{"no match", []string{"cat", "dog"}, "fox", nil},
		{"endpoint at EOF", []string{"k"}, "xK", []board.Hit{{Start: 1, Pattern: 0, Width: 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := board.NewVelozView(tc.patterns)
			if err != nil {
				t.Fatal(err)
			}
			want := originalSourceOrbitMatches(tc.haystack, tc.patterns)
			if !slices.Equal(want, tc.want) {
				t.Fatalf("independent source oracle = %v, fixed stream = %v", want, tc.want)
			}
			var got []board.Hit
			if complete := view.Each(tc.haystack, func(hit board.Hit) bool {
				got = append(got, hit)
				return true
			}); !complete {
				t.Fatal("Each stopped without a stop request")
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Each = %v, want %v", got, tc.want)
			}
			hit, found := view.Find(tc.haystack)
			if len(tc.want) == 0 {
				if found {
					t.Fatalf("Find = %+v, want no match", hit)
				}
			} else if !found || hit != tc.want[0] {
				t.Fatalf("Find = %+v/%t, want %+v", hit, found, tc.want[0])
			}
		})
	}
}

func TestVelozViewStopsAtRequestedMatch(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("pinned Veloz field entry requires its AVX2 route")
	}
	view, err := board.NewVelozView([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	haystack := strings.Repeat("A", 300)
	for _, stop := range []int{1, 256, 257} {
		count := 0
		complete := view.Each(haystack, func(board.Hit) bool {
			count++
			return count < stop
		})
		if complete || count != stop {
			t.Errorf("stop %d: Each complete=%t, yielded %d", stop, complete, count)
		}
	}
	want := originalSourceOrbitMatches(haystack, []string{"a"})
	var got []board.Hit
	if complete := view.Each(haystack, func(hit board.Hit) bool {
		got = append(got, hit)
		return true
	}); !complete || !slices.Equal(got, want) {
		t.Fatalf("full call after stops = %v/%t, want %v", got, complete, want)
	}
}

func TestVelozViewRepeatedAndNestedCalls(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("pinned Veloz field entry requires its AVX2 route")
	}
	patterns := []string{"ks", "k", "s"}
	view, err := board.NewVelozView(patterns)
	if err != nil {
		t.Fatal(err)
	}
	patterns[0] = "changed after construction"
	outerText, innerText := "Kſk", "ßssẞ"
	outerWant := originalSourceOrbitMatches(outerText, []string{"ks", "k", "s"})
	innerWant := originalSourceOrbitMatches(innerText, []string{"ks", "k", "s"})
	for range 3 {
		var got []board.Hit
		if !view.Each(outerText, func(hit board.Hit) bool {
			got = append(got, hit)
			return true
		}) || !slices.Equal(got, outerWant) {
			t.Fatalf("repeated Each = %v, want %v", got, outerWant)
		}
	}
	var outer, nested []board.Hit
	if !view.Each(outerText, func(hit board.Hit) bool {
		outer = append(outer, hit)
		var got []board.Hit
		if !view.Each(innerText, func(inner board.Hit) bool {
			got = append(got, inner)
			return true
		}) {
			t.Error("nested Each stopped")
		}
		if !slices.Equal(got, innerWant) {
			t.Errorf("nested Each = %v, want %v", got, innerWant)
		}
		nested = got
		return true
	}) || !slices.Equal(outer, outerWant) {
		t.Fatalf("outer Each = %v, want %v", outer, outerWant)
	}
	if !slices.Equal(nested, innerWant) {
		t.Fatalf("nested result after outer return = %v, want %v", nested, innerWant)
	}
}

func TestVelozViewConcurrentCallsOwnScratch(t *testing.T) {
	if !cpu.X86.HasAVX2 {
		t.Skip("pinned Veloz field entry requires its AVX2 route")
	}
	previous := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previous)

	patterns := []string{"ks", "k", "s"}
	view, err := board.NewVelozView(patterns)
	if err != nil {
		t.Fatal(err)
	}
	haystack := strings.Repeat("Kſk", 32)
	want := originalSourceOrbitMatches(haystack, patterns)
	const workers = 8
	ready := make(chan struct{}, workers)
	release := make(chan struct{})
	done := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got []board.Hit
			first := true
			complete := view.Each(haystack, func(hit board.Hit) bool {
				got = append(got, hit)
				if first {
					first = false
					ready <- struct{}{}
					<-release
				}
				return true
			})
			if !complete {
				done <- fmt.Errorf("Each stopped unexpectedly")
			} else if !slices.Equal(got, want) {
				done <- fmt.Errorf("concurrent Each = %v, want %v", got, want)
			} else {
				done <- nil
			}
		}()
	}

	completed := 0
	var failure error
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for n := 0; n < workers && failure == nil; {
		select {
		case <-ready:
			n++
		case err := <-done:
			completed++
			if err != nil {
				failure = err
			} else {
				failure = fmt.Errorf("worker completed before its first callback was held")
			}
		case <-timer.C:
			failure = fmt.Errorf("only %d of %d concurrent callbacks reached the ownership barrier", n, workers)
		}
	}
	close(release)
	for completed < workers {
		if err := <-done; err != nil && failure == nil {
			failure = err
		}
		completed++
	}
	wg.Wait()
	if failure != nil {
		t.Fatal(failure)
	}
}

func TestVelozViewAvailabilityMatchesPinnedGate(t *testing.T) {
	view, err := board.NewVelozView([]string{"a"})
	if board.VelozAvailable() {
		if err != nil || view == nil {
			t.Fatalf("AVX2 route is available, constructor = %v, err = %v", view, err)
		}
	} else if err == nil || view != nil {
		t.Fatalf("AVX2 route is unavailable, constructor = %v, err = %v", view, err)
	}
}
