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
// casei's time over the entrant's. Mode flags identify direct and translated
// Veloz and RustAC routes. Run it with -benchtime 1x; the pairing does the timing.
func BenchmarkBoard(b *testing.B) {
	cells, pinned := board.Cells(), board.Pinned()
	fmt.Printf("board: seed=%#x cells=%d %s\n", board.Seed, len(cells), boardHost())
	for _, spec := range cells {
		b.Run(spec.Name(), func(b *testing.B) {
			metrics := map[string]float64{
				"candidate_supported":  0,
				"rustac_direct_active": 0,
				"rustac_view_active":   0,
			}
			for _, name := range boardEntrants {
				metrics[name+"_active"], metrics[name+"_x"] = 0, 0
			}
			metrics["veloz_direct_active"], metrics["veloz_view_active"] = 0, 0
			defer func() {
				for unit, value := range metrics {
					b.ReportMetric(value, unit)
				}
			}()
			c, err := prepareBoardCell(spec, pinned[spec.Name()][1])
			if err != nil {
				b.Fatalf("cell %s: %v", spec.Name(), err)
			}
			defer c.close()
			for _, e := range c.entrants {
				metrics[e.name+"_active"], metrics[e.name+"_x"] = 1, pairedRatio(c.candidate.run, e.run)
				if e.name == "veloz" && c.velozMode != "" {
					metrics["veloz_"+c.velozMode+"_active"] = 1
				}
				if e.name == "rustac" && c.rustacMode != "" {
					metrics["rustac_"+c.rustacMode+"_active"] = 1
				}
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
	candidate  boardRun
	entrants   []boardRun
	releases   []func()
	rustacMode string
	velozMode  string
}

func (c *boardCell) close() {
	if c == nil {
		return
	}
	for i, release := range c.releases {
		if release != nil {
			release()
			c.releases[i] = nil
		}
	}
}

// boardRun is one implementation's timed operation on the cell. scan reports
// each hit; a Pattern or Width of -1 is one the implementation does not report.
type boardRun struct {
	name string
	scan func(visit func(board.Hit) bool)
}

func (r boardRun) run() { r.scan(func(board.Hit) bool { matcherSink++; return true }) }

// boardBinding carries strict agreement metadata and error-aware preflight.
// Keep it out of boardRun, whose value receiver is on the timed path.
type boardBinding struct {
	run           boardRun
	scanErr       func(visit func(board.Hit) bool) error
	strict        bool
	requireWidths bool
}

func (r boardBinding) agree(want []board.Hit) error {
	var got []board.Hit
	visit := func(h board.Hit) bool { got = append(got, h); return true }
	if r.scanErr != nil {
		if err := r.scanErr(visit); err != nil {
			return fmt.Errorf("%s scan: %w", r.run.name, err)
		}
	} else {
		r.run.scan(visit)
	}
	if len(got) != len(want) {
		return fmt.Errorf("%s gives %d matches, oracle %d", r.run.name, len(got), len(want))
	}
	for i, h := range got {
		w := want[i]
		patternMismatch := r.strict && h.Pattern != w.Pattern || !r.strict && h.Pattern >= 0 && h.Pattern != w.Pattern
		widthMismatch := r.requireWidths && h.Width < 0 || h.Width >= 0 && h.Width != w.Width
		if h.Start != w.Start || patternMismatch || widthMismatch {
			return fmt.Errorf("%s match %d is %+v, oracle %+v", r.run.name, i, h, w)
		}
	}
	return nil
}

// prepareBoardCell builds the cell, checks it against its pinned digest, and
// holds casei and every supporting entrant to the oracle before any timing, so
// no ratio comes from a different cell or a wrong answer. A case-sensitive
// cell runs every implementation in its exact mode against the raw-byte
// oracle. The board plants only fold mates every entrant handles
// (board.HazardMates), so any wrong answer is a casei, board, or adapter bug
// and fails the cell.
func prepareBoardCell(spec board.Spec, digest string) (*boardCell, error) {
	cell := spec.Build()
	if got := cell.Digest(); got != digest {
		return nil, fmt.Errorf("built digest %s, cells.txt has %q", got, digest)
	}
	want := board.Matches(cell.Haystack, cell.Patterns, spec.Sensitive)
	if spec.Op != "each" && len(want) > 1 {
		want = want[:1]
	}
	c := &boardCell{candidate: boardCandidate(spec, cell)}
	success := false
	defer func() {
		if !success {
			c.close()
		}
	}()
	runs := []boardBinding{{run: c.candidate, requireWidths: spec.Op == "each"}}
	for _, name := range boardEntrants {
		var e boardEngine
		var supported bool
		var err error
		e, supported, err = boardEntrant(name, cell.Patterns, cell.ASCII, spec.Sensitive)
		if err != nil {
			return nil, fmt.Errorf("%s supports the cell but cannot compile it: %w", name, err)
		}
		if name == "rustac" {
			c.rustacMode = e.mode
		}
		if e.release != nil {
			c.releases = append(c.releases, e.release)
			e.release = nil
		}
		if supported {
			if name == "veloz" {
				c.velozMode = e.mode
			}
			binding := e.bind(name, spec.Op, cell.Haystack)
			c.entrants = append(c.entrants, binding.run)
			runs = append(runs, binding)
		}
	}
	for _, r := range runs {
		if err := r.agree(want); err != nil {
			return nil, err
		}
	}
	success = true
	return c, nil
}

// boardCandidate binds casei to the cell's operation: NewExactMatcher on a
// case-sensitive cell, NewMatcher otherwise. IndexFold is folded by contract,
// so the generator draws no case-sensitive IndexFold cell.
func boardCandidate(spec board.Spec, cell board.Cell) boardRun {
	h := cell.Haystack
	var m *casei.Matcher
	if spec.Sensitive {
		m = casei.NewExactMatcher(cell.Patterns)
	} else {
		m = casei.NewMatcher(cell.Patterns)
	}
	switch spec.Op {
	case "indexfold":
		return boardRun{name: "casei", scan: func(visit func(board.Hit) bool) {
			if i := casei.IndexFold(h, cell.Patterns[0]); i >= 0 {
				visit(board.Hit{Start: i, Pattern: 0, Width: -1})
			}
		}}
	case "each":
		return boardRun{name: "casei", scan: func(visit func(board.Hit) bool) {
			m.Each(h, func(match casei.Match, width int) bool {
				return visit(board.Hit{Start: match.Start, Pattern: match.Pattern, Width: width})
			})
		}}
	}
	return boardRun{name: "casei", scan: func(visit func(board.Hit) bool) {
		if match, ok := m.Find(h); ok {
			visit(board.Hit{Start: match.Start, Pattern: match.Pattern, Width: -1})
		}
	}}
}

// boardEngine is an entrant compiled for one pattern set: find answers Find,
// and each is the engine's own enumeration of consecutive non-overlapping
// matches, with the matching loop on the engine's side. findHit and
// findWithWidth preserve projected spans from translated adapters.
type boardEngine struct {
	mode          string
	find          func(h string) (start, pattern int, ok bool)
	findHit       func(h string) (board.Hit, bool)
	each          func(h string, yield func(start, pattern, width int) bool) bool
	findWithWidth func(h string) (start, pattern, width int, ok bool, err error)
	eachErr       func(h string, yield func(start, pattern, width int) bool) (complete bool, err error)
	strict        bool
	release       func()
}

func rustacViewEngine(view *board.RustACView) boardEngine {
	return boardEngine{
		mode:          "view",
		findWithWidth: view.Find,
		eachErr:       view.Each,
		strict:        true,
		release:       view.Close,
	}
}

func (e *boardEngine) close() {
	if e != nil && e.release != nil {
		e.release()
		e.release = nil
	}
}

// boardEntrant compiles one entrant when it supports the pattern set, in its
// native case-sensitive mode when exact. The support rules are field.yaml's
// tier/board_tier and exact_tier plus the process gates this board applies. A
// supporting entrant that fails to compile is an error.
func boardEntrant(name string, patterns []string, asciiTier, exact bool) (boardEngine, bool, error) {
	single := len(patterns) == 1
	switch name {
	case "regexp":
		// regexp's iterator, FindAllStringIndex, reports spans but not the
		// alternative that matched.
		re := regexpAltFor(patterns, exact)
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
			compile = func(p []string, exact bool) (*pcre2jit.Regex, error) { return pcre2jit.CompileLiteral(p[0], exact) }
		}
		re, err := compile(patterns, exact)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find, each: re.Each}, true, nil
	case "rure":
		compile := rure.CompileAlternation
		if single {
			compile = func(p []string, exact bool) (*rure.Regex, error) { return rure.CompileLiteral(p[0], exact) }
		}
		re, err := compile(patterns, exact)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: re.Find, each: re.Each}, true, nil
	case "vectorscan":
		if bits, _ := expectedVectorscanBits(); bits == 0 {
			return boardEngine{}, false, nil
		}
		m, err := vectorscan.Compile(patterns, exact)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: m.Each}, true, nil
	case "stringzilla":
		if !stringZillaAvailable {
			return boardEngine{}, false, nil
		}
		m, err := stringzilla.CompileAlternation(patterns, exact)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{find: m.Find, each: m.Each}, true, nil
	case "veloz":
		// Veloz's weaker fallback is not this field entrant.
		if exact || !board.VelozAvailable() {
			return boardEngine{}, false, nil
		}
		if asciiTier && single {
			// Keep the existing direct ASCII-singleton binding.
			needle, runes := patterns[0], utf8.RuneCountInString(patterns[0])
			find := func(h string) (int, int, bool) {
				start := veloz.IndexFold(h, needle)
				return start, 0, start >= 0
			}
			return boardEngine{mode: "direct", find: find, strict: true, each: func(h string, yield func(int, int, int) bool) bool {
				for at := 0; ; {
					start, _, ok := find(h[at:])
					if !ok {
						return true
					}
					end := at + start
					for range runes {
						_, size := utf8.DecodeRuneInString(h[end:])
						end += size
					}
					if !yield(at+start, 0, end-at-start) {
						return false
					}
					at = end
				}
			}}, true, nil
		}
		view, err := board.NewVelozView(patterns)
		if err != nil {
			return boardEngine{}, true, err
		}
		find := func(h string) (int, int, bool) {
			hit, ok := view.Find(h)
			return hit.Start, hit.Pattern, ok
		}
		return boardEngine{mode: "view", find: find, findHit: view.Find, each: func(h string, yield func(int, int, int) bool) bool {
			return view.Each(h, func(hit board.Hit) bool {
				return yield(hit.Start, hit.Pattern, hit.Width)
			})
		}, strict: true}, true, nil
	case "rustac":
		// ASCII case insensitivity is exact on the ASCII tier only; exact
		// matching compares bytes on every tier. Other folded cells use the
		// explicitly translated simple-fold view over RustAC exact mode.
		if !asciiTier && !exact {
			view, err := board.NewRustACView(patterns)
			if err != nil {
				return boardEngine{}, true, err
			}
			return rustacViewEngine(view), true, nil
		}
		m, err := rustac.CompileAlternation(patterns, exact)
		if err != nil {
			return boardEngine{}, true, err
		}
		return boardEngine{mode: "direct", find: m.Find, each: m.Each, release: m.Close}, true, nil
	}
	panic("unknown board entrant " + name)
}

// bind fixes the engine to the cell's operation. IndexFold cells hold one
// pattern, so entrants answer them with Find.
func (e boardEngine) bind(name, op, h string) boardBinding {
	binding := boardBinding{
		run:           boardRun{name: name},
		strict:        e.strict,
		requireWidths: op == "each" || e.findWithWidth != nil || e.findHit != nil,
	}
	if op == "each" {
		if eachErr := e.eachErr; eachErr != nil {
			scanErr := func(visit func(board.Hit) bool) error {
				_, err := eachErr(h, func(start, pattern, width int) bool {
					return visit(board.Hit{Start: start, Pattern: pattern, Width: width})
				})
				return err
			}
			binding.scanErr = scanErr
			binding.run.scan = func(visit func(board.Hit) bool) {
				if err := scanErr(visit); err != nil {
					panic(err)
				}
			}
			return binding
		}
		each := e.each
		binding.run.scan = func(visit func(board.Hit) bool) {
			each(h, func(start, pattern, width int) bool {
				return visit(board.Hit{Start: start, Pattern: pattern, Width: width})
			})
		}
		return binding
	}
	if findWithWidth := e.findWithWidth; findWithWidth != nil {
		scanErr := func(visit func(board.Hit) bool) error {
			start, pattern, width, ok, err := findWithWidth(h)
			if err != nil {
				return err
			}
			if ok {
				visit(board.Hit{Start: start, Pattern: pattern, Width: width})
			}
			return nil
		}
		binding.scanErr = scanErr
		binding.run.scan = func(visit func(board.Hit) bool) {
			if err := scanErr(visit); err != nil {
				panic(err)
			}
		}
		return binding
	}
	if findHit := e.findHit; findHit != nil {
		binding.run.scan = func(visit func(board.Hit) bool) {
			if hit, ok := findHit(h); ok {
				visit(hit)
			}
		}
		return binding
	}
	find := e.find
	binding.run.scan = func(visit func(board.Hit) bool) {
		if start, pattern, ok := find(h); ok {
			visit(board.Hit{Start: start, Pattern: pattern, Width: -1})
		}
	}
	return binding
}

// boardHost names the CPU for the verifier's host rule: vendor, family, and
// model from /proc/cpuinfo, and the AVX2/AVX-512 features this process can use.
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
	return fmt.Sprintf("vendor=%s family=%s model=%s avx2=%d avx512f=%d avx512bw=%d avx512vbmi=%d",
		fields["vendor_id"], fields["cpu family"], fields["model"],
		int(boolMetric(cpu.X86.HasAVX2)), int(boolMetric(cpu.X86.HasAVX512F)), int(boolMetric(cpu.X86.HasAVX512BW)), int(boolMetric(cpu.X86.HasAVX512VBMI)))
}

// TestBoardEntrantsAgree prepares every pinned cell up to 256 KiB as
// BenchmarkBoard does before timing it: digest, then casei and every
// supporting entrant against the oracle. It keeps the wiring under CI.
func TestBoardEntrantsAgree(t *testing.T) {
	pinned := board.Pinned()
	for _, spec := range board.Cells() {
		if spec.Size > 256<<10 {
			continue
		}
		c, err := prepareBoardCell(spec, pinned[spec.Name()][1])
		if err != nil {
			t.Errorf("cell %s: %v", spec.Name(), err)
			continue
		}
		wantVelozMode := ""
		if !spec.Sensitive && board.VelozAvailable() {
			if pinned[spec.Name()][0] == "ascii" && spec.Count == 1 {
				wantVelozMode = "direct"
			} else {
				wantVelozMode = "view"
			}
		}
		if c.velozMode != wantVelozMode {
			t.Errorf("cell %s Veloz mode = %q, want %q", spec.Name(), c.velozMode, wantVelozMode)
		}
		wantRustACMode := "direct"
		if !spec.Sensitive && pinned[spec.Name()][0] == "utf8" {
			wantRustACMode = "view"
		}
		if c.rustacMode != wantRustACMode {
			t.Errorf("cell %s RustAC mode = %q, want %q", spec.Name(), c.rustacMode, wantRustACMode)
		}
		c.close()
	}
}

// TestVelozBoardEntrantCoversOriginalFoldedCells exercises the actual
// BenchmarkBoard binding on all original cells, including the translated view
// when a direct ASCII-singleton mode is selected. Exact cells remain excluded.
func TestVelozBoardEntrantCoversOriginalFoldedCells(t *testing.T) {
	if velozVectorBits() != 256 {
		t.Skip("pinned Veloz field entry requires AVX2")
	}
	pinned := board.Pinned()
	folded, exact := 0, 0
	operations := map[string]int{}
	for _, spec := range board.Cells() {
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("%s digest = %s, want %s", spec.Name(), got, want)
		}
		engine, supported, err := boardEntrant("veloz", cell.Patterns, cell.ASCII, spec.Sensitive)
		if err != nil {
			t.Fatalf("%s Veloz setup: %v", spec.Name(), err)
		}
		if spec.Sensitive {
			exact++
			if supported {
				t.Errorf("%s: Veloz must not enter an exact cell", spec.Name())
			}
			continue
		}
		folded++
		operations[spec.Op]++
		if !supported {
			t.Errorf("%s: Veloz does not support this folded cell", spec.Name())
			continue
		}
		wantMode := "view"
		if cell.ASCII && len(cell.Patterns) == 1 {
			wantMode = "direct"
		}
		if engine.mode != wantMode {
			t.Errorf("%s: Veloz mode = %q, want %q", spec.Name(), engine.mode, wantMode)
		}
		if cell.Haystack == "" || !utf8.ValidString(cell.Haystack) {
			t.Fatalf("%s: board input is empty or invalid UTF-8", spec.Name())
		}
		for i, pattern := range cell.Patterns {
			if pattern == "" || !utf8.ValidString(pattern) {
				t.Fatalf("%s: pattern %d is empty or invalid UTF-8", spec.Name(), i)
			}
		}
		want := board.Matches(cell.Haystack, cell.Patterns, false)
		if spec.Op != "each" && len(want) > 1 {
			want = want[:1]
		}
		if err := engine.bind("veloz", spec.Op, cell.Haystack).agree(want); err != nil {
			t.Errorf("%s: %v", spec.Name(), err)
		}
	}
	if folded != 112 || exact != 88 {
		t.Fatalf("Veloz saw %d folded and %d exact cells, want 112 and 88", folded, exact)
	}
	for _, op := range []string{"find", "each", "indexfold"} {
		if operations[op] == 0 {
			t.Errorf("no folded %s cell was exercised", op)
		}
	}
}

func TestBoardBindingPreservesEachVisitorStop(t *testing.T) {
	view, err := board.NewRustACView([]string{"k"})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	binding := rustacViewEngine(view).bind("rustac", "each", "kk")
	if binding.scanErr == nil {
		t.Fatal("translated Each binding has no error-aware scan")
	}
	calls := 0
	err = binding.scanErr(func(board.Hit) bool {
		calls++
		return false
	})
	if err != nil || calls != 1 {
		t.Fatalf("stopped Each = callbacks %d, error %v; want one callback and no error", calls, err)
	}
}

func TestBoardStrictAgreementPreservesReportedFields(t *testing.T) {
	want := []board.Hit{{Start: 2, Pattern: 1, Width: 3}}
	for _, got := range []board.Hit{
		{Start: 2, Pattern: -1, Width: 3},
		{Start: 2, Pattern: 1, Width: -1},
	} {
		scan := func(visit func(board.Hit) bool) {
			visit(got)
		}
		generic := boardBinding{run: boardRun{name: "generic", scan: scan}}
		if err := generic.agree(want); err != nil {
			t.Fatalf("generic agreement no longer accepts an omitted diagnostic field: %v", err)
		}
		strict := boardBinding{run: boardRun{name: "translated", scan: scan}, strict: true, requireWidths: true}
		if err := strict.agree(want); err == nil {
			t.Fatalf("strict agreement accepted unreported hit %+v", got)
		}
	}

	missingVelozID := boardBinding{
		run:    boardRun{name: "veloz", scan: func(visit func(board.Hit) bool) { visit(board.Hit{Start: 2, Pattern: -1, Width: -1}) }},
		strict: true,
	}
	if err := missingVelozID.agree(want[:1]); err == nil {
		t.Fatal("strict Veloz agreement accepted a missing original ID")
	}
	missingEachWidth := boardBinding{
		run:           boardRun{name: "direct each", scan: func(visit func(board.Hit) bool) { visit(board.Hit{Start: 2, Pattern: 1, Width: -1}) }},
		requireWidths: true,
	}
	if err := missingEachWidth.agree(want); err == nil {
		t.Fatal("Each agreement accepted a missing source width")
	}
}

// TestRustACSimpleFoldViewEveryFoldedCell strictly checks the translated view
// on all pinned folded operations, including large size-family inputs. When
// the direct ASCII binding is selected, the translated view is still executed
// separately as required by the board comparison contract.
func TestRustACSimpleFoldViewEveryFoldedCell(t *testing.T) {
	pinned := board.Pinned()
	folded := 0
	for _, spec := range board.Cells() {
		if spec.Sensitive {
			continue
		}
		folded++
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("%s digest %s, want %s", spec.Name(), got, want)
		}
		if !utf8.ValidString(cell.Haystack) || len(cell.Patterns) == 0 {
			t.Fatalf("%s is outside the pinned nonempty valid-UTF8 workload", spec.Name())
		}
		for i, pattern := range cell.Patterns {
			if pattern == "" || !utf8.ValidString(pattern) {
				t.Fatalf("%s pattern %d is empty or invalid UTF-8", spec.Name(), i)
			}
		}
		want := board.Matches(cell.Haystack, cell.Patterns, false)
		if spec.Op != "each" && len(want) > 1 {
			want = want[:1]
		}

		selected, supported, err := boardEntrant("rustac", cell.Patterns, cell.ASCII, false)
		if err != nil || !supported {
			t.Fatalf("%s RustAC support=%t, error=%v", spec.Name(), supported, err)
		}
		wantMode := "direct"
		if !cell.ASCII {
			wantMode = "view"
		}
		if selected.mode != wantMode || selected.strict != (wantMode == "view") {
			t.Fatalf("%s selected RustAC mode=%q strict=%t, want mode=%q strict=%t", spec.Name(), selected.mode, selected.strict, wantMode, wantMode == "view")
		}
		if err := selected.bind("rustac", spec.Op, cell.Haystack).agree(want); err != nil {
			selected.close()
			t.Fatalf("%s selected RustAC: %v", spec.Name(), err)
		}
		selected.close()

		if cell.ASCII {
			translated, err := board.NewRustACView(cell.Patterns)
			if err != nil {
				t.Fatalf("%s translated view compile: %v", spec.Name(), err)
			}
			binding := rustacViewEngine(translated).bind("rustac", spec.Op, cell.Haystack)
			err = binding.agree(want)
			translated.Close()
			if err != nil {
				t.Fatalf("%s translated RustAC view: %v", spec.Name(), err)
			}
		}
	}
	if folded != 112 {
		t.Fatalf("checked %d folded cells, want the 112 pinned folded operations", folded)
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
			e, supported, err := boardEntrant(name, []string{string(r)}, false, false)
			if err != nil {
				t.Fatalf("%s %q: %v", name, r, err)
			}
			for _, mate := range mates {
				if !supported {
					break
				}
				haystack := "\n" + string(mate) + "\n"
				want := []board.Hit{{Start: 1, Pattern: 0, Width: utf8.RuneLen(mate)}}
				if err := e.bind(name, "find", haystack).agree(want); err != nil {
					t.Errorf("%s does not fold %q (%U) with %q (%U): %v", name, r, r, mate, mate, err)
				}
			}
			e.close()
		}
	}
}

// TestBoardExactEntrants holds every entrant's exact mode to case. Each
// haystack holds a fold variant of a pattern before the pattern itself, so an
// entrant that still folds answers Find and Each wrongly.
func TestBoardExactEntrants(t *testing.T) {
	for _, c := range []struct {
		patterns []string
		haystack string
	}{
		{[]string{"Ab"}, "ab AB aB Ab ab Ab"},
		{[]string{"Ab", "cD"}, "cd ab CD cD AB Ab"},
		{[]string{"Жx"}, "жx ЖX Жx жX Жx"},
		{[]string{"Жx", "ǅb"}, "ǆb ǄB жx ǅb Жx"},
	} {
		ascii := !strings.ContainsFunc(c.haystack, func(r rune) bool { return r >= utf8.RuneSelf })
		want := board.Matches(c.haystack, c.patterns, true)
		if slices.Equal(want, board.Matches(c.haystack, c.patterns, false)) {
			t.Fatalf("%q in %q: folding does not change the answer", c.patterns, c.haystack)
		}
		for _, name := range boardEntrants {
			e, supported, err := boardEntrant(name, c.patterns, ascii, true)
			if err != nil {
				t.Fatalf("%s %q: %v", name, c.patterns, err)
			}
			if !supported {
				e.close()
				continue
			}
			if err := e.bind(name, "find", c.haystack).agree(want[:1]); err != nil {
				t.Errorf("find %q in %q: %v", c.patterns, c.haystack, err)
			}
			if err := e.bind(name, "each", c.haystack).agree(want); err != nil {
				t.Errorf("each %q in %q: %v", c.patterns, c.haystack, err)
			}
			e.close()
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
