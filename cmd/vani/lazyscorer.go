package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/resume"
)

func (w *wired) lazyScorer() *lazyScorer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scorer == nil {
		w.scorer = &lazyScorer{cfg: resume.Config{BinaryPath: w.cfg.DevPrism.Binary, PinnedToolsHash: w.cfg.DevPrism.ToolsHash}}
	}
	return w.scorer
}

// scorerRetry is how long a failed DevPrism start is remembered before the
// next use tries again: long enough that one run does not restart a broken
// binary for every posting, short enough that a daemon's next daily run
// recovers from a transient failure (a binary being replaced, a reboot race).
const scorerRetry = 10 * time.Minute

// scorerClient is what lazyScorer uses of a started DevPrism.
type scorerClient interface {
	Gap(ctx context.Context, jd, persona string) (resume.Gap, error)
	Score(ctx context.Context, jd, persona string) (resume.Score, error)
	Close() error
}

// lazyScorer starts DevPrism on first use, so commands that never score never
// spawn it. A failed start is remembered for scorerRetry: retrying a broken
// binary on every posting would only repeat the error, but a daemon must not
// keep a transient failure for the rest of its life.
type lazyScorer struct {
	cfg resume.Config
	// start and now are resume.Start and time.Now; tests replace them.
	start func(context.Context, resume.Config) (scorerClient, error)
	now   func() time.Time

	mu       sync.Mutex
	client   scorerClient
	err      error
	failedAt time.Time
	// gone is set when the client's process exited and it was dropped;
	// restartedAt is when the last such restart started, so a replacement that
	// dies too is not restarted again within scorerRetry.
	gone        bool
	restartedAt time.Time
}

func (s *lazyScorer) get(ctx context.Context) (scorerClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	if s.err != nil && now().Sub(s.failedAt) < scorerRetry {
		return nil, fmt.Errorf("devprism: %w", s.err)
	}
	if s.gone && !s.restartedAt.IsZero() && now().Sub(s.restartedAt) < scorerRetry {
		// A replacement died too, within scorerRetry of the restart that made it:
		// do not restart a binary that keeps dying for every posting.
		return nil, fmt.Errorf("devprism: %w", resume.ErrServerGone)
	}
	start := s.start
	if start == nil {
		start = func(ctx context.Context, cfg resume.Config) (scorerClient, error) {
			c, err := resume.Start(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	c, err := start(ctx, s.cfg)
	if err != nil {
		if ctx.Err() != nil {
			// A cancelled start says nothing about the binary; do not remember it.
			return nil, fmt.Errorf("devprism: %w", err)
		}
		s.err, s.failedAt = err, now()
		return nil, fmt.Errorf("devprism: %w", err)
	}
	s.client, s.err = c, nil
	if s.gone {
		s.gone, s.restartedAt = false, now()
	}
	return c, nil
}

func (s *lazyScorer) Gap(ctx context.Context, jd, persona string) (resume.Gap, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Gap{}, err
	}
	g, err := c.Gap(ctx, jd, persona)
	s.dropIfGone(ctx, c, err)
	return g, err
}

func (s *lazyScorer) Score(ctx context.Context, jd, persona string) (resume.Score, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Score{}, err
	}
	sc, err := c.Score(ctx, jd, persona)
	s.dropIfGone(ctx, c, err)
	return sc, err
}

// dropIfGone closes and forgets c when err says its DevPrism process exited
// (resume.ErrServerGone), so the next use starts a fresh one: a daemon never
// exits on its own, so a client kept after its process died would fail every
// later run. It restarts at most once per scorerRetry (see get). A cancelled
// ctx says nothing about the process and never counts, and only the client
// that failed is dropped, not a replacement another caller already started.
func (s *lazyScorer) dropIfGone(ctx context.Context, c scorerClient, err error) {
	if err == nil || ctx.Err() != nil || !errors.Is(err, resume.ErrServerGone) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != c {
		return
	}
	s.client, s.gone = nil, true
	_ = c.Close() // the process is already gone; Close only reaps it
}

func (s *lazyScorer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	c := s.client
	s.client = nil // a later use starts a fresh DevPrism
	return c.Close()
}
