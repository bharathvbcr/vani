package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1aLedger is a real ledger in a temp dir and its reconciler: the failure
// under test is the ledger refusing a batch, which a fake reconciler cannot show.
func w1aLedger(t *testing.T) (*ledger.File, reconcileFunc) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, pipeline.NewReconciler(l).Reconcile
}

// w1aSink records what the run's BadRowSink was told, per board.
type w1aSink struct {
	mu      sync.Mutex
	skipped map[string]int
	calls   map[string]int
}

func (s *w1aSink) sink(b sources.Board, n int, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skipped == nil {
		s.skipped, s.calls = map[string]int{}, map[string]int{}
	}
	s.skipped[b.Token] += n
	s.calls[b.Token]++
}

func w1aSrc(ats posting.ATS, rows func(sources.Board) []posting.Posting) sources.Source {
	return fakeSource{ats, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
		return rows(b), nil
	}}
}

// w1aStoredKeys is the keys the ledger holds with the given prefix.
func w1aStoredKeys(l *ledger.File, prefix string) []posting.Key {
	var out []posting.Key
	for _, r := range l.All() {
		if strings.HasPrefix(string(r.Key), prefix) {
			out = append(out, r.Key)
		}
	}
	return out
}

// Two boards in one fetch: A serves rows the ledger cannot encode (a posted time
// in year 30000 and, for the other cases, a row past the ledger's line limit);
// B is fine. The ledger used to refuse A's whole batch, reconcile returned the
// error, and the run ended on A. Now A is read short (its bad rows left out and
// counted, so its stored postings are held) or failed naming the row, and B is
// reconciled either way and the run goes on to a board after both.
func TestW1aOneUnstorableRowDoesNotStopTheRun(t *testing.T) {
	farFuture := time.UnixMilli(1 << 62).UTC() // lever's createdAt arithmetic
	huge := strings.Repeat("x", 2<<20)
	cases := []struct {
		name        string
		ats         posting.ATS
		bad         func(p *posting.Posting)
		wantOutcome string
		wantStored  int
		wantSkip    int
	}{
		{"future time is clamped", posting.Lever, func(p *posting.Posting) { p.PostedAt = farFuture }, boardRead, 3, 0},
		{"oversized row is skipped and counted", posting.Lever, func(p *posting.Posting) { p.Location = huge }, boardRead, 2, 1},
		{"oversized row fails a fail-the-board adapter", posting.Workday, func(p *posting.Posting) { p.Location = huge }, boardFailed, 0, 0},
	}
	for _, parallel := range []int{1, 4} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				withBoardListParallel(t, parallel)
				l, reconcile := w1aLedger(t)
				var sk w1aSink
				a := sources.Board{ATS: tc.ats, Token: "aco", Company: "A"}
				if tc.ats == posting.Workday {
					a.Token, a.Host = "aco/Ext", "aco.wd1.myworkdayjobs.com"
				}
				bd := sources.Board{ATS: posting.Greenhouse, Token: "bco", Company: "B"}
				cd := sources.Board{ATS: posting.Ashby, Token: "cco", Company: "C"}
				srcs := map[posting.ATS]sources.Source{
					tc.ats: w1aSrc(tc.ats, func(b sources.Board) []posting.Posting {
						rows := boardPostings(b, 3)
						tc.bad(&rows[1])
						return rows
					}),
					posting.Greenhouse: w1aSrc(posting.Greenhouse, func(b sources.Board) []posting.Posting { return boardPostings(b, 2) }),
					posting.Ashby:      w1aSrc(posting.Ashby, func(b sources.Board) []posting.Posting { return boardPostings(b, 1) }),
				}
				var warn warnLog
				ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
				rep, err := listBoards(ctx, []sources.Board{a, bd, cd}, srcs, nil, reconcile, warn.warnf, "")
				if err != nil {
					t.Fatalf("one board's row ended the run: %v\n%s", err, warn.String())
				}
				if len(rep.Items) != 3 || rep.Items[0].Outcome != tc.wantOutcome || rep.Items[1].Outcome != boardRead || rep.Items[2].Outcome != boardRead {
					t.Fatalf("outcomes: %+v", rep.Items)
				}
				if got := len(w1aStoredKeys(l, "gh:bco:")); got != 2 {
					t.Errorf("board B stored %d postings, want 2", got)
				}
				if got := len(w1aStoredKeys(l, "ab:cco:")); got != 1 {
					t.Errorf("the board after A stored %d postings, want 1", got)
				}
				if tc.wantOutcome == boardFailed {
					if !strings.Contains(rep.Items[0].Error, "row 2 of 3") || !strings.Contains(rep.Items[0].Error, "location") {
						t.Errorf("error does not name the row and field: %q", rep.Items[0].Error)
					}
					if strings.Contains(rep.Items[0].Error, "xxxxxxxx") {
						t.Errorf("error echoes the value")
					}
					return
				}
				if got := len(w1aStoredKeys(l, a.KeyPrefix())); got != tc.wantStored {
					t.Errorf("board A stored %d postings, want %d", got, tc.wantStored)
				}
				if sk.skipped["aco"] != tc.wantSkip {
					t.Errorf("sink saw %d skipped rows for A, want %d", sk.skipped["aco"], tc.wantSkip)
				}
			})
		}
	}
}

// Lever's createdAt, eightfold's postedTs and a jibe stamp whose UTC
// conversion leaves 0..9999 each become a stored posting with an unknown
// (zero) PostedAt; valid dates, including the last second of year 9999, are
// kept as they were.
func TestW1aAdapterTimeShapesStoreWithZeroPostedAt(t *testing.T) {
	jibeStamp, err := time.Parse(time.RFC3339, "9999-12-31T23:59:59-05:00") // UTC year 10000
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	first := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	shapes := []struct {
		name string
		at   time.Time
		kept bool
	}{
		{"lever beyond year 9999", time.UnixMilli(1 << 62).UTC(), false},
		{"eightfold out of range postedTs", time.Unix(1<<40, 0).UTC(), false},
		{"jibe UTC conversion past 9999", jibeStamp.UTC(), false},
		{"negative unix", time.Unix(-1<<40, 0).UTC(), false},
		{"year 0", time.Date(0, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"year 1", first, true},
		{"year 9999 last second", last, true},
		{"year 10000", last.Add(time.Second), false},
	}
	l, reconcile := w1aLedger(t)
	b := sources.Board{ATS: posting.Lever, Token: "times", Company: "T"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: w1aSrc(posting.Lever, func(b sources.Board) []posting.Posting {
			rows := boardPostings(b, len(shapes))
			for i := range rows {
				rows[i].PostedAt, rows[i].UpdatedAt = shapes[i].at, shapes[i].at
			}
			return rows
		}),
	}
	var warn warnLog
	rep, lerr := listBoards(context.Background(), []sources.Board{b}, srcs, nil, reconcile, warn.warnf, "")
	if lerr != nil || rep.Read != 1 {
		t.Fatalf("err %v, report %+v\n%s", lerr, rep, warn.String())
	}
	for i, s := range shapes {
		r, ok := l.Get(posting.Key(b.KeyPrefix() + string(rune('0'+i+1))))
		if !ok {
			t.Fatalf("%s: not stored", s.name)
		}
		if s.kept && !r.Posting.PostedAt.Equal(s.at) {
			t.Errorf("%s: PostedAt %v, want it kept as %v", s.name, r.Posting.PostedAt, s.at)
		}
		if !s.kept && (!r.Posting.PostedAt.IsZero() || !r.Posting.UpdatedAt.IsZero()) {
			t.Errorf("%s: PostedAt %v, want zero", s.name, r.Posting.PostedAt)
		}
	}
}

// A title made only of characters srcutil.Title strips (whitespace, no-break
// spaces) is refused at the seam: the row is skipped and counted, not stored
// under an empty title.
func TestW1aEmptyTitleAfterTitleIsRefused(t *testing.T) {
	l, reconcile := w1aLedger(t)
	var sk w1aSink
	b := sources.Board{ATS: posting.Lever, Token: "blank", Company: "Blank"}
	srcs := map[posting.ATS]sources.Source{
		posting.Lever: w1aSrc(posting.Lever, func(b sources.Board) []posting.Posting {
			rows := boardPostings(b, 12)
			rows[3].Title = "  \t\n  "
			return rows
		}),
	}
	var warn warnLog
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	rep, err := listBoards(ctx, []sources.Board{b}, srcs, nil, reconcile, warn.warnf, "")
	if err != nil || rep.Read != 1 {
		t.Fatalf("err %v, report %+v", err, rep)
	}
	if got := len(w1aStoredKeys(l, b.KeyPrefix())); got != 11 || sk.skipped["blank"] != 1 {
		t.Errorf("stored %d, sink skipped %d; want 11 and 1", got, sk.skipped["blank"])
	}
}

// Without a BadRowSink a row that cannot be stored fails the board (a caller
// that cannot hold what the listing leaves out must not be handed a listing
// that quietly leaves it out), and so does a listing with more bad rows than
// MaxGone allows.
func TestW1aNoSinkOrTooManyBadRowsFailsTheBoard(t *testing.T) {
	b := sources.Board{ATS: posting.Lever, Token: "bad", Company: "Bad"}
	rows := func(bad int) []posting.Posting {
		out := boardPostings(b, 10)
		for i := 0; i < bad; i++ {
			out[i].Title = ""
		}
		return out
	}
	if _, err := storableListing(context.Background(), b, rows(1), 0); err == nil || !strings.Contains(err.Error(), "row 1 of 10") {
		t.Errorf("no sink: err %v", err)
	}
	var sk w1aSink
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	if got, err := storableListing(ctx, b, rows(srcutil.MaxGone(10)), 0); err != nil || len(got) != 9 {
		t.Errorf("at the bound: kept %d, err %v", len(got), err)
	}
	if _, err := storableListing(ctx, b, rows(srcutil.MaxGone(10)+1), 0); err == nil {
		t.Errorf("over the bound: want a board error")
	}
	if _, err := storableListing(ctx, b, rows(10), 0); err == nil {
		t.Errorf("every row bad: want a board error")
	}
}

// The seam reports to the same sink an adapter reported to, once per listing
// each, and the two counts add: the adapter's skipped rows and the seam's are
// different rows, so overwriting either would under-count the shortfall.
func TestW1aSeamAndAdapterSkipsAdd(t *testing.T) {
	var sk w1aSink
	b := sources.Board{ATS: posting.Lever, Token: "both", Company: "Both"}
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	// The adapter's own report, as its Rows.Done makes it.
	adapter := srcutil.NewRows(ctx, b, 20)
	if err := adapter.Bad(context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Done(); err != nil {
		t.Fatal(err)
	}
	rows := boardPostings(b, 30)
	rows[4].Title = ""
	rows[5].Title = ""
	got, err := storableListing(ctx, b, rows, 0)
	if err != nil || len(got) != 28 {
		t.Fatalf("kept %d, err %v", len(got), err)
	}
	if sk.skipped["both"] != 3 || sk.calls["both"] != 2 {
		t.Errorf("sink saw %d skipped in %d reports, want 3 in 2", sk.skipped["both"], sk.calls["both"])
	}
}

// Every adapter the docs list as keeping its board failure still fails its
// board on one unstorable row, naming the row; an adapter that converts (and
// Ashby, whose two paths cannot be told apart here) skips and counts it.
func TestW1aFailTheBoardSetKeepsFailing(t *testing.T) {
	failing := []posting.ATS{posting.Workable, posting.BambooHR, posting.Apple, posting.Eightfold, posting.Avature,
		posting.Workday, posting.SmartRecruiters, posting.OracleHCM, posting.Rippling, posting.TalentBrew, posting.Page}
	converting := []posting.ATS{posting.Greenhouse, posting.Lever, posting.Ashby, posting.Gem, posting.Jibe, posting.Google}
	var sk w1aSink
	ctx := srcutil.WithBadRowSink(context.Background(), sk.sink)
	for _, ats := range failing {
		b := sources.Board{ATS: ats, Token: "t/s", Company: "X"}
		rows := boardPostings(b, 20)
		rows[6].URL = "javascript:alert(1)"
		if _, err := storableListing(ctx, b, rows, 0); err == nil || !strings.Contains(err.Error(), "row 7 of 20") {
			t.Errorf("%s: err %v, want a board error naming row 7", ats, err)
		}
	}
	for _, ats := range converting {
		b := sources.Board{ATS: ats, Token: "t", Company: "X"}
		rows := boardPostings(b, 20)
		rows[6].URL = "javascript:alert(1)"
		if got, err := storableListing(ctx, b, rows, 0); err != nil || len(got) != 19 {
			t.Errorf("%s: kept %d, err %v, want the row skipped", ats, len(got), err)
		}
	}
}
