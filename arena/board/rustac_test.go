package board

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"
)

func requireView(t *testing.T, patterns []string) *RustACView {
	t.Helper()
	view, err := NewRustACView(patterns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(view.Close)
	return view
}

func collect(t *testing.T, view *RustACView, haystack string) []struct{ start, pattern, width int } {
	t.Helper()
	var got []struct{ start, pattern, width int }
	complete, err := view.Each(haystack, func(start, pattern, width int) bool {
		got = append(got, struct{ start, pattern, width int }{start, pattern, width})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("complete Each stopped")
	}
	return got
}

func TestFoldKeysCoverSimpleFoldOrbitsWithoutWidening(t *testing.T) {
	t.Logf("checking simple-fold orbits with Unicode tables %s", unicode.Version)
	var scalars, nonIdentity, nontrivialOrbits int
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue
		}
		scalars++
		key := FoldKey(r)
		if utf8.RuneLen(key) > utf8.RuneLen(r) {
			t.Fatalf("canonical key %U widens %U", key, r)
		}
		if key != r {
			nonIdentity++
		}
		members := 1
		minimum := r
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			members++
			if x < minimum {
				minimum = x
			}
			if got := FoldKey(x); got != key {
				t.Fatalf("orbit member %U maps to %U, %U maps to %U", x, got, r, key)
			}
			if utf8.RuneLen(key) > utf8.RuneLen(x) {
				t.Fatalf("canonical key %U widens orbit member %U", key, x)
			}
		}
		if key != minimum {
			t.Fatalf("FoldKey(%U) = %U, orbit minimum %U", r, key, minimum)
		}
		if key == r && members > 1 {
			nontrivialOrbits++
		}
	}
	if scalars != 1_112_064 {
		t.Fatalf("checked %d Unicode scalar values, want 1,112,064", scalars)
	}
	t.Logf("checked %d Unicode scalars, %d nonidentity keys and %d nontrivial orbits", scalars, nonIdentity, nontrivialOrbits)
}

func TestFoldWithOriginMapsEveryRuneBoundaryAndEOF(t *testing.T) {
	source := "kKsſΣςß"
	text, origin, err := foldWithOriginChecked(source)
	if err != nil {
		t.Fatal(err)
	}
	if want := FoldString(source); text != want {
		t.Fatalf("foldWithOrigin text = %q, want %q", text, want)
	}
	canonicalAt, sourceAt := 0, 0
	for sourceAt < len(source) {
		r, sourceWidth := utf8.DecodeRuneInString(source[sourceAt:])
		canonicalWidth := utf8.RuneLen(FoldKey(r))
		if origin[canonicalAt] != int32(sourceAt) || origin[canonicalAt+canonicalWidth] != int32(sourceAt+sourceWidth) {
			t.Fatalf("origin boundary canonical=%d..%d maps to %d..%d, want %d..%d", canonicalAt, canonicalAt+canonicalWidth, origin[canonicalAt], origin[canonicalAt+canonicalWidth], sourceAt, sourceAt+sourceWidth)
		}
		canonicalEnd := canonicalAt + canonicalWidth
		if !validCanonicalSpan(text, origin, canonicalAt, canonicalEnd) {
			t.Fatalf("boundary canonical=%d..%d was rejected", canonicalAt, canonicalEnd)
		}
		hit := projectFoldedOffsets(int(origin[canonicalAt]), int(origin[canonicalEnd]), 0)
		if hit.Start != sourceAt || hit.Width != sourceWidth {
			t.Fatalf("boundary canonical=%d source=%d: got %+v, want start=%d width=%d", canonicalAt, sourceAt, hit, sourceAt, sourceWidth)
		}
		canonicalAt += canonicalWidth
		sourceAt += sourceWidth
	}
	if canonicalAt != len(text) || canonicalAt+1 != len(origin) || origin[canonicalAt] != int32(len(source)) {
		t.Fatalf("EOF canonical=%d text=%d map=%d source=%d", canonicalAt, len(text), len(origin), origin[canonicalAt])
	}
	if !validCanonicalSpan(text, origin, 0, len(text)) {
		t.Fatal("whole canonical span was rejected")
	}
	hit := projectFoldedOffsets(int(origin[0]), int(origin[len(text)]), 0)
	if hit.Start != 0 || hit.Width != len(source) {
		t.Fatalf("whole span projection = %+v, want (0,%d)", hit, len(source))
	}
	for i := 1; i < len(text); i++ {
		if !canonicalBoundary(text, i) && validCanonicalSpan(text, origin, i, len(text)) {
			t.Fatalf("accepted interior canonical start %d", i)
		}
	}
}

func TestOriginMapMatchesEveryFoldedBoardRuneBoundary(t *testing.T) {
	pinned := Pinned()
	checked := 0
	for _, spec := range Cells() {
		if spec.Sensitive {
			continue
		}
		cell := spec.Build()
		if got, want := cell.Digest(), pinned[spec.Name()][1]; got != want {
			t.Fatalf("%s digest %s, want %s", spec.Name(), got, want)
		}
		text, origin, err := foldWithOriginChecked(cell.Haystack)
		if err != nil {
			t.Fatalf("%s foldWithOrigin: %v", spec.Name(), err)
		}
		canonicalAt, sourceAt := 0, 0
		for sourceAt < len(cell.Haystack) {
			r, sourceWidth := utf8.DecodeRuneInString(cell.Haystack[sourceAt:])
			key := string(FoldKey(r))
			canonicalWidth := len(key)
			if canonicalAt+canonicalWidth > len(text) || text[canonicalAt:canonicalAt+canonicalWidth] != key {
				t.Fatalf("%s rune %U at source %d canonicalizes incorrectly", spec.Name(), r, sourceAt)
			}
			if origin[canonicalAt] != int32(sourceAt) {
				t.Fatalf("%s boundary canonical=%d maps to %d, want source %d", spec.Name(), canonicalAt, origin[canonicalAt], sourceAt)
			}
			for interior := canonicalAt + 1; interior < canonicalAt+canonicalWidth; interior++ {
				if origin[interior] != int32(sourceAt) || canonicalBoundary(text, interior) {
					t.Fatalf("%s canonical interior byte %d maps to %d or is a boundary", spec.Name(), interior, origin[interior])
				}
			}
			canonicalEnd := canonicalAt + canonicalWidth
			if !validCanonicalSpan(text, origin, canonicalAt, canonicalEnd) {
				t.Fatalf("%s rune %U has an invalid canonical boundary", spec.Name(), r)
			}
			hit := projectFoldedOffsets(int(origin[canonicalAt]), int(origin[canonicalEnd]), 0)
			if hit.Start != sourceAt || hit.Width != sourceWidth {
				t.Fatalf("%s rune %U projects to %+v, want (%d,%d)", spec.Name(), r, hit, sourceAt, sourceWidth)
			}
			canonicalAt += canonicalWidth
			sourceAt += sourceWidth
			if origin[canonicalAt] != int32(sourceAt) {
				t.Fatalf("%s end boundary canonical=%d maps to %d, want source %d", spec.Name(), canonicalAt, origin[canonicalAt], sourceAt)
			}
		}
		if canonicalAt != len(text) || canonicalAt+1 != len(origin) || sourceAt != len(cell.Haystack) || origin[canonicalAt] != int32(len(cell.Haystack)) {
			t.Fatalf("%s EOF canonical=%d/%d map=%d source=%d/%d", spec.Name(), canonicalAt, len(text), len(origin), sourceAt, len(cell.Haystack))
		}
		checked++
	}
	if checked != 112 {
		t.Fatalf("checked %d folded board maps, want112", checked)
	}
}

func TestCheckedOriginLengthRejectsOverflow(t *testing.T) {
	if err := checkedOriginLength(-1); err == nil {
		t.Fatal("accepted a negative origin-map length")
	}
	maxInt := int(^uint(0) >> 1)
	if uint64(maxInt) > maxOriginOffset {
		max := int(maxOriginOffset)
		if err := checkedOriginLength(max); err != nil {
			t.Fatalf("rejected largest representable source offset: %v", err)
		}
		if err := checkedOriginLength(max + 1); err == nil {
			t.Fatal("accepted a source length outside the int32 origin-map range")
		}
	}
	if err := checkedOriginLength(maxInt); err == nil {
		t.Fatal("accepted a source length that would overflow len+1")
	}
}

func TestViewDirectedPriorityWidthsAndSimpleFoldOnly(t *testing.T) {
	originals := []string{"K", "k"}
	view := requireView(t, originals)
	if len(view.patternLengths) != len(originals) {
		t.Fatalf("canonical pattern count %d, want one per original %d", len(view.patternLengths), len(originals))
	}
	canonicalKelvin := FoldString(originals[0])
	canonicalK := FoldString(originals[1])
	if canonicalKelvin != canonicalK || view.patternLengths[0] != len(canonicalKelvin) || view.patternLengths[1] != len(canonicalK) {
		t.Fatalf("Kelvin/k patterns did not preserve one canonical duplicate per original: %q and %q, lengths %v", canonicalKelvin, canonicalK, view.patternLengths)
	}
	got := collect(t, view, "kK")
	want := []struct{ start, pattern, width int }{{0, 0, 1}, {1, 0, 3}}
	if len(got) != len(want) {
		t.Fatalf("Each(kK) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Each(kK)[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	for _, tc := range []struct {
		patterns []string
		want     int
	}{
		{[]string{"k", "ka"}, 3},
		{[]string{"ka", "k"}, 4},
	} {
		prefix := requireView(t, tc.patterns)
		start, pattern, width, ok, err := prefix.Find("Ka")
		if err != nil || !ok || start != 0 || pattern != 0 || width != tc.want {
			t.Fatalf("Find(%q, Ka) = (%d,%d,%d,%t,%v), want (0,0,%d,true,nil)", tc.patterns, start, pattern, width, ok, err, tc.want)
		}
		prefix.Close()
	}

	longS := requireView(t, []string{"ſ"})
	start, pattern, width, ok, err := longS.Find("s")
	if err != nil || !ok || start != 0 || pattern != 0 || width != 1 {
		t.Fatalf("long-s width-changing Find = (%d,%d,%d,%t,%v), want (0,0,1,true,nil)", start, pattern, width, ok, err)
	}
	if got := collect(t, longS, "ſs"); len(got) != 2 || got[0].width != 2 || got[1].width != 1 {
		t.Fatalf("long-s source widths = %v", got)
	}

	sigma := requireView(t, []string{"Σ"})
	if got := collect(t, sigma, "σςΣ"); len(got) != 3 || got[0].width != 2 || got[1].width != 2 || got[2].width != 2 {
		t.Fatalf("sigma orbit spans = %v", got)
	}

	sharpS := requireView(t, []string{"ß"})
	if got := collect(t, sharpS, "ßẞss"); len(got) != 2 || got[0].width != 2 || got[1].width != 3 {
		t.Fatalf("sharp-s must match only its simple-fold orbit, got %v", got)
	}

	normalization := requireView(t, []string{"é"})
	if _, _, _, ok, err := normalization.Find("e\u0301"); err != nil || ok {
		t.Fatalf("simple folding unexpectedly normalized a combining sequence: ok=%t err=%v", ok, err)
	}
}

func TestViewEveryCompleteFoldOrbitMember(t *testing.T) {
	orbits, membersChecked := 0, 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if 0xD800 <= r && r <= 0xDFFF || FoldKey(r) != r {
			continue
		}
		members := []rune{r}
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			members = append(members, x)
		}
		if len(members) == 1 {
			continue
		}
		var haystack strings.Builder
		var want []struct{ start, pattern, width int }
		for i, member := range members {
			if i != 0 {
				haystack.WriteByte('!')
			}
			start := haystack.Len()
			haystack.WriteRune(member)
			want = append(want, struct{ start, pattern, width int }{start, 0, utf8.RuneLen(member)})
		}
		view, err := NewRustACView([]string{string(r)})
		if err != nil {
			t.Fatalf("orbit root %U: %v", r, err)
		}
		got := collect(t, view, haystack.String())
		view.Close()
		if len(got) != len(want) {
			t.Fatalf("orbit root %U yielded %d spans, want %d: %v", r, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("orbit root %U member %U span %v, want %v", r, members[i], got[i], want[i])
			}
		}
		orbits++
		membersChecked += len(members)
	}
	if orbits == 0 || membersChecked == 0 {
		t.Fatalf("checked no nontrivial fold orbits")
	}
	t.Logf("checked %d complete nontrivial simple-fold orbits and %d members", orbits, membersChecked)
}

func TestViewNoMatchDenseStreamsAndExactStops(t *testing.T) {
	view := requireView(t, []string{"K"})
	if start, _, _, ok, err := view.Find("xyz"); err != nil || ok || start != 0 {
		t.Fatalf("no-match Find = (%d,%t,%v)", start, ok, err)
	}
	if got := collect(t, view, "xyz"); len(got) != 0 {
		t.Fatalf("no-match Each = %v", got)
	}
	haystack := strings.Repeat("K", 300)
	all := collect(t, view, haystack)
	if len(all) != 300 {
		t.Fatalf("dense Each returned %d matches, want 300", len(all))
	}
	for _, stop := range []int{1, 256, 257} {
		calls := 0
		complete, err := view.Each(haystack, func(start, pattern, width int) bool {
			calls++
			if start != 3*(calls-1) || pattern != 0 || width != 3 {
				t.Fatalf("stop %d callback %d = (%d,%d,%d)", stop, calls, start, pattern, width)
			}
			return calls < stop
		})
		if err != nil || complete || calls != stop {
			t.Fatalf("stop %d: complete=%t callbacks=%d err=%v", stop, complete, calls, err)
		}
	}
}

func TestViewNestedConcurrentCallsAndClose(t *testing.T) {
	view := requireView(t, []string{"K", "k"})
	nested := false
	complete, err := view.Each("kK", func(start, pattern, width int) bool {
		if !nested {
			nested = true
			if start, pattern, width, ok, err := view.Find("K"); err != nil || !ok || start != 0 || pattern != 0 || width != 3 {
				t.Fatalf("nested Find = (%d,%d,%d,%t,%v)", start, pattern, width, ok, err)
			}
			inner := collect(t, view, "Kk")
			if len(inner) != 2 || inner[0].width != 3 || inner[1].width != 1 {
				t.Fatalf("nested Each = %v", inner)
			}
		}
		return true
	})
	if err != nil || !complete || !nested {
		t.Fatalf("outer nested Each complete=%t nested=%t err=%v", complete, nested, err)
	}

	// Hold an outer Each callback while a second call uses the same matcher.
	// The outer batch buffer and origin map remain live during this controlled
	// interleaving; the inner call must have separate scratch ownership.
	entered, resume := make(chan struct{}), make(chan struct{})
	outerDone := make(chan error, 1)
	go func() {
		var outer []span
		complete, err := view.Each("KkKk", func(start, pattern, width int) bool {
			outer = append(outer, span{start, pattern, width})
			if len(outer) == 1 {
				close(entered)
				<-resume
			}
			return true
		})
		if err != nil {
			outerDone <- err
			return
		}
		if !complete {
			outerDone <- fmt.Errorf("controlled outer Each stopped")
			return
		}
		want := []span{{0, 0, 3}, {3, 0, 1}, {4, 0, 3}, {7, 0, 1}}
		if len(outer) != len(want) {
			outerDone <- fmt.Errorf("controlled outer Each returned %v, want %v", outer, want)
			return
		}
		for i := range want {
			if outer[i] != want[i] {
				outerDone <- fmt.Errorf("controlled outer span %d = %v, want %v", i, outer[i], want[i])
				return
			}
		}
		outerDone <- nil
	}()
	select {
	case <-entered:
	case err := <-outerDone:
		t.Fatalf("outer Each completed before its controlled callback: %v", err)
	}
	inner := collectConcurrent(view, "kK")
	if len(inner) != 2 || inner[0] != (span{0, 0, 1}) || inner[1] != (span{1, 0, 3}) {
		close(resume)
		t.Fatalf("controlled inner Each = %v", inner)
	}
	close(resume)
	if err := <-outerDone; err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	ready := make(chan struct{}, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			for range 16 {
				s, pattern, width, ok, err := view.Find("Kk")
				if err != nil || !ok || s != 0 || pattern != 0 || width != 3 {
					errs <- fmt.Errorf("concurrent Find = (%d,%d,%d,%t,%v)", s, pattern, width, ok, err)
					return
				}
				got := collectConcurrent(view, "Kk")
				if len(got) != 2 || got[0] != (span{0, 0, 3}) || got[1] != (span{3, 0, 1}) {
					errs <- fmt.Errorf("concurrent Each = %v", got)
					return
				}
			}
		}()
	}
	for range workers {
		<-ready
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	view.Close()
	view.Close()
	if _, _, _, _, err := view.Find("k"); err == nil {
		t.Fatal("Find after Close did not report a closed view")
	}
}

type span struct{ start, pattern, width int }

func collectConcurrent(view *RustACView, haystack string) []span {
	var got []span
	complete, err := view.Each(haystack, func(start, pattern, width int) bool {
		got = append(got, span{start, pattern, width})
		return true
	})
	if err != nil || !complete {
		return nil
	}
	return got
}

func TestViewRejectsUnsupportedInputsAsErrors(t *testing.T) {
	for _, patterns := range [][]string{nil, {""}, {string([]byte{0xff})}} {
		if _, err := NewRustACView(patterns); err == nil {
			t.Fatalf("NewRustACView(%q) accepted an empty/invalid pattern set", patterns)
		}
	}
	view := requireView(t, []string{"k"})
	if _, _, _, _, err := view.Find(string([]byte{0xff})); err == nil {
		t.Fatal("Find silently accepted malformed input")
	}
	if _, err := view.Each(string([]byte{0xff}), func(int, int, int) bool { return true }); err == nil {
		t.Fatal("Each silently accepted malformed input")
	}
}
