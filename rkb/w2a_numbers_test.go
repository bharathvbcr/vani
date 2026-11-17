package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW2ASameNumbersUnits(t *testing.T) {
	for _, tc := range []struct {
		claim, source string
		want          bool
		note          string
	}{
		// false matches before the fix: digits only, unit ignored
		{"40 ms", "40%", false, "time vs percent"},
		{"3x", "3 hours", false, "multiple vs duration"},
		{"1.5 ms", "1.5 s", false, "milliseconds vs seconds"},
		{"40%", "40 ms", false, "percent vs time"},
		{"5 pp", "5%", false, "percentage points vs percent"},
		{"$40", "40%", false, "currency vs percent"},
		{"2x", "2 h", false, "multiple vs hours"},
		// still equal
		{"1,200", "1200", true, "thousands separator"},
		{"1.2k", "1200", true, "k multiplier, claim side"},
		{"1200", "1.2k", true, "k multiplier, source side"},
		{"1,200", "1.2k", true, "separator against multiplier"},
		{"2.5M", "2,500,000", true, "M multiplier"},
		{"1.5 s", "1500 ms", true, "same duration in two units"},
		{"90 s", "1.5 min", true, "seconds against minutes"},
		{"40 ms", "40 ms", true, "identical"},
		{"40%", "0.4", true, "percent written as a fraction"},
		{"3x", "3×", true, "x against the times sign"},
		{"94 ms", "94", true, "an unstated unit does not contradict"},
		{"12 → 3 calls (-75%)", "12 → 3 calls (-75%)", true, "the sample claim"},
		{"470 → 94 ms (-80%)", "470 ms → 94 ms (-80%)", true, "unit on every number"},
		// rounding keeps its rule: a claim may round, never inflate
		{"1.2k", "1,234", true, "rounded to the claim's precision"},
		{"1.3k", "1,234", false, "inflated"},
		// adversarial
		{"", "", true, "no numbers on either side"},
		{"", "40", false, "claim has none"},
		{"40", "", false, "source has none"},
		{"-5%", "5%", false, "negative against positive"},
		{"-5%", "-5%", true, "negative"},
		{"3.1×10⁻⁵ s", "3.1e-5 s", true, "scientific notation"},
		{"3.1×10⁻⁵ s", "3.1e-5 ms", false, "scientific notation, other unit"},
		{"١٢٣", "123", false, "Unicode digits are not numbers"},
		{"99999999999999999999999999 ms", "99999999999999999999999999 ms", true, "huge"},
		{"99999999999999999999999999 ms", "99999999999999999999999999 s", false, "huge, other unit"},
		{"1E999", "1E999", false, "overflow cannot be verified"},
		{"40 furlongs", "40%", true, "an unknown unit word is not a unit"},
		{"5m", "5 s", true, "lower-case m is not a million, and not a known unit"},
	} {
		if got := sameNumbers(tc.claim, tc.source); got != tc.want {
			t.Errorf("sameNumbers(%q, %q) = %v, want %v (%s)", tc.claim, tc.source, got, tc.want, tc.note)
		}
	}
}

func TestW2AQuoteHasFigure(t *testing.T) {
	for _, tc := range []struct {
		quote, found string
		want         bool
	}{
		{"p95 latency is 94 ms after the index", "94 ms", true},
		{"p95 latency is 94 ms after the index", "94%", false},
		{"p95 latency is 94 ms; the cache hit rate is 40%", "40 ms", false},
		{"| p95 | 94 |", "94 ms", false},
		{"| p95 | 94 |", "94", true},
		{"latency fell from 470 ms to 94 ms (-80%)", "470 → 94 ms (-80%)", true},
		// the percentage is computed from the two quoted figures: no line carries it
		{"latency fell from 470 ms to 94 ms", "470 → 94 ms (-80%)", true},
		{"month before reminders: 18% of loans returned late\nmonth after reminders: 7% of loans returned late", "18% → 7% of loans", true},
		{"month after reminders: 7% of loans returned late", "18% → 7% of loans", false},
		{"before 12 calls; after 3 calls", "12 → 3 calls (-75%)", true},
		// a stated percentage that is not the arithmetic of the quoted pair is not exempt
		{"latency fell from 470 ms to 94 ms", "470 → 94 ms (-50%)", false},
		// both operands must be in the quote
		{"latency is now 94 ms", "470 → 94 ms (-80%)", false},
		{"latency was 470 ms", "470 → 94 ms (-80%)", false},
		// a quoted pair does not excuse a percentage it does not produce, and a
		// computed percentage never excuses an operand that is missing
		{"after: 3 calls", "12 → 3 calls (-75%)", false},
		{"1,200 requests per run", "1.2k", true},
		{"no figure here at all", "94 ms", false},
		{"anything", "no number", true},
	} {
		if got := quoteHasFigure(tc.quote, tc.found); got != tc.want {
			t.Errorf("quoteHasFigure(%q, %q) = %v, want %v", tc.quote, tc.found, got, tc.want)
		}
	}
}

// A quote that holds the digits but not the figure with its unit must not
// confirm the blind pass, even though the verifier's found value matches.
func TestW2AIngestRefusesQuoteWithoutTheFigure(t *testing.T) {
	root, _ := w2aEnv(t)
	src := filepath.Join(root, "projects", "x", "bench.txt")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("run 7: the cache answered 40 ms at median across all of the runs\nrun 8: cache hit rate was 40% across all of the runs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claims := map[string]*Claim{"clmHit": {ID: "clmHit", Value: "40%"}}
	ask := func(quote string) string {
		ws := &Worksheet{Items: []*WorkItem{{Kind: "figure", Claim: "clmHit", SentenceHash: "h", Answer: map[string]any{
			"found_value": "40%", "quote": quote, "source_path": "projects/x/bench.txt", "scope_ok": true}}}}
		add := VerifyLog{}
		ingestWorksheet(ws, "blind-1", claims, add)
		if len(add["clmHit"]) != 1 {
			t.Fatalf("no verdict recorded for %q", quote)
		}
		return add["clmHit"][0].Verdict
	}
	if v := ask("run 7: the cache answered 40 ms at median across all of the runs"); v != "QUOTE_MISMATCH" {
		t.Errorf("quote showing 40 ms for a 40%% claim: verdict %s, want QUOTE_MISMATCH", v)
	}
	if v := ask("run 8: cache hit rate was 40% across all of the runs"); v != "CONFIRMED" {
		t.Errorf("quote showing 40%%: verdict %s, want CONFIRMED", v)
	}
	if v := ask("run 7: the cache answered 40 ms at median across all of the runs"); strings.HasPrefix(v, "CONF") {
		t.Errorf("verdict %s", v)
	}
}
