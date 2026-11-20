package eval

import "testing"

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
