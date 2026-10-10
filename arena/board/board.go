// Package board generates the cells of the arena's generality board.
//
// BenchmarkBar measures a few dozen fixed rows; a route or bound chosen from
// their shape can win them and say nothing about the next input. The board
// measures the population: each family sweeps one property of a search --
// pattern count, pattern length, script, case mode, corpus, match density,
// haystack size, operation -- and draws every other property at random.
//
// Nothing is hidden. The generator, Seed, and every built cell's digest
// (cells.txt) are in the repository, and BenchmarkBoard checks each cell
// against its digest before timing it. A cell is a pure function of Seed and
// its name, so a run filtered to one cell builds the same cell.
package board

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Seed is the board's pinned seed.
const Seed uint64 = 0xca5e1b0a4d

// CellsPerFamily is each family's sample size. Levels share it evenly, rounded
// up, so every cell of a family has the same weight.
const CellsPerFamily = 24

// A level is one value of a swept property. Numeric levels are inclusive
// ranges, and a cell draws its value inside the range.
type level struct {
	label  string
	lo, hi int
}

var countLevels = []level{{"1", 1, 1}, {"2", 2, 2}, {"3-4", 3, 4}, {"5-8", 5, 8}, {"9-16", 9, 16}, {"17-32", 17, 32}, {"33-64", 33, 64}}

// families lists the swept properties and their levels, in board order. A
// script level is the share of non-ASCII patterns in the set, in percent.
var families = []struct {
	name   string
	levels []level
}{
	{"count", countLevels},
	{"length", countLevels}, // bytes per pattern
	{"script", []level{{"ascii", 0, 0}, {"mixed", 50, 50}, {"nonascii", 100, 100}}},
	{"case", labels("ci", "cs")},
	{"corpus", labels("prose", "code", "logs", "russian")},
	{"density", []level{{"none", 0, 0}, {"sparse", 64 << 10, 0}, {"medium", 1 << 10, 0}, {"dense", 64, 0}}},
	{"size", []level{
		{"64B-255B", 64, 255},
		{"256B-4KiB", 256, 4<<10 - 1},
		{"4KiB-64KiB", 4 << 10, 64<<10 - 1},
		{"64KiB-1MiB", 64 << 10, 1<<20 - 1},
		{"1MiB-4MiB", 1 << 20, 4<<20 - 1},
		{"4MiB-16MiB", 4 << 20, 16 << 20},
	}},
	{"op", labels("find", "each", "indexfold")},
}

func labels(names ...string) []level {
	out := make([]level, len(names))
	for i, n := range names {
		out[i] = level{label: n}
	}
	return out
}

// Spec is one cell's drawn properties. Build materializes it.
type Spec struct {
	Family, Level string
	Index         int

	Op        string  // find, each, or indexfold
	Sensitive bool    // case-sensitive matching
	NonASCII  int     // percent of patterns that hold a non-ASCII rune
	Corpus    string  // prose, code, logs, or russian
	Spacing   int     // bytes per planted match; 0 plants none
	Size      int     // haystack bytes before fold variants widen planted runes
	Count     int     // patterns
	LenLo     int     // every pattern's byte length is in [LenLo, LenHi]
	LenHi     int     //
	First     float64 // where the first match starts, as a fraction of the haystack
}

// Name identifies the cell: its place in the board, then its drawn
// properties. It ends in a letter so Go's -GOMAXPROCS suffix stays unambiguous.
func (s Spec) Name() string {
	caseMode := "ci"
	if s.Sensitive {
		caseMode = "cs"
	}
	return fmt.Sprintf("%s/%s/%02d/n=%d,len=%d-%d,size=%d,first=%.2f,spacing=%d,op=%s,nonascii=%d,corpus=%s,case=%s",
		s.Family, s.Level, s.Index, s.Count, s.LenLo, s.LenHi, s.Size, s.First, s.Spacing, s.Op, s.NonASCII, s.Corpus, caseMode)
}

func (s Spec) key() string { return fmt.Sprintf("%s/%s/%02d", s.Family, s.Level, s.Index) }

// Cells draws the whole board, family by family.
func Cells() []Spec {
	var out []Spec
	for _, f := range families {
		per := (CellsPerFamily + len(f.levels) - 1) / len(f.levels)
		for _, l := range f.levels {
			for i := range per {
				out = append(out, draw(f.name, l, i))
			}
		}
	}
	return out
}

// rngFor returns one cell's random stream for one purpose, so drawing a spec
// and building it are independent.
func rngFor(key string, stream uint64) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(key))
	return rand.New(rand.NewPCG(Seed, h.Sum64()^stream))
}

// draw fixes the family's property at its level and draws every other one,
// redrawing until the cell is coherent: IndexFold searches for one pattern,
// and a one-byte pattern cannot hold a non-ASCII rune. Case-sensitive cells
// appear only in the case family: casei has no case-sensitive API yet, and
// drawing case mode everywhere would fail every family on that one gap.
func draw(familyName string, l level, index int) Spec {
	s := Spec{Family: familyName, Level: l.label, Index: index}
	rng := rngFor(s.key(), 0)
	pick := func(name string) level {
		if name == familyName {
			return l
		}
		for _, f := range families {
			if f.name == name {
				return f.levels[rng.IntN(len(f.levels))]
			}
		}
		panic("unknown board family " + name)
	}
	within := func(l level) int { return l.lo + rng.IntN(l.hi-l.lo+1) }
	for {
		s.Op = pick("op").label
		s.Count = within(pick("count"))
		length := pick("length")
		s.LenLo, s.LenHi = length.lo, length.hi
		s.NonASCII = pick("script").lo
		if (s.Op != "indexfold" || s.Count == 1) && (s.NonASCII == 0 || s.LenLo >= 2) {
			break
		}
	}
	s.Sensitive = familyName == "case" && l.label == "cs"
	s.Corpus = pick("corpus").label
	s.Spacing = pick("density").lo
	size := pick("size")
	s.Size = int(math.Round(float64(size.lo) * math.Pow(float64(size.hi)/float64(size.lo), rng.Float64())))
	s.First = math.Round(rng.Float64()*100) / 100
	return s
}

// Cell is a built cell.
type Cell struct {
	Patterns []string
	Haystack string
	ASCII    bool // patterns and haystack are all ASCII
}

//go:embed cells.txt
var cellsTxt string

// Pinned maps each pinned cell's name to its tier ("ascii" or "utf8") and
// digest, as cells.txt records them.
func Pinned() map[string][2]string {
	out := map[string][2]string{}
	for _, line := range strings.Split(cellsTxt, "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			out[f[0]] = [2]string{f[1], f[2]}
		}
	}
	return out
}

// Digest identifies a built cell's bytes.
func (c Cell) Digest() string {
	h := sha256.New()
	for _, p := range c.Patterns {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	h.Write([]byte(c.Haystack))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Build materializes the cell. Patterns are cut from the cell's corpus
// (English for ASCII patterns in Russian text), so they read like the text
// they are searched in; a non-ASCII pattern cut from ASCII text gets a rune
// from the pool. Patterns that occur in the text are mutated until they do
// not, so every match is a planted one: the first at First, the rest one per
// Spacing bytes after it.
func (s Spec) Build() Cell {
	rng := rngFor(s.key(), 1)
	text := s.Corpus
	if text == "russian" {
		text = "prose"
	}
	patterns := make([]string, s.Count)
	for i := range patterns {
		nonASCII := rng.IntN(100) < s.NonASCII
		source := text
		if nonASCII {
			source = s.Corpus
		}
		patterns[i] = samplePattern(rng, corpus(rng, source, 64<<10), s.LenLo+rng.IntN(s.LenHi-s.LenLo+1), nonASCII)
	}
	planted, total := plants(rng, patterns, s)
	base := corpus(rng, s.Corpus, max(s.Size-total, 0))
	absent(rng, patterns, base, s.Sensitive)
	ascii := isASCII(base) && !slices.ContainsFunc(patterns, func(p string) bool { return !isASCII(p) })
	return Cell{Patterns: patterns, Haystack: plant(rng, base, planted, patterns, s, ascii), ASCII: ascii}
}

// pool holds the runes that stand in for ASCII in non-ASCII patterns cut from
// ASCII text and replace runes in absent patterns: letters of two-, three-
// and four-byte scripts, cased and caseless.
var pool = func() []rune {
	var out []rune
	for _, r := range [][2]rune{
		{0x00C0, 0x024F}, {0x0370, 0x03FF}, {0x0400, 0x04FF}, {0x0531, 0x0587}, // Latin, Greek, Cyrillic, Armenian
		{0x10A0, 0x10FF}, {0x1E00, 0x1EFF}, {0x3041, 0x3096}, {0x4E00, 0x4FFF}, // Georgian, Latin, Hiragana, CJK
		{0xAC00, 0xACFF}, {0x10400, 0x1044F}, {0x1E900, 0x1E943}, // Hangul, Deseret, Adlam
	} {
		for c := r[0]; c <= r[1]; c++ {
			if unicode.IsLetter(c) {
				out = append(out, c)
			}
		}
	}
	return out
}()

// samplePattern cuts a pattern of exactly n bytes from src at a random rune
// boundary. Runes that do not fit the remaining length, or are non-ASCII in an
// ASCII pattern, become ASCII letters. A non-ASCII pattern that came out all
// ASCII gets one rune from the pool, trimmed and padded back to n bytes.
func samplePattern(rng *rand.Rand, src string, n int, nonASCII bool) string {
	at := rng.IntN(len(src))
	for src[at]&0xC0 == 0x80 {
		at--
	}
	var runes []rune
	for bytes := 0; bytes < n; {
		r, size := utf8.DecodeRuneInString(src[at:])
		at = (at + size) % len(src)
		if utf8.RuneLen(r) > n-bytes || !nonASCII && r >= utf8.RuneSelf {
			r = rune('a' + rng.IntN(26))
		}
		runes = append(runes, r)
		bytes += utf8.RuneLen(r)
	}
	if nonASCII && isASCII(string(runes)) {
		j, r := rng.IntN(len(runes)), pool[rng.IntN(len(pool))]
		for utf8.RuneLen(r) > n {
			r = pool[rng.IntN(len(pool))]
		}
		runes[j] = r
		for len(string(runes)) > n {
			k := len(runes) - 1
			if k == j {
				k--
				j--
			}
			runes = slices.Delete(runes, k, k+1)
		}
		for len(string(runes)) < n {
			runes = append(runes, rune('a'+rng.IntN(26)))
		}
	}
	return string(runes)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// plants draws the planted occurrences: one at First, then one per Spacing
// bytes of the rest of the haystack. It returns their patterns and total
// pattern bytes, which the corpus draw leaves room for.
func plants(rng *rand.Rand, patterns []string, s Spec) ([]int, int) {
	if s.Spacing == 0 {
		return nil, 0
	}
	expected := float64(s.Size) * (1 - s.First) / float64(s.Spacing)
	count := 1 + int(expected)
	if rng.Float64() < expected-float64(count-1) {
		count++
	}
	var planted []int
	total := 0
	for range count {
		i := rng.IntN(len(patterns))
		if total+len(patterns[i]) > s.Size {
			break
		}
		planted = append(planted, i)
		total += len(patterns[i])
	}
	return planted, total
}

// plant inserts a fold variant of each planted pattern into base, the first at
// First and the rest at random rune boundaries after it.
func plant(rng *rand.Rand, base string, planted []int, patterns []string, s Spec, ascii bool) string {
	if len(planted) == 0 {
		return base
	}
	first := int(s.First * float64(len(base)))
	at := []int{first}
	for range planted[1:] {
		at = append(at, first+rng.IntN(len(base)-first+1))
	}
	slices.Sort(at)
	var b strings.Builder
	prev := 0
	for i, pos := range at {
		for pos > prev && pos < len(base) && base[pos]&0xC0 == 0x80 {
			pos--
		}
		b.WriteString(base[prev:pos])
		b.WriteString(variant(rng, patterns[planted[i]], s, ascii))
		prev = pos
	}
	b.WriteString(base[prev:])
	return b.String()
}

// variant spells each rune of p as a random member of HazardMates. An ASCII
// cell keeps only ASCII mates, so the ASCII tier keeps its ASCII-only
// entrants. Case-sensitive cells plant p.
func variant(rng *rand.Rand, p string, s Spec, ascii bool) string {
	if s.Sensitive {
		return p
	}
	var b strings.Builder
	for _, r := range p {
		mates := HazardMates(r)
		if ascii {
			mates = slices.DeleteFunc(mates, func(m rune) bool { return m >= utf8.RuneSelf })
		}
		b.WriteRune(mates[rng.IntN(len(mates))])
	}
	return b.String()
}

// unfolded lists fold mates some pinned entrant does not fold, as
// TestBoardHazardMatesAgree finds them over every rune: Vectorscan 5.4.12
// predates the case pairs Unicode added from version 8 on (Cherokee, Osage,
// Georgian Mtavruli, Adlam, and others). They stay out of timing cells, where
// they would disqualify an entrant; the oracle tests keep them.
var unfolded = [][2]rune{
	{0x026A, 0x026A}, {0x0282, 0x0282}, {0x029D, 0x029D}, {0x10D0, 0x10FF}, {0x13A0, 0x13FD},
	{0x1C80, 0x1CBF}, {0x1D8E, 0x1D8E}, {0x2C2F, 0x2C2F}, {0x2C5F, 0x2C5F}, {0xA64A, 0xA64B},
	{0xA794, 0xA794}, {0xA7AE, 0xA7D9}, {0xA7F5, 0xA7F6}, {0xAB53, 0xAB53}, {0xAB70, 0xABBF},
	{0x104B0, 0x104FB}, {0x10570, 0x105BC}, {0x10C80, 0x10CF2}, {0x16E40, 0x16E7F}, {0x1E900, 0x1E943},
}

// HazardMates returns r and every member of its simple-fold orbit that all
// pinned entrants fold, the runes the board may plant for r.
func HazardMates(r rune) []rune {
	mates := []rune{r}
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		if !slices.ContainsFunc(unfolded, func(u [2]rune) bool { return u[0] <= x && x <= u[1] }) {
			mates = append(mates, x)
		}
	}
	return mates
}

// absent mutates each pattern that occurs in text until it does not, by
// swapping one rune for a random rune of the same byte width from printable
// ASCII and the pool, which keeps the pattern's length and script.
func absent(rng *rand.Rand, patterns []string, text string, sensitive bool) {
	key := func(s string) string { return s }
	if !sensitive {
		key = FoldString
	}
	text = key(text)
	swaps := map[int][]rune{}
	for r := rune(' '); r <= '~'; r++ {
		swaps[1] = append(swaps[1], r)
	}
	for _, r := range pool {
		swaps[utf8.RuneLen(r)] = append(swaps[utf8.RuneLen(r)], r)
	}
	for i, p := range patterns {
		runes := []rune(p)
		for try := 0; try < 1024 && strings.Contains(text, key(string(runes))); try++ {
			j := rng.IntN(len(runes))
			same := swaps[utf8.RuneLen(runes[j])]
			runes[j] = same[rng.IntN(len(same))]
		}
		patterns[i] = string(runes)
	}
}
