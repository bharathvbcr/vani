package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestUnread(t *testing.T) {
	cases := []struct {
		ann  map[string]string
		want bool
	}{
		{map[string]string{"triage.a.reading": "unavailable", "triage.b.reading": "unavailable", "triage.backend": "llm"}, true},
		{map[string]string{"triage.a.reading": "unavailable", "triage.b.reading": "answered"}, false},
		{map[string]string{"triage.a.reading": "refused"}, false},
		{map[string]string{"triage.backend": "llm"}, false},
		{map[string]string{}, false},
		{map[string]string{"triage.a.shadow_reading": "unavailable"}, false},
	}
	for _, c := range cases {
		if got := Unread(ledger.Record{Annotations: c.ann}); got != c.want {
			t.Errorf("Unread(%v) = %v, want %v", c.ann, got, c.want)
		}
	}
}

// A record that went live -> unsure -> scored because no backend could read it
// returns to live with its triage and scoring forgotten and its badges kept;
// a record with a real reading is left alone.
func TestRetriageUnread(t *testing.T) {
	unread := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Unavailable, Detail: "error"}}}
	r, l, k := runnerWith(t, unread, nil)
	r.SkipScoring = true
	// Mimic the old behaviour that let an unread triage through: evaluate it
	// as the policy did and emit it directly.
	st, reason := r.Policy.Evaluate(unread)
	ann := triageAnnotations(unread)
	ann[ledger.AnnLCA] = "certified H-1B history"
	if err := r.emit(ctx, k, st, reason, ann); err != nil {
		t.Fatal(err)
	}
	if err := r.emit(ctx, k, posting.Scored, "scoring disabled", nil); err != nil {
		t.Fatal(err)
	}

	keys, err := RetriageUnread(ctx, l, t0.Add(time.Hour), true)
	if err != nil || len(keys) != 1 || keys[0] != k {
		t.Fatalf("dry run = %v, %v", keys, err)
	}
	if rec(t, l, k).Status != posting.Scored {
		t.Fatal("a dry run changed the ledger")
	}

	keys, err = RetriageUnread(ctx, l, t0.Add(time.Hour), false)
	if err != nil || len(keys) != 1 {
		t.Fatalf("retriage = %v, %v", keys, err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Live {
		t.Fatalf("status %s, want live", got.Status)
	}
	for key := range got.Annotations {
		if strings.HasPrefix(key, ledger.AnnTriagePrefix) {
			t.Errorf("triage annotation %s survived", key)
		}
	}
	if got.Annotations[ledger.AnnLCA] == "" {
		t.Error("the LCA badge was forgotten")
	}
	if again, _ := RetriageUnread(ctx, l, t0.Add(2*time.Hour), false); len(again) != 0 {
		t.Errorf("a second pass found %v", again)
	}

	// A real reading is never retriaged.
	answered := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Answered, Value: "remote"}}}
	r2, l2, k2 := runnerWith(t, answered, nil)
	r2.SkipScoring = true
	if err := r2.Advance(ctx, k2); err != nil {
		t.Fatal(err)
	}
	if keys, _ := RetriageUnread(ctx, l2, t0.Add(time.Hour), false); len(keys) != 0 {
		t.Errorf("an answered record was retriaged: %v", keys)
	}
}

// failingAppend is a ledger whose Append fails from the n-th call on.
type failingAppend struct {
	ledger.Ledger
	n, calls int
}

var errAppend = errors.New("disk full")

// AppendBatch counts as one call and fails whole.
func (f *failingAppend) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	f.calls++
	if f.calls >= f.n {
		return errAppend
	}
	return f.Ledger.AppendBatch(ctx, evs)
}

func (f *failingAppend) Append(ctx context.Context, e ledger.Event) error {
	f.calls++
	if f.calls >= f.n {
		return errAppend
	}
	return f.Ledger.Append(ctx, e)
}

// When an append fails part-way, the keys returned are exactly the records
// that were returned to live, so the printed count matches the ledger.
func TestRetriageUnreadReportsOnlyWrittenKeys(t *testing.T) {
	l := openLedger(t)
	unread := map[string]string{"triage.remote.reading": "unavailable"}
	for _, id := range []string{"1", "2", "3"} {
		p := ghPost(t, id, "d")
		if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Unsure, Posting: &p, Annotations: unread}); err != nil {
			t.Fatal(err)
		}
	}
	f := &failingAppend{Ledger: l, n: 2}
	keys, err := RetriageUnread(ctx, f, t0.Add(time.Hour), false)
	if !errors.Is(err, errAppend) {
		t.Fatalf("err = %v, want the append error", err)
	}
	live := 0
	for _, r := range l.All() {
		if r.Status == posting.Live {
			live++
		}
	}
	if len(keys) != 1 || live != 1 {
		t.Fatalf("keys %v, %d record(s) live; want exactly the one written", keys, live)
	}
	if r := rec(t, l, keys[0]); r.Status != posting.Live {
		t.Fatalf("returned key %s is %s, not live", keys[0], r.Status)
	}
}
