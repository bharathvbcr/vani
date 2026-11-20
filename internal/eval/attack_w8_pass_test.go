package eval

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// Attack: Merge orderings around an audit answer and a triage label.
func TestAttackMergeOrderPermutations(t *testing.T) {
	h1, h2, h3 := HashText("t1"), HashText("t2"), HashText("t3")
	tri := func(h string) Entry {
		return Entry{Key: "k", Labels: map[string]string{"role_fit": "strong_fit"}, TextSHA256: h}
	}
	aud := func(h string) Entry {
		e := wouldApplyEntry("k", "yes")
		e.TextSHA256 = h
		return e
	}
	// triage, audit, audit: the triage hash survives two audits in a row.
	m := Merge(Merge(tri(h1), aud(h2)), aud(h3))
	if m.TextSHA256 != h1 {
		t.Errorf("two audits moved the triage hash: %s want %s", m.TextSHA256, h1)
	}
	// triage, audit, triage: the second triage replaces the hash and the slot set.
	m = Merge(Merge(tri(h1), aud(h2)), tri(h3))
	if m.TextSHA256 != h3 || m.Labels[WouldApply] != "yes" {
		t.Errorf("triage,audit,triage: %+v", m)
	}
	// audit, triage: the triage hash stands.
	m = Merge(aud(h1), tri(h2))
	if m.TextSHA256 != h2 {
		t.Errorf("audit,triage: %s want %s", m.TextSHA256, h2)
	}
	// audit, audit with no triage anywhere: the later audit's hash stands.
	m = Merge(aud(h1), aud(h2))
	if m.TextSHA256 != h2 {
		t.Errorf("audit,audit: %s want %s", m.TextSHA256, h2)
	}
	// An older file: triage label with no hash, then an audit with a hash, then
	// a hashless triage re-label: nothing invents a hash.
	m = Merge(Merge(tri(""), aud(h2)), tri(""))
	if m.TextSHA256 != "" {
		t.Errorf("hash invented for an unhashed triage label: %s", m.TextSHA256)
	}
	// A triage label with only a skip is still a triage slot and keeps its hash.
	skip := Entry{Key: "k", Labels: map[string]string{"role_fit": Skip}, TextSHA256: h1}
	if m := Merge(skip, aud(h2)); m.TextSHA256 != h1 {
		t.Errorf("skip label lost its hash to an audit: %s", m.TextSHA256)
	}
	// An audit-only entry with a reason (would_apply_reason) is an audit slot.
	r := wouldApplyEntry("k", "no")
	r.Labels[WouldApplyReason] = "too senior"
	r.TextSHA256 = h2
	if m := Merge(tri(h1), r); m.TextSHA256 != h1 {
		t.Errorf("audit reason slot moved the hash: %s", m.TextSHA256)
	}
}

// Attack: drop agreement with NaN / Inf / negative / duplicate rules.
func TestAttackDropAgreementOddRules(t *testing.T) {
	const sh = "local-s"
	rules := []DropRuleSpec{
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: math.NaN()},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: math.Inf(1)},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: -0.5},
	}
	recs := []ledger.Record{cand("a", "mismatch_domain", "0.99", sh, "answered", "strong_fit")}
	rep := BuildDropAgreement(nil, recs, rules, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 0 || len(rep.Skipped) != 3 {
		t.Fatalf("rows %+v skipped %v: all-skipped slot must have no rows and name 3 skips", rep.Rows, rep.Skipped)
	}
	md := rep.Markdown()
	if !strings.Contains(md, "no data") || strings.Contains(md, "100%") || strings.Count(md, "skipped rule:") != 3 {
		t.Errorf("an all-skipped report reads like data:\n%s", md)
	}
	if _, err := json.Marshal(rep); err != nil { // NaN in the skipped text is a string, not a float
		t.Errorf("report with NaN rule does not marshal: %v", err)
	}

	// Duplicate identical rules: a record is one candidate, not two.
	dup := []DropRuleSpec{
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 0.5},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 0.5},
		{Slot: "role_fit", Values: []string{"mismatch_domain", "mismatch_domain"}, MinP: 0.5},
	}
	rep = BuildDropAgreement(nil, recs, dup, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 1 || rep.Rows[0].Asked != 1 || rep.Rows[0].Disagree != 1 {
		t.Errorf("duplicate rules double counted: %+v", rep.Rows)
	}
	// Boundary: p exactly at MinP is a candidate; MinP exactly 1 usable.
	b := []DropRuleSpec{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 1}}
	rep = BuildDropAgreement(nil, []ledger.Record{cand("b", "mismatch_domain", "1", sh, "answered", "mismatch_domain")}, b, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 1 || rep.Rows[0].Agree != 1 || len(rep.Skipped) != 0 {
		t.Errorf("boundary p=1: %+v %v", rep.Rows, rep.Skipped)
	}
	// No rules at all, no records: no panic, no skipped.
	if rep := BuildDropAgreement(nil, nil, nil, Names{}); len(rep.Skipped) != 0 || !strings.Contains(rep.Markdown(), "no data") {
		t.Errorf("empty input: %+v", rep)
	}
}

// Attack: a label with a hash but a record with an unrelated empty-hash audit-only
// entry never counts as stale (audit-only entries are not triage labels).
func TestAttackAuditOnlyEntryNeverStale(t *testing.T) {
	s, _, _ := newStore(t)
	e := wouldApplyEntry("same", "yes")
	e.TextSHA256 = HashText("old")
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	r := cand("same", "mismatch_domain", "0.95", "", "", "")
	r.Posting.Description = "new"
	rep := BuildReport([]ledger.Record{r}, s, Names{OpenRouter: "d1-ultra"})
	if rep.StaleText != 0 || rep.Matched != 0 {
		t.Errorf("audit-only entry affected the triage report: stale %d matched %d", rep.StaleText, rep.Matched)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	if !strings.Contains(string(b), "\"stale_text\":0") {
		t.Errorf("stale_text missing from JSON: %s", b)
	}
}
