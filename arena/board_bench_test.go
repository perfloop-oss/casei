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
	runs := []boardRun{c.candidate}
	for _, name := range boardEntrants {
		e, supported, err := boardEntrant(name, cell.Patterns, cell.ASCII, spec.Sensitive)
		if err != nil {
			return nil, fmt.Errorf("%s supports the cell but cannot compile it: %v", name, err)
		}
		if supported {
			c.entrants = append(c.entrants, e.bind(name, spec.Op, cell.Haystack))
			runs = append(runs, c.entrants[len(c.entrants)-1])
		}
	}
	for _, r := range runs {
		if err := r.agree(want); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// agree runs r once and holds its hits to the oracle's.
func (r boardRun) agree(want []board.Hit) error {
	var got []board.Hit
	r.scan(func(h board.Hit) bool { got = append(got, h); return true })
	if len(got) != len(want) {
		return fmt.Errorf("%s gives %d matches, oracle %d", r.name, len(got), len(want))
	}
	for i, h := range got {
		if w := want[i]; h.Start != w.Start || h.Pattern >= 0 && h.Pattern != w.Pattern || h.Width >= 0 && h.Width != w.Width {
			return fmt.Errorf("%s match %d is %+v, oracle %+v", r.name, i, h, w)
		}
	}
	return nil
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

// asciiFoldOrbit reports whether every rune in r's complete simple-fold orbit
// is ASCII. ASCII case-insensitive byte search is equivalent only for these
// whole orbits, not merely for an ASCII spelling of r.
func asciiFoldOrbit(r rune) bool {
	if r >= utf8.RuneSelf {
		return false
	}
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		if x >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// rustacASCIICaselessEquivalent certifies a complete set for RustAC's existing
// ASCII-caseless mode on valid UTF-8 text.
func rustacASCIICaselessEquivalent(patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if pattern == "" || !utf8.ValidString(pattern) {
			return false
		}
		for _, r := range pattern {
			if !asciiFoldOrbit(r) {
				return false
			}
		}
	}
	return true
}

// rustacExactEquivalent certifies a complete set for RustAC's existing exact
// mode: folding cannot change any rune in any pattern.
func rustacExactEquivalent(patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if pattern == "" || !utf8.ValidString(pattern) {
			return false
		}
		for _, r := range pattern {
			if unicode.SimpleFold(r) != r {
				return false
			}
		}
	}
	return true
}

// velozByteEquivalent certifies Veloz's ASCII-byte fold for one complete
// literal language: ASCII runes may fold only within ASCII, and non-ASCII
// runes must be invariant.
func velozByteEquivalent(pattern string) bool {
	if pattern == "" || !utf8.ValidString(pattern) {
		return false
	}
	for _, r := range pattern {
		if r < utf8.RuneSelf {
			if !asciiFoldOrbit(r) {
				return false
			}
		} else if unicode.SimpleFold(r) != r {
			return false
		}
	}
	return true
}

// boardEntrant compiles one entrant when it supports the complete pattern
// language. The ASCII and exact tiers in field.yaml remain the broad support
// rules; the byte-language certificates below add only proven per-set
// equivalences. A supporting entrant that fails to compile is an error.
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
		// veloz has no exact substring search and only counts when its AVX2
		// path is available. Its broader byte-language equivalence is still
		// singleton-only.
		if exact || !single || velozVectorBits() != 256 {
			return boardEngine{}, false, nil
		}
		if !asciiTier && !velozByteEquivalent(patterns[0]) {
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
		// Preserve the ASCII-tier and exact modes. On a UTF-8 cell, use the
		// existing exact constructor for an invariant language, or the existing
		// ASCII-caseless constructor only when every complete fold orbit is
		// ASCII. Neither mode broadens RustAC's native caseless guard.
		modeExact := exact
		if !exact && !asciiTier {
			switch {
			case rustacExactEquivalent(patterns):
				modeExact = true
			case rustacASCIICaselessEquivalent(patterns):
				modeExact = false
			default:
				return boardEngine{}, false, nil
			}
		}
		m, err := rustac.CompileAlternation(patterns, modeExact)
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
		if spec.Size > 256<<10 {
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
			e, supported, err := boardEntrant(name, []string{string(r)}, false, false)
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
				continue
			}
			if err := e.bind(name, "find", c.haystack).agree(want[:1]); err != nil {
				t.Errorf("find %q in %q: %v", c.patterns, c.haystack, err)
			}
			if err := e.bind(name, "each", c.haystack).agree(want); err != nil {
				t.Errorf("each %q in %q: %v", c.patterns, c.haystack, err)
			}
		}
	}
}

// simpleFoldSpellings returns the finite rune-wise fold language of a pattern.
// Tests use only the short pinned literals; production never expands this set.
func simpleFoldSpellings(pattern string) []string {
	if pattern == "" || !utf8.ValidString(pattern) {
		return nil
	}
	runes := []rune(pattern)
	members := make([][]rune, len(runes))
	for i, r := range runes {
		members[i] = []rune{r}
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			members[i] = append(members[i], x)
		}
	}
	current := make([]rune, len(runes))
	var spellings []string
	var visit func(int)
	visit = func(i int) {
		if i == len(runes) {
			spellings = append(spellings, string(current))
			return
		}
		for _, r := range members[i] {
			current[i] = r
			visit(i + 1)
		}
	}
	visit(0)
	return spellings
}

// TestByteLanguageCertificatesCoverEverySimpleFoldOrbit checks that eligibility
// follows complete Go simple-fold orbits for every Unicode scalar, not a sample
// of current benchmark patterns.
func TestByteLanguageCertificatesCoverEverySimpleFoldOrbit(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		allASCII, invariant := r < utf8.RuneSelf, true
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			if x >= utf8.RuneSelf {
				allASCII = false
			}
			invariant = false
		}
		pattern := string(r)
		if got := rustacASCIICaselessEquivalent([]string{pattern}); got != allASCII {
			t.Fatalf("RustAC ASCII certificate for %U = %t, want %t", r, got, allASCII)
		}
		if got := rustacExactEquivalent([]string{pattern}); got != invariant {
			t.Fatalf("RustAC exact certificate for %U = %t, want %t", r, got, invariant)
		}
		wantVeloz := allASCII || r >= utf8.RuneSelf && invariant
		if got := velozByteEquivalent(pattern); got != wantVeloz {
			t.Fatalf("Veloz byte certificate for %U = %t, want %t", r, got, wantVeloz)
		}
	}
	for _, pattern := range []string{"", string([]byte{0xff})} {
		if rustacASCIICaselessEquivalent([]string{pattern}) || rustacExactEquivalent([]string{pattern}) || velozByteEquivalent(pattern) {
			t.Errorf("invalid or empty pattern %q received a byte-language certificate", pattern)
		}
	}
}

// TestBoardCertifiedEntrantsAgree verifies the thirteen fixed omissions through
// the ordinary board preparation path. It also exercises every spelling in
// each added pattern's complete fold language against the independent oracle.
func TestBoardCertifiedEntrantsAgree(t *testing.T) {
	additional := map[string]map[string]bool{
		"length/3-4/03":     {"rustac": true, "veloz": true},
		"script/ascii/01":   {"rustac": true},
		"case/ci/09":        {"rustac": true, "veloz": true},
		"corpus/russian/02": {"rustac": true, "veloz": true},
		"size/1MiB-4MiB/00": {"rustac": true, "veloz": true},
		"op/indexfold/00":   {"rustac": true, "veloz": true},
		"op/indexfold/04":   {"rustac": true, "veloz": true},
	}
	pinned, seen, pairs := board.Pinned(), map[string]bool{}, 0
	for _, spec := range board.Cells() {
		key := fmt.Sprintf("%s/%s/%02d", spec.Family, spec.Level, spec.Index)
		want, ok := additional[key]
		if !ok {
			continue
		}
		cell := spec.Build()
		if cell.ASCII || pinned[spec.Name()][0] != "utf8" {
			t.Fatalf("%s is no longer the pinned UTF-8 tier", key)
		}
		prepared, err := prepareBoardCell(spec, pinned[spec.Name()][1])
		if err != nil {
			t.Fatalf("prepareBoardCell(%s): %v", key, err)
		}
		got := map[string]bool{}
		for _, entrant := range prepared.entrants {
			if entrant.name == "rustac" || entrant.name == "veloz" {
				got[entrant.name] = true
			}
		}
		wantVeloz := want["veloz"] && velozVectorBits() == 256
		if got["rustac"] != want["rustac"] || got["veloz"] != wantVeloz {
			t.Errorf("%s byte entrants = %v, want rustac=%t veloz=%t", key, got, want["rustac"], wantVeloz)
		}
		seen[key] = true
		if !want["rustac"] {
			continue
		}
		spellings := make([]string, 0)
		for _, pattern := range cell.Patterns {
			spellings = append(spellings, simpleFoldSpellings(pattern)...)
		}
		haystack := "中" + strings.Join(spellings, "\x00") + "中"
		for _, name := range []string{"rustac", "veloz"} {
			if !want[name] || name == "veloz" && !wantVeloz {
				continue
			}
			engine, supported, err := boardEntrant(name, cell.Patterns, cell.ASCII, false)
			if err != nil || !supported {
				t.Fatalf("%s certificate for %s: supported=%t err=%v", name, key, supported, err)
			}
			for _, op := range []string{"find", "each"} {
				wantHits := board.Matches(haystack, cell.Patterns, false)
				if op != "each" && len(wantHits) > 1 {
					wantHits = wantHits[:1]
				}
				if err := engine.bind(name, op, haystack).agree(wantHits); err != nil {
					t.Errorf("%s %s full-language stream for %s: %v", name, op, key, err)
				}
				pairs++
			}
		}
	}
	if len(seen) != len(additional) {
		t.Fatalf("prepared %d named cells, want %d", len(seen), len(additional))
	}
	if velozVectorBits() == 256 && pairs != 26 {
		t.Fatalf("checked %d positive/full-language streams with AVX2 enabled, want 26", pairs)
	}
}

func TestByteLanguageCertificatesRejectOutliers(t *testing.T) {
	for _, pattern := range []string{"K", "k", "S", "s", "K", "ſ", "Σ", "σ", "ς", "Ж", "ж"} {
		if rustacASCIICaselessEquivalent([]string{pattern}) || rustacExactEquivalent([]string{pattern}) || velozByteEquivalent(pattern) {
			t.Errorf("unsafe complete language %q received a byte certificate", pattern)
		}
		for _, name := range []string{"rustac", "veloz"} {
			_, supported, err := boardEntrant(name, []string{pattern}, false, false)
			if err != nil || supported {
				t.Errorf("%s admitted unsafe UTF-8 pattern %q: supported=%t err=%v", name, pattern, supported, err)
			}
		}
	}
	for _, c := range []struct{ pattern, variant string }{{"K", "K"}, {"S", "ſ"}} {
		m, err := rustac.CompileAlternation([]string{c.pattern}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := m.Find(c.variant); ok || len(board.Matches(c.variant, []string{c.pattern}, false)) == 0 {
			t.Errorf("%q vs %q no longer demonstrates the ASCII-byte mismatch", c.pattern, c.variant)
		}
		if at := veloz.IndexFold(c.variant, c.pattern); at >= 0 {
			t.Errorf("Veloz unexpectedly matches %q in %q at %d", c.pattern, c.variant, at)
		}
	}
	for _, c := range []struct{ pattern, variant string }{{"K", "k"}, {"ſ", "s"}} {
		if _, err := rustac.CompileAlternation([]string{c.pattern}, false); err == nil {
			t.Errorf("RustAC caseless constructor accepted non-ASCII %q", c.pattern)
		}
		m, err := rustac.CompileAlternation([]string{c.pattern}, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := m.Find(c.variant); ok || len(board.Matches(c.variant, []string{c.pattern}, false)) == 0 {
			t.Errorf("RustAC exact mode unexpectedly implements %q's fold language", c.pattern)
		}
	}
	mixed := []string{"a", "中"}
	if rustacASCIICaselessEquivalent(mixed) || rustacExactEquivalent(mixed) {
		t.Fatal("mixed ASCII-cased/invariant set received a RustAC certificate")
	}
	if _, supported, err := boardEntrant("rustac", mixed, false, false); err != nil || supported {
		t.Errorf("mixed RustAC set admitted: supported=%t err=%v", supported, err)
	}
	if _, supported, err := boardEntrant("veloz", mixed, false, false); err != nil || supported {
		t.Errorf("multi-pattern Veloz set admitted: supported=%t err=%v", supported, err)
	}
	for _, spec := range board.Cells() {
		if spec.Family != "count" || spec.Level != "2" || spec.Index != 1 {
			continue
		}
		cell := spec.Build()
		if rustacASCIICaselessEquivalent(cell.Patterns) || rustacExactEquivalent(cell.Patterns) {
			t.Fatal("the mixed count/2/01 cell received an unchanged-mode RustAC certificate")
		}
		if _, supported, err := boardEntrant("rustac", cell.Patterns, cell.ASCII, false); err != nil || supported {
			t.Errorf("the mixed count/2/01 cell was admitted: supported=%t err=%v", supported, err)
		}
		if _, err := rustac.CompileAlternation(cell.Patterns, false); err == nil {
			t.Fatal("the mixed count/2/01 cell bypassed RustAC's native caseless guard")
		}
		return
	}
	t.Fatal("pinned mixed count/2/01 cell is missing")
}

// TestByteLanguageNativeOrderingAndWidths preserves the adapter-visible
// ordering and source-byte contracts across the newly certified modes.
func TestByteLanguageNativeOrderingAndWidths(t *testing.T) {
	for _, c := range []struct {
		patterns []string
		haystack string
	}{
		{[]string{"ab", "a"}, "中AbA"},
		{[]string{"a", "ab"}, "中AbA"},
		{[]string{"A", "a"}, "中aA"},
		{[]string{"aa", "a"}, "中aaaa"},
	} {
		engine, supported, err := boardEntrant("rustac", c.patterns, false, false)
		if err != nil || !supported {
			t.Fatalf("RustAC %q: supported=%t err=%v", c.patterns, supported, err)
		}
		wantFind := board.Matches(c.haystack, c.patterns, false)
		if len(wantFind) > 1 {
			wantFind = wantFind[:1]
		}
		if err := engine.bind("rustac", "find", c.haystack).agree(wantFind); err != nil {
			t.Errorf("RustAC Find %q: %v", c.patterns, err)
		}
		if err := engine.bind("rustac", "each", c.haystack).agree(board.Matches(c.haystack, c.patterns, false)); err != nil {
			t.Errorf("RustAC Each %q: %v", c.patterns, err)
		}
	}

	invariant, supported, err := boardEntrant("rustac", []string{"中"}, false, false)
	if err != nil || !supported || !rustacExactEquivalent([]string{"中"}) {
		t.Fatalf("RustAC invariant exact mode: supported=%t err=%v", supported, err)
	}
	if err := invariant.bind("rustac", "each", "é中中").agree(board.Matches("é中中", []string{"中"}, false)); err != nil {
		t.Errorf("RustAC invariant source widths: %v", err)
	}

	mixedNeedle := "a中"
	if !velozByteEquivalent(mixedNeedle) {
		t.Fatal("Veloz mixed ASCII/invariant language was not certified")
	}
	velozEngine, supported, err := boardEntrant("veloz", []string{mixedNeedle}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if velozVectorBits() != 256 {
		if supported {
			t.Fatal("Veloz was active without its AVX2 availability gate")
		}
		return
	}
	if !supported {
		t.Fatal("Veloz omitted a certified singleton with AVX2 available")
	}
	haystack := "éA中!a中"
	if err := velozEngine.bind("veloz", "each", haystack).agree(board.Matches(haystack, []string{mixedNeedle}, false)); err != nil {
		t.Errorf("Veloz multibyte-prefix source widths: %v", err)
	}
	stopped := false
	if completed := velozEngine.each(haystack, func(int, int, int) bool { stopped = true; return false }); completed || !stopped {
		t.Errorf("Veloz Each stop = completed %t, called %t", completed, stopped)
	}
}

func TestVelozAVX2GateRejectsCertifiedLanguages(t *testing.T) {
	available := cpu.X86.HasAVX2
	defer func() { cpu.X86.HasAVX2 = available }()
	cpu.X86.HasAVX2 = false
	if velozVectorBits() != 0 {
		t.Fatal("Veloz availability did not follow the disabled AVX2 feature")
	}
	for _, c := range []struct {
		patterns []string
		ascii    bool
	}{
		{[]string{"a"}, true},
		{[]string{"a中"}, false},
	} {
		_, supported, err := boardEntrant("veloz", c.patterns, c.ascii, false)
		if err != nil || supported {
			t.Errorf("Veloz admitted %q without AVX2: supported=%t err=%v", c.patterns, supported, err)
		}
	}
}

func TestVelozByteCertificateRejectsBitFiveNearMisses(t *testing.T) {
	for _, c := range []struct{ pattern, haystack string }{
		{"A", "@"}, {"a", "`"}, {"[", "{"}, {"\\", "|"}, {"]", "}"}, {"^", "~"},
	} {
		if !velozByteEquivalent(c.pattern) {
			t.Fatalf("ASCII byte pattern %q should be eligible", c.pattern)
		}
		engine, supported, err := boardEntrant("veloz", []string{c.pattern}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if velozVectorBits() != 256 {
			if supported {
				t.Fatalf("Veloz was active without AVX2 on %q", c.pattern)
			}
			continue
		}
		if !supported {
			t.Fatalf("Veloz omitted eligible ASCII pattern %q with AVX2", c.pattern)
		}
		if err := engine.bind("veloz", "find", c.haystack).agree(nil); err != nil {
			t.Errorf("bit-five near miss %q in %q: %v", c.pattern, c.haystack, err)
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
