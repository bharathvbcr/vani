package main

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The footer counts a deferred note only for a role the digest could list. The
// listing excludes a Hidden role and nothing else, so an Applied role (which a
// later note would make listable) still counts, and so does a Saved or unmarked
// one.
func TestW4aNotesDeferredCountFollowsListingMarkRule(t *testing.T) {
	deferred := func(k posting.Key) ledger.Record {
		return ledger.Record{
			Key: k, Status: posting.Scored,
			Annotations: map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferBudget},
		}
	}
	recs := []ledger.Record{deferred("gh:a:none"), deferred("gh:a:applied"), deferred("gh:a:hidden"), deferred("gh:a:saved")}
	m := map[posting.Key]marks.Mark{
		"gh:a:applied": marks.Applied,
		"gh:a:hidden":  marks.Hidden,
		"gh:a:saved":   marks.Saved,
	}
	if c := countsFor(recs, m, time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), time.UTC); c.NotesDeferred != 3 {
		t.Fatalf("NotesDeferred = %d, want 3 (only the Hidden role is never listed)", c.NotesDeferred)
	}
	if c := countsFor(recs, nil, time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), time.UTC); c.NotesDeferred != 4 {
		t.Fatalf("NotesDeferred with no marks = %d, want 4", c.NotesDeferred)
	}
}
