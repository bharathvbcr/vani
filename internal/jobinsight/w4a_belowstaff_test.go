package jobinsight

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func w4aStaffRecord(desc string, evicted bool) ledger.Record {
	return ledger.Record{Key: "gh:a:1", Status: posting.Digested, BodyEvicted: evicted,
		Posting:     posting.Posting{Key: "gh:a:1", Company: "Acme", Title: "Staff Engineer", Description: desc},
		Annotations: map[string]string{"triage.seniority": "staff_plus", "triage.seniority.reading": "answered", "triage.seniority.p": "0.9"}}
}

func w4aPenalty(c RankContext, r ledger.Record) string {
	c.DropRules = []DropRule{{Slot: "seniority", Values: []string{"mid", "senior", "staff_plus"}}}
	for _, p := range blendInputsFor(r, c).Penalties {
		return p.Slot.Value
	}
	return ""
}

// The staff-level penalty is the level the posting's years imply when it is
// loaded, the stored staff reading when they are not stated, and never a level
// worked out from the empty text of a record whose body is not loaded.
func TestW4aBelowStaffNeverReadsAnEvictedBody(t *testing.T) {
	c := RankContext{}
	if got := w4aPenalty(c, w4aStaffRecord("Requires 3+ years of experience.", false)); got != "mid" {
		t.Fatalf("loaded text with 3 years: penalty on %q, want mid", got)
	}
	if got := w4aPenalty(c, w4aStaffRecord("Requires 12+ years of experience.", false)); got != "staff_plus" {
		t.Fatalf("loaded text with 12 years: penalty on %q, want staff_plus", got)
	}
	// An evicted record carries no text: the stored reading stands, and the
	// description (even a stale one left on the value) is not read.
	if got := w4aPenalty(c, w4aStaffRecord("Requires 3+ years of experience.", true)); got != "staff_plus" {
		t.Fatalf("evicted record: penalty on %q, want the stored staff_plus", got)
	}
}

// A surface that supplies the level (stored facts) decides it, loaded or not.
func TestW4aBelowStaffHookDecides(t *testing.T) {
	calls := 0
	c := RankContext{BelowStaff: func(ledger.Record) (string, bool) { calls++; return "senior", true }}
	for _, evicted := range []bool{false, true} {
		if got := w4aPenalty(c, w4aStaffRecord("", evicted)); got != "senior" {
			t.Fatalf("evicted=%v: penalty on %q, want senior", evicted, got)
		}
	}
	if calls != 2 {
		t.Fatalf("hook asked %d times, want 2", calls)
	}
	// A reading that is not staff never asks.
	r := w4aStaffRecord("", false)
	r.Annotations["triage.seniority"] = "senior"
	calls = 0
	w4aPenalty(c, r)
	if calls != 0 {
		t.Fatalf("hook asked for a non-staff reading")
	}
}
