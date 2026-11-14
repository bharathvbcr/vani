package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// putRecord writes a posting under k at at, then moves it to st.
func putRecord(t *testing.T, l ledger.Ledger, k posting.Key, st posting.Status, at time.Time) {
	t.Helper()
	p := posting.Posting{Key: k, ATS: k.ATS(), Company: "C", Title: "Role " + string(k), Location: "Remote", Description: "work"}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Live, Posting: &p, Reason: "new"}); err != nil {
		t.Fatal(err)
	}
	if st != posting.Live {
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: st, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}
}

// tombstone writes b's tombstone removed at at under dir.
func tombstone(t *testing.T, dir string, b sources.Board, at time.Time) RemovedBoardFile {
	t.Helper()
	rb, err := RemovedBoardFor(b, at)
	if err != nil {
		t.Fatal(err)
	}
	path, err := WriteRemovedBoard(dir, rb)
	if err != nil {
		t.Fatal(err)
	}
	return RemovedBoardFile{Path: path, RemovedBoard: rb}
}

func loadTombs(t *testing.T, dir string) []RemovedBoardFile {
	t.Helper()
	tombs, over, problems, err := LoadRemovedBoards(dir)
	if err != nil || over != 0 || len(problems) != 0 {
		t.Fatalf("load: over %d, problems %v, err %v", over, problems, err)
	}
	return tombs
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var (
	removedAt = t0.Add(48 * time.Hour)
	readAt    = removedAt.Add(time.Minute) // the run's config read, after the removal
	runAt     = readAt.Add(time.Minute)
)

// TestCloseRemovedBoardClosesItsOpenPostings: every open posting of the
// removed board dies with died_from its status and no miss marks, the
// tombstone goes, and a listing of the board added back returns each to its
// status.
func TestCloseRemovedBoardClosesItsOpenPostings(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	b := probeBoard("tavily")
	ps := seedBoard(t, l, b, 3)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: ps[1].Key, Status: posting.Digested, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	// A posting mid-absence: its miss marks must not survive the close.
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Hour), Key: ps[2].Key, Status: posting.Live,
		Annotations: map[string]string{ledger.MissedRunsKey: "1", MissedSinceKey: t0.Add(2 * time.Hour).Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	tb := tombstone(t, dir, b, removedAt)

	rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy([]sources.Board{probeBoard("other")}), readAt, runAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Closed) != 1 || rep.Closed[0].Closed != 3 || rep.Closed[0].Board != "gh/tavily" || rep.Waiting != 0 {
		t.Fatalf("report %+v", rep)
	}
	if exists(tb.Path) {
		t.Fatal("the tombstone outlived its board's closings")
	}
	want := map[posting.Key]posting.Status{ps[0].Key: posting.Live, ps[1].Key: posting.Digested, ps[2].Key: posting.Live}
	for k, from := range want {
		r := rec(t, l, k)
		if r.Status != posting.Dead || r.Annotations[DiedFromKey] != string(from) || r.MissedRuns != 0 ||
			r.Annotations[MissedSinceKey] != "" || !r.LastEvent.Equal(runAt) {
			t.Errorf("%s: %+v (want dead from %s, no miss marks)", k, r, from)
		}
	}
	// Added back: its listing revives each to the status it died in.
	if _, err := Reconcile(ctx, l, b, ps, runAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for k, from := range want {
		if r := rec(t, l, k); r.Status != from || r.Annotations[DiedFromKey] != "" {
			t.Errorf("%s after the re-add: %s, died_from %q (want %s)", k, r.Status, r.Annotations[DiedFromKey], from)
		}
	}
}

// TestCloseRemovedBoardLeavesWhatIsNotItsOwn: other boards (a token that
// extends the removed one's included), closed records, scraped page keys and
// postings ingested after the removal are untouched.
func TestCloseRemovedBoardLeavesWhatIsNotItsOwn(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	acme, acme2 := probeBoard("acme"), probeBoard("acme2")
	ps := seedBoard(t, l, acme, 6)
	other := seedBoard(t, l, acme2, 2)
	for i, st := range []posting.Status{posting.Dead, posting.Dropped, posting.Duplicate} {
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: ps[i].Key, Status: st, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	scraped := posting.Key("pg:acme-scrape:abc")
	putRecord(t, l, scraped, posting.Live, t0)
	ingested, _ := posting.GreenhouseKey("acme", "999")
	putRecord(t, l, ingested, posting.Live, removedAt.Add(time.Second)) // first seen after the removal
	before := map[posting.Key]time.Time{}
	for _, r := range l.All() {
		before[r.Key] = r.LastEvent
	}
	tombstone(t, dir, acme, removedAt)

	rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy([]sources.Board{acme2}), readAt, runAt)
	if err != nil {
		t.Fatal(err)
	}
	if c := rep.Closed[0]; c.Closed != 3 || c.KeptIngested != 1 || c.KeptWatched != 0 {
		t.Fatalf("report %+v", c)
	}
	for _, k := range []posting.Key{ps[0].Key, ps[1].Key, ps[2].Key, other[0].Key, other[1].Key, scraped, ingested} {
		if r := rec(t, l, k); !r.LastEvent.Equal(before[k]) {
			t.Errorf("%s (%s) was written: %+v", k, r.Status, r)
		}
	}
	for _, p := range ps[3:] {
		if r := rec(t, l, p.Key); r.Status != posting.Dead {
			t.Errorf("%s is %s, want dead", p.Key, r.Status)
		}
	}
}

// TestRemovedBoardStillWatchedClosesNothing: a board on the watchlist under
// the same prefix (the token in other case), or covering some keys, protects
// them. A recent tombstone of a watched board waits (the config this run read
// may predate the save); an old one is the board added back and goes.
func TestRemovedBoardStillWatchedClosesNothing(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	b := probeBoard("acme")
	ps := seedBoard(t, l, b, 2)
	tb := tombstone(t, dir, b, removedAt)
	again := []sources.Board{{ATS: posting.Greenhouse, Token: "ACME", Company: "Acme"}}

	rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(again), readAt, runAt)
	if err != nil || len(rep.Closed) != 0 || rep.Waiting != 1 || rep.Rewatched != 0 || !exists(tb.Path) {
		t.Fatalf("recent tombstone of a watched board: %+v, %v, kept %v", rep, err, exists(tb.Path))
	}
	late := removedAt.Add(RemovedSettle + time.Minute)
	rep, err = CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(again), late, late)
	if err != nil || len(rep.Closed) != 0 || rep.Rewatched != 1 || exists(tb.Path) {
		t.Fatalf("settled tombstone of a watched board: %+v, %v, kept %v", rep, err, exists(tb.Path))
	}
	for _, p := range ps {
		if r := rec(t, l, p.Key); r.Status != posting.Live {
			t.Errorf("%s is %s", p.Key, r.Status)
		}
	}

	// A watched board whose prefix lies inside the removed one's keeps its keys.
	outer := sources.Board{ATS: posting.Greenhouse, Token: "multi"}
	inner := posting.Key("gh:multi:team:1")
	plain := posting.Key("gh:multi:2")
	putRecord(t, l, inner, posting.Live, t0)
	putRecord(t, l, plain, posting.Live, t0)
	tombstone(t, dir, outer, removedAt)
	rep, err = CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy([]sources.Board{{ATS: posting.Greenhouse, Token: "multi:team"}}), readAt, runAt)
	if err != nil || len(rep.Closed) != 1 || rep.Closed[0].Closed != 1 || rep.Closed[0].KeptWatched != 1 {
		t.Fatalf("nested watched board: %+v, %v", rep, err)
	}
	if rec(t, l, inner).Status != posting.Live || rec(t, l, plain).Status != posting.Dead {
		t.Fatal("the nested watched board's posting was closed, or the removed one's was not")
	}
}

// TestRemovedBoardAfterTheConfigReadWaits: a tombstone written after the run
// read its config waits, whatever that config says; the next run acts on it.
func TestRemovedBoardAfterTheConfigReadWaits(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	b := probeBoard("acme")
	ps := seedBoard(t, l, b, 1)
	tb := tombstone(t, dir, b, removedAt)
	for _, read := range []time.Time{removedAt, removedAt.Add(-time.Minute)} {
		rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(nil), read, runAt)
		if err != nil || rep.Waiting != 1 || len(rep.Closed) != 0 || !exists(tb.Path) || rec(t, l, ps[0].Key).Status != posting.Live {
			t.Fatalf("read at %s: %+v, %v", read, rep, err)
		}
	}
	if rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(nil), readAt, runAt); err != nil || len(rep.Closed) != 1 {
		t.Fatalf("next run: %+v, %v", rep, err)
	}
}

// TestRemovedBoardWorkdayPrefix: a Workday board is closed by tenant and
// site; another site of the tenant stays. A Workday token without its site
// names no single board and is refused.
func TestRemovedBoardWorkdayPrefix(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	ext := posting.Key("wd:intel:external:r1")
	in := posting.Key("wd:intel:internal:r2")
	putRecord(t, l, ext, posting.Live, t0)
	putRecord(t, l, in, posting.Live, t0)
	tombstone(t, dir, sources.Board{ATS: posting.Workday, Token: "Intel/External"}, removedAt)
	if _, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(nil), readAt, runAt); err != nil {
		t.Fatal(err)
	}
	if rec(t, l, ext).Status != posting.Dead || rec(t, l, in).Status != posting.Live {
		t.Fatalf("external %s, internal %s", rec(t, l, ext).Status, rec(t, l, in).Status)
	}
	for _, b := range []sources.Board{
		{ATS: posting.Workday, Token: "intel"},
		{ATS: posting.Greenhouse, Token: " "},
		{ATS: posting.Greenhouse, Token: ""},
		{ATS: posting.Other, Token: "x"},
		{ATS: "zz", Token: "x"},
	} {
		if _, err := RemovedBoardFor(b, removedAt); err == nil {
			t.Errorf("%+v: accepted (prefix %q)", b, b.KeyPrefix())
		}
	}
}

// TestLoadRemovedBoardsRefusesBadFiles: unreadable, oversized, empty-prefix,
// wrong-version and timeless tombstones are problems, left in place; temp
// files and other names are ignored; a missing directory holds none.
func TestLoadRemovedBoardsRefusesBadFiles(t *testing.T) {
	if tombs, over, problems, err := LoadRemovedBoards(t.TempDir()); tombs != nil || over != 0 || problems != nil || err != nil {
		t.Fatalf("missing dir: %v %d %v %v", tombs, over, problems, err)
	}
	dir := t.TempDir()
	good := tombstone(t, dir, probeBoard("acme"), removedAt)
	sub := filepath.Join(dir, RemovedBoardDir)
	bad := map[string]string{
		"a-corrupt.json":  "{not json",
		"b-big.json":      `{"v":1,"board":"gh/x","prefix":"gh:x:","removed_at":"2026-10-03T09:00:00Z","pad":"` + strings.Repeat("x", maxRemovedBoardBytes) + `"}`,
		"c-noprefix.json": `{"v":1,"board":"gh/","prefix":"gh::","removed_at":"2026-10-03T09:00:00Z"}`,
		"d-all.json":      `{"v":1,"board":"gh","prefix":"gh:","removed_at":"2026-10-03T09:00:00Z"}`,
		"e-version.json":  `{"v":2,"board":"gh/x","prefix":"gh:x:","removed_at":"2026-10-03T09:00:00Z"}`,
		"f-notime.json":   `{"v":1,"board":"gh/x","prefix":"gh:x:"}`,
		"g-other.json":    `{"v":1,"board":"x","prefix":"other:x:","removed_at":"2026-10-03T09:00:00Z"}`,
		"h-dupkey.json":   `{"v":1,"prefix":"gh:x:","prefix":"gh:y:","board":"gh/x","removed_at":"2026-10-03T09:00:00Z"}`,
		".tmp.json":       "{",
		"notes.txt":       "x",
	}
	for n, body := range bad {
		if err := os.WriteFile(filepath.Join(sub, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tombs, over, problems, err := LoadRemovedBoards(dir)
	if err != nil || over != 0 {
		t.Fatal(over, err)
	}
	if len(tombs) != 1 || tombs[0].Path != good.Path {
		t.Fatalf("tombs %+v", tombs)
	}
	if len(problems) != 8 {
		t.Fatalf("%d problems, want 8: %v", len(problems), problems)
	}
	for n := range bad {
		if !exists(filepath.Join(sub, n)) {
			t.Errorf("%s was removed", n)
		}
	}
	if _, err := WriteRemovedBoard("", good.RemovedBoard); err == nil {
		t.Error("no state dir: accepted")
	}
}

// TestLoadRemovedBoardsIsBounded: past MaxRemovedBoards the rest wait.
func TestLoadRemovedBoardsIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxRemovedBoards + 3 {
		tombstone(t, dir, probeBoard(fmt.Sprintf("b%d", i)), removedAt)
	}
	tombs, over, problems, err := LoadRemovedBoards(dir)
	if err != nil || len(problems) != 0 || len(tombs) != MaxRemovedBoards || over != 3 {
		t.Fatalf("%d tombs, %d over, %v, %v", len(tombs), over, problems, err)
	}
}

// failingBatch is a ledger whose batches fail.
type failingBatch struct{ ledger.Ledger }

func (failingBatch) AppendBatch(context.Context, []ledger.Event) error {
	return errors.New("disk full")
}

// TestRemovedBoardLedgerFailureKeepsTheTombstone: a failed write keeps the
// tombstone for the next run; one acted on again (a crash after the closings,
// or a duplicate file) finds nothing open and writes nothing.
func TestRemovedBoardLedgerFailureKeepsTheTombstone(t *testing.T) {
	l, dir := openLedger(t), t.TempDir()
	b := probeBoard("acme")
	ps := seedBoard(t, l, b, 2)
	tb := tombstone(t, dir, b, removedAt)
	if _, err := CloseRemovedBoards(ctx, failingBatch{l}, loadTombs(t, dir), WatchedBy(nil), readAt, runAt); err == nil {
		t.Fatal("a failed write was not an error")
	}
	if !exists(tb.Path) || rec(t, l, ps[0].Key).Status != posting.Live {
		t.Fatal("the tombstone went, or a posting closed, though the write failed")
	}
	// Two files of one board (a hand copy): one closes, the other finds none.
	cp := filepath.Join(filepath.Dir(tb.Path), "gh-copy.json")
	data, err := os.ReadFile(tb.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(nil), readAt, runAt)
	if err != nil || len(rep.Closed) != 2 || rep.Closed[0].Closed+rep.Closed[1].Closed != 2 {
		t.Fatalf("%+v, %v", rep, err)
	}
	last := rec(t, l, ps[0].Key).LastEvent
	// A crash after the closings left the tombstone: the rerun is a no-op.
	tombstone(t, dir, b, removedAt)
	rep, err = CloseRemovedBoards(ctx, l, loadTombs(t, dir), WatchedBy(nil), readAt, runAt.Add(time.Hour))
	if err != nil || len(rep.Closed) != 1 || rep.Closed[0].Closed != 0 || !rec(t, l, ps[0].Key).LastEvent.Equal(last) {
		t.Fatalf("rerun: %+v, %v", rep, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	tombstone(t, dir, b, removedAt)
	if _, err := CloseRemovedBoards(cancelled, l, loadTombs(t, dir), WatchedBy(nil), readAt, runAt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
