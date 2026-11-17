package main

import (
	"strings"
	"testing"
	"time"
)

// Adversarial review of slice w2a: each test here fails against 562b822 and
// names a real break. Synthetic data only.

// The shipped sample's own claims (examples/, clmShelfSearch and clmSkyCalls)
// are before/after figures with a derived percentage. The percentage is
// computed, so no source line carries it: a truthful blind verifier quotes the
// before and after lines and gives found_value with the figure the claim
// states. quoteHasFigure demands the derived "-80%" in the quote too, so the
// sample's claims can never reach CONFIRMED in pass 2.
func TestW2AAdvDerivedPercentConfirmsOnSampleSources(t *testing.T) {
	root := sampleCopy(t)
	for _, tc := range []struct{ id, value, src, quote string }{
		{"clmShelfSearch", "470 → 94 ms (-80%)", "projects/shelfwise/bench.txt", "before (table scan):  p95 470 ms\nafter (title index):  p95 94 ms"},
		{"clmSkyCalls", "12 → 3 calls (-75%)", "projects/skycast/calls.txt", "before (one call per panel): 12 calls\nafter (one batched call per city group): 3 calls"},
	} {
		claims := map[string]*Claim{tc.id: {ID: tc.id, Value: tc.value}}
		ws := &Worksheet{Items: []*WorkItem{{Kind: "figure", Claim: tc.id, SentenceHash: "h", Answer: map[string]any{
			"found_value": tc.value, "quote": tc.quote, "source_path": tc.src, "scope_ok": true}}}}
		old := codeRootFlag
		codeRootFlag = root
		add := VerifyLog{}
		ingestWorksheet(ws, "blind-1", claims, add)
		codeRootFlag = old
		if len(add[tc.id]) != 1 {
			t.Fatalf("%s: no verdict recorded", tc.id)
		}
		if v := add[tc.id][0].Verdict; v != "CONFIRMED" {
			t.Errorf("%s: a verbatim quote of the before and after lines gives %s (%s), want CONFIRMED",
				tc.id, v, add[tc.id][0].Evidence)
		}
	}
}

// Spaced word multipliers: "1.2 million" in source prose is the claim's
// "1.2M". Before the unit table the digits matched (10df9f9 returns true for
// all of these); now a multiplier is only read when it touches the number, so
// the same value reads as 1.2 against 1,200,000.
func TestW2AAdvSpacedWordMultiplier(t *testing.T) {
	for _, tc := range []struct{ claim, source string }{
		{"1.2M", "1.2 million"},
		{"1.2 million", "1.2M"},
		{"3k", "3 thousand"},
		{"$2.5B", "$2.5 billion"},
	} {
		if !sameNumbers(tc.claim, tc.source) {
			t.Errorf("sameNumbers(%q, %q) = false: one value written two ways", tc.claim, tc.source)
		}
	}
	if !quoteHasFigure("the table holds 1.2 million rows", "1.2M rows") {
		t.Errorf("quoteHasFigure: 1.2 million rows does not carry 1.2M")
	}
}

// figuresIn re-scans the whole remainder of the string for every number it
// finds (unitTail.Replace(s[loc[1]:])): quadratic. 40,000 numbers (80 KB, a
// pasted quote or a verifier's found_value) take seconds; the old numbersIn
// took milliseconds.
func TestW2AAdvFiguresInIsLinear(t *testing.T) {
	s := strings.Repeat("1 ", 40000)
	st := time.Now()
	n := len(figuresIn(s))
	if d := time.Since(st); d > time.Second {
		t.Errorf("figuresIn on %d numbers (%d bytes) took %v; want well under 1s (linear)", n, len(s), d)
	}
}
