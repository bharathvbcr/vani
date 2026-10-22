package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// rateLimitedErr is what a source returns for a host that kept answering 429:
// the adapter's own wrapping around srcutil's error.
func rateLimitedErr() error {
	return fmt.Errorf(`eightfold "microsoft.com": search "LLM inference": %w`, &srcutil.RateLimitedError{
		Host: "apply.careers.microsoft.com", Method: "GET", Path: "/api/pcsx/search", Attempts: 5,
		Why: "gave up after 5 attempts", Status: &egress.StatusError{Code: 429, Method: "GET", Host: "apply.careers.microsoft.com"},
	})
}

// A board whose host kept answering 429 is reported as rate_limited (counted in
// failed too, so a reader that predates the outcome still counts it), its
// postings are untouched, the warning says it is retried next run, and the
// boards around it are read as usual. A broken board stays plain "failed".
func TestListBoardsRateLimitedBoardIsDistinctAndLeftUnchanged(t *testing.T) {
	ef := sources.Board{ATS: posting.Eightfold, Token: "microsoft.com", Company: "Microsoft", Host: "apply.careers.microsoft.com", Searches: []string{"LLM inference"}}
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	wd := sources.Board{ATS: posting.Workday, Token: "intel/External", Company: "Intel", Host: "intel.wd1.myworkdayjobs.com"}
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	rc := pipeline.NewReconciler(l)

	// Run one: Microsoft listed 3 postings.
	before := boardPostings(ef, 3)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if _, err := rc.Reconcile(context.Background(), ef, before, now); err != nil {
		t.Fatal(err)
	}

	// Run two: Microsoft is throttled.
	srcs := map[posting.ATS]sources.Source{
		posting.Eightfold: fakeSource{posting.Eightfold, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, rateLimitedErr()
		}},
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 2), nil
		}},
		posting.Workday: fakeSource{posting.Workday, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, fmt.Errorf("workday: http 500")
		}},
	}
	var warn warnLog
	rep, err := listBoards(context.Background(), []sources.Board{ef, gh, wd}, srcs, nil, rc.Reconcile, warn.warnf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Configured != 3 || rep.Read != 1 || rep.Failed != 2 || rep.RateLimited != 1 || rep.Skipped != 0 || len(rep.Items) != 3 {
		t.Fatalf("totals: %+v", rep)
	}
	// Items keep watchlist order: Microsoft, Acme, Intel.
	if e := rep.Items[0]; e.Outcome != boardRateLimited || e.Token != "microsoft.com" || e.Listed != nil || e.Reconcile != nil ||
		!strings.Contains(e.Error, "rate limited by apply.careers.microsoft.com (HTTP 429), retry next run") {
		t.Errorf("rate-limited item: %+v", e)
	}
	if e := rep.Items[2]; e.Outcome != boardFailed || e.Token != "intel/External" {
		t.Errorf("a broken board must stay failed: %+v", e)
	}
	if !strings.Contains(warn.String(), "board ef/microsoft.com:") || !strings.Contains(warn.String(), "left unchanged, will retry next run") {
		t.Errorf("warnings:\n%s", warn.String())
	}
	if strings.Contains(warn.String(), "board wd/intel/External: workday: http 500; left unchanged, will retry") {
		t.Errorf("a failed board was told it would be retried as a rate limit:\n%s", warn.String())
	}
	// Left unchanged: every Microsoft posting is still live, none aged or dead.
	for _, p := range before {
		if r, ok := l.Get(p.Key); !ok || r.Status != posting.Live {
			t.Errorf("%s after a throttled run: %+v present=%v, want live", p.Key, r.Status, ok)
		}
	}
	// The record's JSON carries the outcome string and the additive total.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outcome":"rate_limited"`, `"rate_limited":1`, `"failed":2`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("report JSON lacks %s: %s", want, raw)
		}
	}
	// A report with no rate-limited board does not grow the key.
	plain, _ := json.Marshal(boardsReportOf(1, []boardReport{{ATS: "gh", Token: "a", Outcome: boardFailed}}))
	if strings.Contains(string(plain), "rate_limited") {
		t.Errorf("a run with no rate limit wrote the key: %s", plain)
	}
}

// A throttled board that is also cut by the report's bounds is kept before
// read boards, like any other board that was not read, and the totals count it.
func TestRateLimitedBoardsAreKeptBeforeReadOnesWhenTheReportIsCut(t *testing.T) {
	var tried []boardReport
	listed := 1
	for i := 0; i < maxRecordedBoards+20; i++ {
		tried = append(tried, boardReport{ATS: "gh", Token: fmt.Sprint(i), Outcome: boardRead, Listed: &listed})
	}
	tried = append(tried, boardReport{ATS: "ef", Token: "microsoft.com", Outcome: boardRateLimited, Error: "rate limited"})
	rep := boardsReportOf(len(tried), tried)
	if rep.RateLimited != 1 || rep.Failed != 1 || rep.Unrecorded != 21 || len(rep.Items) != maxRecordedBoards {
		t.Fatalf("report: failed %d rate_limited %d unrecorded %d items %d", rep.Failed, rep.RateLimited, rep.Unrecorded, len(rep.Items))
	}
	if rep.Items[len(rep.Items)-1].Outcome != boardRateLimited {
		t.Errorf("the rate-limited board was cut while read boards were kept")
	}
}

// Each board gets a retry budget of its own, so one throttled board cannot spend
// another's waiting.
func TestListBoardsGivesEachBoardItsOwnRetryBudget(t *testing.T) {
	var seen []time.Duration
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			left, ok := srcutil.RetryBudgetLeft(ctx)
			if !ok {
				t.Errorf("board %s: no retry budget on the context", b.Token)
			}
			seen = append(seen, left)
			return boardPostings(b, 1), nil
		}},
	}
	a := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	b := sources.Board{ATS: posting.Greenhouse, Token: "b", Company: "B"}
	var warn warnLog
	if _, err := listBoards(context.Background(), []sources.Board{a, b}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != srcutil.BoardRetryBudget || seen[1] != srcutil.BoardRetryBudget {
		t.Errorf("budgets %v, want %v each", seen, srcutil.BoardRetryBudget)
	}
}
