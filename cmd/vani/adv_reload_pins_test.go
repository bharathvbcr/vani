package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A config path replaced by a FIFO must not hang the run (the daemon holds the
// ledger mutex while it reloads).
func TestAdvConfigAsFIFODoesNotHangTheRun(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(cfgPath, 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	app := &App{Reload: func(config.Config) {}}
	done := make(chan error, 1)
	go func() { _, err := e.reloadConfig(app, start, nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errConfigStale) {
			t.Fatalf("fifo config: %v, want errConfigStale", err)
		}
	case <-time.After(2 * time.Second):
		// Unblock the stuck open so the goroutine ends, then fail.
		if f, err := os.OpenFile(cfgPath, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		<-done
		t.Fatal("reloadConfig blocked on a FIFO config path; a daemon would hang holding the ledger mutex")
	}
}

// A process-lifetime field that is not the schedule changes together with an
// exclusion: the exclusion applies and the pinned field says so.
func TestAdvDevPrismBinaryChangeIsPinnedWithANote(t *testing.T) {
	e, cfgPath, start, stderr := advEnv(t)
	start.DevPrism.Binary = "/opt/devprism-old"
	w, app := lifecycleApp(t, start)
	_ = w.lazyScorer() // the daemon has scored once: the scorer is built
	edited := start
	edited.DevPrism.Binary = "/opt/devprism-new"
	edited.Exclude.Companies = []string{"Acme"}
	writeConfig(t, cfgPath, edited)
	if _, err := e.reloadConfig(app, start, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.lazyScorer().cfg.BinaryPath; got != "/opt/devprism-old" {
		t.Fatalf("scorer binary is %q (setup)", got)
	}
	if len(w.cfg.Exclude.Companies) != 1 {
		t.Fatal("the exclusion did not apply")
	}
	if !strings.Contains(stderr.String(), "devprism") {
		t.Errorf("the devprism binary change is silently ignored (the running scorer keeps the old one) with no note:\n%q", stderr.String())
	}
}

// A marked closed record whose evicted text cannot be read back is skipped by
// the warm; the skip must be visible in the run's semantic report (a note or a
// Skipped count), not silent.
func TestAdvUnavailableMarkedBodyIsReportedNotSilent(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Semantic.Snapshot, cfg.KB.Bank = "snapshot", "bank"
	dir, keys := advClosedLedger(t, 1)
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	events := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(events, bytes.ReplaceAll(raw, []byte("the full job text"), []byte("THE FULL JOB TEXT")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetMark(marks.Dir(stateDir), keys[0], marks.Saved, jobsT0); err != nil {
		t.Fatal(err)
	}
	old := warmSemanticRun
	warmSemanticRun = func(ctx context.Context, cfg config.Config, dir string, recs []ledger.Record, mk map[posting.Key]marks.Mark, max int) semanticReport {
		return old(ctx, cfg, dir, recs, mk, max)
	}
	t.Cleanup(func() { warmSemanticRun = old })
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	rec := &runRecord{}
	app := &App{Ledger: l, RunOnce: func(context.Context) error { return nil }}
	if _, err := e.runAndPublish(context.Background(), app, cfg, runOptions{}, rec); err != nil {
		t.Fatal(err)
	}
	// Setup holds: the record is unreadable and warmDocs leaves it out.
	mk := map[posting.Key]marks.Mark{keys[0]: marks.Saved}
	seen := withMarkedText(l, l.All(), mk)
	if r := seen[0]; !r.BodyEvicted || r.Posting.Description != "" || len(warmDocs(seen, mk)) != 0 {
		t.Fatalf("setup: the marked record's text is readable (%+v)", r)
	}
	const want = "1 marked posting(s) not warmed: stored description unreadable"
	if rec.Semantic == nil || !strings.Contains(rec.Semantic.Note, want) {
		t.Errorf("run record semantic report lacks %q: %+v", want, rec.Semantic)
	}
	if !strings.Contains(e.stderr.(*bytes.Buffer).String(), want) {
		t.Errorf("log line lacks %q:\n%s", want, e.stderr.(*bytes.Buffer).String())
	}
	if strings.Contains(rec.Semantic.Note, "JOB TEXT") {
		t.Error("the note carries posting text")
	}
}
