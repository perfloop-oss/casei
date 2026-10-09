package board

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tsenart/casei"
)

// TestCorporaArePinned holds the BenchmarkBar corpora to the bytes they had
// before the builders moved here. Those rows are only comparable across runs
// while these hashes hold.
func TestCorporaArePinned(t *testing.T) {
	fixed := func() *rand.Rand { return rand.New(rand.NewPCG(0xCA5E1, 0xA12E9A)) }
	for _, c := range []struct {
		name string
		text string
		want string
	}{
		{"logs", Logs(fixed(), 1<<20), "5da84e0ea3e187047335a81cfb94228b2b091541c004da37996763a5193b38e8"},
		{"prose", Prose(fixed(), 1<<20), "c7270c76c933f8105430a0694369de23e0634d7a8ffdc6c446ca770991efce4d"},
		{"code", Code(fixed(), 256<<10), "7a289c7bedb1b64435ea4ea97333f30bc8804a6f7b3ebcdc9ee880f33884cd49"},
		{"russian", Russian(fixed(), 1<<20), "14331eddad6a58cf93a49ac59fcc3ed103237fb2327378e082075e8932e2a8af"},
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(c.text))); got != c.want {
			t.Errorf("%s corpus sha256 = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestInventory(t *testing.T) {
	cells := Cells(1)
	perFamily := map[string]int{}
	perLevel := map[string]int{}
	names := map[string]bool{}
	for _, s := range cells {
		perFamily[s.Family]++
		perLevel[s.Family+"/"+s.Level]++
		if names[s.key()] {
			t.Fatalf("duplicate cell %s", s.key())
		}
		names[s.key()] = true
		if !strings.HasPrefix(s.Name(), s.key()+"/") {
			t.Fatalf("name %q does not start with %q", s.Name(), s.key())
		}
	}
	total := 0
	for _, f := range families {
		per := (CellsPerFamily + len(f.levels) - 1) / len(f.levels)
		if perFamily[f.name] != per*len(f.levels) || perFamily[f.name] < CellsPerFamily {
			t.Errorf("family %s has %d cells, want %d", f.name, perFamily[f.name], per*len(f.levels))
		}
		for _, l := range f.levels {
			if perLevel[f.name+"/"+l.label] != per {
				t.Errorf("level %s/%s has %d cells, want %d", f.name, l.label, perLevel[f.name+"/"+l.label], per)
			}
		}
		total += per * len(f.levels)
	}
	if len(cells) != total || total != 200 {
		t.Errorf("board has %d cells, want %d (200 expected by the runtime budget)", len(cells), total)
	}
}

// TestSpecsHoldTheirLevel checks every drawn property against its family's
// level and the cross-property constraints, over many seeds.
func TestSpecsHoldTheirLevel(t *testing.T) {
	for seed := uint64(0); seed < 50; seed++ {
		for _, s := range Cells(seed) {
			if s.Op == "indexfold" && s.Count != 1 {
				t.Fatalf("%s: indexfold with %d patterns", s.Name(), s.Count)
			}
			if s.NonASCII && s.LenLo < 2 {
				t.Fatalf("%s: non-ASCII one-byte pattern", s.Name())
			}
			if s.Sensitive != (s.Family == "case" && s.Level == "cs") {
				t.Fatalf("%s: case-sensitive outside the cs level", s.Name())
			}
			if s.Count < 1 || s.Count > 64 || s.LenLo < 1 || s.LenHi > 64 || s.LenLo > s.LenHi || s.Size < 64 || s.Size > 16<<20 {
				t.Fatalf("%s: property out of the board's range", s.Name())
			}
			var l level
			for _, x := range familyNamed(s.Family).levels {
				if x.label == s.Level {
					l = x
				}
			}
			var ok bool
			switch s.Family {
			case "count":
				ok = l.lo <= s.Count && s.Count <= l.hi
			case "length":
				ok = s.LenLo == l.lo && s.LenHi == l.hi
			case "size":
				ok = l.lo <= s.Size && s.Size <= l.hi
			case "script":
				ok = s.NonASCII == (s.Level == "nonascii")
			case "case":
				ok = s.Sensitive == (s.Level == "cs")
			case "corpus":
				ok = s.Corpus == s.Level
			case "density":
				ok = s.Density == s.Level
			case "op":
				ok = s.Op == s.Level
			}
			if !ok {
				t.Fatalf("%s does not hold its level", s.Name())
			}
		}
	}
}

func TestSeedDeterminesTheBoard(t *testing.T) {
	a, b := Cells(42), Cells(42)
	if !slices.Equal(a, b) {
		t.Fatal("one seed drew two boards")
	}
	c := Cells(43)
	same := 0
	for i := range a {
		if a[i].Name() == c[i].Name() {
			same++
		}
	}
	if same > len(a)/4 {
		t.Errorf("seeds 42 and 43 drew %d of %d identical specs", same, len(a))
	}
	for i, s := range a {
		if s.Size > 64<<10 {
			continue
		}
		x, y := s.Build(), b[i].Build()
		if x.Haystack != y.Haystack || !slices.Equal(x.Patterns, y.Patterns) {
			t.Fatalf("%s built twice differently", s.Name())
		}
		if z := c[i].Build(); slices.Equal(x.Patterns, z.Patterns) && s.Size > 1024 {
			t.Errorf("%s: seeds 42 and 43 drew the same patterns", s.Name())
		}
	}
}

var update = flag.Bool("update", false, "rewrite cells.txt from the generator")

// TestPinnedBoard builds every cell of the pinned board, including the 16 MiB
// ones, checks each against its spec, holds casei to the oracle on every
// case-insensitive cell, and compares the cells with cells.txt. A change to the
// generator or the seed shows up as a diff of cells.txt; regenerate it with
// go test -run TestPinnedBoard -update.
func TestPinnedBoard(t *testing.T) {
	var pinned strings.Builder
	fmt.Fprintf(&pinned, "# BenchmarkBoard cells for board.Seed. One line per cell: name, then the\n")
	fmt.Fprintf(&pinned, "# sha256 of its patterns and haystack. Generated by TestPinnedBoard -update.\n")
	fmt.Fprintf(&pinned, "seed=%#x\n", Seed)
	for _, s := range Cells(Seed) {
		c := s.Build()
		if len(c.Patterns) != s.Count {
			t.Fatalf("%s: %d patterns", s.Name(), len(c.Patterns))
		}
		for _, p := range c.Patterns {
			if len(p) < s.LenLo || len(p) > s.LenHi || !utf8.ValidString(p) || isASCII(p) == s.NonASCII {
				t.Fatalf("%s: pattern %q breaks the spec", s.Name(), p)
			}
		}
		if !utf8.ValidString(c.Haystack) || len(c.Haystack) > s.Size || len(c.Haystack) < s.Size-utf8.UTFMax {
			t.Fatalf("%s: haystack of %d bytes", s.Name(), len(c.Haystack))
		}
		hits := Matches(c.Haystack, c.Patterns, s.Sensitive, -1)
		if s.Density == "none" && len(hits) != 0 {
			t.Errorf("%s: none-density cell has %d matches", s.Name(), len(hits))
		}
		if s.Density == "dense" && len(c.Haystack) >= 4096 && len(hits) < len(c.Haystack)/256 {
			t.Errorf("%s: dense cell has only %d matches in %d bytes", s.Name(), len(hits), len(c.Haystack))
		}
		digest := sha256.New()
		for _, p := range c.Patterns {
			fmt.Fprintf(digest, "%d:%s", len(p), p)
		}
		digest.Write([]byte(c.Haystack))
		fmt.Fprintf(&pinned, "%s %x\n", s.Name(), digest.Sum(nil))
		if s.Sensitive {
			continue
		}
		checkCasei(t, s.Name(), c, hits)
	}
	if *update {
		if err := os.WriteFile("cells.txt", []byte(pinned.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("cells.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != pinned.String() {
		t.Fatal("the generator no longer builds the cells pinned in cells.txt; review the change and run go test -run TestPinnedBoard -update")
	}
}

func checkCasei(t *testing.T, name string, c Cell, want []Hit) {
	t.Helper()
	m := casei.NewMatcher(c.Patterns)
	var got []Hit
	m.Each(c.Haystack, func(match casei.Match, width int) bool {
		got = append(got, Hit{Start: match.Start, Pattern: match.Pattern, Width: width})
		return true
	})
	if !slices.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("%s: casei Each gives %d hits, oracle %d; first difference at hit %d", name, len(got), len(want), i)
	}
	match, ok := m.Find(c.Haystack)
	if ok != (len(want) > 0) || ok && (match.Start != want[0].Start || match.Pattern != want[0].Pattern) {
		t.Fatalf("%s: casei Find = %+v,%v, oracle %+v", name, match, ok, want)
	}
	if len(c.Patterns) == 1 {
		wantIndex := -1
		if len(want) > 0 {
			wantIndex = want[0].Start
		}
		if got := casei.IndexFold(c.Haystack, c.Patterns[0]); got != wantIndex {
			t.Fatalf("%s: casei IndexFold = %d, oracle %d", name, got, wantIndex)
		}
	}
}

// naive is a third, slower definition: compare fold keys rune by rune at every
// rune start.
func naive(haystack string, patterns []string) []Hit {
	type unit struct {
		key rune
		at  int
	}
	var h []unit
	for i, r := range haystack {
		h = append(h, unit{FoldKey(r), i})
	}
	h = append(h, unit{-1, len(haystack)})
	var hits []Hit
	for i := 0; i < len(h)-1; {
		found := false
		for p, pattern := range patterns {
			k := 0
			for _, r := range pattern {
				if i+k >= len(h)-1 || h[i+k].key != FoldKey(r) {
					k = -1
					break
				}
				k++
			}
			if k > 0 {
				hits = append(hits, Hit{Start: h[i].at, Pattern: p, Width: h[i+k].at - h[i].at})
				i += k
				found = true
				break
			}
		}
		if !found {
			i++
		}
	}
	return hits
}

func TestMatchesAgreesWithNaive(t *testing.T) {
	alphabet := []rune("kKKsSſσςΣaAbB ßẞåÅÅµμΜжЖ")
	rng := rand.New(rand.NewPCG(1, 2))
	random := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for iteration := range 5000 {
		haystack := random(rng.IntN(40))
		patterns := make([]string, 1+rng.IntN(4))
		for i := range patterns {
			patterns[i] = random(1 + rng.IntN(3))
		}
		want := naive(haystack, patterns)
		if got := Matches(haystack, patterns, false, -1); !slices.Equal(got, want) {
			t.Fatalf("iteration %d: Matches(%q, %q) = %v, naive %v", iteration, haystack, patterns, got, want)
		}
		if got := Matches(haystack, patterns, false, 1); len(want) > 0 && (len(got) != 1 || got[0] != want[0]) {
			t.Fatalf("iteration %d: first of Matches(%q, %q) = %v, naive %v", iteration, haystack, patterns, got, want[0])
		}
	}
	if got := Matches("aAa", []string{"a"}, true, -1); len(got) != 2 || got[1].Start != 2 {
		t.Fatalf("case-sensitive Matches = %v", got)
	}
}
