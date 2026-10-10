package arena_test

// The generality board. BenchmarkBoard times the pinned cells of package
// board and pairs casei with every pinned entrant that supports each cell,
// using the bar's pairing (pairedRatio) and adapters. scripts/verify_board.py
// applies the board's rules to the transcript.

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"
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
	"draw BenchmarkBoard from another seed, for exploration; acceptance uses board.Seed")

// boardEntrants names every field entrant that can count on a board cell.
// Diagnostic entrants never count, so they are not timed.
var boardEntrants = []string{"regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac"}

// BenchmarkBoard reports, per cell, x_vs_best and each entrant's paired ratio.
// Run it with -benchtime 1x: the pairing does the timing.
func BenchmarkBoard(b *testing.B) {
	cells := board.Cells(*boardSeed)
	fmt.Printf("board: seed=%#x cells=%d %s\n", *boardSeed, len(cells), boardHost())
	for _, spec := range cells {
		b.Run(spec.Name(), func(b *testing.B) { runBoardCell(b, spec) })
	}
}

func runBoardCell(b *testing.B, spec board.Spec) {
	metrics := map[string]float64{"x_vs_best": 0, "candidate_supported": 0, "ascii_tier": 0, "matches": 0}
	for _, name := range boardEntrants {
		metrics[name+"_active"], metrics[name+"_x"], metrics[name+"_wrong"] = 0, 0, 0
	}
	defer func() {
		for unit, value := range metrics {
			b.ReportMetric(value, unit)
		}
	}()
	if spec.Sensitive {
		// casei has no case-sensitive API: the cell reports it unsupported,
		// which fails the board until casei answers it.
		for b.Loop() {
		}
		return
	}
	c, err := prepareBoardCell(spec)
	if err == nil {
		err = c.check()
	}
	if err != nil {
		b.Fatalf("cell %s: %v", spec.Name(), err)
	}
	for _, w := range c.wrong {
		metrics[w.name+"_wrong"] = 1
		b.Logf("%s answered wrongly (%s); the verifier fails the run", w.name, w.detail)
	}
	for _, e := range c.entrants {
		ratio := pairedRatio(c.candidate.run, e.run)
		metrics[e.name+"_active"], metrics[e.name+"_x"] = 1, ratio
		metrics["x_vs_best"] = max(metrics["x_vs_best"], ratio)
	}
	for b.Loop() {
		c.candidate.run()
	}
	metrics["candidate_supported"] = 1
	metrics["ascii_tier"] = boolMetric(c.asciiTier)
	metrics["matches"] = float64(len(c.want))
}

// boardCell is a built cell with casei and every supporting entrant bound to
// the cell's operation, and the oracle's answer.
type boardCell struct {
	spec      board.Spec
	cell      board.Cell
	asciiTier bool        // patterns and haystack are ASCII
	want      []board.Hit // the oracle's Each answer
	candidate boardRun
	entrants  []boardRun   // supporting entrants that agree with the oracle
	wrong     []boardWrong // supporting entrants that do not
}

// boardRun is one implementation's operation on the cell. scan reports each
// hit; a Pattern or Width of -1 is one the implementation does not report.
type boardRun struct {
	name string
	scan func(visit func(board.Hit) bool)
}

func (r boardRun) run() { r.scan(func(board.Hit) bool { matcherSink++; return true }) }

type boardWrong struct{ name, detail string }

// check holds casei and every entrant to the oracle before any timing, so no
// ratio comes from a wrong answer. A wrong casei answer fails the cell. A
// wrong entrant is reported as <name>_wrong and not timed, and the verifier
// fails the run: cells plant only fold mates every entrant handles
// (board.HazardMates), so a wrong answer is a board or adapter bug.
func (c *boardCell) check() error {
	if w := c.disagreement(c.candidate); w != nil {
		return fmt.Errorf("casei %s", w.detail)
	}
	kept := c.entrants[:0]
	for _, e := range c.entrants {
		if w := c.disagreement(e); w != nil {
			c.wrong = append(c.wrong, *w)
		} else {
			kept = append(kept, e)
		}
	}
	c.entrants = kept
	return nil
}

func (c *boardCell) disagreement(r boardRun) *boardWrong {
	want := c.want
	if c.spec.Op != "each" && len(want) > 1 {
		want = want[:1]
	}
	var got []board.Hit
	r.scan(func(h board.Hit) bool { got = append(got, h); return true })
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
	c.want = board.Matches(cell.Haystack, cell.Patterns, false)
	c.candidate = boardCandidate(spec.Op, cell)
	for _, name := range boardEntrants {
		e, supported, err := boardEntrant(name, cell.Patterns, c.asciiTier)
		if err != nil {
			return nil, fmt.Errorf("%s supports the cell but cannot compile it: %v", name, err)
		}
		if supported {
			c.entrants = append(c.entrants, e.bind(name, spec.Op, cell))
		}
	}
	return c, nil
}

func boardCandidate(op string, cell board.Cell) boardRun {
	h := cell.Haystack
	m := casei.NewMatcher(cell.Patterns)
	switch op {
	case "indexfold":
		return boardRun{"casei", func(visit func(board.Hit) bool) {
			if i := casei.IndexFold(h, cell.Patterns[0]); i >= 0 {
				visit(board.Hit{Start: i, Pattern: 0, Width: -1})
			}
		}}
	case "each":
		return boardRun{"casei", func(visit func(board.Hit) bool) {
			m.Each(h, func(match casei.Match, width int) bool {
				return visit(board.Hit{Start: match.Start, Pattern: match.Pattern, Width: width})
			})
		}}
	}
	return boardRun{"casei", func(visit func(board.Hit) bool) {
		if match, ok := m.Find(h); ok {
			visit(board.Hit{Start: match.Start, Pattern: match.Pattern, Width: -1})
		}
	}}
}

// boardEngine is an entrant compiled for one pattern set. find answers Find:
// the leftmost match, ties to the lowest pattern, with -1 for what the engine
// does not report. each is the engine's own enumeration, when restarting find
// after each match would not be how the engine enumerates.
type boardEngine struct {
	find func(h string) board.Hit // Start < 0: no match
	each func(h string, visit func(board.Hit) bool)
}

func hit(start, pattern int, ok bool) board.Hit {
	if !ok {
		return board.Hit{Start: -1}
	}
	return board.Hit{Start: start, Pattern: pattern, Width: -1}
}

func index(i int) board.Hit { return hit(i, 0, i >= 0) }

// boardEntrant compiles one entrant when it supports the pattern set. The
// support rules are field.yaml's tiers and the process gates BenchmarkBar
// applies. A supporting entrant that fails to compile is an error, never a
// silent absence.
func boardEntrant(name string, patterns []string, asciiTier bool) (boardEngine, bool, error) {
	single := len(patterns) == 1
	switch name {
	case "regexp":
		re := regexpAltFor(patterns)
		return boardEngine{find: func(h string) board.Hit {
			loc := re.FindStringIndex(h)
			if loc == nil {
				return board.Hit{Start: -1}
			}
			return board.Hit{Start: loc[0], Pattern: -1, Width: loc[1] - loc[0]}
		}}, true, nil
	case "pcre2":
		compile := pcre2jit.CompileAlternation
		if single {
			compile = func(p []string) (*pcre2jit.Regex, error) { return pcre2jit.CompileLiteral(p[0]) }
		}
		re, err := compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: func(h string) board.Hit { return hit(re.Find(h)) }}, true, nil
	case "rure":
		compile := rure.CompileAlternation
		if single {
			compile = func(p []string) (*rure.Regex, error) { return rure.CompileLiteral(p[0]) }
		}
		re, err := compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: func(h string) board.Hit { return hit(re.Find(h)) }}, true, nil
	case "vectorscan":
		if bits, _ := expectedVectorscanBits(); bits == 0 {
			return boardEngine{}, false, nil
		}
		m, err := vectorscan.Compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		// Vectorscan reports matches unordered and Find scans the whole input,
		// so Each uses its one-scan enumeration.
		return boardEngine{
			find: func(h string) board.Hit { return hit(m.Find(h)) },
			each: func(h string, visit func(board.Hit) bool) {
				m.Each(h, func(start, pattern, width int) bool {
					return visit(board.Hit{Start: start, Pattern: pattern, Width: width})
				})
			},
		}, true, nil
	case "stringzilla":
		if !stringZillaAvailable {
			return boardEngine{}, false, nil
		}
		literals := make([]func(string) int, len(patterns))
		for i, p := range patterns {
			m, err := stringzilla.CompileLiteral(p)
			if err != nil {
				return boardEngine{}, true, err
			}
			literals[i] = m.Index
		}
		if single {
			return boardEngine{find: func(h string) board.Hit { return index(literals[0](h)) }}, true, nil
		}
		alternation, err := stringzilla.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{
			find: func(h string) board.Hit { return hit(alternation.Find(h)) },
			each: literalsEach(literals, patterns),
		}, true, nil
	case "veloz":
		if !asciiTier || !single || velozVectorBits() != 256 {
			return boardEngine{}, false, nil
		}
		return boardEngine{find: func(h string) board.Hit { return index(veloz.IndexFold(h, patterns[0])) }}, true, nil
	case "rustac":
		if !asciiTier {
			return boardEngine{}, false, nil
		}
		m, err := rustac.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: func(h string) board.Hit { return hit(m.Find(h)) }}, true, nil
	}
	panic("unknown board entrant " + name)
}

// bind fixes the engine to the cell's operation. IndexFold cells hold one
// pattern, so entrants answer them with Find. Without its own enumeration an
// engine answers Each by restarting Find after each match; a width it does not
// report is the span of the pattern's runes, since simple folding maps rune to
// rune.
func (e boardEngine) bind(name, op string, cell board.Cell) boardRun {
	h := cell.Haystack
	if op != "each" {
		return boardRun{name, func(visit func(board.Hit) bool) {
			if found := e.find(h); found.Start >= 0 {
				visit(found)
			}
		}}
	}
	if e.each != nil {
		return boardRun{name, func(visit func(board.Hit) bool) { e.each(h, visit) }}
	}
	runes := patternRunes(cell.Patterns)
	return boardRun{name, func(visit func(board.Hit) bool) {
		for at := 0; at <= len(h); {
			found := e.find(h[at:])
			if found.Start < 0 {
				return
			}
			found.Start += at
			if found.Width < 0 {
				found.Width = runeSpan(h, found.Start, runes[found.Pattern])
			}
			if !visit(found) {
				return
			}
			at = found.Start + found.Width
		}
	}}
}

// literalsEach enumerates a pattern set with an engine that searches one
// literal at a time. It keeps each literal's next occurrence and searches a
// literal again only once the enumeration has passed it, so a rare literal is
// not rescanned after every match of a common one.
func literalsEach(index []func(string) int, patterns []string) func(string, func(board.Hit) bool) {
	runes := patternRunes(patterns)
	next := make([]int, len(index))
	return func(h string, visit func(board.Hit) bool) {
		search := func(i, at int) {
			next[i] = -1
			if j := index[i](h[at:]); j >= 0 {
				next[i] = at + j
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
			width := runeSpan(h, next[best], runes[best])
			if !visit(board.Hit{Start: next[best], Pattern: best, Width: width}) {
				return
			}
			at = next[best] + width
		}
	}
}

func patternRunes(patterns []string) []int {
	runes := make([]int, len(patterns))
	for i, p := range patterns {
		runes[i] = utf8.RuneCountInString(p)
	}
	return runes
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
func boardHost() string {
	fields := map[string]string{"vendor_id": "unknown", "cpu family": "0", "model": "0"}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, _ := strings.Cut(line, ":")
			if key = strings.TrimSpace(key); fields[key] == "unknown" || fields[key] == "0" {
				fields[key] = strings.TrimSpace(value)
			}
		}
	}
	return fmt.Sprintf("vendor=%s family=%s model=%s avx512f=%d avx512bw=%d avx512vbmi=%d",
		fields["vendor_id"], fields["cpu family"], fields["model"],
		int(boolMetric(cpu.X86.HasAVX512F)), int(boolMetric(cpu.X86.HasAVX512BW)), int(boolMetric(cpu.X86.HasAVX512VBMI)))
}

// TestBoardEntrantsAgree holds casei and every supporting entrant to the
// oracle on the pinned board's cells up to 256 KiB, as BenchmarkBoard does
// before it times a cell. It keeps the wiring under CI's arena job.
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
			t.Errorf("cell %s: %s %s", spec.Name(), w.name, w.detail)
		}
	}
}

// TestBoardHazardMatesAgree holds every entrant that searches UTF-8 to
// board.HazardMates: each fold mate the board may plant, of every rune that
// has one, is found by every entrant.
func TestBoardHazardMatesAgree(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		mates := board.HazardMates(r)
		if len(mates) == 1 || !slices.Contains(board.HazardMates(board.FoldKey(r)), r) {
			continue // nothing to plant, or r is itself never planted
		}
		for _, name := range boardEntrants {
			e, supported, err := boardEntrant(name, []string{string(r)}, false)
			if err != nil {
				t.Fatalf("%s %q: %v", name, r, err)
			}
			for _, mate := range mates {
				if supported && e.find("\n"+string(mate)+"\n").Start != 1 {
					t.Errorf("%s does not fold %q (%U) with %q (%U)", name, r, r, mate, mate)
				}
			}
		}
	}
}
