package eval

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var agreeRules = []DropRuleSpec{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinP: 0.9}}

// cand is a record whose role_fit the primary read as value at p; shadow, when
// non-empty, is the shadow backend, with its reading and value.
func cand(key, value, p, shadow, shReading, shValue string) ledger.Record {
	pre := ledger.AnnTriagePrefix + "role_fit"
	ann := map[string]string{
		pre: value, pre + ledger.AnnTriageSuffixReading: "answered",
		pre + ledger.AnnTriageSuffixBackend: "d1-ultra", pre + ledger.AnnTriageSuffixP: p,
	}
	if shadow != "" {
		ann[pre+ledger.AnnTriageSuffixShadowBackend] = shadow
		ann[pre+ledger.AnnTriageSuffixShadowReading] = shReading
		if shReading == "answered" {
			ann[pre+ledger.AnnTriageSuffixShadow] = shValue
		}
	}
	return ledger.Record{Key: posting.Key(key), Annotations: ann}
}

func judgedAs(v string) Entry { return Entry{Labels: map[string]string{WouldApply: v}} }

func TestDropAgreementCountsOnlyDropCandidates(t *testing.T) {
	const sh = "local-d3-mini-per_slot"
	recs := []ledger.Record{
		cand("a", "mismatch_domain", "0.95", sh, "answered", "mismatch_domain"), // agree
		cand("b", "mismatch_domain", "0.99", sh, "answered", "strong_fit"),      // disagree, labelled wanted
		cand("c", "mismatch_domain", "0.91", sh, "answered", "partial_fit"),     // disagree, labelled no
		cand("d", "mismatch_domain", "0.92", sh, "answered", "partial_fit"),     // disagree, unlabelled
		cand("e", "mismatch_domain", "0.93", sh, "unavailable", ""),             // unavailable: not agreement
		cand("f", "mismatch_domain", "0.94", "", "", ""),                        // not asked
		cand("g", "mismatch_domain", "0.89", sh, "answered", "strong_fit"),      // under the rule's p: no candidate
		cand("h", "strong_fit", "0.99", sh, "answered", "strong_fit"),           // not a rule value
		cand("i", "mismatch_domain", "0.99", "escalate", "answered", "strong_fit"),
		cand("j", "mismatch_domain", "NaN", sh, "answered", "strong_fit"),
	}
	inherited := cand("k", "mismatch_domain", "0.99", sh, "answered", "strong_fit")
	inherited.Annotations[ledger.AnnTriageInheritedFrom] = "a"
	recs = append(recs, inherited)
	labels := map[posting.Key]Entry{"b": judgedAs("maybe"), "c": judgedAs("no"), "a": judgedAs("yes"), "d": judgedAs("skip")}

	rep := BuildDropAgreement(labels, recs, agreeRules, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 2 {
		t.Fatalf("rows %+v", rep.Rows)
	}
	var unasked, row AgreementRow
	for _, r := range rep.Rows {
		if r.Shadow == "" {
			unasked = r
		} else {
			row = r
		}
	}
	want := AgreementRow{Slot: "role_fit", Shadow: sh, Asked: 5, Answered: 4, Unavailable: 1, Agree: 1, Disagree: 3, DisagreeJudged: 2, DisagreeWanted: 1}
	if row != want || unasked.NotAsked != 1 {
		t.Fatalf("row %+v want %+v, unasked %+v", row, want, unasked)
	}
	md := rep.Markdown()
	for _, frag := range []string{"## Drop candidates: shadow vs d1-ultra", "1/4 = 25% (95% CI", "| role_fit | " + sh + " | 5 | 1 | 4 |", "1 candidate(s) with no shadow reading"} {
		if !strings.Contains(md, frag) {
			t.Errorf("report lacks %q:\n%s", frag, md)
		}
	}
}

// Legacy "jev" rows are the configured model's; another primary is not.
func TestDropAgreementReadsLegacyPrimaryName(t *testing.T) {
	r := cand("a", "mismatch_domain", "0.95", "s", "answered", "mismatch_domain")
	r.Annotations["triage.role_fit.backend"] = LegacyOpenRouterName
	if got := BuildDropAgreement(nil, []ledger.Record{r}, agreeRules, Names{OpenRouter: "d1-ultra"}); len(got.Rows) != 1 || got.Rows[0].Agree != 1 {
		t.Fatalf("legacy name: %+v", got.Rows)
	}
	r.Annotations["triage.role_fit.backend"] = "llm"
	if got := BuildDropAgreement(nil, []ledger.Record{r}, agreeRules, Names{OpenRouter: "d1-ultra"}); len(got.Rows) != 0 {
		t.Fatalf("an llm answer is no candidate: %+v", got.Rows)
	}
}

// Zero data says "no data", never 0%: no records, only unasked candidates,
// only unavailable shadow readings, and a rule that never drops on p.
func TestDropAgreementZeroDataSaysNoData(t *testing.T) {
	for name, tc := range map[string]struct {
		recs  []ledger.Record
		rules []DropRuleSpec
	}{
		"no records":    {nil, agreeRules},
		"none asked":    {[]ledger.Record{cand("a", "mismatch_domain", "0.95", "", "", "")}, agreeRules},
		"zero p rule":   {[]ledger.Record{cand("a", "mismatch_domain", "0.95", "s", "answered", "x")}, []DropRuleSpec{{Slot: "role_fit", Values: []string{"mismatch_domain"}}}},
		"no rules":      {[]ledger.Record{cand("a", "mismatch_domain", "0.95", "s", "answered", "x")}, nil},
		"all unavail":   {[]ledger.Record{cand("a", "mismatch_domain", "0.95", "s", "unavailable", "")}, agreeRules},
		"all unavail 2": {[]ledger.Record{cand("a", "mismatch_domain", "0.95", "s", "failed", "")}, agreeRules},
	} {
		t.Run(name, func(t *testing.T) {
			md := BuildDropAgreement(nil, tc.recs, tc.rules, Names{OpenRouter: "d1-ultra"}).Markdown()
			unavailable := strings.Contains(name, "unavail")
			switch {
			case unavailable && !strings.Contains(md, "no data (n=0)"):
				t.Fatalf("answered 0 must read no data:\n%s", md)
			case !unavailable && !strings.Contains(md, "no data"):
				t.Fatalf("report:\n%s", md)
			}
			for _, bad := range []string{"0%", "NaN", "100%"} {
				if strings.Contains(md, bad) {
					t.Fatalf("printed %q:\n%s", bad, md)
				}
			}
		})
	}
}

// One shadow's rows do not pool with another's: two local models are two rows.
func TestDropAgreementSeparatesShadows(t *testing.T) {
	var recs []ledger.Record
	for i, s := range []string{"local-a", "local-b", "local-a"} {
		recs = append(recs, cand(fmt.Sprint("k", i), "mismatch_domain", "0.95", s, "answered", "mismatch_domain"))
	}
	rep := BuildDropAgreement(nil, recs, agreeRules, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 2 || rep.Rows[0].Shadow != "local-a" || rep.Rows[0].Answered != 2 || rep.Rows[1].Answered != 1 {
		t.Fatalf("%+v", rep.Rows)
	}
}
