package main

import (
	"context"
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
	return c, nil
}

func (s *lazyScorer) Gap(ctx context.Context, jd, persona string) (resume.Gap, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Gap{}, err
	}
	return c.Gap(ctx, jd, persona)
}

func (s *lazyScorer) Score(ctx context.Context, jd, persona string) (resume.Score, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Score{}, err
	}
	return c.Score(ctx, jd, persona)
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
