package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
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
	_, err := advanceAll(context.Background(), liveRecs(300), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
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
	_, err := advanceAll(context.Background(), liveRecs(4), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error { return unread(k) },
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
	_, err := advanceAll(context.Background(), liveRecs(100), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
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
	recs := liveRecs(config.DefaultAdvancePerRun + 10)
	recs[0].Status = posting.Dead
	_, err := advanceAll(context.Background(), recs, nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		calls++
		if calls == 2 {
			return errors.New("score: devprism down")
		}
		return nil
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil || calls != config.DefaultAdvancePerRun {
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
// of the cause. (Every tenth posting advances, so this is a working run with a
// recurring failure, not an outage; the outage cases are below.)
func TestAdvanceCollapsesRepeatedErrors(t *testing.T) {
	var warn strings.Builder
	i := 0
	_, err := advanceAll(context.Background(), liveRecs(50), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		i++
		if i%10 == 0 {
			return nil
		}
		return fmt.Errorf("pipeline: explain %s: anthropic: http 400 (request req_%04d): not scoped to a workspace", k, i)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	out := warn.String()
	if n := strings.Count(out, "not scoped to a workspace"); n != 1 {
		t.Fatalf("%d lines name the cause, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "44 more posting(s) failed the same way as gh:acme:000") {
		t.Fatalf("no summary count:\n%s", out)
	}
}

// Different causes are each reported.
func TestAdvanceReportsEachDistinctCause(t *testing.T) {
	var warn strings.Builder
	i := 0
	_, err := advanceAll(context.Background(), liveRecs(7), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		i++
		switch {
		case i == 7:
			return nil
		case i%2 == 0:
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

// Title signals spend the cap on the titles the track wants: matches go
// first (more signals first), unmatched keep ledger order, excluded go last,
// and ineligible postings are never asked. Nothing is dropped: an excluded
// posting past the cap waits, and is advanced once the queue is short.
func TestAdvanceOrdersByTitleSignals(t *testing.T) {
	w := &wired{cfg: config.Config{TitleSignals: config.TitleSignals{
		Include: []string{"LLM", "Inference"}, Exclude: []string{"Sales"},
	}}}
	rank := w.titleRank()
	recs := liveRecs(config.DefaultAdvancePerRun + 5)
	for i := range recs {
		recs[i].Posting.Title = "Software Engineer"
	}
	recs[0].Posting.Title = "LLM Sales Engineer"
	recs[1].Posting.Title = "LLM Inference Engineer" // a dead one never runs, whatever its title
	recs[1].Status = posting.Dead
	recs[200].Posting.Title = "Inference Engineer"
	recs[300].Posting.Title = "LLM Inference Engineer"
	recs[304].Posting.Title = "Inference Runtime Engineer"
	var got []posting.Key
	_, err := advanceAll(context.Background(), recs, rank, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		got = append(got, k)
		return nil
	}, func(string, ...any) {})
	if err != nil || len(got) != config.DefaultAdvancePerRun {
		t.Fatalf("err %v, %d advanced", err, len(got))
	}
	want := []posting.Key{recs[300].Key, recs[200].Key, recs[304].Key, recs[2].Key, recs[3].Key}
	if !slices.Equal(got[:5], want) {
		t.Fatalf("first five %v, want %v", got[:5], want)
	}
	if slices.Contains(got, recs[0].Key) || slices.Contains(got, recs[1].Key) {
		t.Fatal("excluded posting ran before the cap, or a dead one ran")
	}
	// Unmatched postings keep ledger order behind the matches.
	if got[5] != recs[4].Key || got[len(got)-1] != recs[config.DefaultAdvancePerRun-1].Key {
		t.Fatalf("ties out of ledger order: %v ... %v", got[5], got[len(got)-1])
	}

	got = nil
	if _, err := advanceAll(context.Background(), recs[:10], rank, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		got = append(got, k)
		return nil
	}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 || got[len(got)-1] != recs[0].Key {
		t.Fatalf("short queue: %v; want all nine live postings, the excluded one last", got)
	}

	// No signals configured: ledger order, as before.
	if (&wired{}).titleRank() != nil {
		t.Fatal("rank without signals")
	}
}

// The report says what the cap left waiting: before it, a run that hit the cap
// only printed a stderr line, and the postings it left read as nothing at all.
func TestAdvanceReportCountsWhatTheCapLeft(t *testing.T) {
	recs := liveRecs(310)
	recs[0].Status = posting.Digested // not eligible
	calls := 0
	rep, err := advanceAll(context.Background(), recs, nil, 300, nil, func(_ context.Context, k posting.Key) error {
		calls++
		if calls == 3 {
			return errors.New("score: devprism down")
		}
		return nil
	}, func(string, ...any) {})
	want := advanceReport{Eligible: 309, Limit: 300, Attempted: 300, Failed: 1, Deferred: 9, Stopped: stopCap}
	if err != nil || rep != want {
		t.Fatalf("err %v\nreport %+v\nwant   %+v", err, rep, want)
	}
}

// advance_per_run is honoured, and a queue shorter than the cap is no stop.
func TestAdvanceHonoursTheConfiguredLimit(t *testing.T) {
	calls := 0
	rep, err := advanceAll(context.Background(), liveRecs(12), nil, 5, nil, func(context.Context, posting.Key) error { calls++; return nil }, func(string, ...any) {})
	if err != nil || calls != 5 || rep.Deferred != 7 || rep.Stopped != stopCap {
		t.Fatalf("err %v calls %d report %+v", err, calls, rep)
	}
	rep, err = advanceAll(context.Background(), liveRecs(3), nil, 5, nil, func(context.Context, posting.Key) error { return nil }, func(string, ...any) {})
	if err != nil || rep != (advanceReport{Eligible: 3, Limit: 5, Attempted: 3}) {
		t.Fatalf("short queue: err %v report %+v", err, rep)
	}
	rep, err = advanceAll(context.Background(), nil, nil, 5, nil, func(context.Context, posting.Key) error { return nil }, func(string, ...any) {})
	if err != nil || rep != (advanceReport{Limit: 5}) {
		t.Fatalf("empty ledger: err %v report %+v", err, rep)
	}
	for _, bad := range []int{0, -1} {
		calls = 0
		if _, err := advanceAll(context.Background(), liveRecs(3), nil, bad, nil, func(context.Context, posting.Key) error { calls++; return nil }, func(string, ...any) {}); err == nil || calls != 0 {
			t.Fatalf("limit %d: err %v, %d calls; want a refusal before any call", bad, err, calls)
		}
	}
}

// A stop (unread streak, nothing read, cancellation) still reports how far it
// got and what it left.
func TestAdvanceReportOnEveryStop(t *testing.T) {
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, 300, nil, func(_ context.Context, k posting.Key) error { return unread(k) }, func(string, ...any) {})
	want := advanceReport{Eligible: 300, Limit: 300, Attempted: maxConsecutiveUnread, Unread: maxConsecutiveUnread, Deferred: 300 - maxConsecutiveUnread, Stopped: stopUnreadStreak}
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep != want {
		t.Fatalf("streak: err %v\nreport %+v\nwant   %+v", err, rep, want)
	}

	rep, err = advanceAll(context.Background(), liveRecs(4), nil, 300, nil, func(_ context.Context, k posting.Key) error { return unread(k) }, func(string, ...any) {})
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopNoneRead || rep.Unread != 4 || rep.Deferred != 0 {
		t.Fatalf("none read: err %v report %+v", err, rep)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	rep, err = advanceAll(ctx, liveRecs(10), nil, 300, nil, func(ctx context.Context, k posting.Key) error {
		calls++
		if calls == 4 {
			cancel()
			return ctx.Err()
		}
		return nil
	}, func(string, ...any) {})
	if !errors.Is(err, context.Canceled) || rep.Stopped != stopCancelled || rep.Attempted != 4 || rep.Deferred != 6 {
		t.Fatalf("cancelled: err %v report %+v", err, rep)
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
