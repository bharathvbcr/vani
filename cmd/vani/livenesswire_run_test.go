package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
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
