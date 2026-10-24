package kbsync

import (
	"fmt"
	"strings"
	"testing"
)

// Red-team tests for the eligibility rule (eligible.go). Each FAIL-marked test
// demonstrates a divergence from rkb's final gate (rkb/lint.go:444-485) or from
// rkb's selection reader (rkb/bank.go:607-698) that lets a bullet through (or
// drops one) that rkb itself would not.

func rtBank(text string, reviews map[string][]Review) (Bank, Claims) {
	c := Claims{Values: map[string]string{}, Caution: map[string]bool{}}
	b := Bank{
		Entries: map[string]Entry{"proj": {Title: "Proj", Bullets: []string{"b1"}}},
		Bullets: map[string]Bullet{"b1": {Text: text, Review: reviews}},
	}
	return b, c
}

func rtSelect() []Selection {
	return []Selection{sel(SelEntry{Entry: "proj", Bullets: []string{"b1"}})}
}

// rkb's round-2 rule: the reviewer must be named, must not be "drafter", must
// not be the bullet's author, and one reviewer must not play both HR and HM
// (lint.go:478-485). eligible.go reads only verdict and text_hash.
func TestRedteamEligibleReviewerIndependence(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	for name, rv := range map[string]map[string][]Review{
		"empty by": {
			"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: ""}},
			"hm": {{Round: 2, Verdict: "pass", TextHash: h, By: ""}},
		},
		"drafter reviews itself": {
			"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "drafter"}},
			"hm": {{Round: 2, Verdict: "pass", TextHash: h, By: "drafter"}},
		},
		"one reviewer plays hr and hm": {
			"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "same-reader"}},
			"hm": {{Round: 2, Verdict: "pass", TextHash: h, By: "same-reader"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, c := rtBank(text, rv)
			el := Eligible(b, c, rtSelect())
			if el.Bullets["b1"] {
				t.Fatalf("FAIL: rkb lint.go:478-485 rejects this review set (no independent reviewer), Eligible accepts it")
			}
		})
	}
}

// A bullet cannot be reviewed by its author (rkb lint.go:478, 786). Vani's
// Bullet drops "author" on parse, so the check cannot be made.
func TestRedteamEligibleSelfReviewByAuthor(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	bank := fmt.Sprintf(`{"entries":{"proj":{"title":"Proj","bullets":["b1"]}},"bullets":{"b1":{"text":%q,"author":"alice",
	  "review":{"hr":[{"round":2,"verdict":"pass","text_hash":%q,"by":"alice"}],"hm":[{"round":2,"verdict":"pass","text_hash":%q,"by":"bob"}]}}}}`, text, h, h)
	b, err := Parse([]byte(bank))
	if err != nil {
		t.Fatal(err)
	}
	el := Eligible(b, Claims{Values: map[string]string{}, Caution: map[string]bool{}}, rtSelect())
	if el.Bullets["b1"] {
		t.Fatal("FAIL: the HR pass was written by the bullet's own author (rkb lint.go:478 refuses it); Eligible accepts it")
	}
}

// rkb requires BOTH rounds: a round-1 pass AND a round-2 pass on the current
// text (lint.go:449-476). eligible.go accepts a single pass of any round.
func TestRedteamEligibleNeedsTwoPasses(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	for name, rv := range map[string]map[string][]Review{
		"round 1 only": {
			"hr": {{Round: 1, Verdict: "pass", TextHash: h, By: "hr-a"}},
			"hm": {{Round: 1, Verdict: "pass", TextHash: h, By: "hm-a"}},
		},
		"round 2 only": {
			"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "hr-a"}},
			"hm": {{Round: 2, Verdict: "pass", TextHash: h, By: "hm-a"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, c := rtBank(text, rv)
			if Eligible(b, c, rtSelect()).Bullets["b1"] {
				t.Fatal("FAIL: rkb lint.go:474 needs a round-1 pass and a round-2 pass on the current wording; Eligible accepts one pass")
			}
		})
	}
}

// rkb infers a section's kind when it is omitted (bank.go:611-621) and shows
// ALL of an entry's bank bullets when a selection entry omits "bullets"
// (bank.go:692-695). eligible.go reads neither: such a selection lists nothing,
// so under `kb sync --replace` the entry's DevPrism block is an orphan and is
// deleted although the owner's resume shows it.
func TestRedteamSelectionDefaultsAreRkbs(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	b, c := rtBank(text, passBoth(h))
	for name, raw := range map[string]string{
		"kind omitted":              `{"sections":[{"title":"Experience","entries":[{"entry":"proj","bullets":["b1"]}]}]}`,
		"entry bullets omitted":     `{"sections":[{"title":"Experience","kind":"entries","entries":[{"entry":"proj"}]}]}`,
		"kind and bullets omitted":  `{"sections":[{"title":"Experience","entries":[{"entry":"proj"}]}]}`,
		"summary kind omitted":      `{"sections":[{"title":"Summary","bullets":["b1"]}]}`,
		"entry bullets explicit []": `{"sections":[{"title":"Experience","kind":"entries","entries":[{"entry":"proj","bullets":[]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := ParseSelection([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			el := Eligible(b, c, []Selection{s})
			if name == "entry bullets explicit []" {
				// rkb: an explicit empty list shows no bullets. Must stay ineligible.
				if el.Bullets["b1"] {
					t.Fatal("an explicit empty bullet list lists nothing in rkb")
				}
				return
			}
			if !el.Bullets["b1"] {
				t.Fatalf("FAIL: rkb shows b1 on this resume (selection defaults, bank.go:611-621, 692-695); Eligible lists it as nothing (why %q)", el.Why["b1"])
			}
			if name == "kind and bullets omitted" || name == "entry bullets omitted" {
				p, err := ResolvePool(b, c, []Selection{s})
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := blockByID(p.Resolved, "proj"); !ok {
					t.Fatalf("FAIL: the pool drops proj, so --replace deletes DevPrism's copy: withheld %+v", p.Withheld)
				}
			}
		})
	}
}

// Probes that hold: each of these must stay ineligible.
func TestRedteamEligibleHeldCases(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	other := reviewHash("**Built X:** another sentence.", nil)
	cases := map[string]map[string][]Review{
		"hash with trailing garbage":       pass2(h + "zz"),
		"hash longer than sha1":            pass2(h + strings.Repeat("0", 40)),
		"eleven hex characters":            pass2(h[:11]),
		"verdict Pass":                     {"hr": {{Round: 2, Verdict: "Pass", TextHash: h, By: "a"}}, "hm": {{Round: 2, Verdict: "Pass", TextHash: h, By: "b"}}},
		"verdict pass with space":          {"hr": {{Round: 2, Verdict: " pass", TextHash: h, By: "a"}}, "hm": {{Round: 2, Verdict: "pass ", TextHash: h, By: "b"}}},
		"hash of a different wording":      pass2(other),
		"hm missing":                       {"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "a"}}},
		"reader key HR (upper)":            {"HR": {{Round: 2, Verdict: "pass", TextHash: h, By: "a"}}, "HM": {{Round: 2, Verdict: "pass", TextHash: h, By: "b"}}},
		"pass then revise on same wording": {"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "a"}, {Round: 2, Verdict: "revise", TextHash: h, By: "a2"}}, "hm": {{Round: 2, Verdict: "pass", TextHash: h, By: "b"}}},
		"revise recorded later withdraws":  {"hr": {{Round: 2, Verdict: "pass", TextHash: h, By: "a"}, {Round: 3, Verdict: "revise", TextHash: strings.ToUpper(h), By: "a2"}}, "hm": {{Round: 2, Verdict: "pass", TextHash: h, By: "b"}}},
	}
	for name, rv := range cases {
		t.Run(name, func(t *testing.T) {
			b, c := rtBank(text, rv)
			if Eligible(b, c, rtSelect()).Bullets["b1"] {
				t.Fatalf("eligible, want held")
			}
		})
	}
}

func pass2(h string) map[string][]Review {
	return map[string][]Review{
		"hr": {{Round: 1, Verdict: "pass", TextHash: h, By: "hr-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hr-a"}},
		"hm": {{Round: 1, Verdict: "pass", TextHash: h, By: "hm-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hm-a"}},
	}
}

// A later pass after a revise on the same wording restores eligibility (the
// latest verdict wins). Pins the direction so a "withdraw forever" change is a
// decision, not an accident.
func TestRedteamEligibleLatestVerdictWins(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	rv := map[string][]Review{
		"hr": {{Round: 1, Verdict: "pass", TextHash: h, By: "a1"}, {Round: 2, Verdict: "revise", TextHash: h, By: "a"}, {Round: 2, Verdict: "pass", TextHash: h, By: "a"}},
		"hm": {{Round: 1, Verdict: "pass", TextHash: h, By: "b1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "b"}},
	}
	b, c := rtBank(text, rv)
	if !Eligible(b, c, rtSelect()).Bullets["b1"] {
		t.Fatal("a pass recorded after a revise on the same wording is the latest verdict")
	}
}

// A selection that lists a bullet under one entry must not make it eligible for
// a summary slot, nor the reverse.
func TestRedteamEligibleSummaryAndEntryDoNotLeakIntoEachOther(t *testing.T) {
	const text = "**Built X:** a plain sentence."
	h := reviewHash(text, nil)
	b, c := rtBank(text, passBoth(h))
	b.Entries["proj2"] = Entry{Title: "Proj2", Bullets: []string{"b1"}}
	entryOnly := Eligible(b, c, rtSelect())
	if entryOnly.Allowed("", "b1") || entryOnly.Allowed("proj2", "b1") {
		t.Fatal("an entry listing leaked to the summary or to another entry")
	}
	summaryOnly := Eligible(b, c, []Selection{{Sections: []SelSection{{Kind: "bullets", Bullets: []string{"b1"}}}}})
	if summaryOnly.Allowed("proj", "b1") {
		t.Fatal("a summary listing leaked to an entry")
	}
	rb := summaryOnly.Restrict(b)
	if len(rb.Entries) != 0 || len(rb.Bullets) != 0 {
		t.Fatalf("Restrict keeps a summary-only bullet: %+v", rb)
	}
}

// A huge selection and many sections must not blow up the rule.
func TestRedteamEligibleHugeSelectionIsBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"sections":[{"kind":"bullets","bullets":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"b%d"`, i)
	}
	sb.WriteString(`]}]}`)
	if sb.Len() > MaxSelectionBytes {
		t.Skipf("fixture is %d bytes, over the cap, so it is refused earlier", sb.Len())
	}
	s, err := ParseSelection([]byte(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	b, c := rtBank("x", nil)
	el := Eligible(b, c, []Selection{s})
	if len(el.Bullets) != 0 || len(el.Why) != 20000 {
		t.Fatalf("eligible %d, why %d", len(el.Bullets), len(el.Why))
	}
}
