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

// split counts a request log's robots.txt reads and its API requests. Every
// board reads robots.txt first; a host the throttle has blocked is sent neither.
func split(seen []string) (robots, api int) {
	for _, r := range seen {
		if strings.HasPrefix(r, "/robots.txt") {
			robots++
		} else {
			api++
		}
	}
	return robots, api
}

func TestThrottledHostIsNotAskedAgainByLaterBoards(t *testing.T) {
	s, seen := throttled(t, func(string, url.Values) bool { return true })
	th := srcutil.NewHostThrottle(nil, nil)
	ctx := throttledCtx(th)

	_, err := s.List(ctx, board("LLM inference"))
	wantRateLimited(t, err)
	if robots, api := split(*seen); robots != 1 || api != 1 {
		t.Fatalf("first board: %d robots.txt and %d API requests, want 1 and 1 (its budget is empty)", robots, api)
	}
	_, err = s.List(ctx, board("agents"))
	wantRateLimited(t, err)
	// The host is in its probe state: robots.txt goes out, then the one probe.
	if robots, api := split(*seen); !strings.Contains(err.Error(), "host rate limited earlier this run; probed once") || robots != 2 || api != 2 {
		t.Errorf("second board: %d robots.txt and %d API requests in all, err %v; want one probe", robots, api, err)
	}
	// The probe was answered 429: the host is blocked, and the robots.txt read
	// that would come first is refused too.
	_, err = s.List(ctx, board("infra"))
	wantRateLimited(t, err)
	if robots, api := split(*seen); robots != 2 || api != 2 {
		t.Errorf("third board: %d robots.txt and %d API requests in all, want none sent", robots, api)
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
	if _, api := split(*seen); !strings.Contains(err.Error(), "host in cooldown after 3 rate-limited runs; probed once") || api != 1 {
		t.Errorf("%d API requests, err %v; want one probe and the cooldown message", api, err)
	}
}
