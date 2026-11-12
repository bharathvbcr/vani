package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// KeepsStoredBody says what reconcile does: for every status, a listing of the
// same text with pay lines and a form is stored exactly when the rule says the
// stored body is not kept. Adapters skip requests on the rule's word, so a
// mismatch either wastes them (the rule too narrow) or loses pay and forms
// (too wide).
func TestKeepsStoredBodyMatchesReconcile(t *testing.T) {
	const text = "Build the thing."
	form := &posting.ApplyForm{V: posting.ApplyFormVersion, Questions: 2, Required: 1}
	for _, st := range []posting.Status{posting.Live, posting.Digested, posting.Dropped, posting.Duplicate, posting.Dead} {
		t.Run(string(st), func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", text)
			survivor := ghPost(t, "2", "Another thing entirely.")
			if _, err := Reconcile(ctx, l, board, []posting.Posting{p, survivor}, t0); err != nil {
				t.Fatal(err)
			}
			if st != posting.Live {
				e := ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: st, Reason: "test"}
				if st == posting.Duplicate {
					e.Annotations = map[string]string{DuplicateOfKey: string(survivor.Key)}
				}
				if err := l.Append(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			before := rec(t, l, p.Key)

			listed := p
			listed.Description = "Pay range: USD 100000 – 150000 per year\n\n" + text
			listed.Form = form
			if _, err := Reconcile(ctx, l, board, []posting.Posting{listed, survivor}, t0.Add(1)); err != nil {
				t.Fatal(err)
			}
			after := rec(t, l, p.Key)
			took := after.Posting.Description == listed.Description && after.Posting.Form.Equal(form)
			untouched := after.Posting.Description == text && after.Posting.Form == nil && after.LastEvent.Equal(before.LastEvent)
			if keeps := KeepsStoredBody(before); keeps == took || keeps != untouched {
				t.Fatalf("KeepsStoredBody=%v, but reconcile stored %q form %+v (event moved: %v)",
					keeps, after.Posting.Description, after.Posting.Form, !after.LastEvent.Equal(before.LastEvent))
			}
		})
	}
}

// A record that missed runs reappears and takes the listing, whatever its status.
func TestKeepsStoredBodyNotForMissedRuns(t *testing.T) {
	for _, st := range []posting.Status{posting.Dropped, posting.Duplicate} {
		if !KeepsStoredBody(ledger.Record{Status: st}) || KeepsStoredBody(ledger.Record{Status: st, MissedRuns: 1}) {
			t.Errorf("%s: missed runs must lift the rule", st)
		}
	}
	for _, st := range []posting.Status{posting.Live, posting.Digested, posting.Dead, posting.Explained, ""} {
		if KeepsStoredBody(ledger.Record{Status: st}) {
			t.Errorf("%s keeps its stored body", st)
		}
	}
}
