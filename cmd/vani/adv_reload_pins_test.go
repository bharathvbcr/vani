package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

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
	if _, err := e.reloadConfig(app, start, nil); !errors.Is(err, errConfigRestart) {
		t.Fatalf("devprism binary change: %v, want errConfigRestart", err)
	}
	if got := w.lazyScorer().cfg.BinaryPath; got != "/opt/devprism-old" {
		t.Fatalf("scorer binary is %q, want the process-lifetime binary", got)
	}
	if len(w.cfg.Exclude.Companies) != 1 {
		t.Fatal("the exclusion did not apply")
	}
	if !strings.Contains(stderr.String(), "devprism") || !strings.Contains(stderr.String(), "restart") {
		t.Errorf("the devprism binary change must say the process is restarting:\n%q", stderr.String())
	}
	// tools_hash is the same pinned field. The binary in the file matches the
	// one this process kept, so only the hash differs.
	hashEdit := edited
	hashEdit.DevPrism.Binary = "/opt/devprism-old"
	hashEdit.DevPrism.ToolsHash = "abc123"
	writeConfig(t, cfgPath, hashEdit)
	if _, err := e.reloadConfig(app, start, nil); !errors.Is(err, errConfigRestart) {
		t.Fatalf("devprism tools_hash change: %v, want errConfigRestart", err)
	}
	if got := w.lazyScorer().cfg.BinaryPath; got != "/opt/devprism-old" {
		t.Fatalf("scorer binary is %q after a tools_hash change", got)
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
