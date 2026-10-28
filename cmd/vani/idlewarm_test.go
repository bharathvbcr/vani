package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// lockedBuf is a writer the idle loop and the test can share.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// idleFixture is an idle warmer over an in-memory ledger of n kept postings,
// with a counting fake embedder, a stubbed power probe, and short timings.
type idleFixture struct {
	t      *testing.T
	cfg    config.Config
	state  string
	led    *memLedger
	w      *idleWarmer
	log    *lockedBuf
	emb    *countingEmbedder
	opens  atomic.Int32
	closes atomic.Int32
	probes atomic.Int32
	power  atomic.Int32 // powerState the probe reports
	// postingTexts counts the embedded texts that are a posting's (only a
	// posting says k8s; the bank says Kubernetes).
	postingTexts atomic.Int32
}

func newIdleFixture(t *testing.T, n int) *idleFixture {
	t.Helper()
	cfgPath, state := semanticSetup(t)
	f := &idleFixture{t: t, cfg: loadCfg(t, cfgPath), state: state, log: &lockedBuf{},
		led: &memLedger{recs: map[posting.Key]*ledger.Record{}}}
	for i := 0; i < n; i++ {
		f.addPosting(i)
	}
	f.emb = &countingEmbedder{Embedder: semantic.NewFakeEmbedder(256, semSynonyms)}
	f.emb.hook = func(_ context.Context, texts []string) ([][]float32, error, bool) {
		for _, tx := range texts {
			if strings.Contains(tx, "k8s") {
				f.postingTexts.Add(1)
			}
		}
		return nil, nil, false
	}
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		f.opens.Add(1)
		return f.emb, func() error { f.closes.Add(1); return nil }, nil
	}
	f.power.Store(int32(powerAC))
	oldProbe := idlePowerProbe
	idlePowerProbe = func(context.Context) (powerState, string) {
		f.probes.Add(1)
		return powerState(f.power.Load()), "stubbed"
	}
	oldNudge, oldCheck, oldChunk := idleNudgeGap, idleCheckInterval, idleChunkDocs
	idleNudgeGap, idleCheckInterval = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		idlePowerProbe, idleNudgeGap, idleCheckInterval, idleChunkDocs = oldProbe, oldNudge, oldCheck, oldChunk
	})
	f.w = &idleWarmer{stateDir: state, stderr: f.log, led: f.led, cfg: func() config.Config { return f.cfg }, gate: newIdleGate()}
	return f
}

func (f *idleFixture) addPosting(i int) {
	r := semRec(fmt.Sprintf("gh:a:%03d", i), "Platform Engineer", fmt.Sprintf("Requirements:\n- Operate k8s clusters, team %d\n- Golang services", i))
	r.Status = posting.Explained
	f.led.recs[r.Key] = &r
}

func (f *idleFixture) state_(t *testing.T) idleState {
	t.Helper()
	st, err := readIdleState(f.state)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *idleFixture) scored() int {
	m, warning := semanticLookup(f.cfg, f.state)
	if m == nil {
		if strings.Contains(warning, "no scores yet") {
			return 0
		}
		f.t.Fatalf("lookup: %s", warning)
	}
	return m.Len()
}

// With nothing to warm the loop never loads the model, and says "nothing to
// warm", which is not the same state as "could not tell whether it may". The
// power source is not even read: an idle Mac runs no process.
func TestIdleWarmEmptyBacklogNeverLoadsModel(t *testing.T) {
	f := newIdleFixture(t, 0)
	ctx := context.Background()

	// An empty ledger.
	if wait := f.w.step(ctx); wait != idleCheckInterval {
		t.Errorf("wait %v", wait)
	}
	if st := f.state_(t); st.Kind != idleNothing || st.Backlog != 0 || !strings.Contains(st.Reason, "nothing to warm") {
		t.Errorf("state %+v", st)
	}

	// Postings that are all scored already.
	for i := 0; i < 3; i++ {
		f.addPosting(i)
	}
	if rep := warmSemantic(ctx, f.cfg, f.state, f.led.All(), nil, 10); rep.Scored != 3 {
		t.Fatalf("setup warm %+v", rep)
	}
	opens := f.opens.Load()
	f.w.step(ctx)
	// Postings no warm can score (no text at all) and closed ones are not backlog.
	empty := semRec("gh:a:empty", "", "")
	empty.Status = posting.Explained
	f.led.recs[empty.Key] = &empty
	dead := semRec("gh:a:dead", "Platform Engineer", "Operate k8s clusters, closed")
	dead.Status = posting.Dead
	f.led.recs[dead.Key] = &dead
	f.w.step(ctx)
	if f.opens.Load() != opens {
		t.Errorf("the model was loaded %d time(s) with nothing to warm", f.opens.Load()-opens)
	}
	if f.probes.Load() != 0 {
		t.Errorf("the power source was read %d time(s) with nothing to warm", f.probes.Load())
	}
	if st := f.state_(t); st.Kind != idleNothing || st.Backlog != 0 {
		t.Errorf("state %+v", st)
	}
}

// A power source that cannot be read skips the warm and says so, with its own
// kind, apart from "nothing to warm"; battery is its own kind too; neither
// loads the model.
func TestIdleWarmPowerUnknownSkipsAndSaysSo(t *testing.T) {
	f := newIdleFixture(t, 3)
	ctx := context.Background()
	f.power.Store(int32(powerUnknown))
	if wait := f.w.step(ctx); wait != idleCheckInterval {
		t.Errorf("wait %v", wait)
	}
	st := f.state_(t)
	if st.Kind != idlePowerUnknown || !strings.Contains(st.Reason, "power state unknown") || st.Backlog != 3 {
		t.Errorf("state %+v", st)
	}
	if st.Kind == idleNothing {
		t.Error("an unreadable power source was recorded as nothing to do")
	}
	if f.opens.Load() != 0 || f.scored() != 0 {
		t.Errorf("warmed with an unknown power state: %d opens, %d scored", f.opens.Load(), f.scored())
	}
	if !strings.Contains(f.log.String(), "power state unknown") {
		t.Errorf("not said in the log: %q", f.log.String())
	}
	lines := strings.Join(idleLines(f.state, now()), "\n")
	if !strings.Contains(lines, "power state unknown") || !strings.Contains(lines, "3 posting(s) without a current score") {
		t.Errorf("status lines: %q", lines)
	}

	f.power.Store(int32(powerBattery))
	f.w.step(ctx)
	if st := f.state_(t); st.Kind != idleBattery || !strings.Contains(st.Reason, "battery") || f.opens.Load() != 0 {
		t.Errorf("on battery: %+v (%d opens)", st, f.opens.Load())
	}

	f.power.Store(int32(powerAC))
	f.w.step(ctx)
	if f.scored() != 3 || f.state_(t).Kind != idleDone {
		t.Errorf("on AC power it did not warm: %d scored, %+v", f.scored(), f.state_(t))
	}
}

func TestParsePower(t *testing.T) {
	for _, c := range []struct {
		in   string
		want powerState
	}{
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n", powerAC},
		{"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t80%; discharging\n", powerBattery},
		{"Now drawing from 'UPS Power'\n", powerBattery},
		{"Now drawing from 'Solar Power'\n", powerUnknown},
		{"", powerUnknown},
		{"error: no battery\n", powerUnknown},
		{"Now drawing from '", powerUnknown},
		{strings.Repeat("x", 1<<16), powerUnknown},
	} {
		if got, why := parsePower(c.in); got != c.want || (got == powerUnknown && why == "") {
			t.Errorf("parsePower(%.40q) = %v, %q; want %v", c.in, got, why, c.want)
		}
	}
	w := &cappedWriter{max: 8}
	if n, err := w.Write([]byte(strings.Repeat("a", 100))); n != 100 || err != nil || len(w.b) != 8 {
		t.Errorf("capped writer kept %d bytes, wrote %d, %v", len(w.b), n, err)
	}
}

// A model that will not load backs the loop off 10 minutes, then doubles it up
// to 4 hours; the backoff is on disk, so a restarted daemon honours it rather
// than hitting the model again at once; progress clears it.
func TestIdleWarmBackoffPersists(t *testing.T) {
	f := newIdleFixture(t, 3)
	ctx := context.Background()
	clock := jobsT0
	oldNow := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = oldNow })
	broken := errors.New("model file unreadable")
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		f.opens.Add(1)
		return nil, nil, fmt.Errorf("%w: %v", semantic.ErrUnavailable, broken)
	}

	if wait := f.w.step(ctx); wait != 10*time.Minute {
		t.Fatalf("first backoff %v, want 10m", wait)
	}
	st := f.state_(t)
	if st.Kind != idleBackoff || st.BackoffSec != 600 || !st.NextEligible.Equal(clock.Add(10*time.Minute)) || !strings.Contains(st.Reason, "model file unreadable") {
		t.Fatalf("state %+v", st)
	}

	// A restarted daemon (a new warmer over the same state) waits out the rest.
	clock = clock.Add(4 * time.Minute)
	restarted := &idleWarmer{stateDir: f.state, stderr: f.log, led: f.led, cfg: f.w.cfg, gate: newIdleGate()}
	if wait := restarted.step(ctx); wait != 6*time.Minute {
		t.Errorf("restarted warmer waits %v, want the remaining 6m", wait)
	}
	if f.opens.Load() != 1 {
		t.Errorf("the model was tried again inside the backoff (%d opens)", f.opens.Load())
	}
	if lines := strings.Join(idleLines(f.state, clock), "\n"); !strings.Contains(lines, "next eligible: in 6m0s") {
		t.Errorf("status lines: %q", lines)
	}

	// Past it, one more failure doubles the wait.
	clock = clock.Add(7 * time.Minute)
	if wait := restarted.step(ctx); wait != 20*time.Minute {
		t.Errorf("second backoff %v, want 20m", wait)
	}
	if f.opens.Load() != 2 {
		t.Errorf("%d opens", f.opens.Load())
	}

	// It never exceeds the cap.
	oldMax := idleBackoffMax
	idleBackoffMax = 30 * time.Minute
	t.Cleanup(func() { idleBackoffMax = oldMax })
	clock = clock.Add(21 * time.Minute)
	if wait := restarted.step(ctx); wait != 30*time.Minute {
		t.Errorf("capped backoff %v, want 30m", wait)
	}

	// The model comes back: the window scores, and the backoff is cleared.
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		f.opens.Add(1)
		return f.emb, func() error { f.closes.Add(1); return nil }, nil
	}
	clock = clock.Add(31 * time.Minute)
	restarted.step(ctx)
	st = f.state_(t)
	if st.Kind != idleDone || st.BackoffSec != 0 || !st.NextEligible.IsZero() || f.scored() != 3 {
		t.Errorf("after recovery: %+v, %d scored", st, f.scored())
	}
}

// A model that loads but fails the same way for every posting scores nothing:
// the window stops after idleBadChunks chunks and backs off, instead of trying
// every posting.
func TestIdleWarmNoProgressBacksOff(t *testing.T) {
	f := newIdleFixture(t, 40)
	idleChunkDocs = 4
	f.emb.hook = func(_ context.Context, texts []string) ([][]float32, error, bool) {
		for _, tx := range texts {
			if strings.Contains(tx, "k8s") {
				return nil, errors.New("embedding failed"), true
			}
		}
		return nil, nil, false
	}
	f.w.step(context.Background())
	st := f.state_(t)
	if st.Kind != idleBackoff || st.Last.Scored != 0 || st.Last.Chunks > idleBadChunks || f.scored() != 0 {
		t.Errorf("state %+v (%d scored)", st, f.scored())
	}
	if f.closes.Load() != f.opens.Load() {
		t.Errorf("model opened %d, closed %d", f.opens.Load(), f.closes.Load())
	}
}

// A chunk that scores nothing only because its own deadline fired (the profile
// embed does not finish) is not a failed model: it does not count toward
// idleBadChunks and does not schedule a backoff. Scores stay absent.
func TestIdleWarmChunkDeadlineDoesNotBackOff(t *testing.T) {
	f := newIdleFixture(t, 2*(idleBadChunks+1))
	idleChunkDocs = 2
	oldChunk := idleChunkTime
	idleChunkTime = 200 * time.Millisecond
	t.Cleanup(func() { idleChunkTime = oldChunk })
	f.emb.hook = func(ctx context.Context, _ []string) ([][]float32, error, bool) {
		<-ctx.Done()
		return nil, ctx.Err(), true
	}
	wait := f.w.step(context.Background())
	st := f.state_(t)
	if st.Kind == idleBackoff || st.BackoffSec != 0 || !st.NextEligible.IsZero() || wait >= idleBackoffMin {
		t.Fatalf("chunk deadline scheduled a backoff: wait %v, state %+v", wait, st)
	}
	if st.Last.Chunks <= idleBadChunks || st.Last.Scored != 0 || st.Last.Rescored != 0 {
		t.Fatalf("state %+v", st)
	}
	if f.scored() != 0 {
		t.Errorf("scores were written: %d", f.scored())
	}
	if strings.Contains(f.log.String(), "backing off") {
		t.Errorf("log backed off:\n%s", f.log.String())
	}
}

// ErrUnavailable still backs off when the same error also carries a deadline.
// It stops on that chunk, rather than treating the deadline as the only failure.
// Scores stay absent.
func TestIdleWarmDeadlineAndUnavailableStillBacksOff(t *testing.T) {
	f := newIdleFixture(t, 2*(idleBadChunks+1))
	idleChunkDocs = 2
	f.emb.hook = func(context.Context, []string) ([][]float32, error, bool) {
		return nil, fmt.Errorf("%w: %w", semantic.ErrUnavailable, context.DeadlineExceeded), true
	}
	wait := f.w.step(context.Background())
	st := f.state_(t)
	if st.Kind != idleBackoff || st.BackoffSec != int64(idleBackoffMin/time.Second) || wait != idleBackoffMin || st.Last.Chunks != 1 || f.scored() != 0 {
		t.Fatalf("wait %v, scored %d, state %+v", wait, f.scored(), st)
	}
}

// The window warms in chunks of idleChunkDocs with one model load, best Score
// first, and closes the model at the end; the next look finds nothing.
func TestIdleWarmWindowWarmsInChunks(t *testing.T) {
	f := newIdleFixture(t, 40)
	idleChunkDocs = 16
	ctx := context.Background()
	f.w.step(ctx)
	st := f.state_(t)
	if st.Kind != idleDone || st.Last.Chunks != 3 || st.Last.Scored != 40 || st.Total.Scored != 40 || st.Backlog != 40 {
		t.Fatalf("state %+v", st)
	}
	if f.opens.Load() != 1 || f.closes.Load() != 1 {
		t.Errorf("model opened %d, closed %d; want once each", f.opens.Load(), f.closes.Load())
	}
	if f.scored() != 40 {
		t.Errorf("%d scored", f.scored())
	}
	if f.probes.Load() != 3 { // once before the window, then before each chunk after the first
		t.Errorf("power read %d times, want 3", f.probes.Load())
	}
	f.w.step(ctx)
	if f.opens.Load() != 1 || f.state_(t).Kind != idleNothing {
		t.Errorf("a second look warmed again: %d opens, %+v", f.opens.Load(), f.state_(t))
	}
	if strings.Contains(f.log.String(), "Platform") || strings.Contains(f.log.String(), "k8s") {
		t.Errorf("posting text reached the log:\n%s", f.log.String())
	}
}

// Leaving AC power mid-window stops it at the next chunk.
func TestIdleWarmStopsWhenPowerLeaves(t *testing.T) {
	f := newIdleFixture(t, 40)
	idleChunkDocs = 8
	var chunks atomic.Int32
	f.emb.hook = func(_ context.Context, texts []string) ([][]float32, error, bool) {
		for _, tx := range texts {
			if strings.Contains(tx, "k8s") {
				if chunks.Add(1) == 1 {
					f.power.Store(int32(powerBattery)) // unplugged during the first chunk
				}
				break
			}
		}
		return nil, nil, false
	}
	f.w.step(context.Background())
	st := f.state_(t)
	if st.Kind != idleBattery || st.Last.Chunks != 1 || st.Last.Scored != 8 {
		t.Errorf("state %+v", st)
	}
	if f.closes.Load() != 1 || f.scored() != 8 {
		t.Errorf("closes %d, scored %d", f.closes.Load(), f.scored())
	}
}

// A profile change leaves every score missing but the chunk vectors stored: the
// idle warm rescored them without embedding any posting text.
func TestIdleWarmRescoresFromVectors(t *testing.T) {
	f := newIdleFixture(t, 5)
	ctx := context.Background()
	f.w.step(ctx)
	if f.scored() != 5 {
		t.Fatalf("setup: %d scored", f.scored())
	}
	embedded := f.postingTexts.Load()
	f.cfg.TargetSummary += " and Rust systems"
	f.w.step(ctx)
	st := f.state_(t)
	if st.Backlog != 5 || st.Last.Rescored != 5 || st.Last.Scored != 0 || st.Kind != idleDone {
		t.Errorf("state %+v", st)
	}
	if got := f.postingTexts.Load(); got != embedded {
		t.Errorf("%d posting text(s) were embedded for a profile change; the vectors were stored", got-embedded)
	}
}

// With semantic matching off the loop does nothing: no state file, no model,
// no power probe.
func TestIdleWarmOffWhenSemanticOff(t *testing.T) {
	f := newIdleFixture(t, 3)
	f.cfg.Semantic.Snapshot = ""
	if wait := f.w.step(context.Background()); wait != idleCheckInterval {
		t.Errorf("wait %v", wait)
	}
	if _, err := os.Stat(idleStatePath(f.state)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a state file was written with semantic matching off: %v", err)
	}
	if f.opens.Load() != 0 || f.probes.Load() != 0 {
		t.Errorf("%d opens, %d power reads", f.opens.Load(), f.probes.Load())
	}
}

// An unreadable state file is said in the log and started again, never trusted
// and never fatal.
func TestIdleWarmStartsAnUnreadableStateAgain(t *testing.T) {
	f := newIdleFixture(t, 0)
	if err := os.MkdirAll(filepath.Join(f.state, "semantic"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"truncated":     `{"v":1,"kind":`,
		"wrong version": `{"v":99,"kind":"done"}`,
		"oversized":     `{"v":1,"reason":"` + strings.Repeat("x", maxIdleState) + `"}`,
	} {
		if err := os.WriteFile(idleStatePath(f.state), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if lines := idleLines(f.state, now()); len(lines) != 1 || !strings.Contains(lines[0], "unreadable") {
			t.Errorf("%s: status lines %q", name, lines)
		}
		f.log.b.Reset()
		f.w.step(context.Background())
		if !strings.Contains(f.log.String(), "starting its state again") {
			t.Errorf("%s: not said in the log: %q", name, f.log.String())
		}
		if st := f.state_(t); st.Kind != idleNothing {
			t.Errorf("%s: state %+v", name, st)
		}
	}
}

// holdEmbedder stays inside Embed until release is closed. It does not return
// on ctx so a closer can be observed while the call is still in flight.
type holdEmbedder struct {
	semantic.Embedder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	events  []string
}

func (h *holdEmbedder) event(name string) {
	h.mu.Lock()
	h.events = append(h.events, name)
	h.mu.Unlock()
}

func (h *holdEmbedder) Embed(context.Context, []string, semantic.Kind) ([][]float32, error) {
	h.once.Do(func() { close(h.entered) })
	h.event("embed-enter")
	<-h.release
	h.event("embed-return")
	return nil, context.Canceled
}

func (h *holdEmbedder) seq() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

// blockingEmbedder blocks inside Embed until its context ends, as an embedding
// call in flight does.
type blockingEmbedder struct {
	semantic.Embedder
	in chan struct{}
}

func (b blockingEmbedder) Embed(ctx context.Context, _ []string, _ semantic.Kind) ([][]float32, error) {
	select {
	case b.in <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// heldByAnother reports whether another open file holds semantic.lock. It is
// called from daemon goroutines too, so a failure to ask is t.Errorf (safe from
// any goroutine) and reads as "held": the caller's check then fails with it.
func heldByAnother(t *testing.T, state string) bool {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(state, "semantic", "semantic.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Errorf("opening the lock file: %v", err)
		return true
	}
	defer f.Close()
	err = filelock.TryLock(f, true)
	if err == nil {
		_ = filelock.Unlock(f)
		return false
	}
	if !errors.Is(err, filelock.ErrWouldBlock) {
		t.Errorf("trying the lock: %v", err)
	}
	return true
}

// stuckWindow starts a step whose first embedding call blocks, and returns when
// the call is in flight (the cache lock is held). The first opened model blocks;
// every later one is the fast fake, as a run's own warm opens.
func stuckWindow(t *testing.T, f *idleFixture, ctx context.Context) (done chan struct{}, firstClosed *atomic.Bool) {
	t.Helper()
	in := make(chan struct{}, 1)
	var opened atomic.Int32
	firstClosed = &atomic.Bool{}
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		if opened.Add(1) == 1 {
			return blockingEmbedder{Embedder: semantic.NewFakeEmbedder(256, semSynonyms), in: in},
				func() error { firstClosed.Store(true); return nil }, nil
		}
		return semantic.NewFakeEmbedder(256, semSynonyms), func() error { return nil }, nil
	}
	done = make(chan struct{})
	go func() { defer close(done); f.w.step(ctx) }()
	select {
	case <-in:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle chunk never reached the embedder")
	}
	return done, firstClosed
}

// A chunk is in flight (and holds the cache lock) when a run starts. Pausing the
// gate stops the window and returns only once the lock is free and the idle
// model closed, so the run's own warm gets the lock at once instead of waiting
// 30 s for ErrWarmBusy.
func TestIdleWarmYieldsToRun(t *testing.T) {
	f := newIdleFixture(t, 5)
	ctx := context.Background()
	done, firstClosed := stuckWindow(t, f, ctx)
	if !heldByAnother(t, f.state) {
		t.Fatal("the in-flight chunk does not hold the cache lock: the scenario is not the one under test")
	}

	start := time.Now()
	resume, yielded := f.w.gate.Pause(ctx, 10*time.Second)
	if !yielded {
		t.Fatal("the window did not stop")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the run waited %v for the idle warm", took)
	}
	if heldByAnother(t, f.state) {
		t.Error("the cache lock is still held after the pause returned")
	}
	if !firstClosed.Load() {
		t.Error("the idle model was still open when the pause returned")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle step did not return")
	}

	// The run's warm: it gets the lock and scores; it is not a skipped warm.
	runStart := time.Now()
	rep := warmSemanticDocs(ctx, f.cfg, f.state, recordDocs(f.led.All()), 10)
	if !rep.Ran || rep.Note != "" || rep.Scored != 5 {
		t.Errorf("the run's warm: %+v", rep)
	}
	if took := time.Since(runStart); took > 5*time.Second {
		t.Errorf("the run's warm took %v", took)
	}

	// While the run holds the gate the loop does not start another window; when
	// it ends, the loop is nudged and may.
	f.addPosting(99)
	if wait := f.w.step(ctx); wait != idleCheckInterval {
		t.Errorf("wait %v while paused", wait)
	}
	if st := f.state_(t); st.Kind == idleDone || f.scored() != 5 {
		t.Errorf("a window ran while a run held the gate: %+v, %d scored", st, f.scored())
	}
	resume()
	select {
	case <-f.w.gate.wake:
	default:
		t.Error("resume did not nudge the loop")
	}
}

// Cancelling a warm does not Close the model while Embed is still inside.
// Close runs only after Embed returns. If Embed is still inside when the join
// bound expires, Close is not called and the window still ends.
func TestIdleWarmCloseWaitsForEmbed(t *testing.T) {
	f := newIdleFixture(t, 2)
	oldJoin := idleEmbedJoin
	idleEmbedJoin = 2 * time.Second
	t.Cleanup(func() { idleEmbedJoin = oldJoin })

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	h := &holdEmbedder{
		Embedder: semantic.NewFakeEmbedder(256, semSynonyms),
		entered:  make(chan struct{}),
		release:  release,
	}
	var closed atomic.Bool
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return h, func() error {
			h.event("close")
			closed.Store(true)
			return nil
		}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); f.w.step(context.Background()) }()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle chunk never reached the embedder")
	}

	type pauseResult struct{ yielded bool }
	paused := make(chan pauseResult, 1)
	go func() {
		_, yielded := f.w.gate.Pause(context.Background(), 5*time.Second)
		paused <- pauseResult{yielded}
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if closed.Load() {
			releaseFn()
			t.Fatal("Close ran while Embed was still inside")
		}
		select {
		case <-done:
			releaseFn()
			t.Fatal("the window ended while Embed was still inside")
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	releaseFn()
	select {
	case got := <-paused:
		if !got.yielded {
			t.Error("the window did not stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pause did not return after Embed returned")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle step did not return")
	}
	got := h.seq()
	want := []string{"embed-enter", "embed-return", "close"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func TestIdleWarmCloseSkippedWhenEmbedOutlastsJoin(t *testing.T) {
	f := newIdleFixture(t, 2)
	oldJoin := idleEmbedJoin
	idleEmbedJoin = 150 * time.Millisecond
	t.Cleanup(func() { idleEmbedJoin = oldJoin })

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	h := &holdEmbedder{
		Embedder: semantic.NewFakeEmbedder(256, semSynonyms),
		entered:  make(chan struct{}),
		release:  release,
	}
	var closed atomic.Bool
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return h, func() error {
			h.event("close")
			closed.Store(true)
			return nil
		}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); f.w.step(context.Background()) }()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle chunk never reached the embedder")
	}
	_, yielded := f.w.gate.Pause(context.Background(), 5*time.Second)
	if !yielded {
		t.Fatal("the window did not stop")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle step did not return")
	}
	if closed.Load() || strings.Contains(strings.Join(h.seq(), ","), "close") {
		t.Fatalf("Close ran while Embed was still inside: %v", h.seq())
	}
	if !strings.Contains(f.log.String(), "model close skipped") {
		t.Errorf("close skip was not recorded:\n%s", f.log.String())
	}
	// Let Embed return after the window has ended. Close stays uncalled.
	releaseFn()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && strings.Join(h.seq(), ",") != "embed-enter,embed-return" {
		if closed.Load() {
			t.Fatal("Close ran after the join bound expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if closed.Load() {
		t.Fatal("Close ran after the join bound expired")
	}
	got := strings.Join(h.seq(), ",")
	if got != "embed-enter,embed-return" {
		t.Fatalf("events %s", got)
	}
}

// The daemon stopping ends the window within the grace and closes the model;
// nothing is left holding the lock.
func TestIdleWarmShutdownDuringChunk(t *testing.T) {
	f := newIdleFixture(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, firstClosed := stuckWindow(t, f, ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle step outlived its context")
	}
	if !firstClosed.Load() || heldByAnother(t, f.state) {
		t.Errorf("model closed %v, lock held %v", firstClosed.Load(), heldByAnother(t, f.state))
	}
	if st := f.state_(t); st.Kind != idleStopped {
		t.Errorf("state %+v", st)
	}
}

// The gate, alone: a pause with no window returns at once; a window that never
// ends is waited for only up to the bound, and says so; a pause is released
// once however often resume is called; a held gate refuses new windows; a nil
// gate pauses nothing; and a done parent opens none.
func TestIdleGate(t *testing.T) {
	ctx := context.Background()
	g := newIdleGate()
	resume, ok := g.Pause(ctx, time.Second)
	if !ok || !g.paused() {
		t.Fatal("pause with no window")
	}
	if _, _, opened := g.open(ctx); opened {
		t.Error("a window opened while a run held the gate")
	}
	resume()
	resume() // a second resume must not release a pause it does not hold
	if g.paused() {
		t.Error("still paused")
	}
	r2, _ := g.Pause(ctx, time.Second)
	r3, _ := g.Pause(ctx, time.Second)
	r2()
	if !g.paused() {
		t.Error("two runs hold the gate; one resume released it")
	}
	r3()
	if g.paused() {
		t.Error("both resumed, still paused")
	}

	wctx, end, opened := g.open(ctx)
	if !opened {
		t.Fatal("no window")
	}
	if _, _, again := g.open(ctx); again {
		t.Error("a second window opened beside the first")
	}
	start := time.Now()
	res, yielded := g.Pause(ctx, 50*time.Millisecond) // the window never ends
	if yielded || time.Since(start) < 40*time.Millisecond || time.Since(start) > 2*time.Second {
		t.Errorf("yielded %v after %v", yielded, time.Since(start))
	}
	if wctx.Err() == nil {
		t.Error("pause did not cancel the window")
	}
	end()
	res()

	gctx, gcancel := context.WithCancel(ctx)
	_, end, _ = g.open(ctx)
	gcancel()
	if res, yielded := g.Pause(gctx, time.Minute); yielded {
		t.Error("a cancelled pause reported a yield")
	} else {
		res()
	}
	end()

	dead, dcancel := context.WithCancel(ctx)
	dcancel()
	if _, _, opened := g.open(dead); opened {
		t.Error("a window opened on a done context")
	}
	var nilGate *idleGate
	if res, yielded := nilGate.Pause(ctx, time.Second); !yielded {
		t.Error("nil gate")
	} else {
		res()
	}
	nilGate.nudge()
}

// idleDaemon is a running `vani daemon` over f's ledger whose idle warm's first
// model blocks inside Embed. runOnce is the app's pipeline run.
type idleDaemon struct {
	in          chan struct{} // an embedding call of the idle warm is in flight
	firstClosed atomic.Bool
	stop        context.CancelFunc
	done        chan daemonResult
}

type daemonResult struct {
	code   int
	stderr string
}

// releaseClock holds every wait until release is closed, then fires each at once
// by moving its time forward, as a jumpClock does: the scheduled run starts when
// the test says.
type releaseClock struct {
	mu      sync.Mutex
	t       time.Time
	release chan struct{}
	quit    chan struct{}
}

func (c *releaseClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *releaseClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		select {
		case <-c.release:
		case <-c.quit:
			return
		}
		c.mu.Lock()
		c.t = c.t.Add(d)
		n := c.t
		c.mu.Unlock()
		ch <- n
	}()
	return ch
}

func startIdleDaemon(t *testing.T, f *idleFixture, runOnce func(context.Context) error) *idleDaemon {
	t.Helper()
	return startIdleDaemonOn(t, f, idleClock{}, runOnce)
}

func startIdleDaemonOn(t *testing.T, f *idleFixture, clock daemon.Clock, runOnce func(context.Context) error) *idleDaemon {
	t.Helper()
	d := &idleDaemon{in: make(chan struct{}, 1), done: make(chan daemonResult, 1)}
	oldBuild, oldClock, oldSig, oldPoll, oldNow := buildApp, daemonClock, signalContext, requestPollInterval, now
	t.Cleanup(func() {
		buildApp, daemonClock, signalContext, requestPollInterval, now = oldBuild, oldClock, oldSig, oldPoll, oldNow
	})
	now = time.Now // a request is fresh by the wall clock
	parent, stop := context.WithCancel(context.Background())
	d.stop = stop
	t.Cleanup(stop)
	signalContext = func() (context.Context, context.CancelFunc) { return parent, func() {} }
	daemonClock, requestPollInterval = clock, 2*time.Millisecond
	var opened atomic.Int32
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		if opened.Add(1) == 1 {
			return blockingEmbedder{Embedder: semantic.NewFakeEmbedder(256, semSynonyms), in: d.in},
				func() error { d.firstClosed.Store(true); return nil }, nil
		}
		return semantic.NewFakeEmbedder(256, semSynonyms), func() error { return nil }, nil
	}
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: f.led, Notifier: &fakeNotifier{}, Tune: func(int, bool) {}, RunOnce: runOnce}, nil
	}
	go func() {
		code, _, stderr := do(t, "daemon")
		d.done <- daemonResult{code, stderr}
	}()
	select {
	case <-d.in:
	case <-time.After(15 * time.Second):
		stop()
		<-d.done
		t.Fatal("the idle warm never put a chunk in flight")
	}
	return d
}

// The daemon pauses the idle warm before a requested run (main.go: manual): with
// a chunk in flight holding the cache lock, the run's warm finds the lock free
// and the idle model closed, and scores; the daemon then stops cleanly.
func TestIdleWarmDaemonYieldsToRequestedRun(t *testing.T) {
	f := newIdleFixture(t, 5)
	idleNudgeGap, idleCheckInterval = 5*time.Millisecond, 20*time.Millisecond
	var rep atomic.Pointer[semanticReport]
	var closedBeforeRun, lockFreeAtRun atomic.Bool
	var d *idleDaemon
	ran := make(chan struct{})
	var once sync.Once
	d = startIdleDaemon(t, f, func(ctx context.Context) error {
		once.Do(func() {
			closedBeforeRun.Store(d.firstClosed.Load())
			lockFreeAtRun.Store(!heldByAnother(t, f.state))
			r := warmSemanticDocs(ctx, f.cfg, f.state, recordDocs(f.led.All()), 10)
			rep.Store(&r)
			close(ran)
		})
		return nil
	})
	if !heldByAnother(t, f.state) {
		t.Fatal("the in-flight chunk does not hold the cache lock: not the scenario under test")
	}
	mustCode(t, 0, "run-request", "--no-digest")
	select {
	case <-ran:
	case <-time.After(30 * time.Second):
		d.stop()
		r := <-d.done
		t.Fatalf("the requested run never started\n%s", r.stderr)
	}
	if !closedBeforeRun.Load() || !lockFreeAtRun.Load() {
		t.Errorf("when the run started: idle model closed %v, lock free %v", closedBeforeRun.Load(), lockFreeAtRun.Load())
	}
	if r := rep.Load(); !r.Ran || r.Note != "" || r.Scored != 5 {
		t.Errorf("the run's warm: %+v", r)
	}
	d.stop()
	select {
	case r := <-d.done:
		if r.code != 0 {
			t.Errorf("daemon exit %d\n%s", r.code, r.stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop")
	}
}

// The scheduled job (main.go: job) pauses the idle warm too, before it takes the
// ledger: the run's warm finds the lock free and the idle model closed.
func TestIdleWarmDaemonYieldsToScheduledRun(t *testing.T) {
	f := newIdleFixture(t, 5)
	clock := &releaseClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC), release: make(chan struct{}), quit: make(chan struct{})}
	t.Cleanup(func() { close(clock.quit) })
	var rep atomic.Pointer[semanticReport]
	var closedBeforeRun, lockFreeAtRun atomic.Bool
	var d *idleDaemon
	ran := make(chan struct{})
	var once sync.Once
	d = startIdleDaemonOn(t, f, clock, func(ctx context.Context) error {
		once.Do(func() {
			closedBeforeRun.Store(d.firstClosed.Load())
			lockFreeAtRun.Store(!heldByAnother(t, f.state))
			r := warmSemanticDocs(ctx, f.cfg, f.state, recordDocs(f.led.All()), 10)
			rep.Store(&r)
			close(ran)
		})
		return nil
	})
	if !heldByAnother(t, f.state) {
		t.Fatal("the in-flight chunk does not hold the cache lock: not the scenario under test")
	}
	close(clock.release)
	select {
	case <-ran:
	case <-time.After(30 * time.Second):
		d.stop()
		r := <-d.done
		t.Fatalf("the scheduled run never started\n%s", r.stderr)
	}
	if !closedBeforeRun.Load() || !lockFreeAtRun.Load() {
		t.Errorf("when the run started: idle model closed %v, lock free %v", closedBeforeRun.Load(), lockFreeAtRun.Load())
	}
	if r := rep.Load(); !r.Ran || r.Note != "" || r.Scored != 5 {
		t.Errorf("the run's warm: %+v", r)
	}
	d.stop()
	select {
	case <-d.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop")
	}
}

// A stop (SIGTERM) while an idle chunk is in flight ends the daemon within the
// grace: the window is cancelled, the model closed, and the lock dropped.
func TestIdleWarmDaemonStopsMidChunk(t *testing.T) {
	f := newIdleFixture(t, 5)
	d := startIdleDaemon(t, f, func(context.Context) error { return nil })
	start := time.Now()
	d.stop()
	select {
	case r := <-d.done:
		if r.code != 0 {
			t.Errorf("daemon exit %d\n%s", r.code, r.stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop with an idle chunk in flight")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stop took %v", took)
	}
	if !d.firstClosed.Load() || heldByAnother(t, f.state) {
		t.Errorf("idle model closed %v, lock held %v", d.firstClosed.Load(), heldByAnother(t, f.state))
	}
	if st := f.state_(t); st.Kind != idleStopped {
		t.Errorf("state %+v", st)
	}
}

// VANI_NO_IDLE_WARM keeps the daemon from starting the loop at all.
func TestIdleWarmCanBeSwitchedOff(t *testing.T) {
	t.Setenv("VANI_NO_IDLE_WARM", "1")
	if idleWarmEnabled() {
		t.Error("VANI_NO_IDLE_WARM=1 left the idle warm on")
	}
	t.Setenv("VANI_NO_IDLE_WARM", "")
	if !idleWarmEnabled() {
		t.Error("the idle warm is not on by default")
	}
}

// orderEmbedder lets profile embeds through and holds the posting embed
// (the text that says k8s) until release is closed.
type orderEmbedder struct {
	semantic.Embedder
	mu       sync.Mutex
	events   []string
	inside   atomic.Bool
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
	once     sync.Once
	retOnce  sync.Once
}

func (o *orderEmbedder) event(name string) {
	o.mu.Lock()
	o.events = append(o.events, name)
	o.mu.Unlock()
}

func (o *orderEmbedder) seq() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.events, ",")
}

func (o *orderEmbedder) Embed(ctx context.Context, texts []string, k semantic.Kind) ([][]float32, error) {
	posting := false
	for _, tx := range texts {
		if strings.Contains(tx, "k8s") {
			posting = true
			break
		}
	}
	if !posting {
		return o.Embedder.Embed(ctx, texts, k)
	}
	o.event("embed-enter")
	o.inside.Store(true)
	o.once.Do(func() { close(o.entered) })
	<-o.release
	o.inside.Store(false)
	o.event("embed-return")
	o.retOnce.Do(func() { close(o.returned) })
	return nil, context.Canceled
}

// Close stays uncalled while the posting Embed is inside, then runs after
// that Embed returns. When the join bound expires first, Close is skipped,
// the skip line is logged, and the window still ends.
func TestIdleWarmCloseDoesNotRaceEmbed(t *testing.T) {
	t.Run("close after embed returns", func(t *testing.T) {
		f := newIdleFixture(t, 2)
		old := idleEmbedJoin
		idleEmbedJoin = 3 * time.Second
		t.Cleanup(func() { idleEmbedJoin = old })
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseFn)
		o := &orderEmbedder{
			Embedder: semantic.NewFakeEmbedder(256, semSynonyms),
			entered:  make(chan struct{}),
			release:  release,
			returned: make(chan struct{}),
		}
		closed := make(chan struct{})
		var closeOnce sync.Once
		openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return o, func() error {
				if o.inside.Load() {
					o.event("close-raced")
				}
				o.event("close")
				closeOnce.Do(func() { close(closed) })
				return nil
			}, nil
		}
		done := make(chan struct{})
		go func() { defer close(done); f.w.step(context.Background()) }()
		select {
		case <-o.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the posting embed did not start")
		}
		paused := make(chan bool, 1)
		go func() {
			_, yielded := f.w.gate.Pause(context.Background(), 5*time.Second)
			paused <- yielded
		}()
		select {
		case <-closed:
			releaseFn()
			t.Fatal("Close ran while Embed was inside")
		case <-time.After(300 * time.Millisecond):
		}
		releaseFn()
		select {
		case <-o.returned:
		case <-time.After(2 * time.Second):
			t.Fatal("Embed did not return")
		}
		select {
		case yielded := <-paused:
			if !yielded {
				t.Fatal("Pause did not see the window end")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Pause did not return after Embed returned")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the window did not end")
		}
		select {
		case <-closed:
		default:
			t.Fatal("Close did not run after Embed returned")
		}
		if o.seq() != "embed-enter,embed-return,close" {
			t.Fatalf("events %s", o.seq())
		}
	})

	t.Run("join expiry skips close and ends the window", func(t *testing.T) {
		f := newIdleFixture(t, 2)
		old := idleEmbedJoin
		idleEmbedJoin = 150 * time.Millisecond
		t.Cleanup(func() { idleEmbedJoin = old })
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseFn)
		o := &orderEmbedder{
			Embedder: semantic.NewFakeEmbedder(256, semSynonyms),
			entered:  make(chan struct{}),
			release:  release,
			returned: make(chan struct{}),
		}
		var closed atomic.Bool
		openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return o, func() error {
				if o.inside.Load() {
					o.event("close-raced")
				}
				o.event("close")
				closed.Store(true)
				return nil
			}, nil
		}
		done := make(chan struct{})
		go func() { defer close(done); f.w.step(context.Background()) }()
		select {
		case <-o.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the posting embed did not start")
		}
		start := time.Now()
		_, yielded := f.w.gate.Pause(context.Background(), idleEmbedJoin+2*time.Second)
		elapsed := time.Since(start)
		if !yielded {
			t.Fatal("the window did not end when the join bound expired")
		}
		if elapsed > idleEmbedJoin+2*time.Second || elapsed+100*time.Millisecond < idleEmbedJoin {
			t.Fatalf("window ended in %v, join %v", elapsed, idleEmbedJoin)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("step did not return after the window ended")
		}
		if closed.Load() || strings.Contains(o.seq(), "close") {
			t.Fatalf("Close ran after the join bound: %s", o.seq())
		}
		if !strings.Contains(f.log.String(), "model close skipped") {
			t.Fatalf("skip line missing:\n%s", f.log.String())
		}
		if st := f.state_(t); st.Kind != idlePaused {
			t.Fatalf("window end did not record the pause: %+v", st)
		}
		releaseFn()
		select {
		case <-o.returned:
		case <-time.After(2 * time.Second):
			t.Fatal("Embed did not return after the window ended")
		}
		if closed.Load() || o.seq() != "embed-enter,embed-return" {
			t.Fatalf("Close after skip: closed %v events %s", closed.Load(), o.seq())
		}
	})
}

// Cancelling the parent, or pausing the gate, while Embed ignores the
// context ends the window within the shortened join plus a small slack.
// The test itself stays under idleYieldWait and never waits out the real
// Tessl call timeout.
func TestIdleWarmJoinBoundOnStop(t *testing.T) {
	started := time.Now()
	old := idleEmbedJoin
	idleEmbedJoin = 200 * time.Millisecond
	t.Cleanup(func() { idleEmbedJoin = old })

	hold := func(t *testing.T) (f *idleFixture, h *orderEmbedder, release func(), done <-chan struct{}, parent context.CancelFunc) {
		t.Helper()
		f = newIdleFixture(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		releaseCh := make(chan struct{})
		var once sync.Once
		release = func() { once.Do(func() { close(releaseCh) }) }
		t.Cleanup(release)
		h = &orderEmbedder{
			Embedder: semantic.NewFakeEmbedder(256, semSynonyms),
			entered:  make(chan struct{}),
			release:  releaseCh,
			returned: make(chan struct{}),
		}
		openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return h, func() error {
				if h.inside.Load() {
					h.event("close-raced")
				}
				h.event("close")
				return nil
			}, nil
		}
		ch := make(chan struct{})
		go func() { defer close(ch); f.w.step(ctx) }()
		select {
		case <-h.entered:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("Embed did not start")
		}
		t.Cleanup(func() {
			cancel()
			select {
			case <-ch:
			case <-time.After(idleEmbedJoin + 2*time.Second):
			}
		})
		return f, h, release, ch, cancel
	}

	assertBound := func(t *testing.T, elapsed time.Duration) {
		t.Helper()
		if elapsed > idleEmbedJoin+2*time.Second {
			t.Fatalf("join wait %v exceeded %v", elapsed, idleEmbedJoin+2*time.Second)
		}
		if elapsed+100*time.Millisecond < idleEmbedJoin {
			t.Fatalf("join wait %v returned before %v", elapsed, idleEmbedJoin)
		}
		if time.Since(started) >= idleYieldWait {
			t.Fatalf("test ran %v, idleYieldWait is %v", time.Since(started), idleYieldWait)
		}
	}

	t.Run("parent cancel", func(t *testing.T) {
		f, h, release, done, cancel := hold(t)
		start := time.Now()
		cancel()
		select {
		case <-done:
		case <-time.After(idleEmbedJoin + 2*time.Second):
			t.Fatal("step did not end within the join bound")
		}
		assertBound(t, time.Since(start))
		if strings.Contains(h.seq(), "close") {
			t.Fatalf("Close ran: %s", h.seq())
		}
		if !strings.Contains(f.log.String(), "model close skipped") {
			t.Fatalf("skip line missing:\n%s", f.log.String())
		}
		if st := f.state_(t); st.Kind != idleStopped {
			t.Fatalf("state %+v", st)
		}
		release()
		select {
		case <-h.returned:
		case <-time.After(2 * time.Second):
			t.Fatal("Embed did not return after the window ended")
		}
		if h.seq() != "embed-enter,embed-return" {
			t.Fatalf("events %s", h.seq())
		}
	})

	t.Run("pause", func(t *testing.T) {
		f, h, release, done, cancel := hold(t)
		defer cancel()
		start := time.Now()
		_, yielded := f.w.gate.Pause(context.Background(), idleEmbedJoin+2*time.Second)
		elapsed := time.Since(start)
		if !yielded {
			t.Fatal("Pause did not see the window end")
		}
		assertBound(t, elapsed)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("step did not return")
		}
		if strings.Contains(h.seq(), "close") || !strings.Contains(f.log.String(), "model close skipped") {
			t.Fatalf("events %s\n%s", h.seq(), f.log.String())
		}
		if st := f.state_(t); st.Kind != idlePaused {
			t.Fatalf("state %+v", st)
		}
		release()
	})
}

// idlePostingTeam reads the "team N" marker the idle fixture puts on a posting.
// Profile and anchor texts do not carry it.
func idlePostingTeam(texts []string) (int, bool) {
	const mark = "team "
	for _, tx := range texts {
		i := strings.Index(tx, mark)
		if i < 0 {
			continue
		}
		n, saw := 0, false
		for _, r := range tx[i+len(mark):] {
			if r < '0' || r > '9' {
				break
			}
			saw = true
			n = n*10 + int(r-'0')
		}
		if saw {
			return n, true
		}
	}
	return 0, false
}

// runIdleModes scores one posting per chunk. modes is per fixture posting:
// score, fail (zero score, not a deadline), deadline (block until the chunk
// context ends), unavail (ErrUnavailable wrapping a deadline), or
// baredeadline (DeadlineExceeded while the chunk clock is still running).
func runIdleModes(t *testing.T, modes []string) (idleState, time.Duration, *idleFixture) {
	t.Helper()
	f := newIdleFixture(t, len(modes))
	idleChunkDocs = 1
	// Warm order is not ledger order. Modes apply to successive posting
	// embeds, which is the chunk order the bad-count sees.
	var n atomic.Int32
	f.emb.hook = func(ctx context.Context, texts []string) ([][]float32, error, bool) {
		if _, ok := idlePostingTeam(texts); !ok {
			return nil, nil, false
		}
		i := int(n.Add(1)) - 1
		if i < 0 || i >= len(modes) {
			return nil, errors.New("unexpected posting"), true
		}
		switch modes[i] {
		case "score":
			return nil, nil, false
		case "fail":
			return nil, errors.New("embedding failed"), true
		case "deadline":
			<-ctx.Done()
			return nil, ctx.Err(), true
		case "unavail":
			return nil, fmt.Errorf("%w: %w", semantic.ErrUnavailable, context.DeadlineExceeded), true
		case "baredeadline":
			return nil, context.DeadlineExceeded, true
		default:
			return nil, errors.New("unknown mode " + modes[i]), true
		}
	}
	wait := f.w.step(context.Background())
	return f.state_(t), wait, f
}

// Deadline-only chunks do not count toward the bad-chunk backoff. A deadline
// between zero-score chunks does not reset that count. A scored chunk does.
// ErrUnavailable wins over a deadline carried in the same error.
func TestIdleWarmBadChunkAdversarial(t *testing.T) {
	old := idleChunkTime
	t.Cleanup(func() { idleChunkTime = old })

	t.Run("three posting deadlines do not back off", func(t *testing.T) {
		idleChunkTime = 2 * time.Second
		var postings atomic.Int32
		modes := []string{"deadline", "deadline", "deadline", "deadline", "deadline"}
		f := newIdleFixture(t, len(modes))
		idleChunkDocs = 1
		f.emb.hook = func(ctx context.Context, texts []string) ([][]float32, error, bool) {
			if _, ok := idlePostingTeam(texts); !ok {
				return nil, nil, false
			}
			postings.Add(1)
			<-ctx.Done()
			return nil, ctx.Err(), true
		}
		wait := f.w.step(context.Background())
		st := f.state_(t)
		if st.Kind != idleLimit || st.BackoffSec != 0 || !st.NextEligible.IsZero() || wait >= idleBackoffMin || st.Last.Chunks != len(modes) || st.Last.Scored != 0 || f.scored() != 0 || postings.Load() != int32(len(modes)) {
			t.Fatalf("wait %v scored %d posting embeds %d state %+v", wait, f.scored(), postings.Load(), st)
		}
		if strings.Contains(f.log.String(), "backing off") {
			t.Fatalf("log:\n%s", f.log.String())
		}
	})

	t.Run("deadline inside ensureProfile does not back off", func(t *testing.T) {
		idleChunkTime = 200 * time.Millisecond
		n := idleBadChunks + 2
		f := newIdleFixture(t, n)
		idleChunkDocs = 1
		f.emb.hook = func(ctx context.Context, texts []string) ([][]float32, error, bool) {
			if _, ok := idlePostingTeam(texts); ok {
				return nil, errors.New("posting embed ran after ensureProfile failed"), true
			}
			<-ctx.Done()
			return nil, ctx.Err(), true
		}
		wait := f.w.step(context.Background())
		st := f.state_(t)
		if st.Kind != idleLimit || st.BackoffSec != 0 || wait >= idleBackoffMin || st.Last.Chunks != n || st.Last.Scored != 0 || f.scored() != 0 {
			t.Fatalf("wait %v scored %d state %+v", wait, f.scored(), st)
		}
	})

	t.Run("unavailable wrapping deadline backs off", func(t *testing.T) {
		idleChunkTime = time.Minute
		st, wait, f := runIdleModes(t, []string{"unavail", "score", "score"})
		if st.Kind != idleBackoff || wait != idleBackoffMin || st.Last.Chunks != 1 || st.Last.Scored != 0 || f.scored() != 0 || !strings.Contains(st.Reason, "unavailable") {
			t.Fatalf("wait %v scored %d state %+v", wait, f.scored(), st)
		}
	})

	t.Run("three zero scores back off", func(t *testing.T) {
		idleChunkTime = time.Minute
		st, wait, f := runIdleModes(t, []string{"fail", "fail", "fail", "score"})
		if st.Kind != idleBackoff || wait != idleBackoffMin || st.Last.Chunks != idleBadChunks || f.scored() != 0 || !strings.Contains(st.Reason, "3 chunks in a row") {
			t.Fatalf("wait %v scored %d state %+v", wait, f.scored(), st)
		}
	})

	t.Run("bare deadline error still counts", func(t *testing.T) {
		idleChunkTime = time.Minute
		st, _, f := runIdleModes(t, []string{"baredeadline", "baredeadline", "baredeadline", "score"})
		if st.Kind != idleBackoff || st.Last.Chunks != idleBadChunks || f.scored() != 0 {
			t.Fatalf("scored %d state %+v", f.scored(), st)
		}
	})

	t.Run("deadline between zeros does not reset", func(t *testing.T) {
		idleChunkTime = 2 * time.Second
		st, wait, f := runIdleModes(t, []string{"fail", "deadline", "fail", "fail", "score"})
		if st.Kind != idleBackoff || wait != idleBackoffMin || st.Last.Chunks != 4 || f.scored() != 0 || !strings.Contains(st.Reason, "3 chunks in a row") {
			t.Fatalf("wait %v scored %d state %+v\n%s", wait, f.scored(), st, f.log.String())
		}
	})

	t.Run("scored chunk resets the bad count", func(t *testing.T) {
		idleChunkTime = time.Minute
		modes := []string{"fail", "fail", "score", "fail", "fail", "score", "score"}
		st, wait, f := runIdleModes(t, modes)
		if st.Kind != idleDone || st.BackoffSec != 0 || !st.NextEligible.IsZero() || wait >= idleBackoffMin || st.Last.Chunks != len(modes) || st.Last.Scored != 3 || f.scored() != 3 {
			t.Fatalf("wait %v scored %d state %+v\n%s", wait, f.scored(), st, f.log.String())
		}
		if strings.Contains(f.log.String(), "backing off") {
			t.Fatalf("log:\n%s", f.log.String())
		}
	})
}
