package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A posting can reach triage again still carrying an earlier triage's keys
// (a dead posting with no usable died_from comes back live). The new triage
// replaces the old one whole: keys it did not write are forgotten, so the
// eval never pairs this run's primary with an earlier run's shadow.
func TestTriageReplacesEarlierTriageKeys(t *testing.T) {
	ans := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	stale := map[string]string{
		"triage.remote.shadow":         "onsite",
		"triage.remote.shadow_reading": "answered",
		"triage.remote.shadow_backend": "old-shadow",
		"triage.remote.disagree":       "true",
		"triage.sponsorship":           "no",
		"triage.sponsorship.reading":   "answered",
		"triage.unsure":                "an old reason",
		"keep.me":                      "not a triage key",
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Annotations: stale, Reason: "revived"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k).Annotations
	for key := range stale {
		if key == "keep.me" {
			continue
		}
		if v, ok := got[key]; ok {
			t.Errorf("stale %s=%q survived the new triage", key, v)
		}
	}
	if got["keep.me"] != "not a triage key" {
		t.Error("a non-triage annotation was cleared")
	}
	if got["triage.remote"] != "remote" || got["triage.remote.reading"] != "answered" || got["triage.backend"] != "d1-ultra" {
		t.Errorf("new triage not recorded: %v", got)
	}
}

// When the new triage did sample the shadow, its shadow keys are the new ones.
func TestTriageWithShadowWritesItsOwnShadowKeys(t *testing.T) {
	ans := decide.Answer{Backend: "d1-ultra",
		Slots:  map[string]decide.SlotReading{"remote": jev("remote", 0.99)},
		Shadow: map[string]decide.SlotReading{"remote": tagged(decide.SlotReading{Reading: decide.Answered, Value: "hybrid"}, "new-shadow")}}
	r, l, k := runnerWith(t, ans, nil)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live,
		Annotations: map[string]string{"triage.remote.shadow": "onsite", "triage.remote.shadow_backend": "old-shadow"}, Reason: "revived"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k).Annotations
	if got["triage.remote.shadow"] != "hybrid" || got["triage.remote.shadow_backend"] != "new-shadow" || got["triage.remote.shadow_reading"] != "answered" {
		t.Errorf("shadow keys %v", got)
	}
}
