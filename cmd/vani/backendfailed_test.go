package main

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/transport"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/decide"
)

// refusingLLM refuses every request with the given HTTP status.
type refusingLLM struct{ status int }

func (refusingLLM) Name() string { return "fake" }
func (refusingLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p refusingLLM) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, &transport.Error{Provider: "fake", Status: p.status, Attempts: 1, Err: errors.New("refused")}
}

func refusedRun(t *testing.T, status, postings int) (backendStat, []decide.Answer) {
	t.Helper()
	lb, err := llmbackend.New(refusingLLM{status}, llmbackend.Config{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	sink := newBackendErrors()
	b := logErrors(lb, func(string, ...any) {}, sink)
	q := decide.Question{Task: "t", Prompt: "Pick.", Context: []byte("jd"),
		Slots: []decide.Slot{{Name: "mode", Options: []string{"remote", "onsite"}}}}
	var answers []decide.Answer
	for range postings {
		a, _ := b.Decide(context.Background(), q)
		answers = append(answers, a)
	}
	return sink.stats()["llm"], answers
}

// Twenty postings whose requests the provider refuses as too large must read
// Failed (so they become Unsure, visibly) and leave the backend's breaker closed:
// the backend answered, the postings are what is wrong.
func TestRequestSpecificRefusalsDoNotTripTheBreaker(t *testing.T) {
	st, answers := refusedRun(t, 413, maxConsecutiveUnread+5)
	if st.Errors != 0 || st.BreakerOpens != 0 || st.Rejected != 0 {
		t.Fatalf("stats %+v: a request-specific refusal is not a backend error", st)
	}
	if st.Readings["failed"] != maxConsecutiveUnread+5 {
		t.Fatalf("readings %v", st.Readings)
	}
	for i, a := range answers {
		if got := a.Slots["mode"]; got.Reading != decide.Failed {
			t.Fatalf("call %d read %+v", i, got)
		}
	}
}

// An ambiguous status keeps the old behaviour: an outage, counted, and the
// breaker opens, so a run-wide refusal (empty credit, a bad key) still stops
// costing a call per posting.
func TestRunWideRefusalsStillTripTheBreaker(t *testing.T) {
	st, answers := refusedRun(t, 400, 6)
	if st.Errors != breakerTrip || st.BreakerOpens != 1 || st.Rejected != 6-breakerTrip {
		t.Fatalf("stats %+v", st)
	}
	for i, a := range answers {
		if got := a.Slots["mode"]; got.Reading != decide.Unavailable {
			t.Fatalf("call %d read %+v", i, got)
		}
	}
}
