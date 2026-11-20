package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// A slot's rules are alternatives: a record any one of them would drop is a
// candidate, not only the first rule's. A rule with no usable minimum
// probability is named in the report instead of vanishing.
func TestDropAgreementConsidersEveryRuleInASlot(t *testing.T) {
	const sh = "local-s"
	rules := []DropRuleSpec{
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 0.9},
		{Slot: "role_fit", Values: []string{"mismatch_level"}, MinP: 0.8},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 0.5}, // broader than the first, same value
		{Slot: "role_fit", Values: []string{"mismatch_seniority"}},          // zero MinP: skipped
		{Slot: "role_fit", Values: []string{"mismatch_seniority"}, MinP: 1.5},
	}
	recs := []ledger.Record{
		cand("a", "mismatch_domain", "0.95", sh, "answered", "mismatch_domain"), // first rule
		cand("b", "mismatch_level", "0.85", sh, "answered", "strong_fit"),       // second rule only
		cand("c", "mismatch_domain", "0.60", sh, "answered", "strong_fit"),      // third rule only
		cand("d", "mismatch_level", "0.70", sh, "answered", "strong_fit"),       // under every rule's p
		cand("e", "mismatch_seniority", "0.99", sh, "answered", "strong_fit"),   // only skipped rules name it
	}
	rep := BuildDropAgreement(nil, recs, rules, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 1 || rep.Rows[0].Asked != 3 || rep.Rows[0].Agree != 1 || rep.Rows[0].Disagree != 2 {
		t.Fatalf("rows %+v, want 3 asked (a, b, c), 1 agree, 2 disagree", rep.Rows)
	}
	if len(rep.Skipped) != 2 {
		t.Fatalf("skipped %v, want the two rules with no usable min_p", rep.Skipped)
	}
	md := rep.Markdown()
	if strings.Count(md, "skipped rule:") != 2 || !strings.Contains(md, "mismatch_seniority") {
		t.Errorf("the report does not name the skipped rules:\n%s", md)
	}

	none := BuildDropAgreement(nil, nil, rules[3:], Names{OpenRouter: "d1-ultra"})
	if md := none.Markdown(); !strings.Contains(md, "no data") || strings.Count(md, "skipped rule:") != 2 {
		t.Errorf("with no usable rule the no-data report must still say what it skipped:\n%s", md)
	}
}
