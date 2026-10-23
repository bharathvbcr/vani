package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Boards of one host that is throttled, listed through the real srcutil
// request loop against a fake host. Retry-After: 0 keeps the retries instant,
// so request counts are the measure.

const msHost = "apply.careers.microsoft.com"

var cooldownClock = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// hostSite is a fake host: status answers each request (n counts from 1) and
// requests are counted.
type hostSite struct {
	mu     sync.Mutex
	n      int
	status func(n int) int
}

func (h *hostSite) doer() *srctest.Doer {
	return srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		h.mu.Lock()
		h.n++
		n := h.n
		h.mu.Unlock()
		st := h.status(n)
		r := srctest.Reply{Status: st, Body: []byte(`{}`)}
		if st == 429 {
			r.Header = map[string][]string{"Retry-After": {"0"}}
		}
		return r, true
	})
}

func (h *hostSite) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

func down(int) int { return 429 }
func up(int) int   { return 200 }

// siteSource lists any Eightfold board by one GET on site, like an adapter.
func siteSource(site *hostSite) map[posting.ATS]sources.Source {
	d := site.doer()
	return map[posting.ATS]sources.Source{posting.Eightfold: fakeSource{posting.Eightfold, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		var v struct{}
		if err := srcutil.GetJSON(ctx, d, "https://"+b.Host+"/api/pcsx/search", &v); err != nil {
			return nil, fmt.Errorf("eightfold %q: search: %w", b.Token, err)
		}
		return boardPostings(b, 2), nil
	}}}
}

func efBoard(token, host string) sources.Board {
	return sources.Board{ATS: posting.Eightfold, Token: token, Company: token, Host: host, Searches: []string{"x"}}
}

// reconciled wraps a reconciler and records the boards it was asked about.
type reconciled struct {
	mu  sync.Mutex
	got []string
}

func (c *reconciled) wrap(rec *pipeline.Reconciler) reconcileFunc {
	return func(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error) {
		c.mu.Lock()
		c.got = append(c.got, b.Token)
		c.mu.Unlock()
		return rec.Reconcile(ctx, b, listed, now)
	}
}

func withClock(t *testing.T, at time.Time) {
	t.Helper()
	old := boardsClock
	boardsClock = func() time.Time { return at }
	t.Cleanup(func() { boardsClock = old })
}

func cooldownFile(t *testing.T) string { return filepath.Join(t.TempDir(), "hosts", "cooldown.json") }

func readCooldowns(t *testing.T, path string) map[string]srcutil.HostCooldown {
	t.Helper()
	got, warn := srcutil.LoadCooldowns(path, cooldownClock.Add(72*time.Hour))
	if warn != "" {
		t.Fatalf("cooldown file: %s", warn)
	}
	return got
}

// Within a run: the first board spends its retries, the second asks once, the
// third not at all. Every one ends rate_limited and none is reconciled, so no
// posting is touched.
func TestListBoardsThrottledHostIsAskedOnceMoreThenNotAtAll(t *testing.T) {
	withClock(t, cooldownClock)
	site := &hostSite{status: down}
	var cr reconciled
	boards := []sources.Board{efBoard("a", msHost), efBoard("b", msHost), efBoard("c", msHost)}
	var warn warnLog
	rep, err := listBoards(t.Context(), boards, siteSource(site), nil, cr.wrap(tempReconciler(t)), warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	if site.requests() != 5+1 {
		t.Errorf("%d requests, want 5 for the first board, 1 probe for the second, none for the third", site.requests())
	}
	if rep.RateLimited != 3 || rep.Failed != 3 || rep.Read != 0 || len(cr.got) != 0 {
		t.Fatalf("report %+v, reconciled %v", rep, cr.got)
	}
	for i, want := range []string{"gave up after 5 attempts", "host rate limited earlier this run; probed once", "host rate limited earlier this run; probed once"} {
		if e := rep.Items[i]; e.Outcome != boardRateLimited || !strings.Contains(e.Error, want) {
			t.Errorf("board %d: %+v, want rate_limited with %q", i, e, want)
		}
	}
}

// The same host on later runs: one probe, then every board stops; the error
// says it is a cooldown and how long it has lasted; the file keeps count and
// forgets the host once it answers.
func TestListBoardsCooldownAcrossRuns(t *testing.T) {
	file := cooldownFile(t)
	rec := tempReconciler(t)
	boards := []sources.Board{efBoard("a", msHost), efBoard("b", msHost)}
	var cr reconciled

	runOnce := func(at time.Time, status func(int) int) (boardsReport, *hostSite, string) {
		withClock(t, at)
		site := &hostSite{status: status}
		var warn warnLog
		rep, err := listBoards(t.Context(), boards, siteSource(site), nil, cr.wrap(rec), warn.warnf, file)
		if err != nil {
			t.Fatal(err)
		}
		return rep, site, warn.String()
	}

	// Day 1: the host is down. It costs the usual five requests, once.
	_, site, _ := runOnce(cooldownClock, down)
	if site.requests() != 5+1 {
		t.Fatalf("day 1: %d requests", site.requests())
	}
	if c := readCooldowns(t, file)[msHost]; c.Runs != 1 || !c.Last.Equal(cooldownClock) {
		t.Fatalf("day 1 cooldown %+v", c)
	}

	// Day 2: still down. One request in all, for both boards.
	day2 := cooldownClock.Add(24 * time.Hour)
	rep, site, log := runOnce(day2, down)
	if site.requests() != 1 {
		t.Fatalf("day 2: %d requests, want one probe", site.requests())
	}
	for _, e := range rep.Items {
		if e.Outcome != boardRateLimited || !strings.Contains(e.Error, "host in cooldown after 2 rate-limited runs; probed once") {
			t.Errorf("day 2 board %+v", e)
		}
	}
	if !strings.Contains(log, "host in cooldown after 2 rate-limited runs; probed once") || !strings.Contains(log, "left unchanged, will retry next run") {
		t.Errorf("day 2 warnings:\n%s", log)
	}
	if c := readCooldowns(t, file)[msHost]; c.Runs != 2 || !c.Last.Equal(day2) {
		t.Errorf("day 2 cooldown %+v", c)
	}
	// The cooldown is in the run record's board report, which the app reads.
	raw, _ := json.Marshal(rep)
	if !strings.Contains(string(raw), `"outcome":"rate_limited"`) || !strings.Contains(string(raw), "probed once") {
		t.Errorf("record: %s", raw)
	}

	// Day 3: back. The probe is answered, both boards are read, the file forgets
	// the host.
	day3 := day2.Add(24 * time.Hour)
	rep, site, _ = runOnce(day3, up)
	if rep.Read != 2 || rep.Failed != 0 || site.requests() != 2 || len(cr.got) != 2 {
		t.Errorf("day 3: %+v, %d requests, reconciled %v", rep, site.requests(), cr.got)
	}
	if got := readCooldowns(t, file); len(got) != 0 {
		t.Errorf("day 3 cooldowns %v, want none", got)
	}
}

// A cooldown never ages or kills a posting: the board that was only probed is
// not reconciled, so its postings keep their status through any number of runs.
func TestListBoardsCooldownLeavesPostingsAlone(t *testing.T) {
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	rc := pipeline.NewReconciler(l)
	b := efBoard("ms", msHost)
	before := boardPostings(b, 3)
	if _, err := rc.Reconcile(t.Context(), b, before, cooldownClock.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	file := cooldownFile(t)
	if err := srcutil.SaveCooldowns(file, map[string]srcutil.HostCooldown{msHost: {Runs: 9, Last: cooldownClock.Add(-24 * time.Hour)}}, cooldownClock); err != nil {
		t.Fatal(err)
	}
	for day := 0; day < 5; day++ {
		withClock(t, cooldownClock.Add(time.Duration(day)*24*time.Hour))
		site := &hostSite{status: down}
		var warn warnLog
		rep, err := listBoards(t.Context(), []sources.Board{b}, siteSource(site), nil, rc.Reconcile, warn.warnf, file)
		if err != nil || rep.RateLimited != 1 || site.requests() != 1 {
			t.Fatalf("day %d: %+v err %v requests %d", day, rep, err, site.requests())
		}
	}
	for _, p := range before {
		if r, ok := l.Get(p.Key); !ok || r.Status != posting.Live || r.MissedRuns != 0 {
			t.Errorf("%s: %+v present=%v, want live with no missed run", p.Key, r, ok)
		}
	}
	if c := readCooldowns(t, file)[msHost]; c.Runs != 14 {
		t.Errorf("cooldown %+v after five more throttled runs from 9", c)
	}
}

// A file that is corrupt, oversized or from a clock that ran ahead does not
// stop the run: the host is asked as if fresh (or probed), a warning says what
// happened, and the file is replaced by a good one.
func TestListBoardsCooldownFileAdversarial(t *testing.T) {
	huge := `{"version":1,"hosts":{"` + strings.Repeat("a", 200<<10) + `":{"runs":1,"last":"2026-10-22T11:00:00Z"}}}`
	for name, c := range map[string]struct {
		body      string
		wantWarn  string
		wantReqs  int // requests to a host that is down: 5 for a fresh host, 1 for a probe
		wantAfter int // runs on file afterwards
	}{
		"garbage":    {"\x00\xff not json", "host cooldowns ignored", 5, 1},
		"oversized":  {huge, "host cooldowns ignored", 5, 1},
		"wrong type": {`{"version":1,"hosts":{"` + msHost + `":{"runs":"lots","last":5}}}`, "dropped", 5, 1},
		"future":     {`{"version":1,"hosts":{"` + msHost + `":{"runs":3,"last":"2031-01-01T00:00:00Z"}}}`, "", 1, 4},
		"far past":   {`{"version":1,"hosts":{"` + msHost + `":{"runs":3,"last":"2020-01-01T00:00:00Z"}}}`, "dropped", 5, 1},
	} {
		t.Run(name, func(t *testing.T) {
			withClock(t, cooldownClock)
			file := cooldownFile(t)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			site := &hostSite{status: down}
			var warn warnLog
			rep, err := listBoards(t.Context(), []sources.Board{efBoard("a", msHost)}, siteSource(site), nil, tempReconciler(t).Reconcile, warn.warnf, file)
			if err != nil || rep.RateLimited != 1 {
				t.Fatalf("report %+v err %v", rep, err)
			}
			if site.requests() != c.wantReqs {
				t.Errorf("%d requests, want %d", site.requests(), c.wantReqs)
			}
			if c.wantWarn != "" && !strings.Contains(warn.String(), c.wantWarn) {
				t.Errorf("warnings %q lack %q", warn.String(), c.wantWarn)
			}
			if got := readCooldowns(t, file)[msHost]; got.Runs != c.wantAfter {
				t.Errorf("file now says %+v, want %d runs", got, c.wantAfter)
			}
		})
	}
}

// A cooldown file that cannot be written is a warning: the run's report stands.
func TestListBoardsCooldownFileUnwritable(t *testing.T) {
	withClock(t, cooldownClock)
	file := filepath.Join(t.TempDir(), "cooldown.json")
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}
	site := &hostSite{status: down}
	var warn warnLog
	rep, err := listBoards(t.Context(), []sources.Board{efBoard("a", msHost)}, siteSource(site), nil, tempReconciler(t).Reconcile, warn.warnf, file)
	if err != nil || rep.RateLimited != 1 {
		t.Fatalf("report %+v err %v", rep, err)
	}
	if !strings.Contains(warn.String(), "host cooldowns not saved") {
		t.Errorf("warnings:\n%s", warn.String())
	}
}

// A run stopped part-way still writes what it learned, and a cooldown for a
// host no board reached is left as it was.
func TestListBoardsCancelledRunKeepsCooldowns(t *testing.T) {
	withClock(t, cooldownClock)
	file := cooldownFile(t)
	other := srcutil.HostCooldown{Runs: 4, Last: cooldownClock.Add(-time.Hour)}
	if err := srcutil.SaveCooldowns(file, map[string]srcutil.HostCooldown{"unreached.test": other}, cooldownClock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	site := &hostSite{status: down}
	srcs := siteSource(site)
	inner := srcs[posting.Eightfold]
	srcs[posting.Eightfold] = fakeSource{posting.Eightfold, func(c context.Context, b sources.Board) ([]posting.Posting, error) {
		if b.Token == "stop" {
			cancel()
			return nil, c.Err()
		}
		return inner.List(c, b)
	}}
	var warn warnLog
	rep, err := listBoards(ctx, []sources.Board{efBoard("a", msHost), efBoard("stop", "x.test"), efBoard("never", "y.test")},
		srcs, nil, tempReconciler(t).Reconcile, warn.warnf, file)
	if err == nil || rep.NotReached != 1 {
		t.Fatalf("err %v, report %+v; want the cancellation and one board not reached", err, rep)
	}
	got := readCooldowns(t, file)
	if got[msHost].Runs != 1 || got["unreached.test"].Runs != other.Runs || !got["unreached.test"].Last.Equal(other.Last) {
		t.Errorf("cooldowns after a cancelled run: %v", got)
	}
}

// listBoards puts the run's retry budget above each board's on the context
// every source sees.
func TestListBoardsCarriesRunBudgetAndBoardBudget(t *testing.T) {
	var runLeft, boardLeft []time.Duration
	srcs := map[posting.ATS]sources.Source{posting.Greenhouse: fakeSource{posting.Greenhouse, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		r, rok := srcutil.RunRetryBudgetLeft(ctx)
		bl, bok := srcutil.RetryBudgetLeft(ctx)
		if !rok || !bok {
			t.Errorf("board %s: run budget %v board budget %v on the context", b.Token, rok, bok)
		}
		runLeft, boardLeft = append(runLeft, r), append(boardLeft, bl)
		return boardPostings(b, 1), nil
	}}}
	a := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	var warn warnLog
	if _, err := listBoards(t.Context(), []sources.Board{a}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, ""); err != nil {
		t.Fatal(err)
	}
	if len(runLeft) != 1 || runLeft[0] != srcutil.RunRetryBudget || boardLeft[0] != srcutil.BoardRetryBudget {
		t.Errorf("run %v board %v, want %v and %v", runLeft, boardLeft, srcutil.RunRetryBudget, srcutil.BoardRetryBudget)
	}
}
