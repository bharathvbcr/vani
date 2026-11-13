package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// Pending reopens (2026-11-12, the owner's choice): retriage no longer puts a
// watched board's drops back at once. It marks them, still dropped, and the
// run's listing reopens each when its board lists it again. Every test here
// failed on 5a9970b.

func pendingFile(stateDir string) string { return filepath.Join(stateDir, pipeline.PendingReopenFile) }

func loadPending(t *testing.T, stateDir string) *pipeline.PendingReopens {
	t.Helper()
	s, err := pipeline.LoadPendingReopens(pendingFile(stateDir), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRetriageMarksWatchedDropsPending(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	model, oldName, title, excluded := seedDrops(t, cfgPath, stateDir)

	o, _ := mustCode(t, 0, "retriage", "--dry-run", "--policy-drops")
	for _, want := range []string{
		"2 posting(s) would be let back",
		"waiting for their board to list them again: 2",
		"title drops, back to live for the backends to read: 1",
		"model drops, back on their stored reading (no model asked): 1",
		"by drop class:",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run lacks %q:\n%s", want, o)
		}
	}
	if _, err := os.Stat(pendingFile(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the pending file (%v)", err)
	}

	o, _ = mustCode(t, 0, "--json", "retriage", "--policy-drops")
	var rt retriageRunReply
	decodeOne(t, o, &rt)
	if rt.Returned != 2 || rt.Deferred == nil || *rt.Deferred != 2 || rt.Reopened == nil || *rt.Reopened != 0 {
		t.Fatalf("reply %s", o)
	}
	// The drop classes of what was let back, as the audit names them, account
	// for every returned posting (the stale-drops report's breakdown).
	classed := 0
	for c, n := range rt.Classes {
		if c == "" {
			t.Errorf("a returned posting has no drop class: %s", o)
		}
		classed += n
	}
	if classed != rt.Returned || !strings.Contains(o, `"classes"`) {
		t.Errorf("classes %v cover %d of %d returned", rt.Classes, classed, rt.Returned)
	}
	s := loadPending(t, stateDir)
	if s.Len() != 2 || s.Items[model].Class != "model" || s.Items[title].Class != "title" {
		t.Fatalf("pending %+v", s.Items)
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records {
		if r.Status != posting.Dropped {
			t.Errorf("%s is %s: a pending reopen must stay dropped until its board lists it", r.Key, r.Status)
		}
	}
	// A second mark adds nothing.
	o, _ = mustCode(t, 0, "--json", "retriage", "--policy-drops")
	decodeOne(t, o, &rt)
	if rt.AlreadyPending == nil || *rt.AlreadyPending != 2 || *rt.Deferred != 0 {
		t.Fatalf("second mark %s", o)
	}
	_, _ = oldName, excluded
}

// The owner's re-read of unjudged drops: --refresh-unjudged marks the drop
// whose reading was made under the model's old name for a fresh read.
func TestRetriageRefreshUnjudged(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	_, oldName, _, _ := seedDrops(t, cfgPath, stateDir)
	o, _ := mustCode(t, 0, "--json", "retriage", "--dry-run", "--policy-drops", "--refresh-unjudged")
	var rt retriageRunReply
	decodeOne(t, o, &rt)
	if rt.RefreshReturned != 1 || rt.Unjudged == nil || *rt.Unjudged != 0 {
		t.Fatalf("dry run %s", o)
	}
	mustCode(t, 0, "retriage", "--policy-drops", "--refresh-unjudged")
	if c := loadPending(t, stateDir).Items[oldName].Class; c != "refresh" {
		t.Fatalf("%s pending as %q, want refresh", oldName, c)
	}
	mustCode(t, 2, "retriage", "--refresh-unjudged")
}

// Finding D: every count is present at zero, in both modes, so a 0 is told
// apart from a binary that does not report it.
func TestRetriageJSONReportsReturnedCountsAtZero(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, flag := range []string{"--policy-drops", "--title-drops"} {
		o, _ := mustCode(t, 0, "--json", "retriage", "--dry-run", flag)
		var m map[string]json.RawMessage
		decodeOne(t, o, &m)
		for _, k := range []string{"title_returned", "model_returned", "fresh_returned", "refresh_returned", "deferred",
			"reopened", "already_pending", "over_cap", "stale"} {
			if v, ok := m[k]; !ok || string(v) != "0" {
				t.Errorf("%s: %q = %s (present %v), want 0 present:\n%s", flag, k, v, ok, o)
			}
		}
		// A dry run reads the event log, where every body is whole: it cannot
		// see a body the ledger would fail to read back, and says so.
		if v := string(m["unreadable_checked"]); v != "false" {
			t.Errorf("%s: unreadable_checked = %s, want false in a dry run", flag, v)
		}
	}
}

// vani status names the pending reopens, text and JSON.
func TestStatusReportsPendingReopens(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedDrops(t, cfgPath, stateDir)
	o, _ := mustCode(t, 0, "--json", "status")
	var st struct {
		Ledger struct {
			PendingReopens *int `json:"pending_reopens"`
		} `json:"ledger"`
	}
	if err := json.Unmarshal([]byte(o), &st); err != nil || st.Ledger.PendingReopens == nil || *st.Ledger.PendingReopens != 0 {
		t.Fatalf("status json before: %s (%v)", o, err)
	}
	mustCode(t, 0, "retriage", "--policy-drops")
	o, _ = mustCode(t, 0, "status")
	if !strings.Contains(o, "pending reopen: 2 dropped posting(s) come back when their board lists them again") {
		t.Errorf("status text:\n%s", o)
	}
	o, _ = mustCode(t, 0, "--json", "status")
	if err := json.Unmarshal([]byte(o), &st); err != nil || st.Ledger.PendingReopens == nil || *st.Ledger.PendingReopens != 2 {
		t.Fatalf("status json after: %s (%v)", o, err)
	}
}

// The run's listing applies the pending reopens: the wiring loads the set,
// the reconciler applies it on a full listing, and the set is saved spent.
func TestRunListingAppliesPendingReopens(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	model, _, title, _ := seedDrops(t, cfgPath, stateDir)
	mustCode(t, 0, "retriage", "--policy-drops")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	w := &wired{cfg: cfg, stateDir: stateDir, led: l, warn: &bytes.Buffer{}}
	rec := pipeline.NewReconciler(l)
	finish := w.reopenOnSighting(rec, time.Now())
	board := sources.Board{ATS: posting.Greenhouse, Token: "d", Company: "Acme"}
	listed := []posting.Posting{ledger.Full(l, mustGet(t, l, model)).Posting, ledger.Full(l, mustGet(t, l, title)).Posting}
	sum, err := rec.Reconcile(context.Background(), board, listed, time.Now())
	if err != nil || sum.Reopened != 2 {
		t.Fatalf("reconcile %+v (%v)", sum, err)
	}
	finish()
	if r := mustGet(t, l, model); r.Status != posting.Triaged {
		t.Errorf("model drop is %s, want triaged on its reading", r.Status)
	}
	if r := mustGet(t, l, title); r.Status != posting.Live {
		t.Errorf("title drop is %s, want live", r.Status)
	}
	if s := loadPending(t, stateDir); s.Len() != 0 {
		t.Errorf("%d still pending after the sighting", s.Len())
	}
	if !strings.Contains(w.warn.(*bytes.Buffer).String(), "pending reopens: 2 applied") {
		t.Errorf("warnings: %s", w.warn.(*bytes.Buffer).String())
	}
}

// An unreadable pending file never stops the run, never looks empty, and is
// never overwritten: the run applies nothing and says why.
func TestRunListingLeavesAnUnreadablePendingFile(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	if err := os.MkdirAll(filepath.Dir(pendingFile(stateDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingFile(stateDir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	warn := &bytes.Buffer{}
	w := &wired{cfg: cfg, stateDir: stateDir, warn: warn}
	rec := pipeline.NewReconciler(nil)
	w.reopenOnSighting(rec, time.Now())()
	if b, _ := os.ReadFile(pendingFile(stateDir)); string(b) != "{broken" {
		t.Errorf("the unreadable file was rewritten: %q", b)
	}
	if !strings.Contains(warn.String(), "pending reopens not applied") {
		t.Errorf("warnings: %s", warn.String())
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "pending reopen: unknown") {
		t.Errorf("status text:\n%s", o)
	}
}

func mustGet(t *testing.T, l ledger.Ledger, k posting.Key) ledger.Record {
	t.Helper()
	r, ok := l.Get(k)
	if !ok {
		t.Fatalf("no record %s", k)
	}
	return r
}
