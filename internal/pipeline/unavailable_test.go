package pipeline

import (
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// When no backend could read a posting (the day's LLM budget is spent, the
// provider is down, every call timed out) that is not an uncertain answer: the
// posting stays live so the next run triages it, instead of being marked unsure
// and never asked about again.
func TestTriageWithNoReadingStaysLive(t *testing.T) {
	ans := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{
		"remote": {Reading: decide.Unavailable, Detail: "error"},
		"level":  {Reading: decide.Unavailable, Detail: "deadline"},
	}}
	r, l, k := runnerWith(t, ans, nil)
	err := r.Advance(ctx, k)
	if !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("err = %v, want ErrTriageUnavailable", err)
	}
	if got := rec(t, l, k); got.Status != posting.Live {
		t.Fatalf("status %s, want live", got.Status)
	}
}

// One unavailable slot beside a real reading is still an unsure posting.
func TestPartlyUnavailableIsStillUnsure(t *testing.T) {
	ans := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{
		"remote": {Reading: decide.Unavailable, Detail: "error"},
		"level":  {Reading: decide.Answered, Value: "senior"},
	}}
	r, l, k := runnerWith(t, ans, nil)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Annotations["triage.unsure"] == "" {
		t.Fatalf("status %s, annotations %v: want an unsure triage", got.Status, got.Annotations)
	}
}
