package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// Postings start in rank order: with three workers the three best start
// first, and the fourth best starts only when one of them has finished. The
// cap holds and the rest wait.
func TestAdvanceConcurrentStartsInRankOrder(t *testing.T) {
	recs := liveRecs(12)
	for i := range recs {
		recs[i].Posting.Title = strconv.Itoa(i) // rank = index: best last in ledger order
	}
	rank := func(p posting.Posting) int { n, _ := strconv.Atoi(p.Title); return n }
	want := []posting.Key{recs[11].Key, recs[10].Key, recs[9].Key, recs[8].Key, recs[7].Key, recs[6].Key, recs[5].Key, recs[4].Key}

	started := make(chan posting.Key, len(recs))
	release := make(chan struct{})
	type out struct {
		rep advanceReport
		err error
	}
	done := make(chan out, 1)
	go func() {
		rep, err := advanceAll(context.Background(), recs, rank, len(want), 3, nil, func(_ context.Context, k posting.Key) error {
			started <- k
			<-release
			return nil
		}, func(string, ...any) {})
		done <- out{rep, err}
	}()

	var got []posting.Key
	take := func(n int) {
		t.Helper()
		for range n {
			select {
			case k := <-started:
				got = append(got, k)
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d started: %v", len(got), got)
			}
		}
		select {
		case k := <-started:
			t.Fatalf("%s started past the three workers (started %v)", k, got)
		case <-time.After(20 * time.Millisecond):
		}
		sorted := slices.Clone(got)
		slices.Sort(sorted)
		wantSet := slices.Clone(want[:len(got)])
		slices.Sort(wantSet)
		if !slices.Equal(sorted, wantSet) {
			t.Fatalf("started %v, want the %d best %v", got, len(got), want[:len(got)])
		}
	}
	take(3)
	for len(got) < len(want) {
		release <- struct{}{}
		take(1)
	}
	for range 3 {
		release <- struct{}{}
	}
	o := <-done
	if wantRep := (advanceReport{Eligible: 12, Limit: 8, Attempted: 8, Deferred: 4, Stopped: stopCap}); o.err != nil || o.rep != wantRep {
		t.Fatalf("err %v report %+v, want %+v", o.err, o.rep, wantRep)
	}
}

// No more than workers postings are ever in flight, and more than one is.
func TestAdvanceConcurrentNeverExceedsWorkers(t *testing.T) {
	var cur, peak, calls atomic.Int64
	rep, err := advanceAll(context.Background(), liveRecs(200), nil, 200, 4, nil, func(context.Context, posting.Key) error {
		calls.Add(1)
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(200 * time.Microsecond)
		cur.Add(-1)
		return nil
	}, func(string, ...any) {})
	if err != nil || rep.Attempted != 200 || calls.Load() != 200 {
		t.Fatalf("err %v report %+v calls %d", err, rep, calls.Load())
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Fatalf("peak in flight %d, want 2..4", p)
	}
	if _, err := advanceAll(context.Background(), liveRecs(3), nil, 5, 0, nil, func(context.Context, posting.Key) error {
		t.Fatal("advanced with zero workers")
		return nil
	}, func(string, ...any) {}); err == nil {
		t.Fatal("zero workers accepted")
	}
}

// The per-run cap counts postings started, so concurrency cannot overshoot it.
func TestAdvanceConcurrentHonoursTheCap(t *testing.T) {
	var calls atomic.Int64
	var warn lockedBuilder
	rep, err := advanceAll(context.Background(), liveRecs(50), nil, 10, 4, nil, func(context.Context, posting.Key) error {
		calls.Add(1)
		time.Sleep(100 * time.Microsecond)
		return nil
	}, warn.printf)
	if err != nil || calls.Load() != 10 || rep != (advanceReport{Eligible: 50, Limit: 10, Attempted: 10, Deferred: 40, Stopped: stopCap}) {
		t.Fatalf("err %v calls %d report %+v", err, calls.Load(), rep)
	}
	if !strings.Contains(warn.String(), "advance cap 10 reached; 40 posting(s) wait") {
		t.Fatalf("no cap warning:\n%s", warn.String())
	}
}

// An outage still stops the run after maxConsecutiveUnread unread postings;
// the postings already in flight finish and are counted, no more start.
func TestAdvanceConcurrentUnreadStreakStops(t *testing.T) {
	const workers = 4
	var calls atomic.Int64
	var warn lockedBuilder
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, 300, workers, nil, func(_ context.Context, k posting.Key) error {
		calls.Add(1)
		time.Sleep(50 * time.Microsecond)
		return unread(k)
	}, warn.printf)
	n := int(calls.Load())
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), fmt.Sprintf("%d postings in a row", maxConsecutiveUnread)) {
		t.Fatalf("err = %v", err)
	}
	if n < maxConsecutiveUnread || n > maxConsecutiveUnread+workers-1 {
		t.Fatalf("%d postings asked, want %d..%d", n, maxConsecutiveUnread, maxConsecutiveUnread+workers-1)
	}
	want := advanceReport{Eligible: 300, Limit: 300, Attempted: n, Unread: n, Deferred: 300 - n, Stopped: stopUnreadStreak}
	if rep != want {
		t.Fatalf("report %+v, want %+v", rep, want)
	}
	if c := strings.Count(warn.String(), "no decision backend could read"); c > maxUnreadExamples {
		t.Fatalf("%d per-posting lines:\n%s", c, warn.String())
	}
}

// Cancellation starts nothing new and returns only once every posting in
// flight has returned, so nothing writes to the ledger after advanceAll.
func TestAdvanceConcurrentCancelWaitsForInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan posting.Key, 10)
	var exited atomic.Int64
	type out struct {
		rep    advanceReport
		err    error
		exited int64
	}
	done := make(chan out, 1)
	go func() {
		rep, err := advanceAll(ctx, liveRecs(10), nil, 300, 4, nil, func(ctx context.Context, k posting.Key) error {
			started <- k
			<-ctx.Done()
			time.Sleep(5 * time.Millisecond) // a stage winding down
			exited.Add(1)
			return ctx.Err()
		}, func(string, ...any) {})
		done <- out{rep, err, exited.Load()}
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("four postings did not start")
		}
	}
	cancel()
	o := <-done
	if !errors.Is(o.err, context.Canceled) || o.rep.Stopped != stopCancelled || o.rep.Attempted != 4 || o.rep.Deferred != 6 || o.rep.Failed != 0 || o.rep.Unread != 0 {
		t.Fatalf("err %v report %+v", o.err, o.rep)
	}
	if o.exited != 4 {
		t.Fatalf("advanceAll returned with %d of 4 postings still running", 4-o.exited)
	}
	if len(started) != 0 {
		t.Fatalf("%d postings started after cancellation", len(started))
	}
}

// A panic in one posting reaches the caller's goroutine, where the daemon
// contains it, after the other postings in flight have finished.
func TestAdvanceConcurrentPanicReachesCaller(t *testing.T) {
	var started, finished atomic.Int64
	p := func() (p any) {
		defer func() { p = recover() }()
		_, _ = advanceAll(context.Background(), liveRecs(6), nil, 300, 3, nil, func(_ context.Context, k posting.Key) error {
			started.Add(1)
			if k == "gh:acme:001" {
				panic("boom")
			}
			time.Sleep(2 * time.Millisecond)
			finished.Add(1)
			return nil
		}, func(string, ...any) {})
		return nil
	}()
	if s, _ := p.(string); !strings.Contains(s, "gh:acme:001") || !strings.Contains(s, "boom") {
		t.Fatalf("recovered %v", p)
	}
	if started.Load()-1 != finished.Load() {
		t.Fatalf("%d started, %d finished: a posting was still running when the panic surfaced", started.Load(), finished.Load())
	}
}

// lockedBuilder collects warnings from any goroutine.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) printf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, f+"\n", a...)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type clientDoer struct{ c *http.Client }

func (d clientDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return d.c.Do(req.WithContext(ctx))
}

// jevAnswer is a decision-model response (decide/jev wire shape) answering every triage slot with its first
// option at cost usd.
func jevAnswer(usd float64) string {
	var b strings.Builder
	b.WriteString(`{"answers":{`)
	for i, s := range jdtriage.Slots {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + s.Name + `":{"choice":"` + s.Options[0].Value + `","confidence":0.9}`)
	}
	fmt.Fprintf(&b, `},"usage":{"cost":%g}}`, usd)
	return b.String()
}

// jevRunner is a Runner whose only triage backend is a real decide/jev client
// against srv, behind a real Router; scoring and notes are switched off so
// every request srv sees is one posting's triage.
func jevRunner(t *testing.T, l ledger.Ledger, srv *httptest.Server, spend decide.Spend) *pipeline.Runner {
	t.Helper()
	b, err := jev.New(jev.Config{Endpoint: srv.URL + "/v1/decisions", Model: "test", Name: "d1-ultra",
		MaxAttempts: 2, Backoff: jev.MinBackoff, Timeout: 5 * time.Second, Spend: spend}, clientDoer{srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: decide.NewRouter(b)}, SkipScoring: true, SkipExplain: true}
}

// The primary rate-limits (429) or goes down (503) mid-run with four
// postings in flight: the postings it read are recorded as usual, every
// posting it could not read stays Live with no triage event (no decision is
// made from an outage), and the unread streak stops the run. Each posting's
// events are in pipeline order.
func TestAdvanceConcurrentPrimaryFailsMidRun(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			const ok = 7
			var served atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(time.Millisecond)
				if served.Add(1) > ok {
					w.Header().Set("Retry-After", "0")
					http.Error(w, "slow down", status)
					return
				}
				_, _ = w.Write([]byte(jevAnswer(0)))
			}))
			defer srv.Close()
			l := benchLedger(t, 100)
			r := jevRunner(t, l, srv, nil)
			var warn lockedBuilder
			rep, err := advanceAll(context.Background(), l.All(), nil, 100, 4, nil, r.Advance, warn.printf)
			if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopUnreadStreak {
				t.Fatalf("err %v report %+v", err, rep)
			}
			explained, lastRead := 0, -1
			for _, rec := range l.All() {
				switch rec.Status {
				case posting.Explained:
					explained++
					lastRead = max(lastRead, indexOf(t, rec.Key))
				case posting.Live:
				default:
					t.Fatalf("%s is %s after an outage", rec.Key, rec.Status)
				}
			}
			// The server's first ok requests need not be the first ok postings
			// dispatched (arrival order, retries), so the bound is from the
			// last posting read: at most maxConsecutiveUnread after it, plus
			// the 3 others in flight.
			if explained != ok || rep.Attempted-rep.Unread != ok || rep.Unread < maxConsecutiveUnread || rep.Attempted > lastRead+1+maxConsecutiveUnread+3 {
				t.Fatalf("%d explained, last read %d, report %+v", explained, lastRead, rep)
			}
			seen := map[posting.Key]posting.Status{}
			pipelineOrder := []posting.Status{posting.Live, posting.Triaged, posting.Scored, posting.Explained}
			for _, e := range l.Events() {
				if prev, has := seen[e.Key]; has && slices.Index(pipelineOrder, e.Status) != slices.Index(pipelineOrder, prev)+1 {
					t.Fatalf("%s went %s -> %s", e.Key, prev, e.Status)
				}
				seen[e.Key] = e.Status
			}
		})
	}
}

// The OpenRouter daily cap still stops paid calls when postings run
// concurrently. Allow and Charge are separate steps, so at most workers-1
// calls past the cap can be in flight when it is reached (one call past it
// with one worker, as before); after that no request is sent.
func TestAdvanceConcurrentSpendCapHolds(t *testing.T) {
	const (
		workers = 4
		cost    = 0.01
		capUSD  = 0.05 // five calls
	)
	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		time.Sleep(2 * time.Millisecond)
		_, _ = w.Write([]byte(jevAnswer(cost)))
	}))
	defer srv.Close()
	spend, err := decide.NewDailyCap(filepath.Join(t.TempDir(), "spend.json"), capUSD)
	if err != nil {
		t.Fatal(err)
	}
	l := benchLedger(t, 40)
	r := jevRunner(t, l, srv, spend)
	// Postings past the cap read NotAsked (budget) on every slot: no backend
	// read them, so they stay live and the run ends on the unread streak
	// instead of parking them as unsure.
	rep, err := advanceAll(context.Background(), l.All(), nil, 40, workers, nil, r.Advance, func(string, ...any) {})
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopUnreadStreak || rep.Unread < maxConsecutiveUnread {
		t.Fatalf("advance: err %v report %+v, want the unread streak to stop the run", err, rep)
	}
	if n := served.Load(); n < 5 || n > 5+workers-1 {
		t.Fatalf("%d paid calls with a cap of 5, want 5..%d", n, 5+workers-1)
	}
	if err := spend.Allow(context.Background(), "jev"); !errors.Is(err, decide.ErrBudget) {
		t.Fatalf("cap not reached: %v", err)
	}
}
