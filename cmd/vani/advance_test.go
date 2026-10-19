package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func liveRecs(n int) []ledger.Record {
	out := make([]ledger.Record, n)
	for i := range out {
		out[i] = ledger.Record{Key: posting.Key(fmt.Sprintf("gh:acme:%03d", i)), Status: posting.Live}
	}
	return out
}

func unread(key posting.Key) error { return fmt.Errorf("%w: %s", pipeline.ErrTriageUnavailable, key) }

// A run in which no backend can read anything (a refused key, an outage) stops
// after maxConsecutiveUnread postings and fails, so `run-once || break` stops a
// loop and no empty digest is published; it does not spend the whole cap.
func TestAdvanceStopsWhenNoBackendCanRead(t *testing.T) {
	var warn strings.Builder
	calls := 0
	err := advanceAll(context.Background(), liveRecs(300), func(_ context.Context, k posting.Key) error {
		calls++
		return unread(k)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), fmt.Sprintf("%d postings in a row", maxConsecutiveUnread)) {
		t.Fatalf("err = %v", err)
	}
	if calls != maxConsecutiveUnread {
		t.Fatalf("%d postings asked, want %d", calls, maxConsecutiveUnread)
	}
	// Three examples, not one line per posting.
	if n := strings.Count(warn.String(), "no decision backend could read"); n > 3 {
		t.Fatalf("%d per-posting lines:\n%s", n, warn.String())
	}
}

// Fewer postings than the breaker: still a failure when none could be read.
func TestAdvanceFailsWhenEveryPostingWasUnread(t *testing.T) {
	err := advanceAll(context.Background(), liveRecs(4), func(_ context.Context, k posting.Key) error { return unread(k) },
		func(string, ...any) {})
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), "4 of 4") {
		t.Fatalf("err = %v", err)
	}
}

// Occasional unread postings in a working run are a summary line, not a
// failure, and a success resets the breaker.
func TestAdvanceToleratesScatteredUnread(t *testing.T) {
	var warn strings.Builder
	i := 0
	err := advanceAll(context.Background(), liveRecs(100), func(_ context.Context, k posting.Key) error {
		i++
		if i%(maxConsecutiveUnread) == 0 { // one success every 20th posting
			return nil
		}
		return unread(k)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(warn.String(), "95 posting(s) could not be read by any decision backend") {
		t.Fatalf("no summary:\n%s", warn.String())
	}
}

// Other per-posting errors are still reported one by one, and the cap holds.
func TestAdvanceReportsOtherErrorsAndCaps(t *testing.T) {
	var warn strings.Builder
	calls := 0
	recs := liveRecs(maxAdvancePerRun + 10)
	recs[0].Status = posting.Dead
	err := advanceAll(context.Background(), recs, func(_ context.Context, k posting.Key) error {
		calls++
		if calls == 2 {
			return errors.New("score: devprism down")
		}
		return nil
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil || calls != maxAdvancePerRun {
		t.Fatalf("err %v calls %d", err, calls)
	}
	for _, want := range []string{"advance gh:acme:002: score: devprism down", "advance cap 300 reached"} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("warnings lack %q:\n%s", want, warn.String())
		}
	}
}

// The same stage failure on many postings is one line plus a count, not one
// line per posting: the posting key and the provider request id are not part
// of the cause.
func TestAdvanceCollapsesRepeatedErrors(t *testing.T) {
	var warn strings.Builder
	i := 0
	err := advanceAll(context.Background(), liveRecs(50), func(_ context.Context, k posting.Key) error {
		i++
		return fmt.Errorf("pipeline: explain %s: anthropic: http 400 (request req_%04d): not scoped to a workspace", k, i)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	out := warn.String()
	if n := strings.Count(out, "not scoped to a workspace"); n != 1 {
		t.Fatalf("%d lines name the cause, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "49 more posting(s) failed the same way as gh:acme:000") {
		t.Fatalf("no summary count:\n%s", out)
	}
}

// Different causes are each reported.
func TestAdvanceReportsEachDistinctCause(t *testing.T) {
	var warn strings.Builder
	i := 0
	err := advanceAll(context.Background(), liveRecs(6), func(_ context.Context, k posting.Key) error {
		i++
		if i%2 == 0 {
			return fmt.Errorf("pipeline: score %s: devprism down", k)
		}
		return fmt.Errorf("pipeline: explain %s: refused", k)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"devprism down", "refused", "2 more posting(s) failed the same way"} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("warnings lack %q:\n%s", want, warn.String())
		}
	}
}

// A provider error that differs only in its request id is one error.
func TestBackendErrorKeyIgnoresRequestIDs(t *testing.T) {
	a := `llmbackend: anthropic: http 400 (request req_011CgAs24ruQ): {"type":"error","request_id":"req_011CgAs24ruQ"}`
	b := `llmbackend: anthropic: http 400 (request req_011CgAs2BYEs): {"type":"error","request_id":"req_011CgAs2BYEs"}`
	if errorKey(a) != errorKey(b) {
		t.Fatalf("keys differ:\n%s\n%s", errorKey(a), errorKey(b))
	}
	if errorKey(a) == errorKey(`llmbackend: anthropic: http 401 (request req_X)`) {
		t.Fatal("different errors share a key")
	}
}
