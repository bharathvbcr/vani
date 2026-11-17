package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// failingSpend allows every call and fails to record what it cost.
type w3dFailingSpend struct{}

func (w3dFailingSpend) Allow(context.Context, string) error           { return nil }
func (w3dFailingSpend) Charge(context.Context, string, float64) error { return errors.New("disk full") }

func w3dDecide(t *testing.T, sink *backendErrors, calls int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(jevAnswer(0.002)))
	}))
	t.Cleanup(srv.Close)
	b, err := jev.New(jev.Config{Endpoint: srv.URL + "/v1/decisions", Model: "test", Name: "d1-ultra",
		MaxAttempts: 1, Timeout: 5 * time.Second, Spend: w3dFailingSpend{}, Key: "k"}, clientDoer{srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	lb := logErrors(b, func(string, ...any) {}, sink)
	q := decide.Question{Task: "career.jd_triage", Prompt: "p", Context: []byte("x")}
	for _, s := range jdtriage.Slots {
		opts := make([]string, len(s.Options))
		for i, o := range s.Options {
			opts[i] = o.Value
		}
		q.Slots = append(q.Slots, decide.Slot{Name: s.Name, Options: opts})
	}
	for i := 0; i < calls; i++ {
		if _, err := lb.Decide(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
}

// A charge the jev backend could not record reaches the sink, the run record,
// `vani status --json` and the status text.
func TestW3dChargeErrorsReachRecordAndStatus(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	sink := newBackendErrors()
	w3dDecide(t, sink, 3)
	stats := sink.stats()
	if got := stats["d1-ultra"].ChargeErrors; got != 3 {
		t.Fatalf("sink charge errors = %d, want 3", got)
	}
	old := buildApp
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error { return nil },
			RunReport: func() runDetail { return runDetail{BackendStats: stats, ChargeErrors: totalChargeErrors(stats)} }}, nil
	}
	t.Cleanup(func() { buildApp = old })
	mustCode(t, 0, "run-once")

	r, err := readRunRecord(stateDir)
	if err != nil || r == nil || r.ChargeErrors != 3 {
		t.Fatalf("record %+v, %v", r, err)
	}
	if got := statusOf(t).LastRun.ChargeErrors; got != 3 {
		t.Errorf("status --json charge_errors = %d, want 3", got)
	}
	raw, _ := mustCode(t, 0, "status", "--json")
	var m struct {
		LastRun map[string]json.RawMessage `json:"last_run"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil || string(m.LastRun["charge_errors"]) != "3" {
		t.Errorf("last_run.charge_errors = %s (%v)", m.LastRun["charge_errors"], err)
	}
	text, _ := mustCode(t, 0, "status")
	if !strings.Contains(text, "3 call(s) whose cost could not be recorded") {
		t.Errorf("status text does not say so:\n%s", text)
	}
}

// A clean run adds nothing: no field, no line.
func TestW3dNoChargeErrorsSaysNothing(t *testing.T) {
	sink := newBackendErrors()
	if line := chargeErrorLine(lastRunFacet{runDetail: runDetail{BackendStats: sink.stats()}}); line != "" {
		t.Errorf("line = %q", line)
	}
	raw, err := json.Marshal(runDetail{})
	if err != nil || strings.Contains(string(raw), "charge_errors") {
		t.Errorf("zero count is written: %s", raw)
	}
}

// Two instances of one backend name add up, each by what it counted since it
// was last read, and a backend with no counter is ignored.
func TestW3dChargeErrorsAddAcrossInstances(t *testing.T) {
	sink := newBackendErrors()
	w3dDecide(t, sink, 2)
	w3dDecide(t, sink, 1)
	if got := totalChargeErrors(sink.stats()); got != 3 {
		t.Errorf("total = %d, want 3", got)
	}
	plain := withoutBreaker(logErrors(&failingBackend{errs: []error{nil}}, func(string, ...any) {}, sink))
	if _, err := plain.Decide(context.Background(), decide.Question{}); err != nil {
		t.Fatal(err)
	}
	if got := totalChargeErrors(sink.stats()); got != 3 {
		t.Errorf("total after a counterless backend = %d, want 3", got)
	}
}
