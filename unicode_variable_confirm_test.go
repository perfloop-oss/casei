package casei

import (
	"strings"
	"testing"
)

func variableConfirmRenderings(forms [][]string, at int, prefix string, out *[]string) {
	if at == len(forms) {
		*out = append(*out, prefix)
		return
	}
	for _, form := range forms[at] {
		variableConfirmRenderings(forms, at+1, prefix+form, out)
	}
}

func TestUnicodePairVariableConfirm(t *testing.T) {
	const needle = "Шерлок Холмс"
	plan := newSearchPlan([]string{needle}, false)
	confirm := plan.unicodePairConfirm()
	if plan.unicodePairs[0].pairPair.valid == 0 || !confirm.valid() || !confirm.variable() {
		t.Fatalf("variable confirmation was not compiled: pair=%+v confirm=%x", plan.unicodePairs[0].pairPair, string(confirm))
	}
	if confirm.minLength() >= confirm.maxLength() {
		t.Fatalf("confirmation bounds = [%d,%d], want a width-changing range", confirm.minLength(), confirm.maxLength())
	}

	forms, _ := patternRawForms(needle)
	var renderings []string
	variableConfirmRenderings(forms, 0, "", &renderings)
	for _, rendering := range renderings {
		width, ok := confirm.matchWidthAt(rendering, 0)
		if !ok || width != len(rendering) || !plan.matchesSingleAt(rendering, 0) {
			t.Fatalf("confirmation rejected rendering %x", rendering)
		}
	}
	nearMiss := strings.TrimSuffix(needle, "с") + "я"
	if _, ok := confirm.matchWidthAt(nearMiss, 0); ok {
		t.Fatalf("confirmation accepted near miss %x", nearMiss)
	}

	matcher := NewMatcher([]string{needle})
	anchor := &plan.unicodePairs[0]
	for _, offset := range []int{0, 1, 63, 64, 127, 128, 4095} {
		for _, rendering := range renderings {
			haystack := strings.Repeat("x", offset) + rendering + strings.Repeat("x", 256)
			got, width, ok := plan.findWithWidth(haystack)
			wantWidth := 0
			if plan.chooseUnicodePairAnchor(haystack) != nil && unicodePairConfirmVectorEnabled() {
				wantWidth = len(rendering)
			}
			if !ok || got != (Match{Pattern: 0, Start: offset}) || width != wantWidth {
				t.Fatalf("offset %d rendering %x: findWithWidth=%+v,%d,%t", offset, rendering, got, width, ok)
			}
			// findUnicodePairConfirm is the VBMI route selected by
			// findUnicodePairAnchor. Do not call it directly on hosts that
			// cannot execute its full-block kernel.
			if unicodePairConfirmVectorEnabled() {
				got, width, ok = plan.findUnicodePairConfirm(haystack, anchor)
				if !ok || got != (Match{Pattern: 0, Start: offset}) || width != len(rendering) {
					t.Fatalf("offset %d rendering %x: direct confirmation=%+v,%d,%t", offset, rendering, got, width, ok)
				}
			}
			calls := 0
			if completed := matcher.Each(haystack, func(match Match, width int) bool {
				calls++
				if match != (Match{Pattern: 0, Start: offset}) || width != len(rendering) {
					t.Errorf("offset %d rendering %x: Each yielded %+v,%d", offset, rendering, match, width)
				}
				return true
			}); !completed || calls != 1 {
				t.Fatalf("offset %d rendering %x: Each completed=%t calls=%d", offset, rendering, completed, calls)
			}
		}
	}
	// A conservative pair-pair survivor is not a match. Confirmation must
	// continue within the same vector block and return the later exact one.
	late := nearMiss + "x" + renderings[len(renderings)-1]
	wantStart := len(nearMiss) + 1
	if got, width, ok := plan.findUnicodePairConfirm(late, anchor); !ok || got != (Match{Pattern: 0, Start: wantStart}) || width != len(renderings[len(renderings)-1]) {
		t.Fatalf("false candidate then match: direct confirmation=%+v,%d,%t", got, width, ok)
	}

	// A shorter rendering can begin after the final max-width-safe vector
	// start; the scalar tail must still inspect it.
	shortest := renderings[0]
	for _, rendering := range renderings[1:] {
		if len(rendering) < len(shortest) {
			shortest = rendering
		}
	}
	haystack := strings.Repeat("x", 4096) + shortest
	wantWidth := 0
	if unicodePairConfirmVectorEnabled() {
		wantWidth = len(shortest)
	}
	if got, width, ok := plan.findWithWidth(haystack); !ok || got != (Match{Pattern: 0, Start: 4096}) || width != wantWidth {
		t.Fatalf("short tail findWithWidth=%+v,%d,%t", got, width, ok)
	}
	if got, width, ok := plan.findUnicodePairConfirm(shortest, anchor); !ok || got != (Match{Pattern: 0}) || width != len(shortest) {
		t.Fatalf("bare short rendering: direct confirmation=%+v,%d,%t", got, width, ok)
	}
	if got, ok := matcher.Find(shortest); !ok || got != (Match{Pattern: 0}) {
		t.Fatalf("bare short rendering: Find=%+v,%t", got, ok)
	}
}

func TestUnicodePairVariableConfirmRejectsMalformedPattern(t *testing.T) {
	const needle = "Шер\x80лок Холмс"
	plan := newSearchPlan([]string{needle}, false)
	if plan.unicodePairs[0].pairPair.valid == 0 {
		t.Fatal("fixture did not compile a pair-pair screen")
	}
	if confirm := plan.unicodePairConfirm(); confirm.valid() {
		t.Fatalf("malformed pattern compiled a variable confirmation: %x", string(confirm))
	}
	haystack := strings.Repeat("x", 97) + strings.ToUpper("Шер") + "\x80" + strings.ToUpper("лок Холмс")
	if got, ok := plan.find(haystack); !ok || got != (Match{Pattern: 0, Start: 97}) {
		t.Fatalf("fallback Find=%+v,%t", got, ok)
	}
}
