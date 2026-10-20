package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/marks"
)

func TestRunCause(t *testing.T) {
	fatal := fmt.Errorf("%w: ledger fsync failed", daemon.ErrFatal)
	held := fmt.Errorf("%w: read the Jobs marks: corrupt", errDigestHeldBack)
	stale := fmt.Errorf("%w; this run uses the previous watchlist: bad json", errConfigStale)
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("pipeline: all boards down"), ""},
		{"fatal", fatal, causeLedgerFatal},
		{"held back", held, causeDigestHeldBack},
		{"stale config", stale, causeConfigStale},
		{"wrapped twice", fmt.Errorf("pipeline: %w", fmt.Errorf("x: %w", stale)), causeConfigStale},
		// A joined error names the first in failureNotice's order.
		{"fatal beats the rest", errors.Join(stale, held, fatal), causeLedgerFatal},
		{"held back beats stale", errors.Join(stale, held), causeDigestHeldBack},
		{"stale beside a plain failure", errors.Join(stale, errors.New("pipeline: down")), causeConfigStale},
		// The code comes from the error's identity, never its text.
		{"text alone", errors.New("config reload failed; digest held back; " + daemon.ErrFatal.Error()), ""},
	} {
		if got := runCause(c.err); got != c.want {
			t.Errorf("%s: runCause = %q, want %q", c.name, got, c.want)
		}
	}
}

// lastRecord is the run record the last run-once wrote.
func lastRecord(t *testing.T, stateDir string) runRecord {
	t.Helper()
	r, err := readRunRecord(stateDir)
	if err != nil || r == nil {
		t.Fatalf("run record: %v, %v", r, err)
	}
	return *r
}

func intPtr(n int) *int { return &n }

func intPtrString(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func TestRunRecordCauseAndDigestRoles(t *testing.T) {
	cases := []struct {
		name  string
		code  int
		arm   func(t *testing.T, cfgPath, stateDir string) // before run-once
		err   error                                        // the pipeline's
		out   string
		stage string
		cause string
		roles *int
	}{
		{name: "ok", code: 0, out: outcomeOK, roles: intPtr(2)},
		{name: "pipeline failed", code: 1, err: errors.New("all boards down"), out: outcomeFailed, stage: stagePipeline},
		{name: "ledger fatal", code: 1, err: fmt.Errorf("%w: fsync", daemon.ErrFatal), out: outcomeFailed, stage: stagePipeline, cause: causeLedgerFatal},
		{name: "cancelled has no cause", code: 1, err: fmt.Errorf("%w: %w", context.Canceled, errConfigStale), out: outcomeCancelled, stage: stagePipeline},
		{name: "digest held back", code: 1, out: outcomeFailed, stage: stageDigest, cause: causeDigestHeldBack,
			arm: func(t *testing.T, _, stateDir string) { corruptMarks(t, marks.Dir(stateDir)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfgPath, stateDir := setup(t)
			mustCode(t, 0, "init")
			n := &fakeNotifier{}
			var before func()
			if c.arm != nil {
				before = func() { c.arm(t, cfgPath, stateDir) }
			}
			noticeApp(t, n, c.err, nil, before)
			mustCode(t, c.code, "run-once")

			r := lastRecord(t, stateDir)
			if r.Outcome != c.out || r.Stage != c.stage || r.Cause != c.cause || intPtrString(r.DigestRoles) != intPtrString(c.roles) {
				t.Errorf("record outcome=%s stage=%q cause=%q digest_roles=%s, want %s %q %q %s",
					r.Outcome, r.Stage, r.Cause, intPtrString(r.DigestRoles), c.out, c.stage, c.cause, intPtrString(c.roles))
			}
			// status --json carries both as the record has them.
			f := statusOf(t).LastRun
			if f.Cause != c.cause || intPtrString(f.DigestRoles) != intPtrString(c.roles) {
				t.Errorf("last_run cause=%q digest_roles=%s, want %q %s", f.Cause, intPtrString(f.DigestRoles), c.cause, intPtrString(c.roles))
			}
		})
	}
}

// A config that could not be re-read does not stop the run: it writes its
// digest and is recorded failed at stage digest. The record says both, and
// both reach status --json and the notice. (No run produces errConfigStale
// until the config reload is wired, so the record is written here.)
func TestConfigStaleRunWithItsDigestWritten(t *testing.T) {
	_, stateDir := setup(t)
	rec := runRecord{
		PID: 1, StartedAt: time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 10, 18, 15, 5, 0, 0, time.UTC),
		Outcome: outcomeFailed, Stage: stageDigest, Error: "config reload failed; this run uses the previous watchlist",
		Cause: runCause(fmt.Errorf("%w; this run uses the previous watchlist: bad json", errConfigStale)), DigestRoles: intPtr(2),
	}
	if err := writeRunRecord(stateDir, rec); err != nil {
		t.Fatal(err)
	}
	f := statusOf(t).LastRun
	if f.State != outcomeFailed || f.Stage != stageDigest || f.Cause != causeConfigStale || intPtrString(f.DigestRoles) != "2" {
		t.Errorf("last_run state=%s stage=%q cause=%q digest_roles=%s, want failed digest config_stale 2",
			f.State, f.Stage, f.Cause, intPtrString(f.DigestRoles))
	}
	title, body, _ := failureNotice(lastRecord(t, stateDir))
	if title != "Vani config not reloaded" || strings.Contains(body, "could not be finished") {
		t.Errorf("notice %q: %q", title, body)
	}
}

// The digest file is written, then marking its roles Digested fails: the run
// failed at stage digest with no known cause, and the digest's size is still
// recorded, because the digest exists.
func TestDigestRolesSurviveAFailedDigestedMark(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	old := buildApp
	t.Cleanup(func() { buildApp = old })
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error {
			l.mu.Lock()
			l.fail = true // after the pipeline, before the digest's appends
			l.mu.Unlock()
			return nil
		}}, nil
	}
	_, e := mustCode(t, 1, "run-once")
	if !strings.Contains(e, "digested") {
		t.Errorf("stderr %q does not name the failed mark", e)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "digests")); err != nil {
		t.Fatalf("no digest written: %v", err)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeFailed || r.Stage != stageDigest || r.Cause != "" || intPtrString(r.DigestRoles) != "2" {
		t.Errorf("record outcome=%s stage=%q cause=%q digest_roles=%s, want failed digest \"\" 2",
			r.Outcome, r.Stage, r.Cause, intPtrString(r.DigestRoles))
	}
}

// A second run on the same day finds nothing new: its digest is the day's
// existing file, and digest_roles is 0, which is present, not absent.
func TestDigestRolesZeroIsWritten(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	mustCode(t, 0, "run-once")
	mustCode(t, 0, "run-once")
	if r := lastRecord(t, stateDir); r.DigestRoles == nil || *r.DigestRoles != 0 {
		t.Fatalf("digest_roles = %s, want 0", intPtrString(r.DigestRoles))
	}
	o, _ := mustCode(t, 0, "status", "--json")
	var raw struct {
		LastRun map[string]json.RawMessage `json:"last_run"`
	}
	if err := json.Unmarshal([]byte(o), &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw.LastRun["digest_roles"]); got != "0" {
		t.Errorf("last_run.digest_roles = %q, want 0", got)
	}
	if _, ok := raw.LastRun["cause"]; ok {
		t.Errorf("an ok run printed a cause: %s", raw.LastRun["cause"])
	}
}

// A run that wrote no digest records neither field: absent is "not known",
// which is also how an older vani's record reads.
func TestNoDigestRecordsNoDigestRoles(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, errors.New("all boards down"))
	mustCode(t, 1, "run-once")
	raw, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"digest_roles"`, `"cause"`} {
		if strings.Contains(string(raw), k) {
			t.Errorf("record has %s:\n%s", k, raw)
		}
	}
}

// A record from a vani that wrote neither field still reads, and its error
// text does not choose the words.
func TestOlderRunRecordReads(t *testing.T) {
	_, stateDir := setup(t)
	old := `{"v":1,"pid":1,"started_at":"2026-10-18T15:00:00Z","finished_at":"2026-10-18T15:05:00Z","outcome":"failed","stage":"digest","error":"config reload failed"}`
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runRecordPath(stateDir), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := lastRecord(t, stateDir)
	if r.Cause != "" || r.DigestRoles != nil {
		t.Errorf("cause=%q digest_roles=%s from a record that has neither", r.Cause, intPtrString(r.DigestRoles))
	}
	if title, _, _ := failureNotice(r); title != "Vani run failed" {
		t.Errorf("title %q: the error text chose the words", title)
	}
}

func corruptMarks(t *testing.T, dir string) {
	t.Helper()
	if _, err := marks.SetMark(dir, "gh:a:1", marks.Hidden, time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "marks.jsonl")
	good, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append([]byte("{not json\n"), good...), 0o600); err != nil {
		t.Fatal(err)
	}
}
