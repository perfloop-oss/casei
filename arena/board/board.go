// Package board generates the cells of the arena's generality board.
//
// BenchmarkBar measures a few dozen fixed rows. A route or bound chosen from
// the shape of those rows can win them and say nothing about the next input.
// The board measures the population instead: each family sweeps one property
// of a search -- pattern count, pattern length, script, case mode, corpus,
// match density, haystack size, operation -- and draws every other property at
// random.
//
// Nothing is hidden. The generator, its seed, and a digest of every cell it
// builds are pinned in this repository (Seed and cells.txt), so every run
// measures the same cells and anyone can read them. What keeps a change
// general is the verifier and the landing rules in AGENTS.md, not secrecy.
//
// A cell is a pure function of the seed and the cell's name, so filtering a
// run to one cell does not change that cell. Other seeds draw other boards
// for exploration; acceptance uses Seed.
package board

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Seed is the pinned seed of the acceptance board. Changing it, or anything the
// generator draws from, changes cells.txt; TestPinnedBoard holds the two
// together.
const Seed uint64 = 0xca5e1b0a4d

// CellsPerFamily is the board's sample size per family. Levels share it
// evenly, rounded up, so every level of a family has the same number of cells
// and every cell of a family has the same weight.
const CellsPerFamily = 24

// A level is one value of a swept property. Numeric levels are inclusive
// ranges; a cell draws its value inside the range, so no exact count, length,
// or size is ever a fixed target.
type level struct {
	label  string
	lo, hi int
}

// A family sweeps one property across its levels.
type family struct {
	name   string
	levels []level
}

var countLevels = []level{{"1", 1, 1}, {"2", 2, 2}, {"3-4", 3, 4}, {"5-8", 5, 8}, {"9-16", 9, 16}, {"17-32", 17, 32}, {"33-64", 33, 64}}

var families = []family{
	{"count", countLevels},
	{"length", countLevels}, // bytes per pattern, the same ranges as the count
	{"script", labels("ascii", "nonascii")},
	{"case", labels("ci", "cs")},
	{"corpus", labels("prose", "code", "logs", "russian")},
	{"density", labels("none", "sparse", "medium", "dense")},
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

func familyNamed(name string) family {
	for _, f := range families {
		if f.name == name {
			return f
		}
	}
	panic("unknown board family " + name)
}

// Spec is one cell's drawn properties. Specs are cheap; Build materializes the
// patterns and haystack.
type Spec struct {
	Family, Level string
	Index         int

	Op        string // find, each, or indexfold
	Sensitive bool   // case-sensitive matching
	NonASCII  bool   // every pattern contains a non-ASCII rune
	Corpus    string // prose, code, logs, or russian
	Density   string // none, sparse, medium, or dense
	Size      int    // haystack bytes; the built haystack ends within a rune of it
	Count     int    // patterns
	LenLo     int    // every pattern's byte length is in [LenLo, LenHi]
	LenHi     int

	seed uint64
}

// Name identifies the cell. Its first three elements are the cell's place in
// the board and do not depend on the seed; the last lists the drawn properties.
// It ends in a letter so Go's -GOMAXPROCS suffix stays unambiguous.
func (s Spec) Name() string {
	caseMode, script := "ci", "ascii"
	if s.Sensitive {
		caseMode = "cs"
	}
	if s.NonASCII {
		script = "nonascii"
	}
	return fmt.Sprintf("%s/%s/%02d/n=%d,len=%d-%d,size=%d,op=%s,script=%s,corpus=%s,density=%s,case=%s",
		s.Family, s.Level, s.Index, s.Count, s.LenLo, s.LenHi, s.Size, s.Op, script, s.Corpus, s.Density, caseMode)
}

// Cells draws the whole board for one seed, family by family.
func Cells(seed uint64) []Spec {
	var out []Spec
	for _, f := range families {
		per := (CellsPerFamily + len(f.levels) - 1) / len(f.levels)
		for _, l := range f.levels {
			for i := range per {
				out = append(out, draw(seed, f.name, l, i))
			}
		}
	}
	return out
}

// rngFor returns the random stream for one purpose of one cell. Distinct
// streams keep drawing a spec independent of building it.
func rngFor(seed uint64, name string, stream uint64) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(name))
	return rand.New(rand.NewPCG(seed, h.Sum64()^stream))
}

// draw fixes the family's property at its level and draws every other one.
// Two pairs of properties constrain each other: IndexFold searches for one
// pattern, and a one-byte pattern cannot contain a non-ASCII rune.
// Case-sensitive cells appear only in the case family. casei has no
// case-sensitive API yet, and drawing case mode everywhere would fail every
// family on that one gap and hide what each family measures.
func draw(seed uint64, familyName string, l level, index int) Spec {
	s := Spec{Family: familyName, Level: l.label, Index: index, seed: seed}
	rng := rngFor(seed, s.key(), 0)
	pick := func(name string) level {
		if name == familyName {
			return l
		}
		levels := familyNamed(name).levels
		return levels[rng.IntN(len(levels))]
	}
	within := func(l level) int { return l.lo + rng.IntN(l.hi-l.lo+1) }

	if familyName == "op" {
		s.Op = l.label
	}
	if s.Op == "indexfold" {
		s.Count = 1
	} else {
		s.Count = within(pick("count"))
	}
	if s.Op == "" {
		ops := []string{"find", "each"}
		if s.Count == 1 {
			ops = append(ops, "indexfold")
		}
		s.Op = ops[rng.IntN(len(ops))]
	}
	length := pick("length")
	s.NonASCII = pick("script").label == "nonascii"
	if s.NonASCII && length.hi < 2 {
		if familyName == "length" {
			s.NonASCII = false
		} else {
			length = countLevels[1+rng.IntN(len(countLevels)-1)]
		}
	}
	s.LenLo, s.LenHi = length.lo, length.hi
	s.Sensitive = familyName == "case" && l.label == "cs"
	s.Corpus = pick("corpus").label
	s.Density = pick("density").label
	size := pick("size")
	// Log-uniform inside the level, so small and large sizes of a wide level
	// are equally likely.
	s.Size = int(math.Round(float64(size.lo) * math.Pow(float64(size.hi)/float64(size.lo), rng.Float64())))
	return s
}

// key is the seed-independent part of the name.
func (s Spec) key() string { return fmt.Sprintf("%s/%s/%02d", s.Family, s.Level, s.Index) }

// Cell is a built cell.
type Cell struct {
	Patterns []string
	Haystack string
}

var corpora = map[string]func(*rand.Rand, int) string{
	"prose":   Prose,
	"code":    Code,
	"logs":    Logs,
	"russian": Russian,
}

// Planted occurrences per haystack byte for each density. Natural occurrences
// of the drawn patterns come on top; the run reports the actual match count.
var plantRate = map[string]float64{
	"none":   0,
	"sparse": 1.0 / (64 << 10),
	"medium": 1.0 / (1 << 10),
	"dense":  1.0 / 64,
}

// Build materializes the cell. Patterns are cut from independent draws of the
// corpora, so they read like the text they are searched in and occur in it at
// its natural rate. Density then plants fold variants of them. A "none" cell
// mutates any pattern that still occurs until none does.
func (s Spec) Build() Cell {
	rng := rngFor(s.seed, s.key(), 1)
	ascii := s.Corpus
	if ascii == "russian" {
		ascii = "prose" // ASCII patterns searched in Russian text
	}
	asciiSrc := corpora[ascii](rng, 64<<10)
	russianSrc := Russian(rng, 64<<10)

	patterns := make([]string, s.Count)
	seen := make(map[string]bool)
	for i := range patterns {
		for try := 0; ; try++ {
			n := s.LenLo + rng.IntN(s.LenHi-s.LenLo+1)
			var p string
			switch {
			case !s.NonASCII:
				p = samplePattern(rng, asciiSrc, n, 0)
			case s.Corpus == "russian" || rng.IntN(2) == 0:
				// Cyrillic text, or a Cyrillic phrase inside English text.
				p = samplePattern(rng, russianSrc, n, 1)
			default:
				// English text with one or two accented or wider fold-mate runes.
				p = samplePattern(rng, asciiSrc, n, 1+rng.IntN(2))
			}
			key := p
			if !s.Sensitive {
				key = FoldString(p)
			}
			// Distinct patterns where the length allows; a set of 64 one-byte
			// patterns may run out of distinct bytes in the source.
			if !seen[key] || try >= 64 {
				seen[key] = true
				patterns[i] = p
				break
			}
		}
	}

	haystack := plant(rng, patterns, s)
	if s.Density == "none" {
		patterns = absent(rng, patterns, haystack, asciiSrc+russianSrc, s.Sensitive)
	}
	return Cell{Patterns: patterns, Haystack: haystack}
}

// nonASCIIRunes stand in for ASCII runes in non-ASCII patterns cut from ASCII
// text. They are two and three bytes long and include the fold orbits whose
// members differ in byte width: sigma, Ohm, Angstrom, micro, and sharp s.
var nonASCIIRunes = []rune("éèüöäßẞåÅÅøæçñµμαβγδελπσςΣΩΩжщЖЩ")

// samplePattern cuts a pattern of exactly n bytes from src at a random rune
// boundary. Non-ASCII runes that do not fit the remaining length become ASCII
// letters. When wide is positive, the pattern then holds at least one
// non-ASCII rune: if it has none, wide random runes are widened.
func samplePattern(rng *rand.Rand, src string, n, wide int) string {
	at := rng.IntN(len(src))
	for src[at]&0xC0 == 0x80 {
		at--
	}
	var runes []rune
	for bytes := 0; bytes < n; {
		r, size := utf8.DecodeRuneInString(src[at:])
		if at += size; at >= len(src) {
			at = 0
		}
		if utf8.RuneLen(r) > n-bytes {
			r = asciiLetter(rng)
		}
		runes = append(runes, r)
		bytes += utf8.RuneLen(r)
	}
	if wide > 0 && isASCII(string(runes)) {
		for range wide {
			runes = widenAt(rng, runes, n)
		}
	}
	return string(runes)
}

// widenAt replaces one random rune with a non-ASCII rune of at most n bytes,
// then trims other runes from the end and pads with ASCII letters back to
// exactly n bytes.
func widenAt(rng *rand.Rand, runes []rune, n int) []rune {
	j := rng.IntN(len(runes))
	r := widen(rng, runes[j])
	for utf8.RuneLen(r) > n {
		r = nonASCIIRunes[rng.IntN(len(nonASCIIRunes))]
	}
	runes[j] = r
	for byteLen(runes) > n {
		k := len(runes) - 1
		if k == j {
			k--
		}
		runes = append(runes[:k], runes[k+1:]...)
		if k < j {
			j--
		}
	}
	for byteLen(runes) < n {
		runes = append(runes, asciiLetter(rng))
	}
	return runes
}

// widen returns a non-ASCII rune to stand in for r.
func widen(rng *rand.Rand, r rune) rune {
	switch unicode.ToLower(r) {
	case 'k':
		return 'K' // KELVIN SIGN, three bytes, folds with k
	case 's':
		return 'ſ' // LONG S, two bytes, folds with s
	}
	return nonASCIIRunes[rng.IntN(len(nonASCIIRunes))]
}

func asciiLetter(rng *rand.Rand) rune {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return rune(letters[rng.IntN(len(letters))])
}

func byteLen(runes []rune) int {
	n := 0
	for _, r := range runes {
		n += utf8.RuneLen(r)
	}
	return n
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// plant builds the haystack: fold variants of the patterns at the density's
// rate, inserted at random rune boundaries of a corpus draw that is shortened
// by their total length, so the haystack keeps every planted occurrence and
// ends within a rune of the cell's size.
func plant(rng *rand.Rand, patterns []string, s Spec) string {
	expected := float64(s.Size) * plantRate[s.Density]
	count := int(expected)
	if rng.Float64() < expected-float64(count) {
		count++
	}
	var planted []string
	total := 0
	for range count {
		v := variant(rng, patterns[rng.IntN(len(patterns))], s)
		if total+len(v) > s.Size {
			break // a wider fold variant would overflow the cell
		}
		planted = append(planted, v)
		total += len(v)
	}
	count = len(planted)
	base := corpora[s.Corpus](rng, max(s.Size-total, 0))
	if count == 0 {
		return base
	}
	at := make([]int, count)
	for i := range at {
		at[i] = rng.IntN(len(base) + 1)
	}
	slices.Sort(at)
	var b strings.Builder
	b.Grow(len(base) + total)
	prev := 0
	for i, pos := range at {
		for pos > prev && pos < len(base) && base[pos]&0xC0 == 0x80 {
			pos--
		}
		b.WriteString(base[prev:pos])
		b.WriteString(planted[i])
		prev = pos
	}
	b.WriteString(base[prev:])
	return b.String()
}

// variant spells p as a planted occurrence. Most copies flip the case of each
// rune, as text does. On non-ASCII cells one copy in eight spells each rune as
// a random member of its whole simple-fold orbit instead -- the Kelvin sign,
// long s, final sigma, and the historic Cyrillic forms U+1C80..U+1C88 -- so the
// hazards are present without dominating the text. ASCII cells stay ASCII, so
// the ASCII tier keeps its ASCII-only entrants. Case-sensitive cells plant p.
func variant(rng *rand.Rand, p string, s Spec) string {
	if s.Sensitive {
		return p
	}
	hazard := s.NonASCII && rng.IntN(8) == 0
	var b strings.Builder
	for _, r := range p {
		if hazard {
			orbit := []rune{r}
			for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
				orbit = append(orbit, x)
			}
			r = orbit[rng.IntN(len(orbit))]
		} else if flipped := flipCase(rng, r); FoldKey(flipped) == FoldKey(r) {
			r = flipped
		}
		b.WriteRune(r)
	}
	return b.String()
}

func flipCase(rng *rand.Rand, r rune) rune {
	if rng.IntN(2) == 0 {
		return unicode.ToUpper(r)
	}
	return unicode.ToLower(r)
}

// absent returns the patterns with every one that occurs in haystack mutated
// until it does not. A mutation swaps one rune for another rune of the same
// byte width from the source text, which keeps the length and script. If that
// fails, it swaps in a rune whose whole fold orbit is missing from the haystack.
// If no such rune exists the pattern stays; the run reports the actual count.
func absent(rng *rand.Rand, patterns []string, haystack, src string, sensitive bool) []string {
	text, key := haystack, func(s string) string { return s }
	if !sensitive {
		text, key = FoldString(haystack), FoldString
	}
	present := func(p string) bool { return strings.Contains(text, key(p)) }
	var srcRunes []rune
	for _, r := range src {
		srcRunes = append(srcRunes, r)
	}
	out := append([]string(nil), patterns...)
	for i, p := range out {
		for try := 0; try < 32 && present(p); try++ {
			p = swapRune(rng, p, func(width int) (rune, bool) {
				for range 16 {
					if r := srcRunes[rng.IntN(len(srcRunes))]; utf8.RuneLen(r) == width {
						return r, true
					}
				}
				return 0, false
			})
		}
		if present(p) {
			missing := missingRunes(text, sensitive)
			p = swapRune(rng, p, func(width int) (rune, bool) {
				for _, r := range missing {
					if utf8.RuneLen(r) == width {
						return r, true
					}
				}
				return 0, false
			})
		}
		out[i] = p
	}
	return out
}

// swapRune replaces one random rune of p with a rune of the same width from
// pick, leaving p unchanged when pick has none.
func swapRune(rng *rand.Rand, p string, pick func(width int) (rune, bool)) string {
	runes := []rune(p)
	j := rng.IntN(len(runes))
	if r, ok := pick(utf8.RuneLen(runes[j])); ok {
		runes[j] = r
	}
	return string(runes)
}

// missingRunes lists printable runes, ASCII and nonASCIIRunes, whose fold key
// (or, case-sensitively, the rune itself) never occurs in text.
func missingRunes(text string, sensitive bool) []rune {
	seen := make(map[rune]bool)
	for _, r := range text {
		seen[r] = true
	}
	var out []rune
	for r := rune('!'); r <= '~'; r++ {
		out = append(out, r)
	}
	out = append(out, nonASCIIRunes...)
	missing := out[:0]
	for _, r := range out {
		if !sensitive {
			r = FoldKey(r)
		}
		if !seen[r] {
			missing = append(missing, r)
		}
	}
	return missing
}
