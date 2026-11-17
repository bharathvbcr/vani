package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A scored posting whose note is deferred is explained, so listed by the digest
// (which lists explained roles), on a later run once the reason is gone, for
// each deferral reason separately. While the reason stands it stays scored run
// after run and writes nothing, so the digest never lists it: the digest
// footer counts those (ft-14258a5e finding 43).
func TestW4aDeferredNoteIsExplainedOnALaterRun(t *testing.T) {
	t.Run("relevance unknown", func(t *testing.T) {
		known := false
		g := &NotesGate{MinRelevance: 10, Relevance: func(r ledger.Record) (float64, bool) { return 80, known }}
		r, ex, l, k := scoredRunner(t, 80, g)
		for run := 1; run <= 3; run++ { // the reason standing: deferred every run, never explained
			if err := r.Advance(ctx, k); err != nil {
				t.Fatal(err)
			}
			if got := rec(t, l, k); got.Status != posting.Scored || got.Annotations[AnnNotesDeferred] != DeferUnknownRelevance || ex.calls != 0 {
				t.Fatalf("run %d: status %s ann %v calls %d", run, got.Status, got.Annotations, ex.calls)
			}
		}
		known = true // a later run knows the relevance (a rescore)
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
		if got := rec(t, l, k); got.Status != posting.Explained || got.Annotations[AnnNotesDeferred] != "" || ex.calls != 1 || NotesDeferred(got) {
			t.Fatalf("status %s ann %v calls %d", got.Status, got.Annotations, ex.calls)
		}
	})
	t.Run("budget spent", func(t *testing.T) {
		g := &NotesGate{MinRelevance: 10, Relevance: fromRank, Spend: fakeSpend{err: decide.ErrBudget}, SpendKey: "notes"}
		r, ex, l, k := scoredRunner(t, 80, g)
		for run := 1; run <= 3; run++ {
			if err := r.Advance(ctx, k); err != nil {
				t.Fatal(err)
			}
			if got := rec(t, l, k); got.Status != posting.Scored || got.Annotations[AnnNotesDeferred] != DeferBudget || ex.calls != 0 {
				t.Fatalf("run %d: status %s ann %v calls %d", run, got.Status, got.Annotations, ex.calls)
			}
		}
		r.Notes = &NotesGate{MinRelevance: 10, Relevance: fromRank, Spend: fakeSpend{}, SpendKey: "notes"} // the next day's budget
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
		if got := rec(t, l, k); got.Status != posting.Explained || got.Annotations[AnnNotesDeferred] != "" || ex.calls != 1 {
			t.Fatalf("status %s ann %v calls %d", got.Status, got.Annotations, ex.calls)
		}
	})
}
