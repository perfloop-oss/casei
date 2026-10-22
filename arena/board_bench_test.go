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

// asciiFoldOrbit reports whether every rune in r's complete simple-fold orbit
// is ASCII and whether that orbit is invariant. The starting rune is included
// in the ASCII property; invariant non-ASCII runes are exact but not ASCII.
func asciiFoldOrbit(r rune) (allASCII, invariant bool) {
	allASCII, invariant = r < utf8.RuneSelf, true
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		allASCII = allASCII && x < utf8.RuneSelf
		invariant = false
	}
	return allASCII, invariant
}

// rustacByteModeCertificates classifies both of RustAC's unchanged native byte
// modes in one orbit walk per rune. Empty or malformed patterns are not
// certified, so the existing view path retains its input checks.
func rustacByteModeCertificates(patterns []string) (exact, asciiCaseless bool) {
	if len(patterns) == 0 {
		return false, false
	}
	exact, asciiCaseless = true, true
	for _, pattern := range patterns {
		if pattern == "" || !utf8.ValidString(pattern) {
			return false, false
		}
		for _, r := range pattern {
			allASCII, invariant := asciiFoldOrbit(r)
			exact = exact && invariant
			asciiCaseless = asciiCaseless && allASCII
		}
	}
	return exact, asciiCaseless
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
		// Keep existing exact and ASCII-tier bindings. On a folded UTF-8 cell,
		// use a native byte mode only when one complete-orbit certificate covers
		// every pattern rune; otherwise retain the translated simple-fold view.
		modeExact := exact
		if !asciiTier && !exact {
			exactLanguage, asciiLanguage := rustacByteModeCertificates(patterns)
			switch {
			case exactLanguage:
				modeExact = true
			case asciiLanguage:
				modeExact = false
			default:
				view, err := board.NewRustACView(patterns)
				if err != nil {
					return boardEngine{}, true, err
				}
				return rustacViewEngine(view), true, nil
			}
		}
		m, err := rustac.CompileAlternation(patterns, modeExact)
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

// rustacCertifiedFoldedUTF8Cells is the expected native-byte partition for
// the pinned UTF-8 board cells. The selector itself uses only the byte-language
// certificate, never these cell identities.
var rustacCertifiedFoldedUTF8Cells = map[string]string{
	"length/3-4/03/n=1,len=3-4,size=206100,first=0.93,spacing=1024,op=indexfold,nonascii=100,corpus=code,case=ci":       "exact",
	"size/1MiB-4MiB/00/n=1,len=3-4,size=2451052,first=0.56,spacing=65536,op=indexfold,nonascii=100,corpus=code,case=ci": "exact",
	"case/ci/09/n=1,len=5-8,size=159739,first=0.26,spacing=0,op=indexfold,nonascii=0,corpus=russian,case=ci":            "ascii",
	"corpus/russian/02/n=1,len=3-4,size=9564,first=0.63,spacing=65536,op=each,nonascii=50,corpus=russian,case=ci":       "ascii",
	"op/indexfold/00/n=1,len=5-8,size=11181,first=0.11,spacing=1024,op=indexfold,nonascii=0,corpus=russian,case=ci":     "ascii",
	"op/indexfold/04/n=1,len=2-2,size=22972,first=0.86,spacing=0,op=indexfold,nonascii=50,corpus=russian,case=ci":       "ascii",
	"script/ascii/01/n=2,len=2-2,size=34069,first=1.00,spacing=65536,op=find,nonascii=0,corpus=russian,case=ci":         "ascii",
}

func expectedRustACModes(name, tier string, sensitive bool) (route, native string) {
	if sensitive {
		return "direct", "exact"
	}
	if tier == "ascii" {
		return "direct", "ascii"
	}
	if native := rustacCertifiedFoldedUTF8Cells[name]; native != "" {
		return "direct", native
	}
	return "view", ""
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
		wantRustACMode, _ := expectedRustACModes(spec.Name(), pinned[spec.Name()][0], spec.Sensitive)
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

// TestRustACModesOnEveryFoldedCell checks the selected native byte mode or
// translated view on every pinned folded operation. It also keeps the view
// independently covered wherever the selected board route is direct.
func TestRustACModesOnEveryFoldedCell(t *testing.T) {
	pinned := board.Pinned()
	folded, utf8Folded, directUTF8, viewUTF8 := 0, 0, 0, 0
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

		tier := pinned[spec.Name()][0]
		wantMode, wantNative := expectedRustACModes(spec.Name(), tier, false)
		if tier == "utf8" {
			utf8Folded++
			exactLanguage, asciiLanguage := rustacByteModeCertificates(cell.Patterns)
			switch wantNative {
			case "exact":
				if !exactLanguage || asciiLanguage {
					t.Errorf("%s certificates exact=%t ASCII=%t, want exact only", spec.Name(), exactLanguage, asciiLanguage)
				}
				directUTF8++
			case "ascii":
				if exactLanguage || !asciiLanguage {
					t.Errorf("%s certificates exact=%t ASCII=%t, want ASCII only", spec.Name(), exactLanguage, asciiLanguage)
				}
				directUTF8++
			default:
				if exactLanguage || asciiLanguage {
					t.Errorf("%s has an unlisted byte-language certificate: exact=%t ASCII=%t", spec.Name(), exactLanguage, asciiLanguage)
				}
				viewUTF8++
			}
		}

		selected, supported, err := boardEntrant("rustac", cell.Patterns, cell.ASCII, false)
		if err != nil || !supported {
			t.Fatalf("%s RustAC support=%t, error=%v", spec.Name(), supported, err)
		}
		if selected.mode != wantMode || selected.strict != (wantMode == "view") {
			t.Fatalf("%s selected RustAC mode=%q strict=%t, want mode=%q strict=%t", spec.Name(), selected.mode, selected.strict, wantMode, wantMode == "view")
		}
		if err := selected.bind("rustac", spec.Op, cell.Haystack).agree(want); err != nil {
			selected.close()
			t.Fatalf("%s selected RustAC: %v", spec.Name(), err)
		}
		selected.close()

		if cell.ASCII || selected.mode == "direct" {
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
	if folded != 112 || utf8Folded != 77 || directUTF8 != 7 || viewUTF8 != 70 {
		t.Fatalf("checked folded=%d UTF-8=%d direct=%d view=%d, want 112/77/7/70", folded, utf8Folded, directUTF8, viewUTF8)
	}
}

func simpleFoldSpellings(pattern string) []string {
	runes := []rune(pattern)
	orbits := make([][]rune, len(runes))
	for i, r := range runes {
		orbits[i] = []rune{r}
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			orbits[i] = append(orbits[i], x)
		}
	}
	var spellings []string
	current := make([]rune, len(runes))
	var visit func(int)
	visit = func(i int) {
		if i == len(runes) {
			spellings = append(spellings, string(current))
			return
		}
		for _, r := range orbits[i] {
			current[i] = r
			visit(i + 1)
		}
	}
	visit(0)
	return spellings
}

func TestRustACByteModeCertificatesCoverEverySimpleFoldOrbit(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		wantASCII, wantInvariant := r < utf8.RuneSelf, true
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			wantASCII = wantASCII && x < utf8.RuneSelf
			wantInvariant = false
		}
		gotASCII, gotInvariant := asciiFoldOrbit(r)
		if gotASCII != wantASCII || gotInvariant != wantInvariant {
			t.Fatalf("orbit certificate for %U = ASCII:%t invariant:%t, want ASCII:%t invariant:%t", r, gotASCII, gotInvariant, wantASCII, wantInvariant)
		}
		gotExact, gotASCIICaseless := rustacByteModeCertificates([]string{string(r)})
		if gotExact != wantInvariant || gotASCIICaseless != wantASCII {
			t.Fatalf("pattern certificate for %U = exact:%t ASCII:%t, want exact:%t ASCII:%t", r, gotExact, gotASCIICaseless, wantInvariant, wantASCII)
		}
	}
	for _, patterns := range [][]string{nil, {""}, {string([]byte{0xff})}} {
		if exact, ascii := rustacByteModeCertificates(patterns); exact || ascii {
			t.Errorf("empty or malformed patterns %q received a byte certificate", patterns)
		}
	}
}

func TestRustACByteModeOutliersStayOnView(t *testing.T) {
	for _, tc := range []struct{ pattern, variant string }{
		{"K", "K"},
		{"S", "ſ"},
		{"Σ", "ς"},
	} {
		if exact, ascii := rustacByteModeCertificates([]string{tc.pattern}); exact || ascii {
			t.Errorf("unsafe complete language %q received exact:%t ASCII:%t", tc.pattern, exact, ascii)
		}
		engine, supported, err := boardEntrant("rustac", []string{tc.pattern}, false, false)
		if err != nil || !supported || engine.mode != "view" {
			t.Fatalf("RustAC %q: mode=%q supported=%t error=%v, want supported view", tc.pattern, engine.mode, supported, err)
		}
		if tc.pattern == "K" || tc.pattern == "S" {
			native, err := rustac.CompileAlternation([]string{tc.pattern}, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, ok := native.Find(tc.variant); ok {
				native.Close()
				t.Errorf("RustAC ASCII mode unexpectedly matches %q for %q", tc.variant, tc.pattern)
			}
			native.Close()
		}
		want := board.Matches(tc.variant, []string{tc.pattern}, false)
		if err := engine.bind("rustac", "each", tc.variant).agree(want); err != nil {
			engine.close()
			t.Errorf("RustAC view %q: %v", tc.pattern, err)
		}
		engine.close()
	}

	pinned := board.Pinned()
	foundMixed := false
	for _, spec := range board.Cells() {
		if spec.Family != "count" || spec.Level != "2" || spec.Index != 1 {
			continue
		}
		foundMixed = true
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("mixed control digest %s, want %s", got, want)
		}
		if exact, ascii := rustacByteModeCertificates(cell.Patterns); exact || ascii {
			t.Fatalf("mixed count/2/01 received exact:%t ASCII:%t", exact, ascii)
		}
		engine, supported, err := boardEntrant("rustac", cell.Patterns, cell.ASCII, false)
		if err != nil || !supported || engine.mode != "view" {
			t.Fatalf("mixed count/2/01: mode=%q supported=%t error=%v, want supported view", engine.mode, supported, err)
		}
		want := board.Matches(cell.Haystack, cell.Patterns, false)
		if err := engine.bind("rustac", spec.Op, cell.Haystack).agree(want); err != nil {
			engine.close()
			t.Fatalf("mixed count/2/01: %v", err)
		}
		engine.close()
	}
	if !foundMixed {
		t.Fatal("pinned mixed count/2/01 control is missing")
	}
	for _, patterns := range [][]string{nil, {""}, {string([]byte{0xff})}} {
		if engine, supported, err := boardEntrant("rustac", patterns, false, false); err == nil || !supported {
			engine.close()
			t.Errorf("invalid folded pattern set %q: mode=%q supported=%t error=%v; want existing view error", patterns, engine.mode, supported, err)
		}
	}
}

func TestRustACCertifiedLanguagesAndNativeOrdering(t *testing.T) {
	pinned := board.Pinned()
	certified := 0
	for _, spec := range board.Cells() {
		name := spec.Name()
		nativeMode, ok := rustacCertifiedFoldedUTF8Cells[name]
		if !ok {
			continue
		}
		certified++
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[name][1]; got != want {
			t.Fatalf("%s digest %s, want %s", name, got, want)
		}
		exact, ascii := rustacByteModeCertificates(cell.Patterns)
		if (nativeMode == "exact" && (!exact || ascii)) || (nativeMode == "ascii" && (exact || !ascii)) {
			t.Fatalf("%s certificates exact:%t ASCII:%t, want %s only", name, exact, ascii, nativeMode)
		}
		engine, supported, err := boardEntrant("rustac", cell.Patterns, false, false)
		if err != nil || !supported || engine.mode != "direct" {
			t.Fatalf("%s: mode=%q supported=%t error=%v, want direct", name, engine.mode, supported, err)
		}
		var spellings []string
		for _, pattern := range cell.Patterns {
			if strings.Contains(pattern, "\x00") {
				engine.close()
				t.Fatalf("%s pattern %q contains the fold-language separator", name, pattern)
			}
			spellings = append(spellings, simpleFoldSpellings(pattern)...)
		}
		if len(spellings) == 0 {
			engine.close()
			t.Fatalf("%s has no complete fold spellings", name)
		}
		haystack := "中" + strings.Join(spellings, "\x00")
		want := board.Matches(haystack, cell.Patterns, false)
		if err := engine.bind("rustac", "each", haystack).agree(want); err != nil {
			engine.close()
			t.Errorf("%s %s full fold language Each: %v", name, nativeMode, err)
		}
		if len(want) > 1 {
			want = want[:1]
		}
		if err := engine.bind("rustac", "find", haystack).agree(want); err != nil {
			engine.close()
			t.Errorf("%s %s full fold language Find: %v", name, nativeMode, err)
		}
		engine.close()
	}

	for _, tc := range []struct {
		patterns []string
		haystack string
	}{
		{[]string{"ab", "a"}, "中AbA"},
		{[]string{"a", "ab"}, "中AbA"},
		{[]string{"A", "a"}, "中aA"},
		{[]string{"aa", "a"}, "éAAa"},
	} {
		engine, supported, err := boardEntrant("rustac", tc.patterns, false, false)
		if err != nil || !supported || engine.mode != "direct" {
			t.Fatalf("RustAC order patterns %q: mode=%q supported=%t error=%v, want direct", tc.patterns, engine.mode, supported, err)
		}
		exact, ascii := rustacByteModeCertificates(tc.patterns)
		if exact || !ascii {
			t.Fatalf("RustAC order patterns %q certificates exact:%t ASCII:%t, want ASCII only", tc.patterns, exact, ascii)
		}
		want := board.Matches(tc.haystack, tc.patterns, false)
		if err := engine.bind("rustac", "each", tc.haystack).agree(want); err != nil {
			engine.close()
			t.Errorf("RustAC Each ordering %q: %v", tc.patterns, err)
		}
		if len(want) > 1 {
			want = want[:1]
		}
		if err := engine.bind("rustac", "find", tc.haystack).agree(want); err != nil {
			engine.close()
			t.Errorf("RustAC Find ordering %q: %v", tc.patterns, err)
		}
		calls := 0
		engine.bind("rustac", "each", tc.haystack).run.scan(func(board.Hit) bool {
			calls++
			return false
		})
		if calls != 1 {
			engine.close()
			t.Errorf("RustAC Each stop for %q called visitor %d times, want 1", tc.patterns, calls)
		}
		engine.close()
	}

	exactEngine, supported, err := boardEntrant("rustac", []string{"亚"}, false, false)
	if err != nil || !supported || exactEngine.mode != "direct" {
		t.Fatalf("RustAC invariant mode: mode=%q supported=%t error=%v", exactEngine.mode, supported, err)
	}
	if exact, ascii := rustacByteModeCertificates([]string{"亚"}); !exact || ascii {
		t.Fatalf("RustAC invariant certificate exact:%t ASCII:%t, want exact only", exact, ascii)
	}
	exactHits := board.Matches("é亚亚", []string{"亚"}, false)
	if err := exactEngine.bind("rustac", "each", "é亚亚").agree(exactHits); err != nil {
		exactEngine.close()
		t.Fatalf("RustAC invariant source spans: %v", err)
	}
	exactEngine.close()
	if certified != len(rustacCertifiedFoldedUTF8Cells) {
		t.Fatalf("checked %d certified cells, want %d", certified, len(rustacCertifiedFoldedUTF8Cells))
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
