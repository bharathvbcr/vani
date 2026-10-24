package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// shadowTestPrimary is a primary that answers every slot with its first option, so
// a test never reaches a real provider.
type shadowTestPrimary struct{}

func (shadowTestPrimary) Name() string       { return "primary" }
func (shadowTestPrimary) Serves(string) bool { return true }
func (shadowTestPrimary) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	a := decide.Answer{Backend: "primary", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a, nil
}

// shadowRunner takes the real local shadow backend vani wires for a model
// served by h and puts it behind a router whose chain is the fake primary.
func shadowRunner(t *testing.T, h http.HandlerFunc, timeoutMS int) (*pipeline.Runner, *decide.Router) {
	t.Helper()
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Endpoint: srv.URL + "/v1/systemone", Model: "test-d1", TimeoutMS: timeoutMS}
	cfg.Decide.Shadow = "local"
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	wiredRunner, err := w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	shadow := wiredRunner.Triager.(*jdtriage.Triager).D.(*decide.Router).Shadow
	if shadow == nil {
		t.Fatal("no shadow wired")
	}
	rt := decide.NewRouter(shadowTestPrimary{})
	rt.Shadow = shadow
	return &pipeline.Runner{Triager: &jdtriage.Triager{D: rt}}, rt
}

func route(t *testing.T, rt *decide.Router) decide.RouterAnswer {
	t.Helper()
	ra, err := rt.Route(context.Background(), jdtriage.Question(posting.Posting{Key: "k", Title: "Engineer", Description: "Fully remote."}))
	if err != nil {
		t.Fatal(err)
	}
	return ra
}

// The run prints the note as a warning line, and only when something failed.
func TestWarnShadowErrors(t *testing.T) {
	var out bytes.Buffer
	w := &wired{warn: &out}
	r, rt := shadowRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, 0)
	w.warnShadowErrors(r)
	if out.Len() != 0 {
		t.Fatalf("warned before any shadow call: %q", out.String())
	}
	route(t, rt)
	w.warnShadowErrors(r)
	if got := out.String(); !strings.HasPrefix(got, `vani: shadow "local-test-d1-combined": 1`) || !strings.HasSuffix(got, "\n") || strings.Count(got, "\n") != 1 {
		t.Fatalf("warning = %q", got)
	}
}

// The end-of-advance shadow notes (the run's and the inbox poll's) include the
// failed-call line.
func TestWarnShadowNotesReportsFailedCalls(t *testing.T) {
	var out bytes.Buffer
	w := &wired{warn: &out}
	r, rt := shadowRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, 0)
	route(t, rt)
	w.warnShadowNotes(r)
	if got := out.String(); !strings.HasPrefix(got, `vani: shadow "local-test-d1-combined": 1`) {
		t.Fatalf("warning = %q", got)
	}
	if w.shadow == nil || w.shadow.Errors != 1 {
		t.Fatalf("shadow miss = %+v", w.shadow)
	}
}

func TestShadowErrorsNoteUnwrapsTheEscalator(t *testing.T) {
	r, rt := shadowRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, 0)
	route(t, rt)
	r.Triager.(*jdtriage.Triager).D = &escalator{primary: rt}
	if got := shadowErrorsNote(r); !strings.Contains(got, `shadow "local-test-d1-combined": 1`) {
		t.Fatalf("wrapped note = %q", got)
	}
	r.Triager.(*jdtriage.Triager).D = shadowTestPrimary{}
	if got := shadowErrorsNote(r); got != "" {
		t.Fatalf("unknown decider recorded %q", got)
	}
	r.Triager.(*jdtriage.Triager).D = &escalator{primary: shadowTestPrimary{}}
	if got := shadowErrorsNote(r); got != "" {
		t.Fatalf("escalated unknown decider recorded %q", got)
	}
}

func TestShadowMissOnANilRunnerRecordsNothing(t *testing.T) {
	w := &wired{}
	w.warnShadowNotes(nil)
	if w.shadow != nil {
		t.Fatalf("%+v", w.shadow)
	}
}

func TestShadowBusySkipsAreRecorded(t *testing.T) {
	rt := decide.NewRouter(shadowTestPrimary{})
	g := newShadowGate(1, 1)
	g.slots <- struct{}{}
	g.busy.Add(3)
	rt.Shadow = gatedShadow{Backend: shadowTestPrimary{}, gate: g}
	r := &pipeline.Runner{Triager: &jdtriage.Triager{D: rt}}
	w := &wired{}
	w.warnShadowNotes(r)
	if w.shadow == nil || w.shadow.BusySkips != 3 || w.shadow.Errors != 0 {
		t.Fatalf("%+v", w.shadow)
	}
}

func TestShadowErrorsNote(t *testing.T) {
	t.Run("a failing shadow is reported", func(t *testing.T) {
		r, rt := shadowRunner(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}, 0)
		if got := shadowErrorsNote(r); got != "" {
			t.Fatalf("note before any call = %q", got)
		}
		// The router records the failure as unavailable on every slot, tagged with
		// the shadow's model-bearing name, so `vani eval` counts it as asked.
		ra := route(t, rt)
		if len(ra.Shadow) != len(jdtriage.Slots) {
			t.Fatalf("shadow readings for %d of %d slots: %+v", len(ra.Shadow), len(jdtriage.Slots), ra.Shadow)
		}
		for _, s := range jdtriage.Slots {
			if got := ra.Shadow[s.Name]; got.Reading != decide.Unavailable || !strings.HasSuffix(got.Detail, ";backend=local-test-d1-combined") {
				t.Errorf("shadow %s = %+v", s.Name, got)
			}
		}
		if got := shadowErrorsNote(r); !strings.Contains(got, `shadow "local-test-d1-combined": 1`) {
			t.Fatalf("note = %q", got)
		}
	})

	t.Run("a shadow that times out is reported", func(t *testing.T) {
		release := make(chan struct{})
		r, rt := shadowRunner(t, func(_ http.ResponseWriter, req *http.Request) {
			select {
			case <-req.Context().Done():
			case <-release:
			}
		}, 50)
		t.Cleanup(func() { close(release) }) // runs before the server's Close, which waits for handlers
		route(t, rt)
		if got := shadowErrorsNote(r); !strings.Contains(got, `shadow "local-test-d1-combined": 1`) {
			t.Fatalf("note = %q", got)
		}
	})

	t.Run("a shadow that answers every slot is silent", func(t *testing.T) {
		r, rt := shadowRunner(t, func(w http.ResponseWriter, _ *http.Request) {
			var b strings.Builder
			b.WriteString(`{"answers":{`)
			for i, s := range jdtriage.Slots {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`"` + s.Name + `":{"choice":"` + s.Options[0].Value + `","confidence":0.9}`)
			}
			b.WriteString(`}}`)
			_, _ = w.Write([]byte(b.String()))
		}, 0)
		ra := route(t, rt)
		for _, s := range jdtriage.Slots {
			if ra.Shadow[s.Name].Reading != decide.Answered {
				t.Fatalf("shadow %s = %+v", s.Name, ra.Shadow[s.Name])
			}
		}
		if got := shadowErrorsNote(r); got != "" {
			t.Fatalf("note = %q for a healthy shadow", got)
		}
	})

	t.Run("nothing to report without a router or a shadow", func(t *testing.T) {
		if shadowErrorsNote(nil) != "" {
			t.Error("nil runner")
		}
		if shadowErrorsNote(&pipeline.Runner{}) != "" {
			t.Error("runner with no triager")
		}
		noShadow := &pipeline.Runner{Triager: &jdtriage.Triager{D: decide.NewRouter(shadowTestPrimary{})}}
		if shadowErrorsNote(noShadow) != "" {
			t.Error("router with no shadow")
		}
	})
}
