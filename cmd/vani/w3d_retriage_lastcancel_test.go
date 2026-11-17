package main

import (
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
)

// A cancel that lands during the last posting's paid call leaves nothing
// unasked, and the summary still says the batch was stopped.
func TestW3dRetriageBatchCancelOnLastItemIsStopped(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 900, OutputTokens: 40}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 1)
	w3dStopAfterPay(t, p)

	evs := rtBatchEvents(t, rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys[:1]...)...))
	s := evs[len(evs)-1].Summary
	if s.NotAsked != 0 {
		t.Fatalf("summary %+v: the one posting was asked, want not_asked 0", s)
	}
	if s.Stopped != rtCancelled {
		t.Errorf("summary stopped = %q, want %q (the batch was cancelled)", s.Stopped, rtCancelled)
	}
}
