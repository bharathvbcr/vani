package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// An exclusion the owner loosens must not put every record it frees back to
// Live at once: a dropped posting is closed, so nothing says its board still
// lists it, and a flood of long-gone roles would run through scoring, the paid
// notes call and the digest. The run's exclusion pass marks them pending
// reopens, still dropped, and the next full listing that shows one lets it back
// (the same set `vani retriage` fills).

var exclusionBoard = sources.Board{ATS: posting.Greenhouse, Token: "d", Company: "Acme"}

// exclusionFixture is a wired run over a ledger of three Live postings in
// Paris: one on the watched board, one on a board no longer watched and one
// weakly keyed (an inbox posting), all dropped by exclude.locations. The owner
// then removes the exclusion; the returned keys are watched, unwatched, weak.
func exclusionFixture(t *testing.T) (w *wired, warn *bytes.Buffer, watched, unwatched, weak posting.Key) {
	t.Helper()
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{exclusionBoard}
	cfg.Exclude = config.Exclude{Locations: []string{"Paris"}}
	w, warn = openWired(t, cfg)
	watched, _ = posting.GreenhouseKey("d", "1")
	unwatched, _ = posting.GreenhouseKey("gone", "2")
	weak, _ = posting.FallbackKey("Acme", "Engineer", "Paris")
	for i, k := range []posting.Key{watched, unwatched, weak} {
		p := posting.Posting{Key: k, ATS: k.ATS(), Company: "Acme", Title: "Engineer", Location: "Paris", Description: "jd " + string(rune('a'+i))}
		if err := w.led.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: time.Now().Add(-48 * time.Hour), Key: k, Status: posting.Live, Reason: "new", Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.applyExclusions(context.Background(), dropPolicy(w.cfg, ""), nil, time.Now(), &pipeline.FailOpen{}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []posting.Key{watched, unwatched, weak} {
		if r := mustGet(t, w.led, k); r.Status != posting.Dropped || !pipeline.Excluded(r) {
			t.Fatalf("setup: %s is %s", k, r.Status)
		}
	}
	w.cfg.Exclude = config.Exclude{}
	return w, warn, watched, unwatched, weak
}

func TestApplyExclusionsLoosenedExclusionWaitsForASighting(t *testing.T) {
	w, warn, watched, unwatched, weak := exclusionFixture(t)
	if err := w.applyExclusions(context.Background(), dropPolicy(w.cfg, ""), nil, time.Now(), &pipeline.FailOpen{}); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, w.led, watched); r.Status != posting.Dropped {
		t.Fatalf("the watched posting is %s: it must wait, still dropped, for its board to list it", r.Status)
	}
	if r := mustGet(t, w.led, unwatched); r.Status != posting.Dropped {
		t.Errorf("a posting on a board no longer watched is %s, want it left dropped", r.Status)
	}
	if r := mustGet(t, w.led, weak); r.Status != posting.Live {
		t.Errorf("a weakly keyed posting has no full listing to wait for: %s, want live", r.Status)
	}
	pend := loadPending(t, w.stateDir)
	if pend.Len() != 1 || pend.Items[watched].Class != "exclusion" {
		t.Fatalf("pending %+v, want only the watched posting, as an exclusion", pend.Items)
	}
	if ex := w.exclusions; ex == nil || ex.Reopened != 1 || ex.Deferred != 1 || ex.Stale != 1 {
		t.Errorf("run record %+v, want 1 reopened, 1 deferred, 1 stale", ex)
	}
	if !strings.Contains(warn.String(), "1 waiting for their board") {
		t.Errorf("warnings: %s", warn.String())
	}

	// The next run's pass marks nothing again; the board's full listing lets it back.
	if err := w.applyExclusions(context.Background(), dropPolicy(w.cfg, ""), nil, time.Now(), &pipeline.FailOpen{}); err != nil {
		t.Fatal(err)
	}
	if ex := w.exclusions; ex.Deferred != 0 || ex.AlreadyPending != 1 {
		t.Errorf("second pass %+v, want it to mark nothing", ex)
	}
	rec := pipeline.NewReconciler(w.led)
	finish := w.reopenOnSighting(rec, time.Now())
	listed := ledger.Full(w.led, mustGet(t, w.led, watched)).Posting
	sum, err := rec.Reconcile(context.Background(), exclusionBoard, []posting.Posting{listed}, time.Now())
	if err != nil || sum.Reopened != 1 {
		t.Fatalf("listing %+v (%v)", sum, err)
	}
	finish()
	if r := mustGet(t, w.led, watched); r.Status != posting.Live || pipeline.Excluded(r) {
		t.Errorf("after the sighting: %s excluded=%v", r.Status, pipeline.Excluded(r))
	}
	if loadPending(t, w.stateDir).Len() != 0 {
		t.Error("the sighting left the pending reopen behind")
	}
}

// A pending set that cannot be read is never taken for an empty one or
// overwritten, and nothing is let back through it: the watched posting waits.
func TestApplyExclusionsWithAnUnreadablePendingFileLetsNothingBack(t *testing.T) {
	w, warn, watched, _, _ := exclusionFixture(t)
	path := filepath.Join(w.stateDir, pipeline.PendingReopenFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.applyExclusions(context.Background(), dropPolicy(w.cfg, ""), nil, time.Now(), &pipeline.FailOpen{}); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, w.led, watched); r.Status != posting.Dropped {
		t.Fatalf("with an unreadable pending set the posting is %s, want dropped", r.Status)
	}
	if b, _ := os.ReadFile(path); string(b) != "{broken" {
		t.Errorf("the unreadable file was rewritten: %q", b)
	}
	if !strings.Contains(warn.String(), "pending reopens") {
		t.Errorf("warnings: %s", warn.String())
	}
}
