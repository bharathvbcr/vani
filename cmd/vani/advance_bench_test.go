package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/decide"
)

// Latencies measured on 2026-10-18/19 (docs/FOLLOWUPS.md), divided by
// benchScale so a benchmark run takes seconds: the d1-ultra triage call
// (about 1 s per posting with no shadow, less the notes pick), the d3-mini
// shadow on a parallel-1 llama-server (p50 3.2 s), and the d1-ultra notes
// pick (about 0.35 s). DevPrism scoring is not measured; it is a small
// assumed cost, serialised like resume.Client's one stdio channel.
const (
	benchScale    = 100
	benchPrimary  = 650 * time.Millisecond / benchScale
	benchShadow   = 3200 * time.Millisecond / benchScale
	benchNotes    = 350 * time.Millisecond / benchScale
	benchScore    = 20 * time.Millisecond / benchScale
	benchPostings = 96
)

// sleepBackend answers every slot with its first option after a fixed delay.
// With serial set it serves one call at a time, as llama-server with
// parallel 1 does.
type sleepBackend struct {
	name   string
	delay  time.Duration
	serial bool
	mu     sync.Mutex
}

func (b *sleepBackend) Name() string       { return b.name }
func (b *sleepBackend) Serves(string) bool { return true }
func (b *sleepBackend) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	if b.serial {
		b.mu.Lock()
		defer b.mu.Unlock()
	}
	if err := sleepCtx(ctx, b.delay); err != nil {
		return decide.Answer{}, err
	}
	a := decide.Answer{Backend: b.name, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type sleepScorer struct {
	delay time.Duration
	mu    sync.Mutex
}

func (s *sleepScorer) Gap(ctx context.Context, _, _ string) (resume.Gap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return resume.Gap{CoveragePct: 50}, sleepCtx(ctx, s.delay)
}

func (s *sleepScorer) Score(ctx context.Context, _, _ string) (resume.Score, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return resume.Score{}, sleepCtx(ctx, s.delay)
}

type sleepExplainer struct{ delay time.Duration }

func (e sleepExplainer) Explain(ctx context.Context, _ ledger.Record) (string, error) {
	return "Fit: bench.", sleepCtx(ctx, e.delay)
}

// eventLedger is an in-memory ledger.Ledger, safe for concurrent use, that
// keeps every appended event. The benchmark uses it instead of ledger.File
// because an fsync (a few ms on macOS) is not divided by benchScale: three
// events per posting would be about 1% of a real posting's time but would
// dominate a scaled one.
type eventLedger struct {
	mu     sync.Mutex
	recs   map[posting.Key]*ledger.Record
	order  []posting.Key
	events []ledger.Event
}

func newEventLedger() *eventLedger { return &eventLedger{recs: map[posting.Key]*ledger.Record{}} }

func (m *eventLedger) Append(ctx context.Context, e ledger.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[e.Key]
	if !ok {
		r = &ledger.Record{Key: e.Key, Annotations: map[string]string{}, FirstSeen: e.At}
		m.recs[e.Key] = r
		m.order = append(m.order, e.Key)
	}
	r.LastEvent = e.At
	r.Status = e.Status
	if e.Posting != nil {
		r.Posting = *e.Posting
	}
	for k, v := range e.Annotations {
		r.Annotations[k] = v
	}
	m.events = append(m.events, e)
	return nil
}

func (m *eventLedger) Get(k posting.Key) (ledger.Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[k]
	if !ok {
		return ledger.Record{}, false
	}
	c := *r
	c.Annotations = make(map[string]string, len(r.Annotations))
	for k, v := range r.Annotations {
		c.Annotations[k] = v
	}
	return c, true
}

func (m *eventLedger) All() []ledger.Record {
	m.mu.Lock()
	keys := append([]posting.Key(nil), m.order...)
	m.mu.Unlock()
	out := make([]ledger.Record, 0, len(keys))
	for _, k := range keys {
		r, _ := m.Get(k)
		out = append(out, r)
	}
	return out
}

func (m *eventLedger) Close() error { return nil }

func (m *eventLedger) Events() []ledger.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ledger.Event(nil), m.events...)
}

// benchLedger is a eventLedger holding n live postings.
func benchLedger(tb testing.TB, n int) *eventLedger {
	tb.Helper()
	l := newEventLedger()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for i := range n {
		k := posting.Key(fmt.Sprintf("gh:acme:%03d", i))
		e := ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Live, Reason: "listed",
			Posting: &posting.Posting{Key: k, Company: "Acme", Title: "Engineer", Description: "Build inference systems. Fully remote."}}
		if err := l.Append(context.Background(), e); err != nil {
			tb.Fatal(err)
		}
	}
	return l
}

// benchRunner is the run's Runner over fakes: a real Router, so the wait on
// the shadow in compareShadow is the real one. every is the shadow sample:
// 0 no shadow, 1 the ungated shadow on every posting (the run before
// decide.shadow_every), above 1 the gate the run wires, one call in flight
// as on a parallel-1 llama-server.
func benchRunner(l ledger.Ledger, every, slots int) (*pipeline.Runner, *shadowGate) {
	rt := decide.NewRouter(&sleepBackend{name: "d1-ultra", delay: benchPrimary})
	var g *shadowGate
	if every > 0 {
		rt.Shadow = &sleepBackend{name: "local-vllm-d3-mini-combined", delay: benchShadow, serial: true}
	}
	if every > 1 {
		g = gateShadow(rt, every, slots)
	}
	return &pipeline.Runner{
		Ledger:    l,
		Triager:   &jdtriage.Triager{D: rt},
		Scorer:    &sleepScorer{delay: benchScore},
		Explainer: sleepExplainer{delay: benchNotes},
	}, g
}

// BenchmarkAdvance times the advance phase of a run over benchPostings live
// postings and reports seconds per posting at the measured (unscaled)
// latencies, and for a gated shadow how many postings per run it read and
// how many sampled ones it skipped while busy.
func BenchmarkAdvance(b *testing.B) {
	for _, c := range []struct{ workers, every, slots int }{
		{1, 0, 1}, {1, 1, 1}, {1, config.DefaultShadowEvery, 2},
		{4, 0, 1}, {4, 1, 1}, {4, config.DefaultShadowEvery, 1}, {4, config.DefaultShadowEvery, 2}, {4, 4, 2},
	} {
		workers, every, slots := c.workers, c.every, c.slots
		b.Run(fmt.Sprintf("workers=%d/shadow_every=%d/slots=%d", workers, every, slots), func(b *testing.B) {
			var total time.Duration
			var asked, busy int64
			for range b.N {
				b.StopTimer()
				l := benchLedger(b, benchPostings)
				r, g := benchRunner(l, every, slots)
				b.StartTimer()
				start := time.Now()
				rep, err := advanceAll(context.Background(), l.All(), nil, benchPostings, workers, nil, r.Advance, func(string, ...any) {})
				total += time.Since(start)
				if err != nil || rep.Attempted != benchPostings || rep.Failed != 0 || rep.Unread != 0 {
					b.Fatalf("err %v report %+v", err, rep)
				}
				if g != nil {
					asked += g.asked.Load()
					busy += g.busy.Load()
				}
			}
			perPosting := total / time.Duration(b.N*benchPostings)
			b.ReportMetric((perPosting * benchScale).Seconds(), "real-s/posting")
			if every > 1 {
				b.ReportMetric(float64(asked)/float64(b.N), "shadowed/run")
				b.ReportMetric(float64(busy)/float64(b.N), "busy-skips/run")
			}
		})
	}
}
