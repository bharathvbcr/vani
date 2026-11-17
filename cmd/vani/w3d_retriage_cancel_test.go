package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
)

// w3dCancelAfterPay is a provider whose reply arrives paid for, and whose
// caller is stopped the moment it has arrived: the answer is discarded, the
// money is spent.
type w3dCancelAfterPay struct {
	llm.Provider
	cancel context.CancelFunc
}

func (p w3dCancelAfterPay) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	st, err := p.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return w3dPaidStream{Stream: st, cancel: p.cancel}, nil
}

type w3dPaidStream struct {
	llm.Stream
	cancel context.CancelFunc
}

func (s w3dPaidStream) Response() (llm.Response, error) {
	resp, err := s.Stream.Response()
	s.cancel()
	return resp, err
}

func w3dStopAfterPay(t *testing.T, p *retriageLLM) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	oldSig := signalContext
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	oldProv := retriageProvider
	retriageProvider = func(context.Context, *wired) (llm.Provider, error) {
		return w3dCancelAfterPay{Provider: p, cancel: cancel}, nil
	}
	t.Cleanup(func() { signalContext, retriageProvider = oldSig, oldProv })
}

// A single reading stopped after its call was paid still reports the cost.
func TestW3dRetriageCancelledAfterPayingReportsTheCost(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 900, OutputTokens: 40}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.4"})
	w3dStopAfterPay(t, p)

	e := rtFail(t, "gh:acme:1")
	if e.Code != rtCancelled {
		t.Fatalf("code %q (%s), want %q", e.Code, e.Error, rtCancelled)
	}
	if e.Cost == nil || e.Cost.InputTokens != 900 || e.Cost.OutputTokens != 40 || e.Cost.USD <= 0 {
		t.Fatalf("cost %+v: a paid call's cost was lost on cancel", e.Cost)
	}
	rtNoStore(t, state)
}

// The same for text output: the error words carry the cost.
func TestW3dRetriageCancelledAfterPayingTextSaysTheCost(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 900, OutputTokens: 40}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.4"})
	w3dStopAfterPay(t, p)

	_, stderr := mustCode(t, 1, "jobs", "retriage", "gh:acme:1")
	if !strings.Contains(stderr, "stopped") || !strings.Contains(stderr, "cost") {
		t.Errorf("the stop does not say what the call cost: %q", stderr)
	}
}

// A call stopped before anything was paid has no cost to show.
func TestW3dRetriageCancelledBeforePayingHasNoCost(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.4"})
	ctx, cancel := context.WithCancel(context.Background())
	old := signalContext
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { signalContext = old })
	p.onStream = func(c context.Context) error { cancel(); <-c.Done(); return c.Err() }

	e := rtFail(t, "gh:acme:1")
	if e.Code != rtCancelled || e.Cost != nil {
		t.Errorf("code %q cost %+v, want cancelled with no cost", e.Code, e.Cost)
	}
}

// A batch stopped after a paid call counts it: an item with the cost, and the
// summary's usd.
func TestW3dRetriageBatchCancelledAfterPayingKeepsTheCost(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 900, OutputTokens: 40}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 3)
	w3dStopAfterPay(t, p)

	evs := rtBatchEvents(t, rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys[:3]...)...))
	s := evs[len(evs)-1].Summary
	if s.USD <= 0 {
		t.Fatalf("summary usd = %v: the paid call is not in the batch's cost (%+v)", s.USD, s)
	}
	if s.Stopped != rtCancelled || s.Kept != 0 {
		t.Errorf("summary %+v, want stopped by cancel with nothing kept", s)
	}
	paid := 0
	for _, ev := range rtBatchItems(evs) {
		if ev.Cost != nil && ev.Cost.InputTokens == 900 && ev.Code == rtCancelled {
			paid++
		}
	}
	if paid != 1 {
		t.Errorf("%d items carry the paid cancelled call, want 1: %+v", paid, evs)
	}
	rtNoStore(t, state)
}
