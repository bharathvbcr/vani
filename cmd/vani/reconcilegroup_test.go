package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func ledgerAt(t *testing.T) (*ledger.File, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, dir
}

// The run's Reconciler (runReconciler, as readBoards builds it) writes a
// board's events with one ledger fsync: the run's ledger is a ledger.Grouper
// all the way down. A wrapper between them would fall back to one fsync per
// event without any test failing but this one.
func TestTheRunsBoardListingSyncsTheLedgerOnce(t *testing.T) {
	l, dir := ledgerAt(t)
	board := sources.Board{ATS: posting.Greenhouse, Token: "snap", Company: "Snap"}
	w := &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: &bytes.Buffer{}, live: &liveNote{}}
	rec, finish, err := w.runReconciler(nil, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var listed []posting.Posting
	for i := range 30 {
		k := posting.Key(fmt.Sprintf("gh:snap:%d", i+1))
		listed = append(listed, posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Snap", Title: fmt.Sprint("T", i), Description: fmt.Sprint("Body ", i)})
	}
	log := synctest.Record(t, dir)
	s, err := rec.Reconcile(context.Background(), board, listed, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	finish()
	if err != nil || s.New != 30 {
		t.Fatalf("listing: %+v %v", s, err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("30 new postings through the run's Reconciler: %d ledger syncs, want 1", n)
	}
}

// If the ledger breaks during the listing (a board's fsync failed), the board's
// events never reached the disk, so the pending reopens it spent must stay in
// the saved set for the next run. Here the board's listing clears a reopen
// left out past PendingReopenExpiry; with the failed sync, the set on disk is
// left as it was.
func TestAFailedListingDoesNotSaveThePendingReopens(t *testing.T) {
	l, dir := ledgerAt(t)
	snap := sources.Board{ATS: posting.Greenhouse, Token: "snap", Company: "Snap"}
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"1", "2", "3"} {
		k := posting.Key("gh:snap:" + id)
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Snap", Title: "T" + id, Description: "Body " + id + "."}
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	dropped := posting.Key("gh:snap:3")
	if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: t0, Key: dropped, Status: posting.Dropped, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, pipeline.PendingReopenFile)
	pending := pipeline.NewPendingReopens()
	first := t0.Add(pipeline.PendingReopenExpiry)
	pending.Items[dropped] = pipeline.PendingReopen{Class: "title", At: t0, Reason: "test",
		Out: pipeline.PendingReopenExpiry - 12*time.Hour, Last: first.Add(-24 * time.Hour)}
	if err := pending.Save(path, t0); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{snap}
	warn := &bytes.Buffer{}
	w := &wired{cfg: cfg, stateDir: stateDir, led: l, warn: warn, live: &liveNote{}}
	rec, finish, err := w.runReconciler(nil, first)
	if err != nil {
		t.Fatal(err)
	}
	log := synctest.Record(t, dir)
	log.Fail(func(_ string, d bool) error {
		if d {
			return nil
		}
		return errors.New("fsync: boom")
	})
	if _, err := rec.Reconcile(context.Background(), snap, nil, first); err == nil {
		t.Fatal("the board's failed fsync did not fail the listing")
	}
	finish()
	after, err := pipeline.LoadPendingReopens(path, first)
	if err != nil || after.Len() != 1 {
		t.Fatalf("the set was saved over a broken ledger: %d pending, %v; want the 1 kept", after.Len(), err)
	}
	if !strings.Contains(warn.String(), "pending reopens: not saved") {
		t.Fatalf("the run did not say why:\n%s", warn)
	}
}
