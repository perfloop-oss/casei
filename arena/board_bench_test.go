package arena_test

// The generality board. BenchmarkBoard times the pinned cells of package
// board and pairs casei with every pinned entrant that supports each cell,
// using the bar's pairing (pairedRatio) and adapters. scripts/verify_board.py
// applies the board's rules to the transcript.

import (
	"fmt"
	"math/rand/v2"
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

// boardEntrants names every field entrant that can count on a board cell.
// Diagnostic entrants never count, so they are not timed.
var boardEntrants = []string{"regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac"}

// BenchmarkBoard reports, per cell, each supporting entrant's paired ratio:
// casei's time over the entrant's. Run it with -benchtime 1x; the pairing
// does the timing.
func BenchmarkBoard(b *testing.B) {
	cells, pinned := board.Cells(), board.Pinned()
	fmt.Printf("board: seed=%#x cells=%d %s\n", board.Seed, len(cells), boardHost())
	for _, spec := range cells {
		b.Run(spec.Name(), func(b *testing.B) {
			metrics := map[string]float64{"candidate_supported": 0}
			for _, name := range boardEntrants {
				metrics[name+"_active"], metrics[name+"_x"] = 0, 0
			}
			defer func() {
				for unit, value := range metrics {
					b.ReportMetric(value, unit)
				}
			}()
			if spec.Sensitive {
				// casei has no case-sensitive API: the cell reports it
				// unsupported, which fails the board until casei answers it.
				for b.Loop() {
				}
				return
			}
			c, err := prepareBoardCell(spec, pinned[spec.Name()][1])
			if err != nil {
				b.Fatalf("cell %s: %v", spec.Name(), err)
			}
			for _, e := range c.entrants {
				metrics[e.name+"_active"], metrics[e.name+"_x"] = 1, pairedRatio(c.candidate.run, e.run)
			}
			for b.Loop() {
				c.candidate.run()
			}
			metrics["candidate_supported"] = 1
		})
	}
}

// boardCell is a built cell with casei and every supporting entrant bound to
// the cell's operation.
type boardCell struct {
	candidate boardRun
	entrants  []boardRun
}

// boardRun is one implementation's operation on the cell. scan reports each
// hit; a Pattern or Width of -1 is one the implementation does not report.
type boardRun struct {
	name string
	scan func(visit func(board.Hit) bool)
}

func (r boardRun) run() { r.scan(func(board.Hit) bool { matcherSink++; return true }) }

// prepareBoardCell builds the cell, checks it against its pinned digest, and
// holds casei and every supporting entrant to the oracle before any timing, so
// no ratio comes from a different cell or a wrong answer. The board plants
// only fold mates every entrant handles (board.HazardMates), so any wrong
// answer is a casei, board, or adapter bug and fails the cell.
func prepareBoardCell(spec board.Spec, digest string) (*boardCell, error) {
	cell := spec.Build()
	if got := cell.Digest(); got != digest {
		return nil, fmt.Errorf("built digest %s, cells.txt has %q", got, digest)
	}
	want := board.Matches(cell.Haystack, cell.Patterns, false)
	if spec.Op != "each" && len(want) > 1 {
		want = want[:1]
	}
	c := &boardCell{candidate: boardCandidate(spec.Op, cell)}
	runs := []boardRun{c.candidate}
	for _, name := range boardEntrants {
		e, supported, err := boardEntrant(name, cell.Patterns, cell.ASCII)
		if err != nil {
			return nil, fmt.Errorf("%s supports the cell but cannot compile it: %v", name, err)
		}
		if supported {
			c.entrants = append(c.entrants, e.bind(name, spec.Op, cell.Haystack))
			runs = append(runs, c.entrants[len(c.entrants)-1])
		}
	}
	for _, r := range runs {
		var got []board.Hit
		r.scan(func(h board.Hit) bool { got = append(got, h); return true })
		if len(got) != len(want) {
			return nil, fmt.Errorf("%s gives %d matches, oracle %d", r.name, len(got), len(want))
		}
		for i, h := range got {
			if w := want[i]; h.Start != w.Start || h.Pattern >= 0 && h.Pattern != w.Pattern || h.Width >= 0 && h.Width != w.Width {
				return nil, fmt.Errorf("%s match %d is %+v, oracle %+v", r.name, i, h, w)
			}
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

// boardEngine is an entrant compiled for one pattern set: find answers Find,
// and each is the engine's own enumeration of consecutive non-overlapping
// matches, with the matching loop on the engine's side.
type boardEngine struct {
	find func(h string) (start, pattern int, ok bool)
	each func(h string, yield func(start, pattern, width int) bool) bool
}

// boardEntrant compiles one entrant when it supports the pattern set. The
// support rules are field.yaml's tiers and the process gates BenchmarkBar
// applies. A supporting entrant that fails to compile is an error.
func boardEntrant(name string, patterns []string, asciiTier bool) (boardEngine, bool, error) {
	single := len(patterns) == 1
	switch name {
	case "regexp":
		// regexp's iterator, FindAllStringIndex, reports spans but not the
		// alternative that matched.
		re := regexpAltFor(patterns)
		return boardEngine{
			find: func(h string) (int, int, bool) {
				if loc := re.FindStringIndex(h); loc != nil {
					return loc[0], -1, true
				}
				return 0, 0, false
			},
			each: func(h string, yield func(int, int, int) bool) bool {
				for _, loc := range re.FindAllStringIndex(h, -1) {
					if !yield(loc[0], -1, loc[1]-loc[0]) {
						return false
					}
				}
				return true
			},
		}, true, nil
	case "pcre2":
		compile := pcre2jit.CompileAlternation
		if single {
			compile = func(p []string) (*pcre2jit.Regex, error) { return pcre2jit.CompileLiteral(p[0]) }
		}
		re, err := compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find, each: re.Each}, true, nil
	case "rure":
		compile := rure.CompileAlternation
		if single {
			compile = func(p []string) (*rure.Regex, error) { return rure.CompileLiteral(p[0]) }
		}
		re, err := compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find, each: re.Each}, true, nil
	case "vectorscan":
		if bits, _ := expectedVectorscanBits(); bits == 0 {
			return boardEngine{}, false, nil
		}
		m, err := vectorscan.Compile(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: m.Each}, true, nil
	case "stringzilla":
		if !stringZillaAvailable {
			return boardEngine{}, false, nil
		}
		m, err := stringzilla.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: m.Each}, true, nil
	case "veloz":
		if !asciiTier || !single || velozVectorBits() != 256 {
			return boardEngine{}, false, nil
		}
		// veloz has no iterator, so Each restarts IndexFold after each match.
		needle, runes := patterns[0], utf8.RuneCountInString(patterns[0])
		find := func(h string) (int, int, bool) { i := veloz.IndexFold(h, needle); return i, 0, i >= 0 }
		return boardEngine{find: find, each: func(h string, yield func(int, int, int) bool) bool {
			for at := 0; ; {
				i, _, ok := find(h[at:])
				if !ok {
					return true
				}
				end := at + i
				for range runes {
					_, size := utf8.DecodeRuneInString(h[end:])
					end += size
				}
				if !yield(at+i, 0, end-at-i) {
					return false
				}
				at = end
			}
		}}, true, nil
	case "rustac":
		if !asciiTier {
			return boardEngine{}, false, nil
		}
		m, err := rustac.CompileAlternation(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: m.Each}, true, nil
	}
	panic("unknown board entrant " + name)
}

// bind fixes the engine to the cell's operation. IndexFold cells hold one
// pattern, so entrants answer them with Find.
func (e boardEngine) bind(name, op, h string) boardRun {
	if op == "each" {
		return boardRun{name, func(visit func(board.Hit) bool) {
			e.each(h, func(start, pattern, width int) bool {
				return visit(board.Hit{Start: start, Pattern: pattern, Width: width})
			})
		}}
	}
	return boardRun{name, func(visit func(board.Hit) bool) {
		if start, pattern, ok := e.find(h); ok {
			visit(board.Hit{Start: start, Pattern: pattern, Width: -1})
		}
	}}
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

// TestBoardEntrantsAgree prepares every pinned cell up to 256 KiB as
// BenchmarkBoard does before timing it: digest, then casei and every
// supporting entrant against the oracle. It keeps the wiring under CI.
func TestBoardEntrantsAgree(t *testing.T) {
	pinned := board.Pinned()
	for _, spec := range board.Cells() {
		if spec.Sensitive || spec.Size > 256<<10 {
			continue
		}
		if _, err := prepareBoardCell(spec, pinned[spec.Name()][1]); err != nil {
			t.Errorf("cell %s: %v", spec.Name(), err)
		}
	}
}

// TestBoardHazardMatesAgree holds every entrant that searches UTF-8 to
// board.HazardMates: for every rune with a fold mate the board may plant, each
// entrant finds every such mate.
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
				if !supported {
					break
				}
				if start, _, _ := e.find("\n" + string(mate) + "\n"); start != 1 {
					t.Errorf("%s does not fold %q (%U) with %q (%U)", name, r, r, mate, mate)
				}
			}
		}
	}
}

// TestMatchesAgreesWithOracle holds board.Matches to the arena's oracle:
// repeated leftmost, lowest-pattern refFind from the end of each match.
func TestMatchesAgreesWithOracle(t *testing.T) {
	alphabet := []rune("kKKsSſσςΣaAbB ßẞåÅÅµμΜжЖоᲂ𐐀𐐨中")
	for seed := range uint64(3000) {
		rng := rand.New(rand.NewPCG(seed, 1))
		random := func(n int) string {
			var b strings.Builder
			for range n {
				b.WriteRune(alphabet[rng.IntN(len(alphabet))])
			}
			return b.String()
		}
		haystack, patterns := random(rng.IntN(40)), make([]string, 1+rng.IntN(4))
		for i := range patterns {
			patterns[i] = random(1 + rng.IntN(3))
		}
		var want []board.Hit
		for at := 0; at <= len(haystack); {
			m, ok := refFind(haystack[at:], patterns)
			if !ok {
				break
			}
			end := at + m.Start
			for range utf8.RuneCountInString(patterns[m.Pattern]) {
				_, size := utf8.DecodeRuneInString(haystack[end:])
				end += size
			}
			want = append(want, board.Hit{Start: at + m.Start, Pattern: m.Pattern, Width: end - at - m.Start})
			at = end
		}
		if got := board.Matches(haystack, patterns, false); !slices.Equal(got, want) {
			t.Fatalf("Matches(%q, %q) = %v, oracle %v", haystack, patterns, got, want)
		}
	}
}
