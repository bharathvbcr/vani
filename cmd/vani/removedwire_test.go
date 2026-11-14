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

func removedTombs(t *testing.T, stateDir string) map[string]pipeline.RemovedBoard {
	t.Helper()
	tombs, _, problems, err := pipeline.LoadRemovedBoards(stateDir)
	if err != nil || len(problems) != 0 {
		t.Fatalf("tombstones: %v %v", problems, err)
	}
	out := map[string]pipeline.RemovedBoard{}
	for _, tb := range tombs {
		out[tb.Prefix] = tb.RemovedBoard
	}
	return out
}

// TestWatchlistRemoveLeavesATombstone: remove records the board for the next
// run; --keep-postings records nothing; a board already off the watchlist is
// an error unless --close-postings, which records it and leaves the config
// file as it was; a failed save leaves no tombstone behind.
func TestWatchlistRemoveLeavesATombstone(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "lv", "--token", "globex", "--company", "Globex", "--allow-host")

	before := time.Now()
	o, _ := mustCode(t, 0, "--json", "watchlist", "remove", "--ats", "gh", "--token", "Acme")
	var reply struct {
		Removed  *watchlistJSON `json:"removed"`
		Postings string         `json:"postings"`
	}
	if err := json.Unmarshal([]byte(o), &reply); err != nil || reply.Removed == nil || reply.Removed.Token != "Acme" || reply.Postings != "close" {
		t.Fatalf("reply %s (%v)", o, err)
	}
	tb, ok := removedTombs(t, stateDir)["gh:acme:"]
	if !ok || tb.Board != "gh/Acme" || tb.RemovedAt.Before(before.Add(-time.Second)) || tb.RemovedAt.After(time.Now()) {
		t.Fatalf("tombstone %+v (found %v)", tb, ok)
	}

	o, _ = mustCode(t, 0, "watchlist", "remove", "--ats", "lv", "--token", "globex", "--keep-postings")
	if !strings.Contains(o, "stay open") {
		t.Errorf("keep text: %q", o)
	}
	if _, ok := removedTombs(t, stateDir)["lv:globex:"]; ok {
		t.Error("--keep-postings left a tombstone")
	}

	// Already off the watchlist: an error that names the flag, and no tombstone.
	_, e := mustCode(t, 1, "watchlist", "remove", "--ats", "ab", "--token", "tavily")
	if !strings.Contains(e, "--close-postings") {
		t.Errorf("absent board error: %q", e)
	}
	if _, ok := removedTombs(t, stateDir)["ab:tavily:"]; ok {
		t.Fatal("a refused remove left a tombstone")
	}
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "--json", "watchlist", "remove", "--ats", "ab", "--token", "tavily", "--close-postings")
	reply.Removed, reply.Postings = nil, ""
	if err := json.Unmarshal([]byte(o), &reply); err != nil || reply.Removed != nil || reply.Postings != "close" {
		t.Fatalf("absent reply %s (%v)", o, err)
	}
	if _, ok := removedTombs(t, stateDir)["ab:tavily:"]; !ok {
		t.Fatal("--close-postings left no tombstone")
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(after, cfgBefore) {
		t.Error("--close-postings on an absent board rewrote the config")
	}

	mustCode(t, 2, "watchlist", "remove", "--ats", "gh", "--token", "x", "--keep-postings", "--close-postings")
	mustCode(t, 2, "watchlist", "remove", "--ats", "wd", "--token", "intel", "--close-postings") // no site: names no single board

	// A save that fails takes its tombstone back: a config the update cannot
	// read refuses before anything is written.
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "initech", "--company", "Initech", "--allow-host")
	if err := os.WriteFile(cfgPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 1, "watchlist", "remove", "--ats", "gh", "--token", "initech")
	if _, ok := removedTombs(t, stateDir)["gh:initech:"]; ok {
		t.Error("a failed remove left a tombstone")
	}
}

// removedFixture is a wired run whose whole watchlist is gh/keep, over a
// ledger with one open posting on gh/keep and two on gh/gone, which was
// removed after they were seen.
func removedFixture(t *testing.T) (w *wired, warn *bytes.Buffer, kept, gone posting.Key) {
	t.Helper()
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "keep", Company: "Keep"}}
	w, warn = openWired(t, cfg)
	for _, k := range []string{"gh:keep:1", "gh:gone:1", "gh:gone:2"} {
		appendLive(t, w.led, k, "Acme")
	}
	rb, err := pipeline.RemovedBoardFor(sources.Board{ATS: posting.Greenhouse, Token: "gone"}, time.Now()) // after the postings were seen
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.WriteRemovedBoard(w.stateDir, rb); err != nil {
		t.Fatal(err)
	}
	return w, warn, "gh:keep:1", "gh:gone:1"
}

// TestRunClosesARemovedBoardsPostings: the run closes the removed board's
// open postings, says so, reports them, and leaves the watched board's.
func TestRunClosesARemovedBoardsPostings(t *testing.T) {
	w, warn, kept, gone := removedFixture(t)
	w.watch(w.cfg.Watchlist, time.Now())
	got, err := w.closeRemovedBoards(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (removedBoardRun{Board: "gh/gone", Closed: 2}) {
		t.Fatalf("report %+v", got)
	}
	if r := mustGet(t, w.led, gone); r.Status != posting.Dead || r.Annotations[pipeline.DiedFromKey] != string(posting.Live) {
		t.Errorf("removed board's posting: %+v", r)
	}
	if r := mustGet(t, w.led, kept); r.Status != posting.Live {
		t.Errorf("watched board's posting is %s", r.Status)
	}
	if !strings.Contains(warn.String(), "removed board gh/gone: closed 2 open posting(s)") {
		t.Errorf("warnings: %s", warn.String())
	}
	if tombs := removedTombs(t, w.stateDir); len(tombs) != 0 {
		t.Errorf("tombstones left: %+v", tombs)
	}
}

// TestRunWithoutAReadWatchlistClosesNothing: a run that read no watchlist
// from the file (the config could not be read, or none was handed over) or
// read an empty one closes nothing and keeps the tombstone.
func TestRunWithoutAReadWatchlistClosesNothing(t *testing.T) {
	for name, watch := range map[string]func(w *wired){
		"never read":  func(*wired) {},
		"stale file":  func(w *wired) { w.watch(w.cfg.Watchlist, time.Time{}) },
		"empty list":  func(w *wired) { w.watch(nil, time.Now()) },
		"read before": func(w *wired) { w.watch(w.cfg.Watchlist, time.Now().Add(-time.Hour)) },
	} {
		t.Run(name, func(t *testing.T) {
			w, warn, _, gone := removedFixture(t)
			watch(w)
			got, err := w.closeRemovedBoards(context.Background(), time.Now())
			if err != nil || len(got) != 0 {
				t.Fatalf("%+v, %v", got, err)
			}
			if r := mustGet(t, w.led, gone); r.Status != posting.Live {
				t.Errorf("closed with no fresh watchlist: %s", r.Status)
			}
			if len(removedTombs(t, w.stateDir)) != 1 {
				t.Error("the tombstone went")
			}
			if name != "read before" && !strings.Contains(warn.String(), "1 wait") {
				t.Errorf("warnings: %s", warn.String())
			}
		})
	}
}

// TestReloadHandsOverWhenTheWatchlistWasRead: a good read hands over its
// time; a stale one, cut or not, never a time (the list is not this file's).
func TestReloadHandsOverWhenTheWatchlistWasRead(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}}
	start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
	writeConfig(t, cfgPath, start)
	var gotList []sources.Board
	var gotRead time.Time
	app := &App{Reload: func(config.Config) {}, Watch: func(l []sources.Board, read time.Time) { gotList, gotRead = l, read }}
	before := time.Now()
	if _, err := e.reloadConfig(app, start, []string{"gh:a"}); err != nil {
		t.Fatal(err)
	}
	if len(gotList) != 2 || gotRead.Before(before) || gotRead.After(time.Now()) {
		t.Fatalf("good read: %d boards at %v", len(gotList), gotRead)
	}
	if err := os.WriteFile(cfgPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reloadConfig(app, start, []string{"gh:a"}); err == nil {
		t.Fatal("a broken config was not stale")
	}
	if !gotRead.IsZero() || len(gotList) != 2 {
		t.Fatalf("stale read: %d boards at %v, want the last list and no time", len(gotList), gotRead)
	}
}

// TestRunCutKeepsOtherBoardsPendingReopens: a run limited to some boards
// judges what is watched by the whole watchlist, so it neither clears (and
// saves away) the other boards' pending reopens nor leaves their loosened
// exclusion drops for good as if their boards were gone.
func TestRunCutKeepsOtherBoardsPendingReopens(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedDrops(t, cfgPath, stateDir) // board gh/d
	mustCode(t, 0, "retriage", "--policy-drops")
	if n := loadPending(t, stateDir).Len(); n != 2 {
		t.Fatalf("setup: %d pending", n)
	}
	watchGreenhouse(t, cfgPath, "e")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cut := cfg
	cut.Watchlist = []sources.Board{cfg.Watchlist[len(cfg.Watchlist)-1]} // --board gh:e
	w := &wired{cfg: cut, stateDir: stateDir, led: l, warn: &bytes.Buffer{}}
	w.watch(cfg.Watchlist, time.Now())
	rec := pipeline.NewReconciler(l)
	finish := w.reopenOnSighting(rec, time.Now())
	if _, err := rec.Reconcile(context.Background(), cut.Watchlist[0], nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	finish()
	if n := loadPending(t, stateDir).Len(); n != 2 {
		t.Fatalf("a run cut to gh/e left %d of gh/d's 2 pending reopens", n)
	}

	// The exclusion pass of a cut run defers a watched board's drop as well.
	w2, _, watched, _, _ := exclusionFixture(t)
	whole := append(w2.cfg.Watchlist, sources.Board{ATS: posting.Greenhouse, Token: "other", Company: "Other"})
	w2.watch(whole, time.Now())
	w2.cfg.Watchlist = whole[1:]
	if err := w2.applyExclusions(context.Background(), dropPolicy(w2.cfg, ""), nil, time.Now(), &pipeline.FailOpen{}); err != nil {
		t.Fatal(err)
	}
	if pend := loadPending(t, w2.stateDir); pend.Items[watched].Class != "exclusion" {
		t.Fatalf("a cut run left the watched board's loosened drop out: %+v", pend.Items)
	}
}
