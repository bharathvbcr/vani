package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/decide"
)

// outageRouter is the wire.go chain for one promoted slot: the Lappi tier is
// wrapped as logFallback (breaker + fall-through counting), then d1-ultra for
// the other slots and the LLM tier.
func outageRouter(promoted []string, l, or, llm *fakeBackend) (*decide.Router, *backendErrors) {
	sink := newBackendErrors()
	warn := func(string, ...any) {}
	return decide.NewRouter(
		onlySlots(logFallback(l, warn, sink), promoted),
		exceptSlots(logErrors(or, warn, sink), promoted),
		logErrors(llm, warn, sink),
	), sink
}

// A Lappi that answers Unavailable (no error, as the socket client does) trips
// the breaker after breakerTrip questions, is not dialled again, and every
// question it could not read is counted as fallen through to the next tier.
func TestLappiOutageTripsBreakerAndCountsFallThrough(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	l := &fakeBackend{name: "lappi", down: true}
	or := &fakeBackend{name: "d1-ultra"}
	llm := &fakeBackend{name: "llm"}
	rt, sink := outageRouter(promoted, l, or, llm)

	const n = 8 // under breakerProbeEvery: no probe gets through
	for i := 0; i < n; i++ {
		if _, err := rt.Route(context.Background(), triageQ()); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.asked) != breakerTrip {
		t.Errorf("lappi dialled %d times, want %d (the breaker should open and stop it)", len(l.asked), breakerTrip)
	}
	st := sink.stats()["lappi"]
	if st.BreakerOpens != 1 || st.Rejected != n-breakerTrip {
		t.Errorf("lappi breaker opens=%d rejected=%d, want 1 and %d", st.BreakerOpens, st.Rejected, n-breakerTrip)
	}
	if st.FellThrough != n {
		t.Errorf("FellThrough = %d, want %d (every question handed to the fallback tier)", st.FellThrough, n)
	}
	// The cap stays the hard bound: the LLM tier is still what answers.
	if got := len(llm.asked); got != n {
		t.Errorf("llm asked %d times, want %d", got, n)
	}
	body := digestBody(3, nil, sink.stats())
	if !strings.Contains(body, "8 question(s) went to the paid model because Lappi was down") {
		t.Errorf("notice %q does not report the fallback count", body)
	}
}

// A healthy Lappi never counts, and neither does a slot it was not asked
// (NotAsked): the breaker stays closed and the notice says nothing.
func TestLappiHealthyOrFilteredIsNotAFallThrough(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	l := &fakeBackend{name: "lappi"}
	rt, sink := outageRouter(promoted, l, &fakeBackend{name: "d1-ultra"}, &fakeBackend{name: "llm"})
	for i := 0; i < 2*breakerTrip; i++ {
		if _, err := rt.Route(context.Background(), triageQ()); err != nil {
			t.Fatal(err)
		}
	}
	st := sink.stats()["lappi"]
	if st.BreakerOpens != 0 || st.FellThrough != 0 || st.Rejected != 0 {
		t.Errorf("healthy lappi: %+v", st)
	}
	if body := digestBody(3, nil, sink.stats()); strings.Contains(body, "Lappi") {
		t.Errorf("notice %q mentions Lappi on a healthy run", body)
	}

	// A question that names no promoted slot never reaches the Lappi tier.
	other := decide.Question{Slots: nil}
	if allUnavailable(decide.Answer{}, other) {
		t.Error("an empty question reads as an outage")
	}
}
