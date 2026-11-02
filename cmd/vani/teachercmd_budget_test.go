package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/adaptertest"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A budget below concurrency x worst case still makes progress: the question
// refused at the limit stops further issuing but never cancels those already in
// flight, so every answer that was paid for is stored, nothing stays reserved,
// and a re-run resumes instead of repeating the same loss.
func TestTeacherBudgetBelowConcurrencyTimesWorstStoresEveryPaidAnswer(t *testing.T) {
	state := advSetup(t, nil, advN(20, advDense))
	srv := advServe(t, 0.4, 150, 0, false, 8)
	l := advReal(t, state, srv.URL, 5)
	var sentBefore int64
	for run := 1; run <= 3; run++ {
		o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "0.02", "--concurrency", "8")
		st, err := eval.OpenTeacherStore(filepath.Join(state, eval.TeacherFile))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := l.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sent := srv.calls.Load()
		t.Logf("run %d: %d requests sent in all, %d readings stored, settled %s, retained %s, in flight %s", run, sent, st.Len(), usd(snap.SpentNanoUSD), usd(snap.RetainedNanoUSD), usd(snap.InFlightNanoUSD()))
		if sent == sentBefore {
			t.Fatalf("run %d asked nothing:\n%s", run, o)
		}
		if int64(st.Len()) != sent {
			t.Errorf("run %d: %d requests were paid for but %d readings are stored", run, sent, st.Len())
		}
		if snap.RetainedNanoUSD != 0 || snap.InFlightNanoUSD() != 0 {
			t.Errorf("run %d left %s retained and %s in flight", run, usd(snap.RetainedNanoUSD), usd(snap.InFlightNanoUSD()))
		}
		if !strings.Contains(o, "run again to resume") && st.Len() < 20 {
			t.Errorf("run %d left %d postings unasked but did not say to run again:\n%s", run, 20-st.Len(), o)
		}
		sentBefore = sent
	}
}

// The report says what the Manvi ledger settled, and "run again" only while
// something remains to ask.
func TestTeacherSummaryReportsSettledSpendAndResumeOnlyWhenSomethingRemains(t *testing.T) {
	state := advSetup(t, nil, advN(3, advProse))
	srv := advServe(t, 0.25, 150, 0, false, 0)
	l := advReal(t, state, srv.URL, 5)
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1", "--concurrency", "1")
	snap, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.SpentNanoUSD == 0 {
		t.Fatal("the ledger settled nothing")
	}
	for _, want := range []string{"asked 3, stored 3", "settled spend " + usd(snap.SpentNanoUSD), "budget remaining today"} {
		if !strings.Contains(o, want) {
			t.Errorf("report lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "run again") {
		t.Errorf("a finished run tells the owner to run again:\n%s", o)
	}
}

// The prompt-token bound the spend gate reserves is a physical ceiling: a
// token spans at least one byte, so no text, however dense, can price above it.
func TestTeacherWorstNanoBoundsAnyTextDensity(t *testing.T) {
	p := budget.Prices{Rates: budget.Rates{Input: 3000, Output: 15000, CacheWrite: 3750, CacheRead: 300}}
	for name, desc := range map[string]string{
		"prose":  advProse(1),
		"base64": advDense(1),
		"cjk":    strings.Repeat("日本語のテキスト", 3000),
		"emoji":  strings.Repeat("\U0001F600", 5000),
		"binary": strings.Repeat("\x01\x02\xff", 4000),
	} {
		q := jdtriage.Question(posting.Posting{Title: "T", Location: "L", Description: desc})
		sys, user := llmbackend.Messages(q)
		worstTokens, err := p.Charge(llm.Usage{InputTokens: len(sys) + len(user), OutputTokens: llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom})
		if err != nil {
			t.Fatal(err)
		}
		if got := teacherWorstNano(p, q); got < worstTokens {
			t.Errorf("%s: worst %d is below the cost of one token per prompt byte, %d", name, got, worstTokens)
		}
	}
}

// One run reads one UTC day's cap: a clock that crosses midnight mid-run must
// not move the gate's spend reads, the report or the provider's metering to
// another day's ledger.
func TestTeacherRunAcrossUTCMidnightUsesOneBudgetDay(t *testing.T) {
	state := advSetup(t, nil, advN(6, advProse))
	srv := advServe(t, 0.25, 150, 0, false, 0)
	var ticks atomic.Int64
	oldC := budgetClock
	t.Cleanup(func() { budgetClock = oldC })
	budgetClock = func() time.Time {
		if ticks.Add(1) == 1 {
			return time.Date(2026, 10, 20, 23, 59, 59, 0, time.UTC)
		}
		return time.Date(2026, 10, 21, 0, 0, 1, 0, time.UTC)
	}
	oldD := teacherDecider
	t.Cleanup(func() { teacherDecider = oldD })
	teacherDecider = func(ctx context.Context, w *wired, model string) (decide.Decider, error) {
		l, err := budget.Open(ctx, w.budgetLedgerPath(), 5e9)
		if err != nil {
			return nil, err
		}
		p := &budget.Provider{Inner: newPricedProvider(anthropic.New(srv.URL, adaptertest.Secret("fixture-key")), "claude-test", 3000, 15000), Ledger: l}
		return llmbackend.New(p, llmbackend.Config{Model: model})
	}
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1", "--concurrency", "2")
	day1 := filepath.Join(state, "budget", "llm-2026-10-20.json")
	day2 := filepath.Join(state, "budget", "llm-2026-10-21.json")
	if _, err := os.Stat(day1); err != nil {
		t.Errorf("the run's day ledger is missing: %v", err)
	}
	if _, err := os.Stat(day2); err == nil {
		t.Error("the run touched the next day's ledger after the clock crossed midnight")
	}
	s, err := budget.Inspect(context.Background(), day1)
	if err != nil {
		t.Fatal(err)
	}
	if s.SpentNanoUSD == 0 || !strings.Contains(o, "settled spend "+usd(s.SpentNanoUSD)) {
		t.Errorf("the report did not read the day the provider metered into (settled %d):\n%s", s.SpentNanoUSD, o)
	}
	w := &wired{stateDir: state}
	first := w.budgetLedgerPath()
	ticks.Store(5)
	if again := w.budgetLedgerPath(); again != first {
		t.Errorf("one run named two ledgers: %s then %s", first, again)
	}
}

// The llm budget day is fixed for one run, including a run that crosses UTC
// midnight, and moves when the same process starts another run. Pinning it
// for the life of the process bills a later day against the file the daemon
// opened on its first run.
func TestBudgetDayFollowsTheRunNotTheProcess(t *testing.T) {
	now := time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)
	old := budgetClock
	t.Cleanup(func() { budgetClock = old })
	budgetClock = func() time.Time { return now }

	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM.Provider = ""
	w, warn := openWired(t, cfg)

	first := w.budgetLedgerPath()
	if !strings.HasSuffix(first, "llm-2026-10-29.json") {
		t.Fatalf("first use pinned %s", first)
	}
	now = now.Add(72 * time.Hour)
	if again := w.budgetLedgerPath(); again != first {
		t.Fatalf("inside one run the ledger moved from %s to %s", first, again)
	}

	_, _, stop, err := w.startRun(context.Background(), false)
	if err != nil {
		t.Fatalf("startRun: %v\n%s", err, warn.String())
	}
	stop()
	got := w.budgetLedgerPath()
	if !strings.HasSuffix(got, "llm-2026-11-01.json") {
		t.Fatalf("the next run stayed on %s, want the UTC day that run started", got)
	}
	now = now.Add(24 * time.Hour)
	if again := w.budgetLedgerPath(); again != got {
		t.Fatalf("midnight inside the new run moved the ledger from %s to %s", got, again)
	}
}
