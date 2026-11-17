package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	w1dText    = "alpha beta gamma delta epsilon"
	w1dGarbled = "ZZZZZ ZZZZ ZZZZZ ZZZZZ ZZZZZZZ" // same length: the line still decodes, its hash does not match
)

// w1dEvicted builds a ledger that holds p (text w1dText) as a record of status
// st with a triage reading, reopens it so the body is evicted, then garbles the
// body in the events file so that reading it back fails: Get returns the record
// with an empty Description and BodyEvicted still true.
func w1dEvicted(t *testing.T, st posting.Status, ann map[string]string) (*ledger.File, posting.Posting) {
	t.Helper()
	dir := t.TempDir()
	l := reopenLedger(t, dir, nil)
	p := ghPost(t, "1", w1dText)
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if ann == nil {
		ann = map[string]string{}
	}
	ann[ledger.AnnTriagePrefix+"reading"] = "onsite"
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: st, Annotations: ann, Reason: "w1d"}); err != nil {
		t.Fatal(err)
	}
	l = reopenLedger(t, dir, l)
	path := filepath.Join(dir, "events.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), w1dText) {
		t.Fatal("setup: the body is not in the events file")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), w1dText, w1dGarbled)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := rec(t, l, p.Key)
	if !r.BodyEvicted || r.Posting.Description != "" || r.Status != st {
		t.Fatalf("setup: want an evicted unreadable %s record, got evicted=%v desc=%q status=%s", st, r.BodyEvicted, r.Posting.Description, r.Status)
	}
	return l, p
}

// An unreadable stored body is never changed text. The listing carries the same
// text the record was stored with, so a dropped or dead record keeps its triage;
// a listing whose hash really differs from the stored one is still a change.
func TestW1dReconcileUnreadableBody(t *testing.T) {
	for _, tc := range []struct {
		name        string
		st          posting.Status
		ann         map[string]string
		storedHash  bool // the description hash annotation survives (it is always written by reconcile)
		listed      string
		wantChanged int
		wantStatus  posting.Status
		wantReading bool
	}{
		{name: "dropped same text", st: posting.Dropped, storedHash: true, listed: w1dText, wantStatus: posting.Dropped, wantReading: true},
		{name: "dropped same text, other spacing", st: posting.Dropped, storedHash: true, listed: "alpha  beta gamma\ndelta epsilon", wantStatus: posting.Dropped, wantReading: true},
		{name: "dropped changed text", st: posting.Dropped, storedHash: true, listed: w1dText + " zeta", wantChanged: 1, wantStatus: posting.Live},
		{name: "dropped no stored hash", st: posting.Dropped, storedHash: false, listed: w1dText + " zeta", wantStatus: posting.Dropped, wantReading: true},
		{name: "dead same text", st: posting.Dead, ann: map[string]string{DiedFromKey: string(posting.Triaged)}, storedHash: true, listed: w1dText, wantStatus: posting.Triaged, wantReading: true},
		{name: "dead changed text", st: posting.Dead, ann: map[string]string{DiedFromKey: string(posting.Triaged)}, storedHash: true, listed: w1dText + " zeta", wantChanged: 1, wantStatus: posting.Live},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, p := w1dEvicted(t, tc.st, tc.ann)
			if !tc.storedHash {
				// the annotation is cleared by an event that carries the empty value
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: p.Key, Status: tc.st,
					Annotations: map[string]string{DescHashKey: ""}, Reason: "w1d clear"}); err != nil {
					t.Fatal(err)
				}
			}
			q := p
			q.Description = tc.listed
			s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			got := rec(t, l, p.Key)
			if s.Changed != tc.wantChanged || got.Status != tc.wantStatus {
				t.Fatalf("summary %+v, status %s: want Changed=%d status=%s", s, got.Status, tc.wantChanged, tc.wantStatus)
			}
			if has := got.Annotations[ledger.AnnTriagePrefix+"reading"] != ""; has != tc.wantReading {
				t.Fatalf("triage reading kept=%v, want %v (annotations %v)", has, tc.wantReading, got.Annotations)
			}
		})
	}
}

// A revived duplicate whose stored body cannot be read keeps its triage when the
// listed text is the one it was stored with.
func TestW1dReviveUnreadableBody(t *testing.T) {
	for _, tc := range []struct {
		name        string
		listed      string
		wantReading bool
	}{
		{"same text", w1dText, true},
		{"changed text", w1dText + " zeta", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// the survivor key is not in the ledger, so it is gone
			l, p := w1dEvicted(t, posting.Duplicate, map[string]string{DuplicateOfKey: "greenhouse:acme:gone"})
			q := p
			q.Description = tc.listed
			s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			got := rec(t, l, p.Key)
			if s.Revived != 1 || got.Status != posting.Live {
				t.Fatalf("summary %+v, status %s: want a revived live record", s, got.Status)
			}
			if has := got.Annotations[ledger.AnnTriagePrefix+"reading"] != ""; has != tc.wantReading {
				t.Fatalf("triage reading kept=%v, want %v (annotations %v)", has, tc.wantReading, got.Annotations)
			}
		})
	}
}
