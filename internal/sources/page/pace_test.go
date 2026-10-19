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

// timingBrowser records when each load starts and ends, fails on two
// overlapping loads from one host, counts Resets, and delegates to next (or
// answers a stub page).
type timingBrowser struct {
	next    Browser
	hold    time.Duration // how long each run takes
	block   chan struct{} // if set, each run waits for a receive from it
	entered chan struct{} // if set (buffered), signalled as each run starts
	err     error         // if set, returned by every run

	mu       sync.Mutex
	spans    []span
	inflight map[string]int
	overlap  bool
	resets   int
}

func (r *timingBrowser) Reset() error {
	r.mu.Lock()
	r.resets++
	r.mu.Unlock()
	if r.next != nil {
		return r.next.Reset()
	}
	return nil
}

func (r *timingBrowser) Load(ctx context.Context, u string) ([]byte, error) {
	host, _ := targetHost(u)
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
			out, err = r.next.Load(ctx, u)
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

func (r *timingBrowser) recorded() ([]span, bool) {
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

// mustPaced paces by interval alone: every host's robots.txt is missing.
func mustPaced(t *testing.T, next Browser, interval time.Duration) *Paced {
	t.Helper()
	return mustRobotsPaced(t, next, robotsReply(404, ""), interval)
}

// A load the wrapper cannot attribute to a host is refused, never made
// unpaced.
func TestPacedRefusesUntargetedURLs(t *testing.T) {
	inner := &timingBrowser{}
	p := mustPaced(t, inner, time.Second)
	for _, u := range []string{"", "--dump", "http://careers.example.com/x",
		"https://", "careers.example.com/jobs", "%zz", "file:///etc/passwd"} {
		if _, err := p.Load(context.Background(), u); err == nil {
			t.Errorf("url %q loaded", u)
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
	inner := &timingBrowser{hold: 20 * time.Millisecond}
	p := mustPaced(t, inner, interval)
	for _, u := range []string{"https://careers.example.com/a", "https://CAREERS.example.com/b", "https://careers.example.com:443/c"} {
		if _, err := p.Load(context.Background(), u); err != nil {
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
	inner := &timingBrowser{hold: 10 * time.Millisecond}
	p := mustPaced(t, inner, interval)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Load(context.Background(), "https://careers.example.com/"+strings.Repeat("x", i+1))
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

// A host resting holds up no other host.
func TestPacedHostsDoNotWaitOnEachOther(t *testing.T) {
	const interval = 400 * time.Millisecond
	inner := &timingBrowser{}
	p := mustPaced(t, inner, interval)
	begin := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, u := range []string{"https://a.example.com/", "https://b.example.com/", "https://c.example.org/"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Load(context.Background(), u)
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
	// Each host rests once, after its robots.txt read; one after another
	// would be at least 3*interval.
	if took := time.Since(begin); took >= 2*interval {
		t.Fatalf("three hosts took %s; they waited on each other", took)
	}

	// a.example.com itself is still resting: a second run must wait.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Load(ctx, "https://a.example.com/again"); !errors.Is(err, context.DeadlineExceeded) {
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
	inner := &timingBrowser{}
	p := mustPaced(t, inner, interval)
	if _, err := p.Load(context.Background(), "https://careers.example.com/1"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := p.Load(ctx, "https://careers.example.com/cancelled")
		first <- err
	}()
	time.Sleep(30 * time.Millisecond) // let it take the host's token and start resting
	second := make(chan error, 1)
	go func() {
		_, err := p.Load(context.Background(), "https://careers.example.com/2")
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
	inner := &timingBrowser{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	p := mustPaced(t, inner, 10*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := p.Load(context.Background(), "https://careers.example.com/slow")
		done <- err
	}()
	<-inner.entered // the slow run holds the host

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Load(ctx, "https://careers.example.com/queued"); !errors.Is(err, context.DeadlineExceeded) {
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
	inner := &timingBrowser{err: boom}
	p := mustPaced(t, inner, interval)
	for range 2 {
		if _, err := p.Load(context.Background(), "https://careers.example.com/x"); !errors.Is(err, boom) {
			t.Fatalf("got %v, want the inner error", err)
		}
	}
	spans, _ := inner.recorded()
	checkGaps(t, spans, interval)
}

// Hosts whose rest is over and that nobody is using are forgotten, so the
// schedule does not grow with every host ever fetched.
func TestPacedForgetsIdleHosts(t *testing.T) {
	inner := &timingBrowser{}
	p := mustPaced(t, inner, 50*time.Millisecond)
	t0 := time.Now()
	p.now = func() time.Time { return t0 } // every host stays within its rest
	for _, u := range []string{"https://a.example.com/", "https://b.example.com/", "https://c.example.com/"} {
		if _, err := p.Load(context.Background(), u); err != nil {
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
	if _, err := p.Load(context.Background(), "https://d.example.com/"); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.hosts) != 1 || p.hosts["d.example.com"] == nil {
		t.Fatalf("hosts after their rest: %v", p.hosts)
	}
}

// Through the source: the listing page and each posting page on one host are
// spaced by the interval, and the board's end resets the browser through
// Paced.
func TestListThroughPacedBrowser(t *testing.T) {
	const interval = 50 * time.Millisecond
	pages := &fakeBrowser{pages: map[string]string{
		listURL:                                  listing("/jobs/eng-1", "/jobs/ops-2"),
		"https://careers.example.com/jobs/eng-1": ldPage("Eng", `{"@type":"JobPosting","title":"Eng"}`),
		"https://careers.example.com/jobs/ops-2": ldPage("Ops", `{"@type":"JobPosting","title":"Ops"}`),
	}}
	inner := &timingBrowser{next: pages}
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
	if inner.resets != 1 || pages.resetCount() != 1 {
		t.Errorf("resets: Paced's inner %d, browser %d; want 1 each", inner.resets, pages.resetCount())
	}
}

// Reset reaches the wrapped browser and keeps the run's schedule: a host
// still resting after a Reset still waits.
func TestPacedResetKeepsTheSchedule(t *testing.T) {
	const interval = 300 * time.Millisecond
	boom := errors.New("cannot end it")
	inner := &timingBrowser{next: &fakeBrowser{pages: map[string]string{"https://careers.example.com/a": "<p>x</p>"}, resetErr: boom}}
	p := mustPaced(t, inner, interval)
	if _, err := p.Load(context.Background(), "https://careers.example.com/a"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reset(); !errors.Is(err, boom) {
		t.Fatalf("Reset: %v, want the browser's error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Load(ctx, "https://careers.example.com/a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("load right after a Reset: %v; the host's rest must survive the Reset", err)
	}
}
