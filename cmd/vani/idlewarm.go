package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// Idle warm: between runs the daemon scores the postings that have no current
// semantic score, so the daily run finds them done. It is the same Matcher.Warm
// a run calls, in small chunks, and it only ever adds scores (Rule 7).
//
// It never takes the ledger mutex: it reads the ledger's snapshot (All copies
// under the ledger's own lock) and writes only under the semantic directory.
// What it must not do is hold semantic.lock when a run starts, or the run's own
// warm would wait 30 s and give up (semantic.ErrWarmBusy). The idleGate is the
// in-process answer: a run pauses the gate before it starts, which cancels the
// window, and the pause returns only once the window has dropped the cache lock
// and closed its embedder.

// Tunables. Variables only so tests need not wait.
var (
	// idleCheckInterval is how often the backlog is looked at when nothing
	// nudges the loop; a finished run or inbox poll nudges it sooner.
	idleCheckInterval = 10 * time.Minute
	// idleNudgeGap is how soon after a nudge the loop looks again.
	idleNudgeGap = 30 * time.Second
	// idleChunkDocs and idleChunkTime bound one Warm call. The cache lock is
	// released between chunks, so a `vani semantic warm` gets in within one.
	idleChunkDocs = 16
	idleChunkTime = 2 * time.Minute
	// idleWindowMax bounds one window (one model load); the loop looks again
	// after it.
	idleWindowMax = 2 * time.Hour
	// idleLoadTimeout bounds loading the model.
	idleLoadTimeout = 2 * time.Minute
	// idleBackoffMin and idleBackoffMax bound the wait after the model is
	// unavailable or a window scored nothing: it doubles from the first to the
	// second.
	idleBackoffMin = 10 * time.Minute
	idleBackoffMax = 4 * time.Hour
	// idleBusyRetry is the wait after another warm held the cache lock.
	idleBusyRetry = 5 * time.Minute
	// idleYieldWait bounds how long a run waits for a window to stop: one
	// embedding call (Tessl may finish its batch on Metal after the context
	// ends) and a margin.
	idleYieldWait = semantic.DefaultTesslCallTimeout + 15*time.Second
	// idlePowerTimeout bounds the power probe.
	idlePowerTimeout = 5 * time.Second
	// idleBadChunks is how many chunks in a row may score nothing before the
	// window stops and backs off.
	idleBadChunks = 3
)

// idleWarmEnabled says whether the daemon runs the idle warm: on unless
// VANI_NO_IDLE_WARM is set. The config has no key for it (an unknown key is a
// config error for a binary that does not know it).
var idleWarmEnabled = func() bool { return os.Getenv("VANI_NO_IDLE_WARM") == "" }

// idleGate lets a run stop the idle warm and keeps it stopped until the run
// ends. The zero value is not usable; newIdleGate makes one. A nil *idleGate
// pauses nothing.
type idleGate struct {
	mu     sync.Mutex
	holds  int                // runs currently holding a pause
	cancel context.CancelFunc // of the open window, nil when none
	done   chan struct{}      // closed when the open window has ended
	wake   chan struct{}      // a nudge for the loop; capacity 1
}

func newIdleGate() *idleGate { return &idleGate{wake: make(chan struct{}, 1)} }

// paused reports whether a run holds the gate.
func (g *idleGate) paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holds > 0
}

// open starts a window: the returned context ends when a run pauses the gate or
// parent ends, and end must be called once the window holds neither the cache
// lock nor the embedder. ok is false while a run holds the gate (or a window is
// already open).
func (g *idleGate) open(parent context.Context) (ctx context.Context, end func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holds > 0 || g.done != nil || parent.Err() != nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	g.cancel, g.done = cancel, done
	return ctx, func() {
		cancel()
		g.mu.Lock()
		g.cancel, g.done = nil, nil
		g.mu.Unlock()
		close(done)
	}, true
}

// nudge asks the loop to look again soon. It never blocks.
func (g *idleGate) nudge() {
	if g == nil {
		return
	}
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// Pause stops the idle warm before a run starts and keeps it stopped until
// resume is called: it cancels the open window and waits, at most wait or until
// ctx ends, for the window to drop the cache lock and close its embedder.
// yielded is false when that wait ran out: the run then goes on, and its warm
// may find the cache busy. Resume is safe to call more than once, and nudges
// the loop.
func (g *idleGate) Pause(ctx context.Context, wait time.Duration) (resume func(), yielded bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	g.holds++
	cancel, done := g.cancel, g.done
	g.mu.Unlock()
	var once sync.Once
	resume = func() {
		once.Do(func() {
			g.mu.Lock()
			g.holds--
			g.mu.Unlock()
			g.nudge()
		})
	}
	if cancel == nil {
		return resume, true
	}
	cancel()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return resume, true
	case <-t.C:
	case <-ctx.Done():
	}
	return resume, false
}

// pauseIdle pauses the idle warm for a run and says on stderr when the window
// did not stop in time.
func (e *env) pauseIdle(ctx context.Context, g *idleGate) (resume func()) {
	resume, yielded := g.Pause(ctx, idleYieldWait)
	if !yielded && ctx.Err() == nil {
		fmt.Fprintf(e.stderr, "vani: idle warm did not stop within %s; this run's semantic warm may find the cache busy\n", idleYieldWait)
	}
	return resume
}

// Kinds of idleState. Each names why the loop last did what it did, so that
// "nothing to warm" and "could not tell whether it may" are never the same word.
const (
	idleWarming      = "warming"       // a window is running (or was when the daemon last wrote)
	idleDone         = "done"          // the last window ran to its end
	idleNothing      = "nothing"       // every browsable posting has a current score
	idlePowerUnknown = "power_unknown" // the power source could not be read: skipped
	idleBattery      = "battery"       // on battery or UPS power: skipped
	idlePaused       = "paused"        // a run holds the gate
	idleBackoff      = "backoff"       // the model was unavailable, or a window scored nothing
	idleStopped      = "stopped"       // the daemon was stopping
	idleBusy         = "busy"          // another warm held the cache lock
	idleLimit        = "window_limit"  // the window hit its time bound; the next check continues
	idleCheckFailed  = "check_failed"  // the backlog could not be counted
)

const (
	idleStateFile    = "semantic-idle.json"
	idleStateVersion = 1
	maxIdleState     = 16 << 10
)

// idleState is semantic/semantic-idle.json: what the idle warm last did. It is
// counts, times and reasons only.
type idleState struct {
	V            int        `json:"v"`
	Kind         string     `json:"kind"`
	Reason       string     `json:"reason,omitempty"`
	CheckedAt    time.Time  `json:"checked_at,omitzero"`    // the last backlog check
	Backlog      int        `json:"backlog"`                // postings without a current score at that check
	LastChunkAt  time.Time  `json:"last_chunk_at,omitzero"` // the last chunk that ran
	Last         idleCounts `json:"last,omitzero"`          // the last window
	Total        idleCounts `json:"total,omitzero"`         // every window since the file began
	NextEligible time.Time  `json:"next_eligible,omitzero"` // no window before this (backoff)
	BackoffSec   int64      `json:"backoff_sec,omitempty"`  // the current backoff step
	// VectorsRebuilt: the last window found the vector log unreadable and
	// started it again.
	VectorsRebuilt bool `json:"vectors_rebuilt,omitempty"`
}

type idleCounts struct {
	Chunks   int `json:"chunks,omitempty"`
	Scored   int `json:"scored,omitempty"`
	Rescored int `json:"rescored,omitempty"`
	Failed   int `json:"failed,omitempty"`
}

func (c *idleCounts) add(o idleCounts) {
	c.Chunks += o.Chunks
	c.Scored += o.Scored
	c.Rescored += o.Rescored
	c.Failed += o.Failed
}

func idleStatePath(stateDir string) string {
	return filepath.Join(semanticDir(stateDir), idleStateFile)
}

func writeIdleState(stateDir string, st idleState) error {
	st.V = idleStateVersion
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(raw) > maxIdleState {
		return fmt.Errorf("idle warm state exceeds %d bytes", maxIdleState)
	}
	if err := os.MkdirAll(semanticDir(stateDir), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(idleStatePath(stateDir), raw)
}

// readIdleState reads the state. A missing file is the zero state; one that is
// too big, truncated or from another version is an error, which a reader says
// rather than showing an empty state.
func readIdleState(stateDir string) (idleState, error) {
	f, err := os.Open(idleStatePath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return idleState{}, nil
	}
	if err != nil {
		return idleState{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxIdleState+1))
	if err != nil {
		return idleState{}, err
	}
	if len(raw) > maxIdleState {
		return idleState{}, fmt.Errorf("%s exceeds %d bytes", idleStateFile, maxIdleState)
	}
	var st idleState
	if err := json.Unmarshal(raw, &st); err != nil {
		return idleState{}, fmt.Errorf("%s: %w", idleStateFile, err)
	}
	if st.V != idleStateVersion {
		return idleState{}, fmt.Errorf("%s is version %d, want %d", idleStateFile, st.V, idleStateVersion)
	}
	return st, nil
}

// idleLines is the idle warm's state for `vani status` and `vani semantic
// status`: nothing when the file does not exist (the daemon never ran it).
func idleLines(stateDir string, at time.Time) []string {
	st, err := readIdleState(stateDir)
	if err != nil {
		return []string{"idle warm: state unreadable: " + oneLine(err.Error(), 200)}
	}
	if st.Kind == "" {
		return nil
	}
	lines := []string{fmt.Sprintf("idle warm: %s", st.Reason)}
	if lines[0] == "idle warm: " {
		lines[0] = "idle warm: " + st.Kind
	}
	if !st.CheckedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("  last check: %s, %d posting(s) without a current score", ago(at, st.CheckedAt), st.Backlog))
	}
	if !st.LastChunkAt.IsZero() {
		lines = append(lines, fmt.Sprintf("  last chunk: %s; last window scored %d, rescored %d, failed %d; all windows scored %d, rescored %d",
			ago(at, st.LastChunkAt), st.Last.Scored, st.Last.Rescored, st.Last.Failed, st.Total.Scored, st.Total.Rescored))
	}
	if st.NextEligible.After(at) {
		lines = append(lines, fmt.Sprintf("  next eligible: in %s (backoff %s)", st.NextEligible.Sub(at).Round(time.Second), time.Duration(st.BackoffSec)*time.Second))
	}
	if st.VectorsRebuilt {
		lines = append(lines, "  the last window found the vector log unreadable and started it again")
	}
	return lines
}

func ago(at, t time.Time) string {
	d := at.Sub(t)
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String() + " ago"
}

// powerState is where the Mac draws its power from.
type powerState int

const (
	powerUnknown powerState = iota
	powerAC
	powerBattery
)

// idlePowerProbe reads the power source; detail says why it is unknown. Tests
// replace it so none of them runs pmset.
var idlePowerProbe = pmsetPower

// cappedWriter keeps the first max bytes written to it and drops the rest.
type cappedWriter struct {
	b   []byte
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.b); room > 0 {
		w.b = append(w.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// pmsetPower runs `pmset -g batt` (5 s bound, 4 KiB of output) and parses its
// first line. Anything it cannot read is powerUnknown with the reason.
func pmsetPower(ctx context.Context) (powerState, string) {
	if runtime.GOOS != "darwin" {
		return powerUnknown, "power source is read with pmset, which is macOS only"
	}
	ctx, cancel := context.WithTimeout(ctx, idlePowerTimeout)
	defer cancel()
	out := &cappedWriter{max: 4096}
	cmd := exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "batt")
	cmd.Stdout = out
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return powerUnknown, "pmset: " + oneLine(err.Error(), 120)
	}
	return parsePower(string(out.b))
}

// parsePower reads the first line of `pmset -g batt`: "Now drawing from 'AC
// Power'" or "'Battery Power'". A UPS is treated as the battery it is.
func parsePower(s string) (powerState, string) {
	line, _, _ := strings.Cut(s, "\n")
	_, rest, ok := strings.Cut(line, "Now drawing from '")
	if !ok {
		return powerUnknown, "pmset output not recognised"
	}
	src, _, _ := strings.Cut(rest, "'")
	switch src {
	case "AC Power":
		return powerAC, ""
	case "Battery Power", "UPS Power":
		return powerBattery, ""
	}
	return powerUnknown, "pmset names a power source it does not know"
}

// idleWarmer is the idle warm loop's state.
type idleWarmer struct {
	stateDir string
	stderr   io.Writer
	led      ledger.Ledger // All() only: the loop never writes the ledger or takes its mutex
	cfg      func() config.Config
	gate     *idleGate

	lastKind string // the last kind logged, so a repeated reason is said once
}

// idleWarmLoop runs until ctx ends: look at the backlog, warm it if there is
// one and the Mac is on AC power, then wait for the next check (or a nudge,
// which is honoured after idleNudgeGap). A panic in a step is reported and the
// loop goes on.
func (e *env) idleWarmLoop(ctx context.Context, w *idleWarmer) {
	wait := idleNudgeGap // let the daemon's start settle before the first look
	for {
		if !sleepOrNudge(ctx, w.gate.wake, wait) {
			return
		}
		wait = w.safeStep(ctx)
	}
}

// sleepOrNudge waits d, or idleNudgeGap (whichever is shorter) once nudged. It
// is false when ctx ended first.
func sleepOrNudge(ctx context.Context, wake <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case <-wake:
	}
	// A run or an inbox poll just ended: look soon, not at once.
	t.Reset(min(d, idleNudgeGap))
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (w *idleWarmer) safeStep(ctx context.Context) (wait time.Duration) {
	defer func() {
		if p := recover(); p != nil {
			writePanic(w.stderr, "vani: idle warm panicked: ", p)
			wait = idleCheckInterval
		}
	}()
	return w.step(ctx)
}

// step is one look at the backlog and, when there is something to do and it may,
// one window. It returns how long to wait before the next.
func (w *idleWarmer) step(ctx context.Context) time.Duration {
	cfg := w.cfg()
	if !cfg.SemanticOn() || w.led == nil || ctx.Err() != nil {
		return idleCheckInterval
	}
	at := now()
	st, rerr := readIdleState(w.stateDir)
	if rerr != nil {
		// Start the file again, and say so in the log: a state that cannot be
		// read must not read as one that says nothing.
		fmt.Fprintf(w.stderr, "vani: idle warm: %s; starting its state again\n", oneLine(rerr.Error(), 200))
		st = idleState{}
	}
	if rem := st.NextEligible.Sub(at); rem > 0 {
		return min(rem, idleBackoffMax) // a clock set back must not park it for good
	}
	if w.gate.paused() {
		return idleCheckInterval // the run's resume nudges the loop
	}
	docs, ranks, prof, err := w.backlog(ctx, cfg)
	if ctx.Err() != nil {
		return idleCheckInterval
	}
	st.CheckedAt = at
	if err != nil {
		w.record(&st, idleCheckFailed, "backlog could not be counted: "+oneLine(err.Error(), 200))
		return idleCheckInterval
	}
	st.Backlog = len(docs)
	if len(docs) == 0 {
		w.record(&st, idleNothing, "nothing to warm: every browsable posting has a current score")
		return idleCheckInterval
	}
	// Power is read only when there is something to do, so an idle Mac runs no
	// process; and "unknown" is its own answer, never "nothing to do".
	if ok, kind, reason := idlePower(ctx); !ok {
		w.record(&st, kind, reason)
		return idleCheckInterval
	}
	return w.window(ctx, cfg, prof, docs, ranks, &st)
}

// idlePower reads the power source: ok when on AC power. Otherwise it says which
// kind of "no" it is.
func idlePower(ctx context.Context) (ok bool, kind, reason string) {
	ps, detail := idlePowerProbe(ctx)
	switch ps {
	case powerAC:
		return true, "", ""
	case powerBattery:
		return false, idleBattery, "on battery power: not warming"
	}
	return false, idlePowerUnknown, "power state unknown (" + oneLine(detail, 120) + "): not warming"
}

// backlog is the browsable postings that have no current score, the best Score
// first (warmOrder), with the live set for the vector log (every browsable
// posting by its Score, the one warmOrder sorted by) and the profile.
// Postings with no text to embed are left out: no warm can score them, and
// counting them would keep a window opening for nothing. It loads no model.
func (w *idleWarmer) backlog(ctx context.Context, cfg config.Config) (docs []semantic.Doc, ranks map[string]float64, prof semantic.Profile, err error) {
	mk, merr := marks.ReadMarks(marks.Dir(w.stateDir)) // unreadable: the kept postings, and no live set
	all, allRanks := warmRanked(w.led.All(), mk, cfg)
	if merr == nil {
		ranks = allRanks
	}
	pctx, cancel := context.WithTimeout(ctx, eligibleKBTimeout)
	defer cancel()
	prof, err = semanticProfile(pctx, cfg)
	if err != nil {
		return nil, nil, prof, err
	}
	m, err := semantic.OpenLookupFor(prof, semanticDir(w.stateDir), semanticModelPrefix(cfg))
	switch {
	case errors.Is(err, semantic.ErrModelMismatch), errors.Is(err, semantic.ErrCache):
		m = nil // nothing cached is usable: all of it is backlog, and the warm replaces the file
	case err != nil:
		return nil, nil, prof, err
	}
	for _, d := range all {
		if m != nil {
			if _, ok := m.Lookup(d.Key, semantic.TextHash(d.Title, d.Description)); ok {
				continue
			}
		}
		if len(semantic.ChunkPosting(d.Title, d.Description)) == 0 {
			continue
		}
		docs = append(docs, d)
	}
	return docs, ranks, prof, nil
}

// window loads the model once and warms docs in chunks until they are done, a
// run pauses the gate, the Mac leaves AC power, the model is unavailable or the
// window's time is up. It closes the embedder, and only then ends the window,
// so a run that paused the gate finds the model and the lock both free.
func (w *idleWarmer) window(ctx context.Context, cfg config.Config, prof semantic.Profile, docs []semantic.Doc, ranks map[string]float64, st *idleState) time.Duration {
	wctx, end, ok := w.gate.open(ctx)
	if !ok {
		if ctx.Err() == nil {
			w.record(st, idlePaused, "paused for a run")
		}
		return idleCheckInterval
	}
	defer end()
	wctx, wcancel := context.WithTimeout(wctx, idleWindowMax)
	defer wcancel()

	lctx, lcancel := context.WithTimeout(wctx, idleLoadTimeout)
	emb, closeEmb, err := openEmbedder(lctx, cfg)
	lcancel()
	if err != nil {
		return w.stopped(ctx, wctx, st, "the embedding model could not be loaded: "+oneLine(err.Error(), 200))
	}
	defer func() { _ = closeEmb() }()
	m, _, err := openScoreCache(emb, prof, semanticDir(w.stateDir))
	if err != nil {
		return w.stopped(ctx, wctx, st, "the score cache could not be opened: "+oneLine(err.Error(), 200))
	}
	m.SetVectorPriority(ranks)
	fmt.Fprintf(w.stderr, "vani: idle warm: %d posting(s) without a current score; warming in chunks of %d\n", len(docs), idleChunkDocs)

	var win idleCounts
	rebuilt := false
	bad := 0
	// commit folds the window's counts into the state, once, whichever way the
	// window ends.
	commit := func() {
		st.Last = win
		st.Total.add(win)
		st.VectorsRebuilt = rebuilt
		if win.Chunks > 0 {
			fmt.Fprintf(w.stderr, "vani: idle warm: scored %d, rescored %d, failed %d in %d chunk(s)\n", win.Scored, win.Rescored, win.Failed, win.Chunks)
		}
	}
	for i := 0; i < len(docs); i += idleChunkDocs {
		if wctx.Err() != nil {
			break
		}
		if i > 0 { // the first chunk follows the check made a moment ago
			if ok, kind, reason := idlePower(wctx); !ok && wctx.Err() == nil {
				commit()
				w.record(st, kind, reason)
				return idleCheckInterval
			}
		}
		chunk := docs[i:min(i+idleChunkDocs, len(docs))]
		cctx, ccancel := context.WithTimeout(wctx, idleChunkTime)
		r, werr := m.Warm(cctx, chunk, len(chunk))
		ccancel()
		win.Chunks++
		win.Scored += r.Scored
		win.Rescored += r.Rescored
		win.Failed += r.Failed
		rebuilt = rebuilt || r.VectorsRebuilt
		st.LastChunkAt = now()
		st.Kind, st.Reason = idleWarming, fmt.Sprintf("warming: %d of %d posting(s) tried", min(i+len(chunk), len(docs)), len(docs))
		cur := *st
		cur.Last = win
		cur.Total.add(win)
		cur.VectorsRebuilt = rebuilt
		w.persist(&cur)
		switch {
		case wctx.Err() != nil:
			// A run paused the gate, the daemon is stopping, or the window's time is
			// up. Warm saved what it scored before the context ended.
		case errors.Is(werr, semantic.ErrWarmBusy):
			commit()
			w.record(st, idleBusy, "another warm holds the semantic cache; will retry")
			return idleBusyRetry
		case werr != nil && (r.Unavailable || errors.Is(werr, semantic.ErrUnavailable)):
			commit()
			return w.backoff(st, "the embedding model is unavailable: "+oneLine(werr.Error(), 160))
		case r.Scored+r.Rescored == 0:
			if bad++; bad >= idleBadChunks {
				commit()
				return w.backoff(st, fmt.Sprintf("%d chunks in a row scored nothing (%d failed)", bad, win.Failed))
			}
		default:
			bad = 0
			st.BackoffSec, st.NextEligible = 0, time.Time{} // progress ends a backoff
		}
	}
	commit()
	if wctx.Err() != nil {
		return w.stopped(ctx, wctx, st, "")
	}
	if win.Scored+win.Rescored == 0 {
		return w.backoff(st, fmt.Sprintf("the window scored nothing (%d failed)", win.Failed))
	}
	w.record(st, idleDone, fmt.Sprintf("caught up for now: the last window scored %d, rescored %d, failed %d", win.Scored, win.Rescored, win.Failed))
	return idleCheckInterval
}

// stopped says why a window ended without finishing, by whose context ended:
// the daemon's (shutting down), a run's pause, the window's time, or neither
// (the model or cache failed: a backoff). why is the failure and is used only
// for the last.
func (w *idleWarmer) stopped(ctx, wctx context.Context, st *idleState, why string) time.Duration {
	switch {
	case ctx.Err() != nil:
		w.record(st, idleStopped, "stopped with the daemon")
		return idleCheckInterval
	case w.gate.paused():
		w.record(st, idlePaused, "paused for a run")
		return idleCheckInterval
	case wctx.Err() != nil:
		w.record(st, idleLimit, fmt.Sprintf("the window's %s limit ended it; the next check continues", idleWindowMax))
		return idleNudgeGap
	}
	return w.backoff(st, why)
}

// backoff doubles the wait before the next window (idleBackoffMin up to
// idleBackoffMax), persists it, and returns it.
func (w *idleWarmer) backoff(st *idleState, why string) time.Duration {
	d := idleBackoffMin
	if prev := time.Duration(st.BackoffSec) * time.Second; prev > 0 {
		d = prev * 2
	}
	d = min(d, idleBackoffMax)
	st.BackoffSec = int64(d / time.Second)
	st.NextEligible = now().Add(d)
	w.record(st, idleBackoff, fmt.Sprintf("backing off %s: %s", d, why))
	return d
}

// record sets the state's kind and reason, writes it, and logs the reason when
// the kind changed since the last one logged.
func (w *idleWarmer) record(st *idleState, kind, reason string) {
	st.Kind, st.Reason = kind, reason
	w.persist(st)
	if kind != w.lastKind && reason != "" {
		fmt.Fprintf(w.stderr, "vani: idle warm: %s\n", reason)
	}
	w.lastKind = kind
}

// persist writes st. A failed write is said on stderr; the warm goes on.
func (w *idleWarmer) persist(st *idleState) {
	if err := writeIdleState(w.stateDir, *st); err != nil {
		fmt.Fprintf(w.stderr, "vani: idle warm: could not write its state: %s\n", oneLine(err.Error(), 200))
	}
}
