package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestStarvedFirstIsStableAndDoesNotMutate(t *testing.T) {
	list := []sources.Board{efBoard("a", "h"), efBoard("b", "h"), efBoard("c", "h"), efBoard("d", "h"), efBoard("e", "h")}
	orig := slices.Clone(list)
	got := starvedFirst(list, []string{"ef:d", "ef:b", "ef:gone"})
	var tokens []string
	for _, b := range got {
		tokens = append(tokens, b.Token)
	}
	if strings.Join(tokens, "") != "bdace" {
		t.Fatalf("order %v, want the starved boards first and each group in watchlist order", tokens)
	}
	if !slices.EqualFunc(list, orig, func(a, b sources.Board) bool { return a.Token == b.Token }) {
		t.Fatal("the configured watchlist was reordered")
	}
	// Nothing starved, everything starved, or no match: the list as it was.
	for name, keys := range map[string][]string{"none": nil, "unknown": {"ef:zzz"}, "all": {"ef:a", "ef:b", "ef:c", "ef:d", "ef:e"}} {
		if g := starvedFirst(list, keys); !slices.EqualFunc(g, list, func(a, b sources.Board) bool { return a.Token == b.Token }) {
			t.Errorf("%s: reordered to %v", name, g)
		}
	}
	if g := starvedFirst(nil, []string{"ef:a"}); len(g) != 0 {
		t.Errorf("an empty watchlist grew to %v", g)
	}
}

func TestStarvedKeyIsCaseAndLengthSafe(t *testing.T) {
	if a, b := boardStarvedKey(sources.Board{ATS: posting.Workday, Token: " Nvidia/Site "}), starvedKey("wd", "nvidia/site"); a != b {
		t.Errorf("%q vs %q", a, b)
	}
	long := strings.Repeat("x", 5000)
	if k := starvedKey("gh", long); len(k) > 4*maxBoardNameRunes+20 {
		t.Errorf("a %d-byte key for a 5000-byte token", len(k))
	}
	// A board report writes its token through oneLine; the board must agree.
	b := sources.Board{ATS: posting.Greenhouse, Token: "line\nbreak"}
	if boardStarvedKey(b) != starvedKey("gh", oneLine(b.Token, maxBoardNameRunes)) {
		t.Errorf("a board and its report name %q and %q", boardStarvedKey(b), starvedKey("gh", oneLine(b.Token, maxBoardNameRunes)))
	}
}

func TestNextStarved(t *testing.T) {
	rl := func(tok string) boardReport { return boardReport{ATS: "ef", Token: tok, Outcome: boardRateLimited} }
	read := func(tok string) boardReport { return boardReport{ATS: "ef", Token: tok, Outcome: boardRead} }
	failed := func(tok string) boardReport { return boardReport{ATS: "ef", Token: tok, Outcome: boardFailed} }
	prior := []string{"ef:old-read", "ef:old-failed", "ef:old-limited", "ef:old-unreached"}
	tried := []boardReport{rl("new-a"), read("old-read"), failed("old-failed"), rl("old-limited"), rl("new-a"), read("fine"), rl("New-B")}
	got := nextStarved(prior, tried)
	want := []string{"ef:new-a", "ef:old-limited", "ef:new-b", "ef:old-unreached"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v: this run's rate-limited boards once each, then the unreached; a board that had its turn drops off", got, want)
	}
	if got := nextStarved(nil, nil); len(got) != 0 {
		t.Errorf("nothing tried: %v", got)
	}
}

// siteBySource lists boards of one host, recording the order they were started.
type startOrder struct {
	mu    sync.Mutex
	order []string
}

func (s *startOrder) add(tok string) {
	s.mu.Lock()
	s.order = append(s.order, tok)
	s.mu.Unlock()
}

func orderedSource(site *hostSite, so *startOrder) map[posting.ATS]sources.Source {
	inner := siteSource(site)[posting.Eightfold]
	return map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		so.add(b.Token)
		return inner.List(ctx, b)
	}}}
}

// The boards a throttled host refused are tried first the next run, so the same
// boards at the back of its queue are not the ones that go without every day;
// every one of them stays unreconciled (nothing aged) until it is read.
func TestListBoardsTriesRateLimitedBoardsFirstNextRun(t *testing.T) {
	file := cooldownFile(t)
	var cr reconciled
	rec := tempReconciler(t)
	boards := []sources.Board{efBoard("a", msHost), efBoard("b", msHost), efBoard("c", msHost), efBoard("d", msHost),
		efBoard("e", msHost), efBoard("f", msHost), efBoard("g", msHost)}

	// Day 1: a is read, then the host answers 429 to everything.
	withClock(t, cooldownClock)
	site := &hostSite{status: func(n int) int {
		if n == 1 {
			return 200
		}
		return 429
	}}
	var so startOrder
	var warn warnLog
	rep, err := listBoards(t.Context(), boards, orderedSource(site, &so), nil, cr.wrap(rec), warn.warnf, file)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 1 || rep.RateLimited != 6 || strings.Join(cr.got, ",") != "a" {
		t.Fatalf("day 1: %+v, reconciled %v; want a read and the other six rate limited, unreconciled", rep, cr.got)
	}
	if strings.Join(so.order, "") != "abcdefg"[:len(so.order)] {
		t.Fatalf("day 1 started %v; the first run keeps the configured order", so.order)
	}
	starved, warnMsg := srcutil.LoadStarved(starvedFile(file), cooldownClock)
	if warnMsg != "" || strings.Join(starved, ",") != "ef:b,ef:c,ef:d,ef:e,ef:f,ef:g" {
		t.Fatalf("starved file %v, %q", starved, warnMsg)
	}

	// Day 2: the host is back (after the cooldown file's window). The starved
	// boards go first, a last; all are read; nothing is starved any more.
	day2 := cooldownClock.Add(48 * 60 * 60 * 1e9)
	withClock(t, day2)
	site2 := &hostSite{status: up}
	var so2 startOrder
	cr.got = nil
	rep, err = listBoards(t.Context(), boards, orderedSource(site2, &so2), nil, cr.wrap(rec), warn.warnf, file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(so2.order, "") != "bcdefga" {
		t.Errorf("day 2 started %v, want the starved boards first", so2.order)
	}
	if rep.Read != 7 || rep.RateLimited != 0 {
		t.Errorf("day 2: %+v", rep)
	}
	if starved, _ := srcutil.LoadStarved(starvedFile(file), day2); len(starved) != 0 {
		t.Errorf("starved after a clean run: %v", starved)
	}
}

// A run that stops part-way leaves the starved boards it never reached starved:
// they have not had their turn.
func TestListBoardsCancelledRunKeepsUnreachedStarvedBoards(t *testing.T) {
	withBoardListParallel(t, 1)
	withClock(t, cooldownClock)
	file := cooldownFile(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := srcutil.SaveStarved(starvedFile(file), []string{"ef:stop", "ef:late"}, cooldownClock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srcs := map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(c context.Context, b sources.Board) ([]posting.Posting, error) {
		if b.Token == "stop" {
			cancel()
			return nil, c.Err()
		}
		return boardPostings(b, 1), nil
	}}}
	var warn warnLog
	boards := []sources.Board{efBoard("a", "x.test"), efBoard("stop", "z.test"), efBoard("late", "y.test")}
	_, err := listBoards(ctx, boards, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, file)
	if err == nil {
		t.Fatal("want the cancellation")
	}
	got, _ := srcutil.LoadStarved(starvedFile(file), cooldownClock)
	if !slices.Contains(got, "ef:late") {
		t.Errorf("starved after a cancelled run: %v; the board it never reached must stay", got)
	}
}

// A starved file that cannot be used never stops the run: a warning says so,
// the configured order stands, and the file is replaced by a good one.
func TestListBoardsStarvedFileAdversarial(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":       "\x00\xff not json",
		"wrong version": `{"version":7,"at":"2026-10-22T12:00:00Z","boards":["ef:b"]}`,
		"oversized":     `{"version":1,"at":"2026-10-22T12:00:00Z","boards":["` + strings.Repeat("x", 2<<20) + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			withClock(t, cooldownClock)
			file := cooldownFile(t)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(starvedFile(file), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			site := &hostSite{status: up}
			var so startOrder
			var warn warnLog
			boards := []sources.Board{efBoard("a", msHost), efBoard("b", msHost)}
			rep, err := listBoards(t.Context(), boards, orderedSource(site, &so), nil, tempReconciler(t).Reconcile, warn.warnf, file)
			if err != nil || rep.Read != 2 || strings.Join(so.order, "") != "ab" {
				t.Fatalf("%+v, %v, order %v", rep, err, so.order)
			}
			if !strings.Contains(warn.String(), "starved boards ignored") {
				t.Errorf("warnings %q", warn.String())
			}
			if got, warn := srcutil.LoadStarved(starvedFile(file), cooldownClock); len(got) != 0 || warn != "" {
				t.Errorf("file not replaced by a good one: %v, %q", got, warn)
			}
		})
	}
}

// With no cooldown file there is no starved file: the memory stays in the run.
func TestListBoardsWithoutACooldownFileKeepsNoStarvedFile(t *testing.T) {
	if starvedFile("") != "" {
		t.Fatal("a starved file without a cooldown file")
	}
	prior, save := openStarved("", func(string, ...any) {})
	save([]boardReport{{ATS: "ef", Token: "a", Outcome: boardRateLimited}})
	if len(prior) != 0 {
		t.Fatal(prior)
	}
}
