package pipeline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w1dadvEvictedBy builds a Dropped record holding w1dText with a triage
// reading, evicted by a reopen, then damages the events file with damage and
// checks that Hydrate reports ErrBodyUnavailable (the real unreadable path,
// not another decode error) and Get still answers BodyEvicted.
func w1dadvEvictedBy(t *testing.T, damage func(t *testing.T, path string, raw []byte)) (*ledger.File, posting.Posting) {
	t.Helper()
	dir := t.TempDir()
	l := reopenLedger(t, dir, nil)
	p := ghPost(t, "1", w1dText)
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped,
		Annotations: map[string]string{ledger.AnnTriagePrefix + "reading": "onsite"}, Reason: "w1dadv"}); err != nil {
		t.Fatal(err)
	}
	l = reopenLedger(t, dir, l)
	path := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	damage(t, path, raw)
	if _, err := l.Hydrate(p.Key); !errors.Is(err, ledger.ErrBodyUnavailable) {
		t.Fatalf("setup: Hydrate = %v, want ErrBodyUnavailable (a different failure than the one under test)", err)
	}
	if r := rec(t, l, p.Key); !r.BodyEvicted || r.Posting.Description != "" {
		t.Fatalf("setup: Get returned evicted=%v desc=%q", r.BodyEvicted, r.Posting.Description)
	}
	return l, p
}

// The same-length garble of the slice's own tests is one way a body becomes
// unreadable; a truncated events file and an emptied one reach the same
// ErrBodyUnavailable by a different read error (EOF, not a hash mismatch), and
// must give the same reconcile answer: the unchanged listing keeps the triage.
func TestW1dadvUnreadableBodyOtherDamage(t *testing.T) {
	for name, damage := range map[string]func(*testing.T, string, []byte){
		"truncated inside the body line": func(t *testing.T, path string, raw []byte) {
			i := bytes.Index(raw, []byte(w1dText))
			if i < 0 {
				t.Fatal("no body in the file")
			}
			if err := os.Truncate(path, int64(i+5)); err != nil {
				t.Fatal(err)
			}
		},
		"emptied": func(t *testing.T, path string, _ []byte) {
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, p := w1dadvEvictedBy(t, damage)
			q := p
			s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			got := rec(t, l, p.Key)
			if s.Changed != 0 || got.Status != posting.Dropped || got.Annotations[ledger.AnnTriagePrefix+"reading"] == "" {
				t.Fatalf("summary %+v, status %s, annotations %v: an unchanged listing of an unreadable dropped record lost its triage", s, got.Status, got.Annotations)
			}
		})
	}
}

// The unchanged listing of an unreadable record whose hash matches restores the
// body without a status change or a re-triage, for a dropped and a live record,
// and a second identical listing writes nothing more.
func TestW1dadvUnreadableSameHashRestoresBody(t *testing.T) {
	l, p := w1dadvEvictedBy(t, func(t *testing.T, path string, raw []byte) {
		if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte(w1dText), []byte(w1dGarbled)), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	s, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, p.Key)
	if got.BodyEvicted || got.Posting.Description != w1dText || got.Status != posting.Dropped ||
		got.Annotations[ledger.AnnTriagePrefix+"reading"] == "" || s.Refreshed != 1 || s.Changed != 0 {
		t.Fatalf("summary %+v, evicted %v, text %q, status %s, annotations %v: want body restored, status and triage kept", s, got.BodyEvicted, got.Posting.Description, got.Status, got.Annotations)
	}
	s, err = Reconcile(ctx, l, board, []posting.Posting{p}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Refreshed != 0 || s.Unchanged != 1 {
		t.Fatalf("second listing %+v: a restored record is unchanged", s)
	}
}

// A record that is not evicted and legitimately has no text, listed with text,
// is changed text as it always was: the evicted branch of textChange must not
// swallow it.
func TestW1dadvEmptyDescriptionNotEvictedIsStillChange(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, p.Key); r.BodyEvicted {
		t.Fatal("setup: an empty body is evicted")
	}
	q := p
	q.Description = "now it has a text"
	s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Changed != 1 || rec(t, l, p.Key).Posting.Description != q.Description {
		t.Fatalf("summary %+v, text %q: empty stored text listed with text is a change", s, rec(t, l, p.Key).Posting.Description)
	}
}

// An unreadable dropped record with no stored hash is judged on the empty text
// it reads as, so a listing with text is a change and the record takes the
// listed text: the stale body does not outlive the first sighting.
func TestW1dadvUnknownHashUnreadableRecordHeals(t *testing.T) {
	l, p := w1dadvEvictedBy(t, func(t *testing.T, path string, raw []byte) {
		if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte(w1dText), []byte(w1dGarbled)), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: p.Key, Status: posting.Dropped,
		Annotations: map[string]string{DescHashKey: ""}, Reason: "w1dadv clear"}); err != nil {
		t.Fatal(err)
	}
	q := p
	q.Description = w1dText + " zeta eta theta"
	for i := 1; i <= 3; i++ {
		if _, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if r := rec(t, l, p.Key); r.BodyEvicted {
		t.Fatalf("after three listings of new text the record is still unreadable and stale (status %s, hash %q)", r.Status, r.Annotations[DescHashKey])
	}
}
