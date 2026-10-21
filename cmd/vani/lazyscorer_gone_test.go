package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// dyingClient answers like DevPrism whose process has exited.
type dyingClient struct {
	fakeScorerClient
	err error
}

func (d *dyingClient) Gap(context.Context, string, string) (resume.Gap, error) {
	return resume.Gap{}, d.err
}
func (d *dyingClient) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{}, d.err
}

// A DevPrism that exited is dropped and closed, and the next posting starts a
// fresh one instead of failing every run until the daemon restarts.
func TestLazyScorerReplacesAClientThatDied(t *testing.T) {
	starts := 0
	dead := &dyingClient{err: fmt.Errorf("devprism: %w", resume.ErrServerGone)}
	s := &lazyScorer{start: func(context.Context, resume.Config) (scorerClient, error) {
		starts++
		if starts == 1 {
			return dead, nil
		}
		return &fakeScorerClient{}, nil
	}}
	ctx := context.Background()
	if _, err := s.Gap(ctx, "jd", "p"); !errors.Is(err, resume.ErrServerGone) {
		t.Fatalf("first posting: %v, want the death reported", err)
	}
	if dead.closed != 1 {
		t.Errorf("the dead client was closed %d times, want 1", dead.closed)
	}
	if _, err := s.Gap(ctx, "jd", "p"); err != nil || starts != 2 {
		t.Fatalf("second posting: err %v, %d starts; want a fresh client that scores", err, starts)
	}
}

// A replacement that dies at once is not restarted for every posting: one
// restart per scorerRetry, then the window ends and it tries again.
func TestLazyScorerRestartsAtMostOncePerWindow(t *testing.T) {
	clock := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	starts := 0
	s := &lazyScorer{
		now: func() time.Time { return clock },
		start: func(context.Context, resume.Config) (scorerClient, error) {
			starts++
			return &dyingClient{err: resume.ErrServerGone}, nil
		},
	}
	ctx := context.Background()
	for range 5 {
		_, _ = s.Gap(ctx, "jd", "p")
	}
	if starts != 2 {
		t.Fatalf("%d starts across five postings, want the first and one restart", starts)
	}
	clock = clock.Add(scorerRetry)
	_, _ = s.Gap(ctx, "jd", "p")
	if starts != 3 {
		t.Fatalf("%d starts after the window, want 3", starts)
	}
}

// A call cut off by a cancelled run says nothing about the process.
func TestLazyScorerKeepsAClientWhenTheRunWasCancelled(t *testing.T) {
	c := &dyingClient{err: resume.ErrServerGone}
	s := &lazyScorer{start: func(context.Context, resume.Config) (scorerClient, error) { return c, nil }}
	if _, err := s.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Gap(ctx, "jd", "p"); !errors.Is(err, resume.ErrServerGone) {
		t.Fatalf("%v", err)
	}
	if c.closed != 0 {
		t.Errorf("a cancelled call closed the client (%d)", c.closed)
	}
	if got, err := s.get(context.Background()); err != nil || got != scorerClient(c) {
		t.Errorf("the client was replaced after a cancelled call: %v", err)
	}
}
