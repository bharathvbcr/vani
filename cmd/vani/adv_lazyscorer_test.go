package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// gateClient blocks every Gap until release is closed, then reports its process
// gone; it counts Close calls atomically.
type gateClient struct {
	release chan struct{}
	arrived chan struct{}
	err     error
	closed  atomic.Int32
}

func (g *gateClient) Gap(context.Context, string, string) (resume.Gap, error) {
	g.arrived <- struct{}{}
	<-g.release
	return resume.Gap{}, g.err
}
func (g *gateClient) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{}, g.err
}
func (g *gateClient) Close() error { g.closed.Add(1); return nil }

// ErrServerGone buried under several wrappings and a join is still a death.
func TestAdvLazyScorerGoneWrappedDeep(t *testing.T) {
	for name, err := range map[string]error{
		"deep":    fmt.Errorf("a: %w", fmt.Errorf("b: %w", fmt.Errorf("c: %w", resume.ErrServerGone))),
		"joined":  errors.Join(errors.New("x"), fmt.Errorf("y: %w", resume.ErrServerGone)),
		"pointer": fmt.Errorf("%w", errors.Join(fmt.Errorf("%w", resume.ErrServerGone))),
	} {
		t.Run(name, func(t *testing.T) {
			starts := 0
			dead := &dyingClient{err: err}
			s := &lazyScorer{start: func(context.Context, resume.Config) (scorerClient, error) {
				starts++
				if starts == 1 {
					return dead, nil
				}
				return &fakeScorerClient{}, nil
			}}
			_, _ = s.Score(context.Background(), "jd", "p")
			if dead.closed != 1 {
				t.Fatalf("closed %d", dead.closed)
			}
			if _, err := s.Gap(context.Background(), "jd", "p"); err != nil || starts != 2 {
				t.Fatalf("err %v starts %d", err, starts)
			}
		})
	}
}

// Two callers hold the same dead client and both see ErrServerGone while a
// third has already started the replacement: exactly one drop, one close, one
// restart, and the replacement is never dropped by a stale failure.
func TestAdvLazyScorerConcurrentGoneOneRestart(t *testing.T) {
	for rep := 0; rep < 50; rep++ {
		c1 := &gateClient{release: make(chan struct{}), arrived: make(chan struct{}, 8), err: fmt.Errorf("devprism: %w", resume.ErrServerGone)}
		c2 := &fakeScorerClient{}
		var starts atomic.Int32
		clock := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
		var cmu sync.Mutex
		s := &lazyScorer{
			now: func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock },
			start: func(context.Context, resume.Config) (scorerClient, error) {
				if starts.Add(1) == 1 {
					return c1, nil
				}
				return c2, nil
			},
		}
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = s.Gap(context.Background(), "jd", "p") }()
		}
		<-c1.arrived
		<-c1.arrived
		close(c1.release)
		wg.Wait()
		if c1.closed.Load() != 1 {
			t.Fatalf("rep %d: dead client closed %d times, want 1", rep, c1.closed.Load())
		}
		// Many callers now: one restart.
		var wg2 sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg2.Add(1)
			go func() { defer wg2.Done(); _, _ = s.Score(context.Background(), "jd", "p") }()
		}
		wg2.Wait()
		if starts.Load() != 2 {
			t.Fatalf("rep %d: %d starts, want the first and one restart", rep, starts.Load())
		}
		if c2.closed != 0 {
			t.Fatalf("rep %d: the replacement was closed", rep)
		}
		// A straggler holding the dead client reports gone after the replacement
		// is up: it must not drop the replacement.
		s.dropIfGone(context.Background(), c1, resume.ErrServerGone)
		if got, err := s.get(context.Background()); err != nil || got != scorerClient(c2) {
			t.Fatalf("rep %d: the replacement was dropped by a stale failure (%v)", rep, err)
		}
	}
}

// A persistently dying binary and a persistently failing start never loop: the
// number of starts is bounded by the number of scorerRetry windows, however
// many postings are tried.
func TestAdvLazyScorerPersistentFailureDoesNotBusyLoop(t *testing.T) {
	clock := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	for name, start := range map[string]func(*int) func(context.Context, resume.Config) (scorerClient, error){
		"dies": func(n *int) func(context.Context, resume.Config) (scorerClient, error) {
			return func(context.Context, resume.Config) (scorerClient, error) {
				*n++
				return &dyingClient{err: resume.ErrServerGone}, nil
			}
		},
		"start fails": func(n *int) func(context.Context, resume.Config) (scorerClient, error) {
			return func(context.Context, resume.Config) (scorerClient, error) {
				*n++
				return nil, errors.New("exec format error")
			}
		},
		"dies after a failed restart": func(n *int) func(context.Context, resume.Config) (scorerClient, error) {
			return func(context.Context, resume.Config) (scorerClient, error) {
				*n++
				if *n == 2 {
					return nil, errors.New("binary being replaced")
				}
				return &dyingClient{err: resume.ErrServerGone}, nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			now := clock
			starts := 0
			s := &lazyScorer{now: func() time.Time { return now }, start: start(&starts)}
			for i := 0; i < 2000; i++ {
				_, _ = s.Gap(context.Background(), "jd", "p")
			}
			if starts > 2 {
				t.Fatalf("%d starts inside one window", starts)
			}
			for w := 0; w < 3; w++ {
				now = now.Add(scorerRetry)
				for i := 0; i < 500; i++ {
					_, _ = s.Score(context.Background(), "jd", "p")
				}
			}
			if starts > 2+3*2 {
				t.Fatalf("%d starts over four windows", starts)
			}
		})
	}
}

// A start cut off by a cancelled run is not remembered: the next run tries again.
func TestAdvLazyScorerCancelledStartNotRemembered(t *testing.T) {
	starts := 0
	s := &lazyScorer{start: func(ctx context.Context, _ resume.Config) (scorerClient, error) {
		starts++
		if starts == 1 {
			return nil, ctx.Err()
		}
		return &fakeScorerClient{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Gap(ctx, "jd", "p"); err == nil {
		t.Fatal("no error from a cancelled start")
	}
	if _, err := s.Gap(context.Background(), "jd", "p"); err != nil || starts != 2 {
		t.Fatalf("err %v starts %d; the cancelled start was remembered", err, starts)
	}
}

// Close during a death and after one is safe and closes each client once.
func TestAdvLazyScorerCloseAfterGone(t *testing.T) {
	dead := &dyingClient{err: resume.ErrServerGone}
	s := &lazyScorer{start: func(context.Context, resume.Config) (scorerClient, error) { return dead, nil }}
	_, _ = s.Gap(context.Background(), "jd", "p")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if dead.closed != 1 {
		t.Fatalf("dead client closed %d times across the drop and Close", dead.closed)
	}
}
