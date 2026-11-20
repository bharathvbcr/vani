package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// An audit answer on a key that already has triage labels does not move those
// labels' text hash: the hash says which text they were given on.
func TestAuditAnswerKeepsTriageLabelsTextHash(t *testing.T) {
	old, now := HashText("old text"), HashText("new text")
	s, _, _ := newStore(t)
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"role_fit": "strong_fit"}, TextSHA256: old}); err != nil {
		t.Fatal(err)
	}
	audit := wouldApplyEntry("gh:a:1", "yes")
	audit.TextSHA256 = now
	if err := s.Put(audit); err != nil {
		t.Fatal(err)
	}
	e := mustLabels(t, s)["gh:a:1"]
	if e.TextSHA256 != old || e.Labels["role_fit"] != "strong_fit" || e.Labels[WouldApply] != "yes" {
		t.Fatalf("after the audit: %+v, want the triage label's hash %s kept", e, old)
	}

	// A triage label with no hash is not vouched for by the audit's hash either.
	if err := s.Put(Entry{Key: "gh:a:2", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatal(err)
	}
	audit2 := wouldApplyEntry("gh:a:2", "no")
	audit2.TextSHA256 = now
	if err := s.Put(audit2); err != nil {
		t.Fatal(err)
	}
	if e := mustLabels(t, s)["gh:a:2"]; e.TextSHA256 != "" {
		t.Errorf("an unhashed triage label picked up the audit's hash: %+v", e)
	}

	// Without triage labels the audit's hash stands, and a new triage label
	// replaces the hash.
	audit3 := wouldApplyEntry("gh:a:3", "maybe")
	audit3.TextSHA256 = old
	if err := s.Put(audit3); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:3", Labels: map[string]string{"role_fit": "strong_fit"}, TextSHA256: now}); err != nil {
		t.Fatal(err)
	}
	if e := mustLabels(t, s)["gh:a:3"]; e.TextSHA256 != now {
		t.Errorf("a new triage label did not set its hash: %+v", e)
	}
}

// A label given on text that has since changed is not scored against the
// record's reading of the new text; it is counted as stale. A label or record
// with nothing to compare is kept.
func TestReportLeavesOutLabelsWhoseTextChanged(t *testing.T) {
	s, _, _ := newStore(t)
	put := func(key, hash string) {
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"role_fit": "mismatch_domain"}, TextSHA256: hash}); err != nil {
			t.Fatal(err)
		}
	}
	rec := func(key, desc string) ledger.Record {
		r := cand(key, "mismatch_domain", "0.95", "", "", "")
		r.Posting.Description = desc
		return r
	}
	put("same", HashText("body"))
	put("changed", HashText("old body"))
	put("nohash", "")
	put("nodesc", HashText("anything"))
	recs := []ledger.Record{rec("same", "body"), rec("changed", "new body"), rec("nohash", "x"), rec("nodesc", "")}
	rep := BuildReport(recs, s, Names{OpenRouter: "d1-ultra"})
	if rep.StaleText != 1 || rep.Matched != 3 {
		t.Fatalf("stale %d matched %d, want 1 and 3", rep.StaleText, rep.Matched)
	}
	var labeled int
	for _, c := range rep.Cells {
		labeled += c.Labeled
	}
	if labeled != 3 {
		t.Errorf("%d labelled readings scored, want 3 (the changed posting's is left out)", labeled)
	}
	if md := rep.Markdown(); !strings.Contains(md, "text changed") {
		t.Errorf("the report does not say a label was left out:\n%s", md)
	}
}
