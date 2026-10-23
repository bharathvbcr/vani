package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/decide"
)

func notesItem(t *testing.T, s statusJSON) (backendJSON, bool) {
	t.Helper()
	for _, b := range s.Backends.Items {
		if b.Kind == "openrouter_notes" {
			return b, true
		}
	}
	return backendJSON{}, false
}

// The notes keep their own daily cap in their own file; status shows it beside
// the triage spend, and neither moves the other.
func TestStatusJSONShowsTheNotesBudgetBesideTriage(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg := config.Default()
	notesCap := cfg.Funnel.NotesCapUSD()
	n, err := decide.NewDailyCap(filepath.Join(stateDir, "budget", notesBudgetFile), notesCap)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Charge(context.Background(), openRouterSpendKey, notesCap/4); err != nil {
		t.Fatal(err)
	}
	s := statusOf(t)
	notes, ok := notesItem(t, s)
	if !ok {
		t.Fatalf("no notes budget in %+v", s.Backends.Items)
	}
	if !notes.OK || notes.CapUSD != notesCap || notes.SpentUSD == nil || *notes.SpentUSD < notesCap/4-1e-9 || *notes.SpentUSD > notesCap/4+1e-9 || notes.Exhausted {
		t.Errorf("notes: %+v", notes)
	}
	if or := openRouterItem(t, s); or.SpentUSD == nil || *or.SpentUSD != 0 || or.CapUSD != cfg.Decide.DailyUSD.OpenRouter {
		t.Errorf("notes spend leaked into triage: %+v", or)
	}
	// Spent to the cap: exhausted.
	if err := n.Charge(context.Background(), openRouterSpendKey, notesCap); err != nil {
		t.Fatal(err)
	}
	if notes, _ = notesItem(t, statusOf(t)); !notes.Exhausted || *notes.RemainingUSD != 0 {
		t.Errorf("notes past the cap: %+v", notes)
	}
}

func TestStatusJSONNotesBudgetNeverStartedIsZeroAndUnreadableIsUnknown(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	notes, ok := notesItem(t, statusOf(t))
	if !ok || !notes.OK || notes.SpentUSD == nil || *notes.SpentUSD != 0 {
		t.Fatalf("no file yet: %+v", notes)
	}
	writeStateFile(t, stateDir, "budget/"+notesBudgetFile, `{not json`)
	s := statusOf(t)
	notes, _ = notesItem(t, s)
	if notes.OK || notes.SpentUSD != nil || notes.Reason == "" {
		t.Fatalf("unreadable notes budget must be unknown with a reason, never $0: %+v", notes)
	}
	if or := openRouterItem(t, s); !or.OK {
		t.Errorf("an unreadable notes file took the triage spend with it: %+v", or)
	}
}
