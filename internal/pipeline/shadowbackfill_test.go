package pipeline

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const escBackend = "escalate"

// triagedPosting seeds a posting the way a triage left it: status st, the
// primary's reading of "remote" (Liquid D at p) and nothing from a shadow.
func triagedPosting(t *testing.T, l ledger.Ledger, id string, st posting.Status, p float64) (*Runner, posting.Key) {
	t.Helper()
	post := ghPost(t, id, "Build things. Fully remote.")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{post}, t0); err != nil {
		t.Fatal(err)
	}
	ann := triageAnnotations(decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"remote": tagged(jev("remote", p), "d1-ultra"),
	}})
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: post.Key, Status: st, Annotations: ann, Reason: "triaged"}); err != nil {
		t.Fatal(err)
	}
	return &Runner{Ledger: l, Now: func() time.Time { return t0.Add(time.Hour) }}, post.Key
}

func escalated(value string) decide.SlotReading {
	return tagged(decide.SlotReading{Reading: decide.Answered, Value: value}, escBackend)
}

// The backfill writes exactly the annotations a triage writes for the same
// readings (one owner, shadowAnnotations), keeps the status and leaves the
// primary's reading, probability and backend untouched.
func TestRecordShadowsWritesWhatATriageWrites(t *testing.T) {
	for _, st := range []posting.Status{posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested} {
		l := openLedger(t)
		r, key := triagedPosting(t, l, "1", st, 0.3)
		before := rec(t, l, key)

		n, err := r.RecordShadows(ctx, key, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": escalated("hybrid")})
		if err != nil || n != 1 {
			t.Fatalf("%s: n=%d err=%v", st, n, err)
		}
		got := rec(t, l, key)
		if got.Status != st {
			t.Fatalf("status %s became %s", st, got.Status)
		}
		oracle := triageAnnotations(decide.Answer{Backend: "d1-ultra",
			Slots:  map[string]decide.SlotReading{"remote": tagged(jev("remote", 0.3), "d1-ultra")},
			Shadow: map[string]decide.SlotReading{"remote": escalated("hybrid")}})
		for k, v := range oracle {
			if got.Annotations[k] != v {
				t.Errorf("%s: annotation %s = %q, a triage writes %q", st, k, got.Annotations[k], v)
			}
		}
		for k, v := range before.Annotations {
			if strings.Contains(k, ".shadow") || strings.HasSuffix(k, ".disagree") {
				continue
			}
			if got.Annotations[k] != v {
				t.Errorf("%s: annotation %s moved from %q to %q", st, k, v, got.Annotations[k])
			}
		}
		if got.Annotations["triage.remote.shadow_backend"] != escBackend || got.Annotations["triage.remote.disagree"] != "true" {
			t.Errorf("%s: shadow annotations %v", st, got.Annotations)
		}
	}
}

// What was not read, or no longer applies, is not recorded and writes no event.
func TestRecordShadowsSkipsWhatIsNotARecordableReading(t *testing.T) {
	cases := map[string]struct {
		prep   func(t *testing.T, l ledger.Ledger, r *Runner, key posting.Key)
		asked  string
		reads  decide.SlotReading
		wantN  int
		reason string
	}{
		"unavailable": {reads: tagged(decide.SlotReading{Reading: decide.Unavailable}, escBackend), asked: "remote"},
		"not asked":   {reads: tagged(decide.SlotReading{Reading: decide.NotAsked}, escBackend), asked: "remote"},
		"already read by the tier": {asked: "remote", reads: escalated("hybrid"),
			prep: func(t *testing.T, l ledger.Ledger, r *Runner, key posting.Key) {
				if n, err := r.RecordShadows(ctx, key, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": escalated("onsite")}); n != 1 || err != nil {
					t.Fatal(n, err)
				}
			}},
		"read by another shadow": {asked: "remote", reads: escalated("hybrid"),
			prep: func(t *testing.T, l ledger.Ledger, r *Runner, key posting.Key) {
				ann := map[string]string{"triage.remote.shadow_reading": "unavailable", "triage.remote.shadow_backend": "lappi"}
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: key, Status: posting.Explained, Annotations: ann}); err != nil {
					t.Fatal(err)
				}
			}},
		"primary read again meanwhile": {asked: "onsite", reads: escalated("hybrid")},
		"primary no longer an answer": {asked: "remote", reads: escalated("hybrid"),
			prep: func(t *testing.T, l ledger.Ledger, r *Runner, key posting.Key) {
				ann := map[string]string{"triage.remote.reading": "unavailable"}
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: key, Status: posting.Explained, Annotations: ann}); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			r, key := triagedPosting(t, l, "1", posting.Explained, 0.3)
			if c.prep != nil {
				c.prep(t, l, r, key)
			}
			before := rec(t, l, key)
			n, err := r.RecordShadows(ctx, key, escBackend, map[string]string{"remote": c.asked}, map[string]decide.SlotReading{"remote": c.reads})
			if err != nil {
				t.Fatal(err)
			}
			after := rec(t, l, key)
			wantN := c.wantN
			if n != wantN || !maps.Equal(before.Annotations, after.Annotations) || !before.LastEvent.Equal(after.LastEvent) {
				t.Fatalf("n=%d (want %d); record moved: %v -> %v", n, wantN, before.Annotations, after.Annotations)
			}
		})
	}
}

// A reading of an outage the tier itself recorded is replaced by a real one; a
// second pass over the replaced reading writes nothing (idempotent).
func TestRecordShadowsReplacesTheTiersOutageOnce(t *testing.T) {
	l := openLedger(t)
	r, key := triagedPosting(t, l, "1", posting.Digested, 0.3)
	down := tagged(decide.SlotReading{Reading: decide.Unavailable, Detail: "error"}, escBackend)
	// A triage-time outage is recorded as asked (shadowAnnotations).
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: key, Status: posting.Digested,
		Annotations: shadowAnnotations("remote", true, "remote", down)}); err != nil {
		t.Fatal(err)
	}
	if !ShadowPending(rec(t, l, key).Annotations, "remote", escBackend) {
		t.Fatal("the tier's own outage reading is not pending")
	}
	asked, reads := map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": escalated("remote")}
	if n, err := r.RecordShadows(ctx, key, escBackend, asked, reads); n != 1 || err != nil {
		t.Fatalf("replace: n=%d err=%v", n, err)
	}
	a := rec(t, l, key)
	if a.Annotations["triage.remote.shadow_reading"] != "answered" || a.Annotations["triage.remote.shadow"] != "remote" {
		t.Fatalf("not replaced: %v", a.Annotations)
	}
	if ShadowPending(a.Annotations, "remote", escBackend) {
		t.Fatal("a recorded answer is still pending")
	}
	if n, err := r.RecordShadows(ctx, key, escBackend, asked, reads); n != 0 || err != nil || !rec(t, l, key).LastEvent.Equal(a.LastEvent) {
		t.Fatalf("second pass wrote: n=%d err=%v", n, err)
	}
}

func TestShadowPending(t *testing.T) {
	ann := func(reading, backend string) map[string]string {
		m := map[string]string{}
		if reading != "" {
			m["triage.remote.shadow_reading"] = reading
		}
		if backend != "" {
			m["triage.remote.shadow_backend"] = backend
		}
		return m
	}
	for name, c := range map[string]struct {
		ann  map[string]string
		want bool
	}{
		"none":                    {ann("", ""), true},
		"blank":                   {ann("  ", ""), true},
		"tier outage":             {ann("unavailable", " escalate "), true},
		"tier answered":           {ann("answered", escBackend), false},
		"tier failed":             {ann("failed", escBackend), false},
		"another shadow's outage": {ann("unavailable", "lappi"), false},
		"outage, backend unnamed": {ann("unavailable", ""), false},
		"another shadow answered": {ann("answered", "lappi"), false},
	} {
		if got := ShadowPending(c.ann, "remote", escBackend); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Only a kept posting is written (a closed one's last event day is what the digest
// footer counts), an unknown key is an error, and a cancelled context writes nothing.
func TestRecordShadowsRefusals(t *testing.T) {
	l := openLedger(t)
	r, key := triagedPosting(t, l, "1", posting.Explained, 0.3)
	asked, reads := map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": escalated("hybrid")}

	if _, err := r.RecordShadows(ctx, "gh:acme:404", escBackend, asked, reads); err == nil {
		t.Error("an unknown key was recorded")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	before := rec(t, l, key)
	if n, err := r.RecordShadows(cctx, key, escBackend, asked, reads); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: n=%d err=%v", n, err)
	}
	for _, st := range []posting.Status{posting.Dead, posting.Dropped} {
		l2 := openLedger(t)
		r2, k2 := triagedPosting(t, l2, "2", posting.Explained, 0.3)
		if err := l2.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(3 * time.Minute), Key: k2, Status: st, Reason: "closed"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r2.RecordShadows(ctx, k2, escBackend, asked, reads); err == nil {
			t.Errorf("a %s posting was written", st)
		}
	}
	if !rec(t, l, key).LastEvent.Equal(before.LastEvent) {
		t.Error("a refused call moved the record")
	}
}

// Rule 7: a reading by the escalation tier only annotates. The policy drops on the
// primary's calibrated reading, so a posting the primary dropped stays dropped
// whatever the tier says, and a tier's "drop" value on a doubtful primary drops
// nothing.
func TestEscalationNeverChangesADrop(t *testing.T) {
	sure := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": tagged(jev("onsite", 0.97), "d1-ultra")},
		Shadow: map[string]decide.SlotReading{"remote": escalated("remote")}}
	if st, why := policy.Evaluate(sure); st != posting.Dropped {
		t.Fatalf("a drop the tier would overturn: %s (%s)", st, why)
	}
	doubtful := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": tagged(jev("remote", 0.3), "d1-ultra")},
		Shadow: map[string]decide.SlotReading{"remote": escalated("onsite")}}
	if st, why := policy.Evaluate(doubtful); st != posting.Triaged {
		t.Fatalf("the tier's onsite on a doubtful primary dropped it: %s (%s)", st, why)
	}
	// And the stored annotations never read back as a drop: the shadow keys are
	// evidence only (RecordShadows writes nothing but them).
	l := openLedger(t)
	r, key := triagedPosting(t, l, "1", posting.Explained, 0.3)
	if _, err := r.RecordShadows(ctx, key, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": escalated("onsite")}); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, key); got.Status != posting.Explained || got.Annotations["triage.remote"] != "remote" {
		t.Fatalf("record %s %v", got.Status, got.Annotations)
	}
}
