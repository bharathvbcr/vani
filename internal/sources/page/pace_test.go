package page

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// span is one inner run, start to end.
type span struct {
	url        string
	start, end time.Time
}

// timingRunner records when each run starts and ends, fails on two
// overlapping runs to one host, and delegates to next (or answers a stub page).
type timingRunner struct {
	next    Runner
	hold    time.Duration // how long each run takes
	block   chan struct{} // if set, each run waits for a receive from it
	entered chan struct{} // if set (buffered), signalled as each run starts
	err     error         // if set, returned by every run

	mu       sync.Mutex
	spans    []span
	inflight map[string]int
	overlap  bool
}

func (r *timingRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	u := args[len(args)-1]
	host, _ := targetHost(args)
	r.mu.Lock()
	if r.inflight == nil {
		r.inflight = map[string]int{}
	}
	r.inflight[host]++
	if r.inflight[host] > 1 {
		r.overlap = true
	}
	r.mu.Unlock()
	start := time.Now()
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}

	if r.hold > 0 {
		time.Sleep(r.hold)
	}
	err := r.err
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	var out []byte
	if err == nil {
		if r.next != nil {
			out, err = r.next.Run(ctx, args)
		} else {
			out = []byte("<html></html>")
		}
	}

	end := time.Now()
	r.mu.Lock()
	r.inflight[host]--
	r.spans = append(r.spans, span{url: u, start: start, end: end})
	r.mu.Unlock()
	return out, err
}

func (r *timingRunner) recorded() ([]span, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spans), r.overlap
}

// checkGaps fails unless, in start order, each run started at least interval
// after the previous one ended.
func checkGaps(t *testing.T, spans []span, interval time.Duration) {
	t.Helper()
	slices.SortFunc(spans, func(a, b span) int { return a.start.Compare(b.start) })
	for i := 1; i < len(spans); i++ {
		if gap := spans[i].start.Sub(spans[i-1].end); gap < interval {
			t.Errorf("run %d started %s after run %d ended, want at least %s", i, gap, i-1, interval)
		}
	}
}

func fetchArgs(u string) []string { return []string{"fetch", "--dump", "html", u} }

func mustPaced(t *testing.T, next Runner, d time.Duration) *Paced {
	t.Helper()
	p, err := NewPaced(next, d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPacedBounds(t *testing.T) {
	inner := &timingRunner{}
	for _, d := range []time.Duration{0, -time.Second, MaxInterval + time.Nanosecond, time.Duration(1 << 62)} {
		if _, err := NewPaced(inner, d); err == nil {
			t.Errorf("interval %s accepted", d)
		}
	}
	if _, err := NewPaced(nil, time.Second); err == nil {
		t.Error("nil runner accepted")
	}
	for _, d := range []time.Duration{time.Nanosecond, MaxInterval} {
		if _, err := NewPaced(inner, d); err != nil {
			t.Errorf("interval %s refused: %v", d, err)
		}
	}
}

// A run the wrapper cannot attribute to a host is refused, never made
// unpaced.
func TestPacedRefusesUntargetedArgs(t *testing.T) {
	inner := &timingRunner{}
	p := mustPaced(t, inner, time.Second)
	for _, args := range [][]string{nil, {}, {"fetch", "--dump"}, fetchArgs("http://careers.example.com/x"),
		fetchArgs("https://"), fetchArgs("careers.example.com/jobs"), fetchArgs("%zz")} {
		if _, err := p.Run(context.Background(), args); err == nil {
			t.Errorf("args %q ran", args)
		}
	}
	if spans, _ := inner.recorded(); len(spans) != 0 {
		t.Fatalf("inner runner called %d times", len(spans))
	}
}

// Runs to one host, however its name is spelled, are spaced from the previous
// run's end.
func TestPacedSpacesOneHost(t *testing.T) {
	const interval = 80 * time.Millisecond
	inner := &timingRunner{hold: 20 * time.Millisecond}
	p := mustPaced(t, inner, interval)
	for _, u := range []string{"https://careers.example.com/a", "https://CAREERS.example.com/b", "https://careers.example.com:443/c"} {
		if _, err := p.Run(context.Background(), fetchArgs(u)); err != nil {
			t.Fatal(err)
		}
	}
	spans, _ := inner.recorded()
	if len(spans) != 3 {
		t.Fatalf("%d runs", len(spans))
	}
	checkGaps(t, spans, interval)
}

// Concurrent callers for one host run one at a time, each after the previous
// one's rest.
func TestPacedSameHostConcurrently(t *testing.T) {
	const interval = 40 * time.Millisecond
	inner := &timingRunner{hold: 10 * time.Millisecond}
	p := mustPaced(t, inner, interval)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/"+strings.Repeat("x", i+1)))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	spans, overlap := inner.recorded()
	if overlap {
		t.Fatal("two runs to one host overlapped")
	}
	if len(spans) != 5 {
		t.Fatalf("%d runs", len(spans))
	}
	checkGaps(t, spans, interval)
}

// A host resting for the longest interval holds up no other host.
func TestPacedHostsDoNotWaitOnEachOther(t *testing.T) {
	inner := &timingRunner{}
	p := mustPaced(t, inner, MaxInterval)
	begin := time.Now()
	for _, u := range []string{"https://a.example.com/", "https://b.example.com/", "https://c.example.org/"} {
		if _, err := p.Run(context.Background(), fetchArgs(u)); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("three hosts took %s; they waited on each other", took)
	}

	// a.example.com itself is still resting: a second run must wait.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Run(ctx, fetchArgs("https://a.example.com/again")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second run to a resting host: %v", err)
	}
	if spans, _ := inner.recorded(); len(spans) != 3 {
		t.Fatalf("%d runs, want 3: the resting host ran", len(spans))
	}
}

// Cancelling a caller during the rest returns at once, starts no run, and
// does not hold up the caller queued behind it.
func TestPacedCancelMidWait(t *testing.T) {
	const interval = 300 * time.Millisecond
	inner := &timingRunner{}
	p := mustPaced(t, inner, interval)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/1")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx, fetchArgs("https://careers.example.com/cancelled"))
		first <- err
	}()
	time.Sleep(30 * time.Millisecond) // let it take the host's token and start resting
	second := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/2"))
		second <- err
	}()
	time.Sleep(30 * time.Millisecond)

	cancelled := time.Now()
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller: %v", err)
		}
		if d := time.Since(cancelled); d > time.Second {
			t.Fatalf("cancelled caller returned after %s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller did not return")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller behind a cancelled one never ran")
	}
	spans, _ := inner.recorded()
	for _, s := range spans {
		if strings.HasSuffix(s.url, "/cancelled") {
			t.Fatal("the cancelled caller started a run")
		}
	}
	if len(spans) != 2 {
		t.Fatalf("%d runs, want 2", len(spans))
	}
	checkGaps(t, spans, interval)
}

// Cancelling a caller queued behind a run in flight returns without waiting
// for that run.
func TestPacedCancelWhileHostBusy(t *testing.T) {
	inner := &timingRunner{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	p := mustPaced(t, inner, 10*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/slow"))
		done <- err
	}()
	<-inner.entered // the slow run holds the host

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Run(ctx, fetchArgs("https://careers.example.com/queued")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued caller: %v", err)
	}
	close(inner.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	spans, overlap := inner.recorded()
	if overlap || len(spans) != 1 {
		t.Fatalf("%d runs, want 1 (overlap %v): the queued caller ran", len(spans), overlap)
	}
}

// A failed run still counts as a visit: the host rests after it, and the
// error comes back unchanged.
func TestPacedFailedRunStillRests(t *testing.T) {
	const interval = 60 * time.Millisecond
	boom := errors.New("boom")
	inner := &timingRunner{err: boom}
	p := mustPaced(t, inner, interval)
	for range 2 {
		if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/x")); !errors.Is(err, boom) {
			t.Fatalf("got %v, want the inner error", err)
		}
	}
	spans, _ := inner.recorded()
	checkGaps(t, spans, interval)
}

// Hosts whose rest is over and that nobody is using are forgotten, so the
// schedule does not grow with every host ever fetched.
func TestPacedForgetsIdleHosts(t *testing.T) {
	inner := &timingRunner{}
	p := mustPaced(t, inner, time.Second)
	for _, u := range []string{"https://a.example.com/", "https://b.example.com/", "https://c.example.com/"} {
		if _, err := p.Run(context.Background(), fetchArgs(u)); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	n := len(p.hosts)
	p.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	p.mu.Unlock()
	if n != 3 {
		t.Fatalf("%d hosts tracked during their rest, want 3", n)
	}
	if _, err := p.Run(context.Background(), fetchArgs("https://d.example.com/")); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.hosts) != 1 || p.hosts["d.example.com"] == nil {
		t.Fatalf("hosts after their rest: %v", p.hosts)
	}
}

// Through the source: the listing page and each posting page on one host are
// spaced by the interval, and every fetch still carries the page URL last.
func TestListThroughPacedRunner(t *testing.T) {
	const interval = 50 * time.Millisecond
	pages := &fakeRunner{pages: map[string]string{
		listURL:                                  listing("/jobs/eng-1", "/jobs/ops-2"),
		"https://careers.example.com/jobs/eng-1": ldPage("Eng", `{"@type":"JobPosting","title":"Eng"}`),
		"https://careers.example.com/jobs/ops-2": ldPage("Ops", `{"@type":"JobPosting","title":"Ops"}`),
	}}
	inner := &timingRunner{next: pages}
	got, err := newSrc(mustPaced(t, inner, interval)).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d postings", len(got))
	}
	spans, overlap := inner.recorded()
	if overlap || len(spans) != 3 {
		t.Fatalf("%d runs, overlap %v", len(spans), overlap)
	}
	checkGaps(t, spans, interval)
}
