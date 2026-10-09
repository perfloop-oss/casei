package arena_test

// The generality board. BenchmarkBar times fixed rows; BenchmarkBoard times the
// pinned cells of package board, family by family, and pairs casei with every
// pinned entrant that supports each cell. It uses the bar's pairing
// (pairedRatio) and the bar's adapters. scripts/verify_board.py turns a
// transcript into per-family aggregates and applies the board's rules.
//
// A run prints its seed before the first cell. -board.seed draws another
// board for exploration; acceptance runs use the pinned board.Seed.

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	veloz "github.com/mhr3/veloz/ascii"
	"golang.org/x/sys/cpu"

	"github.com/tsenart/casei"
	"github.com/tsenart/casei/arena/board"
	pcre2jit "github.com/tsenart/casei/arena/pcre2"
	rure "github.com/tsenart/casei/arena/rure"
	rustac "github.com/tsenart/casei/arena/rustac"
	stringzilla "github.com/tsenart/casei/arena/stringzilla"
	vectorscan "github.com/tsenart/casei/arena/vectorscan"
)

var boardSeed = flag.Uint64("board.seed", board.Seed,
	"draw BenchmarkBoard from another seed, for exploration; acceptance uses the pinned board.Seed")

// boardEntrants names every field entrant that can count on a board cell, in
// the order each cell reports them. Diagnostic entrants never count, so they
// are not timed.
var boardEntrants = []string{"regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac"}

// BenchmarkBoard reports, per cell, x_vs_best against every supporting
// entrant, each entrant's paired ratio, and the actual match count. Run it with
// -benchtime 1x: the pairing does the timing, and the closing b.Loop only adds
// casei's own ns/op as a diagnostic.
func BenchmarkBoard(b *testing.B) {
	seed := *boardSeed
	cells := board.Cells(seed)
	fmt.Printf("board: seed=%#x cells=%d %s\n", seed, len(cells), boardHost())
	for _, spec := range cells {
		b.Run(spec.Name(), func(b *testing.B) { runBoardCell(b, seed, spec) })
	}
}

func runBoardCell(b *testing.B, seed uint64, spec board.Spec) {
	metrics := map[string]float64{}
	for _, name := range boardEntrants {
		metrics[name+"_active"], metrics[name+"_x"], metrics[name+"_wrong"] = 0, 0, 0
	}
	report := func() {
		for unit, value := range metrics {
			b.ReportMetric(value, unit)
		}
	}
	if spec.Sensitive {
		// casei has no case-sensitive API. The cell stays on the board as
		// unsupported, which fails the board until casei can answer it.
		for b.Loop() {
		}
		for _, unit := range []string{"x_vs_best", "competitors", "entrants", "candidate_supported", "ascii_tier", "matches", "bytes"} {
			metrics[unit] = 0
		}
		report()
		return
	}

	c, err := prepareBoardCell(spec)
	if err == nil {
		err = c.check()
	}
	if err != nil {
		b.Fatalf("seed %#x cell %s: %v", seed, spec.Name(), err)
	}
	for _, w := range c.wrong {
		metrics[w.name+"_wrong"] = 1
		b.Logf("seed %#x: %v; not timed on this cell", seed, w)
	}
	best, competitors := 0.0, 0
	for _, e := range c.entrants {
		ratio := pairedRatio(c.candidate.run, e.run)
		metrics[e.name+"_active"], metrics[e.name+"_x"] = 1, ratio
		best = max(best, ratio)
		competitors++
	}
	for b.Loop() {
		c.candidate.run()
	}
	metrics["x_vs_best"] = best
	metrics["competitors"] = float64(competitors)
	metrics["entrants"] = float64(competitors + 1)
	metrics["candidate_supported"] = 1
	metrics["ascii_tier"] = boolMetric(c.asciiTier)
	metrics["matches"] = float64(len(c.all))
	metrics["bytes"] = float64(len(c.cell.Haystack))
	report()
}

// boardCell is a built cell with casei and every supporting entrant ready to
// run its operation.
type boardCell struct {
	spec      board.Spec
	cell      board.Cell
	asciiTier bool        // patterns and haystack are ASCII
	all       []board.Hit // the oracle's Each answer
	candidate boardRun
	entrants  []boardRun    // supporting entrants that agree with the oracle
	wrong     []*boardWrong // supporting entrants that do not
}

// boardRun is one implementation's operation on the cell. answer reports what
// run computes; a Pattern or Width of -1 means the entrant does not report it.
type boardRun struct {
	name   string
	run    func()
	answer func() []board.Hit
}

// check holds casei and every entrant to the oracle before any timing, so a
// ratio never comes from a wrong answer. A wrong casei answer fails the cell.
// A wrong entrant answer moves the entrant to wrong: it is reported by name on
// the cell, not timed, and not hidden. The board reaches fold orbits the fixed
// rows never did: Vectorscan 5.4.12 does not fold в, д, о, с, т, and ъ with
// their Unicode 9 orbit members U+1C80..U+1C86, which Go's regexp does.
func (c *boardCell) check() error {
	if err := c.disagreement(c.candidate); err != nil {
		return err
	}
	kept := c.entrants[:0]
	for _, e := range c.entrants {
		if err := c.disagreement(e); err != nil {
			c.wrong = append(c.wrong, err)
			continue
		}
		kept = append(kept, e)
	}
	c.entrants = kept
	return nil
}

// boardWrong is an entrant that disagreed with the oracle on a cell.
type boardWrong struct {
	name   string
	detail string
}

func (w boardWrong) Error() string { return w.name + " " + w.detail }

func (c *boardCell) disagreement(r boardRun) *boardWrong {
	want := c.all
	if c.spec.Op != "each" && len(want) > 1 {
		want = want[:1]
	}
	got := r.answer()
	if len(got) != len(want) {
		return &boardWrong{r.name, fmt.Sprintf("gives %d matches, oracle %d", len(got), len(want))}
	}
	for i, h := range got {
		w := want[i]
		if h.Start != w.Start || h.Pattern >= 0 && h.Pattern != w.Pattern || h.Width >= 0 && h.Width != w.Width {
			return &boardWrong{r.name, fmt.Sprintf("match %d is %+v, oracle %+v", i, h, w)}
		}
	}
	return nil
}

func prepareBoardCell(spec board.Spec) (*boardCell, error) {
	cell := spec.Build()
	c := &boardCell{spec: spec, cell: cell, asciiTier: isASCIIText(cell.Haystack)}
	for _, p := range cell.Patterns {
		c.asciiTier = c.asciiTier && isASCIIText(p)
	}
	c.all = board.Matches(cell.Haystack, cell.Patterns, false, -1)
	c.candidate = boardCandidate(spec.Op, cell)
	for _, name := range boardEntrants {
		e, supported, err := boardEntrant(name, c)
		if err != nil {
			return nil, fmt.Errorf("%s supports the cell but cannot compile it: %v", name, err)
		}
		if supported {
			c.entrants = append(c.entrants, e.op(name, spec.Op, cell))
		}
	}
	return c, nil
}

func boardCandidate(op string, cell board.Cell) boardRun {
	h := cell.Haystack
	m := casei.NewMatcher(cell.Patterns)
	switch op {
	case "indexfold":
		needle := cell.Patterns[0]
		return boardRun{"casei", func() { sink = casei.IndexFold(h, needle) }, func() []board.Hit {
			if i := casei.IndexFold(h, needle); i >= 0 {
				return []board.Hit{{Start: i, Pattern: 0, Width: -1}}
			}
			return nil
		}}
	case "each":
		return boardRun{"casei", func() {
			m.Each(h, func(casei.Match, int) bool { matcherSink++; return true })
		}, func() []board.Hit {
			var hits []board.Hit
			m.Each(h, func(match casei.Match, width int) bool {
				hits = append(hits, board.Hit{Start: match.Start, Pattern: match.Pattern, Width: width})
				return true
			})
			return hits
		}}
	default:
		return boardRun{"casei", func() { _, matcherFound = m.Find(h) }, func() []board.Hit {
			if match, ok := m.Find(h); ok {
				return []board.Hit{{Start: match.Start, Pattern: match.Pattern, Width: -1}}
			}
			return nil
		}}
	}
}

// boardEngine is an entrant compiled for one cell. find answers Find: the
// leftmost match, ties to the lowest pattern, with -1 for a pattern the engine
// does not report. each is the engine's own enumeration, when it has one that
// differs from restarting find after each match.
type boardEngine struct {
	find func(h string) (start, pattern int, ok bool)
	each func(h string, visit func(start, pattern, width int) bool)
}

// boardEntrant compiles one entrant for the cell when it supports it. The
// support rules are field.yaml's tiers and the process gates BenchmarkBar
// already applies. A supporting entrant that fails to compile is an error: it
// is never quietly left out of the field.
func boardEntrant(name string, c *boardCell) (boardEngine, bool, error) {
	patterns := c.cell.Patterns
	switch name {
	case "regexp":
		re := regexpAltFor(patterns)
		find := func(h string) (int, int, bool) {
			loc := re.FindStringIndex(h)
			if loc == nil {
				return 0, 0, false
			}
			return loc[0], -1, true
		}
		return boardEngine{find: find, each: func(h string, visit func(int, int, int) bool) { regexpEach(re, h, visit) }}, true, nil
	case "pcre2":
		var re *pcre2jit.Regex
		var err error
		if len(patterns) == 1 {
			re, err = pcre2jit.CompileLiteral(patterns[0])
		} else {
			re, err = pcre2jit.CompileAlternation(patterns)
		}
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find}, true, nil
	case "rure":
		var re *rure.Regex
		var err error
		if len(patterns) == 1 {
			re, err = rure.CompileLiteral(patterns[0])
		} else {
			re, err = rure.CompileAlternation(patterns)
		}
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find}, true, nil
	case "vectorscan":
		if bits, _ := expectedVectorscanBits(); bits == 0 {
			return boardEngine{}, false, nil
		}
		m, err := vectorscan.Compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: func(h string, visit func(int, int, int) bool) { m.Each(h, visit) }}, true, nil
	case "stringzilla":
		if !stringZillaAvailable {
			return boardEngine{}, false, nil
		}
		literals := make([]*stringzilla.Matcher, len(patterns))
		for i, p := range patterns {
			var err error
			if literals[i], err = stringzilla.CompileLiteral(p); err != nil {
				return boardEngine{}, true, err
			}
		}
		if len(patterns) == 1 {
			return boardEngine{find: func(h string) (int, int, bool) {
				i := literals[0].Index(h)
				return i, 0, i >= 0
			}}, true, nil
		}
		alternation, err := stringzilla.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		index := make([]func(string) int, len(literals))
		for i, m := range literals {
			index[i] = m.Index
		}
		return boardEngine{find: alternation.Find, each: literalsEach(index, patterns)}, true, nil
	case "veloz":
		if !c.asciiTier || len(patterns) != 1 || velozVectorBits() != 256 {
			return boardEngine{}, false, nil
		}
		needle := patterns[0]
		return boardEngine{find: func(h string) (int, int, bool) {
			i := veloz.IndexFold(h, needle)
			return i, 0, i >= 0
		}}, true, nil
	case "rustac":
		if !c.asciiTier {
			return boardEngine{}, false, nil
		}
		m, err := rustac.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find}, true, nil
	}
	panic("unknown board entrant " + name)
}

// op binds the engine to the cell's operation. IndexFold cells hold one
// pattern, so every entrant answers them with its Find. An engine without its
// own enumeration answers Each by restarting Find after each match, which is
// how its Find would be used to enumerate; the match width it does not report
// is the source span of the pattern's runes, because simple folding maps rune
// to rune.
func (e boardEngine) op(name, op string, cell board.Cell) boardRun {
	h := cell.Haystack
	each := e.each
	if each == nil {
		runes := make([]int, len(cell.Patterns))
		for i, p := range cell.Patterns {
			runes[i] = utf8.RuneCountInString(p)
		}
		find := e.find
		each = func(h string, visit func(int, int, int) bool) {
			for at := 0; at <= len(h); {
				start, pattern, ok := find(h[at:])
				if !ok {
					return
				}
				start += at
				width := runeSpan(h, start, runes[pattern])
				if !visit(start, pattern, width) {
					return
				}
				at = start + width
			}
		}
	}
	if op == "each" {
		return boardRun{name, func() {
			each(h, func(int, int, int) bool { matcherSink++; return true })
		}, func() []board.Hit {
			var hits []board.Hit
			each(h, func(start, pattern, width int) bool {
				hits = append(hits, board.Hit{Start: start, Pattern: pattern, Width: width})
				return true
			})
			return hits
		}}
	}
	return boardRun{name, func() { _, _, matcherFound = e.find(h) }, func() []board.Hit {
		if start, pattern, ok := e.find(h); ok {
			return []board.Hit{{Start: start, Pattern: pattern, Width: -1}}
		}
		return nil
	}}
}

// regexpEach enumerates with FindStringIndex, which reports the match end but
// not the alternative that matched.
func regexpEach(re *regexp.Regexp, h string, visit func(start, pattern, width int) bool) {
	for at := 0; at <= len(h); {
		loc := re.FindStringIndex(h[at:])
		if loc == nil || !visit(at+loc[0], -1, loc[1]-loc[0]) {
			return
		}
		at += loc[1]
	}
}

// literalsEach enumerates a pattern set with an engine that searches one
// literal at a time. It keeps each literal's next occurrence and searches a
// literal again only once the enumeration has passed that occurrence, so a
// rare literal is not rescanned after every match of a common one.
func literalsEach(index []func(string) int, patterns []string) func(string, func(int, int, int) bool) {
	runes := make([]int, len(patterns))
	for i, p := range patterns {
		runes[i] = utf8.RuneCountInString(p)
	}
	next := make([]int, len(index))
	return func(h string, visit func(start, pattern, width int) bool) {
		search := func(i, at int) {
			if j := index[i](h[at:]); j >= 0 {
				next[i] = at + j
			} else {
				next[i] = -1
			}
		}
		for i := range next {
			search(i, 0)
		}
		for at := 0; ; {
			best := -1
			for i := range next {
				if next[i] >= 0 && next[i] < at {
					search(i, at)
				}
				if next[i] >= 0 && (best < 0 || next[i] < next[best]) {
					best = i
				}
			}
			if best < 0 {
				return
			}
			start := next[best]
			width := runeSpan(h, start, runes[best])
			if !visit(start, best, width) {
				return
			}
			at = start + width
		}
	}
}

// runeSpan returns the bytes taken by n runes of h from start.
func runeSpan(h string, start, n int) int {
	at := start
	for range n {
		_, size := utf8.DecodeRuneInString(h[at:])
		at += size
	}
	return at - start
}

func isASCIIText(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// boardHost names the CPU for the verifier's host rule: vendor, family, and
// model from /proc/cpuinfo, and the AVX-512 features this process can use.
// The widths after them are diagnostics.
func boardHost() string {
	vendor, family, model := "unknown", "0", "0"
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch key, value = strings.TrimSpace(key), strings.TrimSpace(value); {
			case key == "vendor_id" && vendor == "unknown":
				vendor = value
			case key == "cpu family" && family == "0":
				family = value
			case key == "model" && model == "0":
				model = value
			}
		}
	}
	vectorscanBits, _ := expectedVectorscanBits()
	return fmt.Sprintf("vendor=%s family=%s model=%s avx512f=%d avx512bw=%d avx512vbmi=%d "+
		"casei_vector_bits=%d vectorscan_vector_bits=%d stringzilla_vector_bits=%d veloz_vector_bits=%d",
		vendor, family, model,
		int(boolMetric(cpu.X86.HasAVX512F)), int(boolMetric(cpu.X86.HasAVX512BW)), int(boolMetric(cpu.X86.HasAVX512VBMI)),
		casei.RuntimeVectorBits(), vectorscanBits, stringzilla.VectorBits(), velozVectorBits())
}

// TestBoardEntrantsAgree builds the small cells of the pinned board and holds
// casei to the oracle, as BenchmarkBoard does before it times a cell. It logs
// each entrant that disagrees; the board reports those as wrong, not timed. It keeps the board's wiring under CI's arena job,
// which builds the native field but does not run benchmarks.
func TestBoardEntrantsAgree(t *testing.T) {
	for _, spec := range board.Cells(board.Seed) {
		if spec.Sensitive || spec.Size > 256<<10 {
			continue
		}
		c, err := prepareBoardCell(spec)
		if err == nil {
			err = c.check()
		}
		if err != nil {
			t.Fatalf("cell %s: %v", spec.Name(), err)
		}
		for _, w := range c.wrong {
			t.Logf("cell %s: %v", spec.Name(), w)
		}
	}
}
