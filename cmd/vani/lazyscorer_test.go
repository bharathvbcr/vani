package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/resume"
)

type fakeScorerClient struct{ closed int }

func (f *fakeScorerClient) Gap(context.Context, string, string) (resume.Gap, error) {
	return resume.Gap{}, nil
}
func (f *fakeScorerClient) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{}, nil
}
func (f *fakeScorerClient) Close() error { f.closed++; return nil }

// A failed DevPrism start is remembered for scorerRetry, then tried again: a
// daemon recovers on a later run instead of scoring nothing for the rest of
// its life (the old sync.Once kept the first error forever).
func TestLazyScorerRetriesAFailedStart(t *testing.T) {
	clock := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	starts := 0
	client := &fakeScorerClient{}
	s := &lazyScorer{
		now: func() time.Time { return clock },
		start: func(context.Context, resume.Config) (scorerClient, error) {
			starts++
			if starts == 1 {
				return nil, errors.New("binary is being replaced")
			}
			return client, nil
		},
	}
	ctx := context.Background()
	if _, err := s.Score(ctx, "jd", "p"); err == nil || !strings.Contains(err.Error(), "devprism: binary is being replaced") {
		t.Fatalf("first use: %v", err)
	}
	// The same run, minutes later: the error is remembered, nothing restarts.
	clock = clock.Add(scorerRetry - time.Second)
	if _, err := s.Gap(ctx, "jd", "p"); err == nil || starts != 1 {
		t.Fatalf("within the window: err %v, %d starts", err, starts)
	}
	// The next run: it tries again and works, and keeps the client.
	clock = clock.Add(24 * time.Hour)
	if _, err := s.Score(ctx, "jd", "p"); err != nil || starts != 2 {
		t.Fatalf("after the window: err %v, %d starts", err, starts)
	}
	if _, err := s.Score(ctx, "jd", "p"); err != nil || starts != 2 {
		t.Fatalf("reuse: err %v, %d starts", err, starts)
	}
	if err := s.Close(); err != nil || client.closed != 1 {
		t.Fatalf("close: %v, closed %d", err, client.closed)
	}
	if err := s.Close(); err != nil || client.closed != 1 {
		t.Fatalf("second close: %v, closed %d", err, client.closed)
	}
}

// A start cut off by a cancelled run says nothing about the binary, so the
// next use tries at once.
func TestLazyScorerDoesNotRememberACancelledStart(t *testing.T) {
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
	if _, err := s.Score(ctx, "jd", "p"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := s.Score(context.Background(), "jd", "p"); err != nil || starts != 2 {
		t.Fatalf("after cancel: err %v, %d starts", err, starts)
	}
}

// Concurrent first uses start DevPrism once.
func TestLazyScorerStartsOnceUnderConcurrency(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	s := &lazyScorer{start: func(context.Context, resume.Config) (scorerClient, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return &fakeScorerClient{}, nil
	}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Score(context.Background(), "jd", "p"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if starts != 1 {
		t.Fatalf("%d starts, want 1", starts)
	}
}
