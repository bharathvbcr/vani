package config

import "testing"

// The floor is the integrated ranking's ~p78 of the live ledger (measured
// 2026-10-20: 32 admits about 22%); 20 admitted 40% once the ranking changed.
func TestDefaultNotesFloorIsTheIntegratedP78(t *testing.T) {
	if DefaultNotesMinRelevance != 32 || (Funnel{}).NotesFloor() != 32 {
		t.Fatalf("notes floor default %v, want 32", DefaultNotesMinRelevance)
	}
}
