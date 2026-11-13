package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// runBoardsOffline runs the production board read (readBoards, as runOnce
// calls it) over cfg with an empty watchlist: no request leaves the process,
// and the run's Reconciler is still built and used for the report.
func runBoardsOffline(t *testing.T, cfg config.Config, l *ledger.File) (*wired, error) {
	t.Helper()
	cfg.Watchlist = nil
	d, err := egress.New(egress.Config{AllowedHosts: []string{"boards.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: cfg, stateDir: t.TempDir(), led: l, warn: &bytes.Buffer{}, live: &liveNote{}}
	_, err = w.readBoards(context.Background(), d, pipeline.DropPolicy{}, nil)
	return w, err
}

// The Reconciler that ages postings on the production path is the one
// newBoardReconciler makes from the run's config: a non-default mass hold
// reaches the hold dates the run reports (which read that Reconciler's
// settings), and a setting the rule refuses fails the run before any board is
// read. A run that built its Reconciler any other way (pipeline.NewReconciler,
// the defaults) would report 14-day holds.
func TestRunBoardsAgeWithTheConfigsLiveness(t *testing.T) {
	l := newTestLedger(t)
	k := posting.Key("gh:snap:1")
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Snap", Title: "T", Description: "Body."}
	first := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	stamp := first.Format(time.RFC3339Nano)
	for _, e := range []ledger.Event{
		{V: ledger.SchemaVersion, At: first.Add(-24 * time.Hour), Key: k, Status: posting.Live, Posting: &p},
		{V: ledger.SchemaVersion, At: first, Key: k, Status: posting.Live, Annotations: map[string]string{
			ledger.MissedRunsKey: "1", pipeline.MissedSinceKey: stamp, pipeline.MassMissedSinceKey: stamp}},
	} {
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Liveness = config.Liveness{MassHoldHours: 72}
	w, err := runBoardsOffline(t, cfg, l)
	if err != nil {
		t.Fatal(err)
	}
	if w.boards == nil || len(w.boards.MassHeld) != 1 || !w.boards.MassHeld[0].Until.Equal(first.Add(72*time.Hour)) {
		t.Fatalf("the run's Reconciler did not take the config's 72h mass hold: %+v", w.boards)
	}

	w, err = runBoardsOffline(t, config.Default(), l)
	if err != nil || !w.boards.MassHeld[0].Until.Equal(first.Add(pipeline.DefaultMassHold)) {
		t.Fatalf("defaults: %+v %v", w.boards, err)
	}

	cfg.Liveness = config.Liveness{MassHoldHours: 3}
	if _, err := runBoardsOffline(t, cfg, l); err == nil || !strings.Contains(err.Error(), "mass hold") {
		t.Fatalf("a hold the rule refuses did not fail the run: %v", err)
	}
}

// The merged production path: the Reconciler a run ages postings with carries
// BOTH the config's liveness settings and the pending reopens. Through it, an
// empty listing mass-marks the board's postings and clears a pending reopen
// its board left out for PendingReopenExpiry (saved by finish); a later run's
// Reconciler kills the held postings at the config's 72h mass hold, where the
// defaults would hold them 14 days. And readBoards attaches that set: its
// finish reports it.
func TestRunReconcilerHasTheConfigsLivenessAndThePendingReopens(t *testing.T) {
	l := newTestLedger(t)
	snap := sources.Board{ATS: posting.Greenhouse, Token: "snap", Company: "Snap"}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var live []posting.Key
	for _, id := range []string{"1", "2", "3"} {
		k := posting.Key("gh:snap:" + id)
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Snap", Title: "T" + id, Description: "Body " + id + "."}
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		live = append(live, k)
	}
	dropped := live[2]
	live = live[:2]
	if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: dropped, Status: posting.Dropped, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, pipeline.PendingReopenFile)
	pending := pipeline.NewPendingReopens()
	// 14 days of reads without it counted but a day: the read below completes them.
	first := t0.Add(pipeline.PendingReopenExpiry)
	pending.Items[dropped] = pipeline.PendingReopen{Class: "title", At: t0, Reason: "test",
		Out: pipeline.PendingReopenExpiry - 12*time.Hour, Last: first.Add(-24 * time.Hour)}
	if err := pending.Save(path, t0); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{snap}
	cfg.Liveness = config.Liveness{MassHoldHours: 72}
	w := &wired{cfg: cfg, stateDir: stateDir, led: l, warn: &bytes.Buffer{}, live: &liveNote{}}

	read := func(at time.Time) pipeline.Summary {
		t.Helper()
		rec, finish, err := w.runReconciler(nil, at)
		if err != nil {
			t.Fatal(err)
		}
		s, err := rec.Reconcile(context.Background(), snap, nil, at)
		finish()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := read(first); !s.Mass || s.MassMarked != 2 {
		t.Fatalf("the empty listing: %+v", s)
	}
	after, err := pipeline.LoadPendingReopens(path, first)
	if err != nil || after.Len() != 0 {
		t.Fatalf("the pending reopen left out for %s was not cleared and saved: %+v %v", pipeline.PendingReopenExpiry, after, err)
	}
	if s := read(first.Add(36 * time.Hour)); s.Dead != 0 || s.Held != 2 {
		t.Fatalf("36h into a 72h hold: %+v", s)
	}
	if s := read(first.Add(72 * time.Hour)); s.Dead != 2 || s.MassDead != 2 {
		t.Fatalf("72h into the config's 72h mass hold, the held postings are not dead: %+v", s)
	}

	// readBoards builds its Reconciler the same way: a pending set it finds is
	// attached and reported.
	marked := now() // readBoards loads the set at the run's own clock
	pending.Items[dropped] = pipeline.PendingReopen{Class: "title", At: marked, Reason: "test"}
	if err := pending.Save(path, marked); err != nil {
		t.Fatal(err)
	}
	warn := &bytes.Buffer{}
	d, err := egress.New(egress.Config{AllowedHosts: []string{"boards.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Watchlist = nil
	w = &wired{cfg: cfg, stateDir: stateDir, led: l, warn: warn, live: &liveNote{}}
	if _, err := w.readBoards(context.Background(), d, pipeline.DropPolicy{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "pending reopens: 0 applied, 0 judged again and left dropped, 0 cleared unseen, 1 still waiting") {
		t.Fatalf("readBoards did not attach the pending reopens to its Reconciler:\n%s", warn)
	}
}
