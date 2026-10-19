package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// indexOf is a liveRecs key's position, which is its dispatch order with no
// rank and no dates.
func indexOf(t testing.TB, k posting.Key) int {
	t.Helper()
	s := string(k)
	n, err := strconv.Atoi(s[strings.LastIndexByte(s, ':')+1:])
	if err != nil {
		t.Fatalf("key %q: %v", k, err)
	}
	return n
}

func failed(k posting.Key) error { return fmt.Errorf("pipeline: score %s: devprism down", k) }

// A posting dispatched before an outage that finishes after postings
// dispatched behind it must not reset their streak: they are still in a row
// after it in rank order. Posting 0 is held until posting 16 has started, so
// at least 13 later postings have finished first; counted in completion
// order, its result zeroed their streak and the run went on for 20 more.
// The bound is by construction: a stop comes once maxConsecutiveUnread (or
// maxConsecutiveFailed) postings dispatched after the last posting that
// ended the streak have finished, plus at most workers-1 in flight.
func TestAdvanceStreakIgnoresALateEarlierResult(t *testing.T) {
	const workers = 4
	cases := []struct {
		name     string
		late     func(posting.Key) error // posting 0's result
		rest     func(posting.Key) error // every other posting's
		stopped  string
		streakOf int
	}{
		{"late read, unread streak", func(posting.Key) error { return nil }, unread, stopUnreadStreak, maxConsecutiveUnread},
		{"late failure, unread streak", failed, unread, stopUnreadStreak, maxConsecutiveUnread},
		{"late read, failure streak", func(posting.Key) error { return nil }, failed, stopFailStreak, maxConsecutiveFailed},
		{"late unread, failure streak", unread, failed, stopFailStreak, maxConsecutiveFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const n = 300
			release := make([]chan struct{}, n)
			for i := range release {
				release[i] = make(chan struct{})
			}
			starts := make(chan int, n)
			counted := make(chan bool)
			testHookCounted = func(_ posting.Key, stopped bool) { counted <- stopped }
			t.Cleanup(func() { testHookCounted = nil })

			type out struct {
				rep advanceReport
				err error
			}
			done := make(chan out, 1)
			go func() {
				rep, err := advanceAll(context.Background(), liveRecs(n), nil, n, workers, nil, func(_ context.Context, k posting.Key) error {
					i := indexOf(t, k)
					starts <- i
					<-release[i]
					if i == 0 {
						return tc.late(k)
					}
					return tc.rest(k)
				}, func(string, ...any) {})
				done <- out{rep, err}
			}()

			wait := func(what string) int {
				t.Helper()
				select {
				case i := <-starts:
					return i
				case <-time.After(5 * time.Second):
					t.Fatalf("no posting started (%s)", what)
					return -1
				}
			}
			inFlight := map[int]bool{}
			for range workers {
				inFlight[wait("first wave")] = true
			}
			// Release 1..13 one at a time, each counted before the next, then
			// posting 0, then the rest in order: posting 0 finishes after 13
			// postings dispatched behind it.
			next := 1
			pick := func(step int) int {
				if step == 13 {
					return 0
				}
				for !inFlight[next] {
					next++
				}
				return next
			}
			for step := 0; ; step++ {
				i := pick(step)
				delete(inFlight, i)
				close(release[i])
				if stopped := <-counted; stopped {
					for j := range inFlight {
						close(release[j])
						<-counted
					}
					break
				}
				inFlight[wait(fmt.Sprintf("after posting %d", i))] = true
			}
			o := <-done
			rep, err := o.rep, o.err
			if err == nil || rep.Stopped != tc.stopped {
				t.Fatalf("err %v report %+v", err, rep)
			}
			// Posting 0 is the last posting before the streak.
			if bound := 0 + 1 + tc.streakOf + workers - 1; rep.Attempted > bound {
				t.Fatalf("attempted %d, want at most %d: a late earlier result reset the streak (report %+v)", rep.Attempted, bound, rep)
			}
		})
	}
}

// The bound holds whatever the timing: with random latencies and an outage
// from a random posting on, a run never attempts more than the last posting
// before the outage, plus maxConsecutiveUnread, plus workers-1.
func TestAdvanceStreakBoundUnderRandomTiming(t *testing.T) {
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 7))
		workers := 1 + rng.IntN(8)
		from := rng.IntN(60) // first posting of the outage
		delays := make([]time.Duration, 300)
		for i := range delays {
			delays[i] = time.Duration(rng.IntN(400)) * time.Microsecond
		}
		rep, err := advanceAll(context.Background(), liveRecs(300), nil, 300, workers, nil, func(_ context.Context, k posting.Key) error {
			i := indexOf(t, k)
			time.Sleep(delays[i])
			if i < from {
				return nil
			}
			return unread(k)
		}, func(string, ...any) {})
		if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopUnreadStreak {
			t.Fatalf("seed %d: err %v report %+v", seed, err, rep)
		}
		if bound := from + maxConsecutiveUnread + workers - 1; rep.Attempted > bound || rep.Unread < maxConsecutiveUnread {
			t.Fatalf("seed %d (workers %d, outage from %d): report %+v, want attempted <= %d", seed, workers, from, rep, bound)
		}
	}
}
