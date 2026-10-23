package eightfold

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// The live failure: Microsoft's host answers 429 to every search. Two boards
// (two search lists) on it, then a later run: the first board gives up on the
// retries it has, the second asks once, the third not at all, and a later run
// starts from a cooldown and asks once.

func throttledCtx(th *srcutil.HostThrottle) context.Context {
	return srcutil.WithHostThrottle(noRetry(), th)
}

func TestThrottledHostIsNotAskedAgainByLaterBoards(t *testing.T) {
	s, seen := throttled(t, func(string, url.Values) bool { return true })
	th := srcutil.NewHostThrottle(nil, nil)
	ctx := throttledCtx(th)

	_, err := s.List(ctx, board("LLM inference"))
	wantRateLimited(t, err)
	if len(*seen) != 1 {
		t.Fatalf("first board: %d requests, want 1 (its budget is empty)", len(*seen))
	}
	_, err = s.List(ctx, board("agents"))
	wantRateLimited(t, err)
	if !strings.Contains(err.Error(), "host rate limited earlier this run; probed once") || len(*seen) != 2 {
		t.Errorf("second board: %d requests in all, err %v; want one probe", len(*seen), err)
	}
	_, err = s.List(ctx, board("infra"))
	wantRateLimited(t, err)
	if len(*seen) != 2 {
		t.Errorf("third board: %d requests in all, want none sent", len(*seen))
	}
	if c := th.Cooldowns()[host]; c.Runs != 1 {
		t.Errorf("cooldowns %+v, want one run for %s", th.Cooldowns(), host)
	}
}

func TestCooldownFromLastRunSendsOneProbe(t *testing.T) {
	s, seen := throttled(t, func(string, url.Values) bool { return true })
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	th := srcutil.NewHostThrottle(map[string]srcutil.HostCooldown{host: {Runs: 2, Last: at.Add(-24 * time.Hour)}}, func() time.Time { return at })
	ctx := srcutil.WithRetryBudget(srcutil.WithHostThrottle(context.Background(), th), srcutil.BoardRetryBudget) // a full budget: none is spent
	_, err := s.List(ctx, board("LLM inference"))
	wantRateLimited(t, err)
	if !strings.Contains(err.Error(), "host in cooldown after 3 rate-limited runs; probed once") || len(*seen) != 1 {
		t.Errorf("%d requests, err %v; want one probe and the cooldown message", len(*seen), err)
	}
}
