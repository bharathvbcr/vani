package kbsync

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewHash(text string, claims map[string]string) string {
	s := sha1.Sum([]byte(rkbPlain(text, claims)))
	return hex.EncodeToString(s[:])[:12]
}

func passBoth(h string) map[string][]Review {
	return map[string][]Review{
		"hr": {{Round: 1, Verdict: "pass", TextHash: h, By: "hr-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hr-r2"}},
		"hm": {{Round: 1, Verdict: "pass", TextHash: h, By: "hm-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hm-r2"}},
	}
}

// eligBank: one entry with a reviewed bullet, a CAUTION bullet, a cleared
// CAUTION bullet, an unreviewed bullet, a reworded bullet, and a revised one.
func eligBank(t *testing.T) (Bank, Claims) {
	t.Helper()
	c := Claims{
		Values:  map[string]string{"clmGood": "3.1x", "clmRag": "92%"},
		Caution: map[string]bool{"clmRag": true},
	}
	good := "**Built X:** cut latency {{clmGood}} with [Tool](https://example.com/tool)."
	rag := "**Built retrieval** at {{clmRag}} recall."
	cleared := "**Built retrieval v2** at {{clmRag}} recall on the pinned set."
	reworded := "**Built Y** faster than before."
	revised := "**Built Z** for the team."
	b := Bank{
		Entries: map[string]Entry{
			"proj":  {Title: "Proj", Bullets: []string{"good", "rag", "cleared", "unreviewed", "reworded", "revised"}},
			"other": {Title: "Other", Bullets: []string{"lonely"}},
		},
		Bullets: map[string]Bullet{
			"good":       {Text: good, Review: passBoth(reviewHash(good, c.Values))},
			"rag":        {Text: rag, Review: passBoth(reviewHash(rag, c.Values))},
			"cleared":    {Text: cleared, CautionOK: map[string]string{"clmRag": "re-measured 2026-10-22"}, Review: passBoth(reviewHash(cleared, c.Values))},
			"unreviewed": {Text: "**Built W** alone."},
			"reworded":   {Text: reworded, Review: passBoth(reviewHash("**Built Y** before the rewrite.", c.Values))},
			"revised": {Text: revised, Review: map[string][]Review{
				"hr": {{Round: 1, Verdict: "pass", TextHash: reviewHash(revised, c.Values), By: "hr-r1"}, {Round: 2, Verdict: "pass", TextHash: reviewHash(revised, c.Values), By: "hr-r2"}},
				"hm": {
					{Round: 1, Verdict: "pass", TextHash: reviewHash(revised, c.Values)},
					{Round: 2, Verdict: "revise", TextHash: reviewHash(revised, c.Values)},
				},
			}},
			"lonely": {Text: "**Built L.**", Review: passBoth(reviewHash("**Built L.**", c.Values))},
		},
	}
	return b, c
}

func sel(entries ...SelEntry) Selection {
	return Selection{Sections: []SelSection{{Kind: "entries", Entries: entries}}}
}

func TestEligibleRules(t *testing.T) {
	b, c := eligBank(t)
	el := Eligible(b, c, []Selection{sel(SelEntry{Entry: "proj", Bullets: []string{"good", "rag", "cleared", "unreviewed", "reworded", "revised"}})})
	want := map[string]bool{"good": true, "cleared": true}
	for _, id := range b.Entries["proj"].Bullets {
		if el.Bullets[id] != want[id] {
			t.Errorf("%s eligible=%v, want %v (why %q)", id, el.Bullets[id], want[id], el.Why[id])
		}
	}
	for id, frag := range map[string]string{"rag": "CAUTION", "unreviewed": "has not passed", "reworded": "has not passed", "revised": "HM reader"} {
		if !strings.Contains(el.Why[id], frag) {
			t.Errorf("%s why = %q, want it to mention %q", id, el.Why[id], frag)
		}
	}
	if el.Bullets["lonely"] || el.Why["lonely"] != "" {
		t.Errorf("a bullet no selection lists must be neither eligible nor judged: %v %q", el.Bullets["lonely"], el.Why["lonely"])
	}
	if got := el.EntryBullets["proj"]; strings.Join(got, ",") != "good,cleared" {
		t.Errorf("EntryBullets[proj] = %v, want [good cleared] in selection order", got)
	}
	if !el.Allowed("proj", "good") || el.Allowed("other", "good") || el.Allowed("", "good") {
		t.Error("Allowed must bind a bullet to the entry it was selected under")
	}
}

func TestEligibleUnresolvedClaim(t *testing.T) {
	b, c := eligBank(t)
	delete(c.Values, "clmGood")
	el := Eligible(b, c, []Selection{sel(SelEntry{Entry: "proj", Bullets: []string{"good"}})})
	if el.Bullets["good"] || !strings.Contains(el.Why["good"], "no value") {
		t.Fatalf("a bullet with an unresolved claim must be ineligible: %v %q", el.Bullets["good"], el.Why["good"])
	}
}

func TestEligibleSelectionMismatches(t *testing.T) {
	b, c := eligBank(t)
	el := Eligible(b, c, []Selection{sel(
		SelEntry{Entry: "missing", Bullets: []string{"good"}},
		SelEntry{Entry: "other", Bullets: []string{"good", "ghost"}},
	)})
	if el.Bullets["good"] {
		t.Error("a bullet selected only under an entry that does not list it must not be eligible")
	}
	if !strings.Contains(el.Why["ghost"], "not in the bank") {
		t.Errorf("ghost why = %q", el.Why["ghost"])
	}
	// A later selection that lists it correctly makes it eligible; order does not matter.
	el = Eligible(b, c, []Selection{
		sel(SelEntry{Entry: "other", Bullets: []string{"good"}}),
		sel(SelEntry{Entry: "proj", Bullets: []string{"good", "good"}}),
	})
	if !el.Bullets["good"] || el.Why["good"] != "" {
		t.Errorf("good should be eligible once a selection lists it under its entry: why %q", el.Why["good"])
	}
	if got := el.EntryBullets["proj"]; len(got) != 1 {
		t.Errorf("a duplicate selection of one bullet must appear once, got %v", got)
	}
}

func TestEligibleShortOrEmptyHashNeverMatches(t *testing.T) {
	b, c := eligBank(t)
	bl := b.Bullets["unreviewed"]
	bl.Review = map[string][]Review{
		"hr": {{Verdict: "pass", TextHash: ""}},
		"hm": {{Verdict: "pass", TextHash: reviewHash(bl.Text, c.Values)[:4]}},
	}
	b.Bullets["unreviewed"] = bl
	el := Eligible(b, c, []Selection{sel(SelEntry{Entry: "proj", Bullets: []string{"unreviewed"}})})
	if el.Bullets["unreviewed"] {
		t.Fatal("an empty or 4-character text_hash must not count as a pass on this wording")
	}
}

func TestEligibleSummarySection(t *testing.T) {
	b, c := eligBank(t)
	s := Selection{Sections: []SelSection{{Kind: "bullets", Bullets: []string{"good", "rag"}}}}
	el := Eligible(b, c, []Selection{s})
	if strings.Join(el.Summary, ",") != "good" || !el.Allowed("", "good") || el.Allowed("", "rag") {
		t.Fatalf("summary = %v", el.Summary)
	}
}

func TestParseSelectionAdversarial(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":        ``,
		"array":        `[]`,
		"no sections":  `{"bank":"../bank.json"}`,
		"dup key":      `{"sections":[],"sections":[{"kind":"bullets","bullets":["a"]}]}`,
		"bad id":       `{"sections":[{"kind":"bullets","bullets":["a b"]}]}`,
		"bad entry id": `{"sections":[{"kind":"entries","entries":[{"entry":"../x","bullets":["a"]}]}]}`,
		"trailing":     `{"sections":[{"kind":"bullets","bullets":["a"]}]} {}`,
		"wrong type":   `{"sections":[{"kind":"bullets","bullets":"a"}]}`,
	} {
		if _, err := ParseSelection([]byte(raw)); err == nil {
			t.Errorf("%s: ParseSelection accepted %q", name, raw)
		}
	}
	big := `{"sections":[{"kind":"bullets","bullets":["` + strings.Repeat("a", MaxSelectionBytes) + `"]}]}`
	if _, err := ParseSelection([]byte(big)); err == nil {
		t.Error("an oversized selection must be refused")
	}
	s, err := ParseSelection([]byte(`{"bank":"../bank.json","sections":[{"title":"X","kind":"lines","lines":[{"a":1}]},{"kind":"entries","entries":[{"entry":"e","bullets":["b"],"title":"T"}]}]}`))
	if err != nil || len(s.Sections) != 2 || s.Sections[1].Entries[0].Bullets[0] != "b" {
		t.Fatalf("a valid selection with lines and overrides: %+v %v", s, err)
	}
}

// TestRkbPlainMatchesRkbOrder pins the order rkb strips markup in: a link's
// target is dropped (ResolveText keeps it for DevPrism).
func TestRkbPlainMatchesRkbOrder(t *testing.T) {
	got := rkbPlain("**Lead {{clmA}}:** see [*Docs*](https://x.dev/docs) and *this*.", map[string]string{"clmA": "2x"})
	if want := "Lead 2x: see Docs and this."; got != want {
		t.Fatalf("rkbPlain = %q, want %q", got, want)
	}
}

// TestRealBankFinalizedResumeIsEligible is the parity oracle: every bullet on
// the resume rkb finalize last passed (its full final gate) must be eligible
// under this rule with freshly exported claims. Opt-in like TestRealBankCounts
// (KBSYNC_REAL_BANK=1, KBSYNC_BANK, KBSYNC_RKB, KBSYNC_CODE_ROOT, and
// KBSYNC_FINAL_SELECTION naming that resume's selection). Logs counts only.
func TestRealBankFinalizedResumeIsEligible(t *testing.T) {
	if os.Getenv("KBSYNC_REAL_BANK") != "1" || os.Getenv("KBSYNC_FINAL_SELECTION") == "" {
		t.Skip("set KBSYNC_REAL_BANK=1 and KBSYNC_FINAL_SELECTION (plus KBSYNC_BANK, KBSYNC_RKB, KBSYNC_CODE_ROOT)")
	}
	b, err := Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := RKB{Binary: os.Getenv("KBSYNC_RKB"), CodeRoot: os.Getenv("KBSYNC_CODE_ROOT")}.Claims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSelection(filepath.Clean(os.Getenv("KBSYNC_FINAL_SELECTION")))
	if err != nil {
		t.Fatal(err)
	}
	el := Eligible(b, c, []Selection{s})
	if n := len(el.Why); n != 0 {
		// A wording the readers never passed (a reworded bullet, or a claim whose
		// value changed since finalize) fails rkb's final gate as much as this
		// rule: that is a stale bank, not a disagreement. Reasons hold no text.
		reasons := map[string]int{}
		stale := 0
		for _, why := range el.Why {
			reasons[why]++
			if strings.Contains(why, "has not passed this wording") {
				stale++
			}
		}
		if stale == n {
			t.Fatalf("%d bullets of the finalized resume are ineligible (ids withheld) because their wording changed since finalize; rkb's final gate refuses them too, so they need a fresh round-2 review: %v", n, reasons)
		}
		t.Fatalf("%d bullets of the finalized resume are ineligible (ids withheld); unless the bank changed since finalize, the rule disagrees with rkb's final gate: %v", n, reasons)
	}
	t.Logf("finalized resume: %d bullets, all eligible", len(el.Bullets))
}
