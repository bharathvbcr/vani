package main

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// primaryStub is the decide.Decider standing in for the router: it returns ans
// (with its maps copied, as a router builds them fresh per call).
type primaryStub struct {
	ans   decide.Answer
	err   error
	calls atomic.Int64
}

func (p *primaryStub) Decide(context.Context, decide.Question) (decide.Answer, error) {
	p.calls.Add(1)
	return cloneAnswer(p.ans), p.err
}

func cloneAnswer(a decide.Answer) decide.Answer {
	out := a
	out.Slots = make(map[string]decide.SlotReading, len(a.Slots))
	for k, v := range a.Slots {
		out.Slots[k] = v
	}
	if a.Shadow != nil {
		out.Shadow = make(map[string]decide.SlotReading, len(a.Shadow))
		for k, v := range a.Shadow {
			out.Shadow[k] = v
		}
	}
	return out
}

// escFake is a decide.Backend named "escalate": it records the slots of every
// question it is asked and replies with reply (default: each slot's last option).
type escFake struct {
	mu    sync.Mutex
	asked [][]string
	calls atomic.Int64
	reply func(q decide.Question) (decide.Answer, error)
}

func (f *escFake) Name() string       { return escalateName }
func (f *escFake) Serves(string) bool { return true }
func (f *escFake) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.calls.Add(1)
	names := make([]string, len(q.Slots))
	for i, s := range q.Slots {
		names[i] = s.Name
	}
	f.mu.Lock()
	f.asked = append(f.asked, names)
	f.mu.Unlock()
	if f.reply != nil {
		return f.reply(q)
	}
	a := decide.Answer{Backend: escalateName, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[len(s.Options)-1]}
	}
	return a, nil
}

// testEscalator is an escalator over primary and an escFake behind the same
// errorLogged + Router stack the wiring builds, with the run's meter.
func testEscalator(t *testing.T, primary decide.Decider, f *escFake, belowP float64, maxPerRun int) (*escalator, *backendErrors, *bytes.Buffer) {
	t.Helper()
	sink := newBackendErrors()
	var warn bytes.Buffer
	var mu sync.Mutex
	warnf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		warn.WriteString(format + "\n")
	}
	ask := decide.NewRouter(logErrors(f, warnf, sink))
	return newEscalator(primary, ask, belowP, maxPerRun, warnf), sink, &warn
}

func triageQuestion(key string) decide.Question {
	return jdtriage.Question(posting.Posting{Key: posting.Key(key), Title: "Engineer", Description: "Build inference systems. Fully remote."})
}

// jevReading is a Liquid D-calibrated answer: the slot's first option at p.
func jevReading(s decide.Slot, p float64) decide.SlotReading {
	return decide.SlotReading{Reading: decide.Answered, Value: s.Options[0], Jev: &decide.JevCalib{Probability: p}, Detail: "backend=d1-ultra"}
}

// primaryFor answers every slot of q with Liquid D at 0.95, except the named ones.
func primaryFor(q decide.Question, ps map[string]float64) decide.Answer {
	a := decide.Answer{Backend: "d1-ultra", CostUSD: 0.002, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		p := 0.95
		if v, ok := ps[s.Name]; ok {
			p = v
		}
		a.Slots[s.Name] = jevReading(s, p)
	}
	return a
}

// A slot read under below_p is asked of the escalation tier, and only that slot
// (a slot at exactly below_p is not low). The tier's reading lands in Shadow,
// tagged with its backend; the primary's slots, their probabilities, backend and
// cost are exactly what the router returned.
func TestEscalatesOnlyTheLowSlotIntoShadow(t *testing.T) {
	q := triageQuestion("k1")
	want := primaryFor(q, map[string]float64{"role_fit": 0.3, "seniority": 0.5})
	p := &primaryStub{ans: want}
	f := &escFake{}
	e, sink, _ := testEscalator(t, p, f, 0.5, 200)

	got, err := e.Decide(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 || !reflect.DeepEqual(f.asked, [][]string{{"role_fit"}}) {
		t.Fatalf("escalation asked %v in %d call(s), want exactly role_fit once", f.asked, f.calls.Load())
	}
	if !reflect.DeepEqual(got.Slots, want.Slots) || got.Backend != want.Backend || got.CostUSD != want.CostUSD {
		t.Fatalf("the primary changed:\n got %+v\nwant %+v", got, want)
	}
	if got.Slots["role_fit"].Jev.Probability != 0.3 {
		t.Fatalf("p changed: %+v", got.Slots["role_fit"])
	}
	sh, ok := got.Shadow["role_fit"]
	if !ok || len(got.Shadow) != 1 {
		t.Fatalf("shadow %+v, want exactly role_fit", got.Shadow)
	}
	roleOpts := q.Slots[0].Options
	if sh.Reading != decide.Answered || sh.Value != roleOpts[len(roleOpts)-1] || sh.Detail != "backend=escalate" {
		t.Fatalf("shadow reading %+v", sh)
	}
	if st := sink.stats()[escalateName]; st.Calls != 1 || st.Readings["answered"] != 1 {
		t.Fatalf("the meter did not count the escalate backend: %+v", sink.stats())
	}
	if e.Escalated() != 1 || e.Skipped() != 0 {
		t.Fatalf("escalated %d skipped %d", e.Escalated(), e.Skipped())
	}
}

// Nothing under below_p: no call, and the answer is the primary's, untouched
// (Shadow stays nil, so no shadow annotation is written for the posting).
func TestNoLowSlotMeansZeroEscalationCalls(t *testing.T) {
	q := triageQuestion("k1")
	want := primaryFor(q, map[string]float64{"role_fit": 0.5, "sponsorship": 0.99, "work_mode": 1})
	f := &escFake{}
	e, _, _ := testEscalator(t, &primaryStub{ans: want}, f, 0.5, 200)
	got, err := e.Decide(context.Background(), q)
	if err != nil || f.calls.Load() != 0 {
		t.Fatalf("err %v, %d escalation calls", err, f.calls.Load())
	}
	if !reflect.DeepEqual(got, want) || got.Shadow != nil {
		t.Fatalf("answer changed:\n got %+v\nwant %+v", got, want)
	}
}

// Only a calibrated Liquid D answer under below_p is low confidence. A slot the LLM
// answered (no calibration), a Lappi answer, an outage reading, an abstention
// and an unusable probability are not escalated, even when other slots are.
func TestOnlyLowJevAnswersAreEscalated(t *testing.T) {
	q := triageQuestion("k1")
	slots := map[string]decide.Slot{}
	for _, s := range q.Slots {
		slots[s.Name] = s
	}
	cases := map[string]decide.SlotReading{
		"llm answered":                {Reading: decide.Answered, Value: slots["role_fit"].Options[0], Detail: "backend=llm"},
		"lappi answered":              {Reading: decide.Answered, Value: slots["role_fit"].Options[0], Lappi: &decide.LappiCalib{Score: 0.01}, Detail: "backend=lappi"},
		"unavailable":                 {Reading: decide.Unavailable, Detail: "deadline;backend=d1-ultra"},
		"failed":                      {Reading: decide.Failed, Detail: "unknown_choice;backend=d1-ultra"},
		"not asked":                   {Reading: decide.NotAsked, Detail: "backend=d1-ultra"},
		"abstained":                   {Reading: decide.Abstained, Detail: "backend=llm"},
		"NaN probability":             {Reading: decide.Answered, Value: slots["role_fit"].Options[0], Jev: &decide.JevCalib{Probability: math.NaN()}},
		"refused":                     {Reading: decide.Refused, Detail: "task_not_trained;backend=lappi"},
		"answered, p exactly below_p": {Reading: decide.Answered, Value: slots["role_fit"].Options[0], Jev: &decide.JevCalib{Probability: 0.5}},
	}
	for name, rd := range cases {
		t.Run(name, func(t *testing.T) {
			want := primaryFor(q, nil)
			want.Slots["role_fit"] = rd
			f := &escFake{}
			e, _, _ := testEscalator(t, &primaryStub{ans: want}, f, 0.5, 200)
			got, err := e.Decide(context.Background(), q)
			if err != nil || f.calls.Load() != 0 || !reflect.DeepEqual(got, want) {
				t.Fatalf("err %v, %d calls, answer %+v", err, f.calls.Load(), got)
			}
		})
	}
}

// An escalation that fails changes nothing in the primary answer: the tier's
// slots read Unavailable in Shadow, and the failure is in the run's meter under
// "escalate" (the meter and breaker are the ones every backend uses).
func TestEscalationFailureNeverTouchesThePrimary(t *testing.T) {
	q := triageQuestion("k1")
	replies := map[string]func(decide.Question) (decide.Answer, error){
		"error": func(decide.Question) (decide.Answer, error) {
			return decide.Answer{}, errors.New("anthropic: 529 overloaded")
		},
		"panic": func(decide.Question) (decide.Answer, error) { panic("boom") },
	}
	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			want := primaryFor(q, map[string]float64{"role_fit": 0.2, "work_mode": 0.4})
			f := &escFake{reply: reply}
			e, sink, _ := testEscalator(t, &primaryStub{ans: want}, f, 0.5, 200)
			got, err := e.Decide(context.Background(), q)
			if err != nil {
				t.Fatalf("an escalation failure failed the posting: %v", err)
			}
			if !reflect.DeepEqual(got.Slots, want.Slots) || got.Backend != want.Backend || got.CostUSD != want.CostUSD {
				t.Fatalf("the primary changed: %+v", got)
			}
			if len(got.Shadow) != 2 {
				t.Fatalf("shadow %+v, want the two escalated slots", got.Shadow)
			}
			for _, slot := range []string{"role_fit", "work_mode"} {
				if sh := got.Shadow[slot]; sh.Reading != decide.Unavailable || sh.Detail == "" {
					t.Fatalf("%s shadow %+v, want an unavailable reading with a cause", slot, sh)
				}
			}
			if name == "error" {
				st := sink.stats()[escalateName]
				rep := sink.report()
				if st.Calls != 1 || st.Errors != 1 || rep == nil || len(rep.Causes) != 1 || rep.Causes[0].Backend != escalateName {
					t.Fatalf("the error was not counted: stats %+v report %+v", st, rep)
				}
			}
		})
	}
}

// A tier that answers something unusable (a value not among the options) is a
// Failed shadow reading, courtesy of the router's Rule 7 check, and not a
// primary change either.
func TestUnusableEscalationValueIsRecordedAsFailed(t *testing.T) {
	q := triageQuestion("k1")
	want := primaryFor(q, map[string]float64{"role_fit": 0.1})
	f := &escFake{reply: func(q decide.Question) (decide.Answer, error) {
		return decide.Answer{Backend: escalateName, Slots: map[string]decide.SlotReading{
			"role_fit": {Reading: decide.Answered, Value: "definitely_not_an_option"}}}, nil
	}}
	e, _, _ := testEscalator(t, &primaryStub{ans: want}, f, 0.5, 200)
	got, _ := e.Decide(context.Background(), q)
	if sh := got.Shadow["role_fit"]; sh.Reading != decide.Failed || sh.Value != "" {
		t.Fatalf("shadow %+v", sh)
	}
	if !reflect.DeepEqual(got.Slots, want.Slots) {
		t.Fatal("primary changed")
	}
}

// max_per_run is one budget across the concurrent workers: with a limit of 2
// and ten postings each carrying one low slot, exactly two are escalated and
// eight are counted as skipped. Run under -race.
func TestMaxPerRunHoldsAcrossConcurrentPostings(t *testing.T) {
	const postings = 10
	f := &escFake{}
	q0 := triageQuestion("k")
	e, _, warn := testEscalator(t, &primaryStub{ans: primaryFor(q0, map[string]float64{"role_fit": 0.1})}, f, 0.5, 2)

	var wg sync.WaitGroup
	var withShadow atomic.Int64
	start := make(chan struct{})
	for i := range postings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, err := e.Decide(context.Background(), triageQuestion(string(rune('a'+i))))
			if err != nil {
				t.Error(err)
			}
			if len(a.Shadow) > 0 {
				withShadow.Add(1)
			}
			if a.Slots["role_fit"].Jev.Probability != 0.1 {
				t.Error("primary changed")
			}
		}()
	}
	close(start)
	wg.Wait()
	if f.calls.Load() != 2 || withShadow.Load() != 2 || e.Escalated() != 2 || e.Skipped() != postings-2 {
		t.Fatalf("calls %d, shadows %d, escalated %d, skipped %d; want 2, 2, 2, %d", f.calls.Load(), withShadow.Load(), e.Escalated(), e.Skipped(), postings-2)
	}
	if n := bytes.Count(warn.Bytes(), []byte("max_per_run")); n != 1 {
		t.Fatalf("the exhaustion line was printed %d times, want once:\n%s", n, warn.String())
	}
}

// The limit counts slots: a posting with more low slots than the run has room
// for is escalated for the room that is left, in slot order, and the rest are
// skipped and counted.
func TestMaxPerRunCountsSlotsAndEscalatesThePrefix(t *testing.T) {
	q := triageQuestion("k")
	low := map[string]float64{"role_fit": 0.1, "sponsorship": 0.1, "work_mode": 0.1, "seniority": 0.1}
	f := &escFake{}
	e, _, _ := testEscalator(t, &primaryStub{ans: primaryFor(q, low)}, f, 0.5, 3)
	a1, _ := e.Decide(context.Background(), q)
	a2, _ := e.Decide(context.Background(), q)
	if len(a1.Shadow) != 3 || len(a2.Shadow) != 0 {
		t.Fatalf("shadows %d then %d, want 3 then 0", len(a1.Shadow), len(a2.Shadow))
	}
	if _, ok := a1.Shadow["seniority"]; ok { // the fourth slot in fixture order
		t.Fatalf("escalated past the limit: %+v", a1.Shadow)
	}
	if e.Escalated() != 3 || e.Skipped() != 5 || f.calls.Load() != 1 {
		t.Fatalf("escalated %d skipped %d calls %d", e.Escalated(), e.Skipped(), f.calls.Load())
	}
}

// A shadow reading the primary already carries is kept: the escalation is
// merged into a copy of the map and never overwrites it (and the router's own
// map is not mutated).
func TestEscalationMergesIntoExistingShadow(t *testing.T) {
	q := triageQuestion("k1")
	want := primaryFor(q, map[string]float64{"role_fit": 0.1, "seniority": 0.1})
	existing := decide.SlotReading{Reading: decide.Answered, Value: "senior", Detail: "backend=lappi"}
	want.Shadow = map[string]decide.SlotReading{"seniority": existing}
	f := &escFake{}
	e, _, _ := testEscalator(t, &primaryStub{ans: want}, f, 0.5, 200)
	got, _ := e.Decide(context.Background(), q)
	if !reflect.DeepEqual(f.asked, [][]string{{"role_fit"}}) {
		t.Fatalf("asked %v; a slot with a shadow reading must not be re-asked", f.asked)
	}
	if got.Shadow["seniority"] != existing || got.Shadow["role_fit"].Detail != "backend=escalate" || len(got.Shadow) != 2 {
		t.Fatalf("shadow %+v", got.Shadow)
	}
	if len(want.Shadow) != 1 {
		t.Fatalf("the primary's own shadow map was mutated: %+v", want.Shadow)
	}
}

// A failed primary (or a finished context) is returned as it is: nothing is
// escalated for an answer that does not exist.
func TestNoEscalationForAFailedPrimaryOrDoneContext(t *testing.T) {
	q := triageQuestion("k1")
	f := &escFake{}
	boom := errors.New("invalid question")
	e, _, _ := testEscalator(t, &primaryStub{ans: primaryFor(q, map[string]float64{"role_fit": 0.1}), err: boom}, f, 0.5, 200)
	if _, err := e.Decide(context.Background(), q); !errors.Is(err, boom) || f.calls.Load() != 0 {
		t.Fatalf("err %v calls %d", err, f.calls.Load())
	}
	e, _, _ = testEscalator(t, &primaryStub{ans: primaryFor(q, map[string]float64{"role_fit": 0.1})}, f, 0.5, 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Decide(ctx, q); err != nil || f.calls.Load() != 0 || e.Escalated() != 0 {
		t.Fatalf("err %v calls %d escalated %d", err, f.calls.Load(), e.Escalated())
	}
}

// A limit of zero (a config that skipped Validate) escalates nothing, and says
// so in Skipped rather than looking like a quiet run.
func TestZeroMaxPerRunEscalatesNothing(t *testing.T) {
	q := triageQuestion("k1")
	f := &escFake{}
	e, _, _ := testEscalator(t, &primaryStub{ans: primaryFor(q, map[string]float64{"role_fit": 0.1})}, f, 0.5, 0)
	got, _ := e.Decide(context.Background(), q)
	if f.calls.Load() != 0 || got.Shadow != nil || e.Skipped() != 1 {
		t.Fatalf("calls %d shadow %v skipped %d", f.calls.Load(), got.Shadow, e.Skipped())
	}
}

// Measure mode: the DropPolicy ignores Shadow, so an escalated disagreement
// (even "mismatch_domain" against a keep) decides nothing, and a drop the
// primary earned stands when the tier disagrees with it.
func TestDropPolicyIgnoresEscalatedReadings(t *testing.T) {
	policy := pipeline.DropPolicy{OpenRouter: "d1-ultra", Rules: []pipeline.DropRule{
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
		{Slot: "seniority", Values: []string{"staff_plus"}, MinOpenRouterProb: 0.3},
	}}
	run := func(t *testing.T, primary map[string]decide.SlotReading, tier map[string]string) (plain, escalated decide.Answer) {
		t.Helper()
		q := triageQuestion("k1")
		base := primaryFor(q, map[string]float64{"role_fit": 0.2, "seniority": 0.4})
		for name, rd := range primary {
			base.Slots[name] = rd
		}
		f := &escFake{reply: func(q decide.Question) (decide.Answer, error) {
			a := decide.Answer{Backend: escalateName, Slots: map[string]decide.SlotReading{}}
			for _, s := range q.Slots {
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: tier[s.Name]}
			}
			return a, nil
		}}
		e, _, _ := testEscalator(t, &primaryStub{ans: base}, f, 0.5, 200)
		got, err := e.Decide(context.Background(), q)
		if err != nil || len(got.Shadow) == 0 {
			t.Fatalf("err %v shadow %+v: the fixture must escalate", err, got.Shadow)
		}
		return base, got
	}
	t.Run("tier says mismatch_domain on a kept posting", func(t *testing.T) {
		plain, esc := run(t, nil, map[string]string{"role_fit": "mismatch_domain", "seniority": "mid"})
		ps, pr := policy.Evaluate(plain)
		es, er := policy.Evaluate(esc)
		if ps != es || pr != er || ps != posting.Triaged {
			t.Fatalf("plain %s (%s), escalated %s (%s)", ps, pr, es, er)
		}
	})
	t.Run("tier disagrees with a drop the primary earned", func(t *testing.T) {
		primary := map[string]decide.SlotReading{"seniority": {Reading: decide.Answered, Value: "staff_plus", Jev: &decide.JevCalib{Probability: 0.4}, Detail: "backend=d1-ultra"}}
		plain, esc := run(t, primary, map[string]string{"role_fit": "strong_fit", "seniority": "mid"})
		ps, pr := policy.Evaluate(plain)
		es, er := policy.Evaluate(esc)
		if ps != es || pr != er || ps != posting.Dropped {
			t.Fatalf("plain %s (%s), escalated %s (%s)", ps, pr, es, er)
		}
	})
}

// With decide.escalate off the triager's Decider is the plain router itself,
// not a wrapper; with it on it is the escalator over that router.
func TestEscalatorIsInstalledOnlyWhenEnabled(t *testing.T) {
	cfg := reasonsConfig(t)
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	cfg.Decide.DailyUSD.LLM = 1
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r, _ := runnerFor(t, cfg, t.TempDir())
	if _, ok := r.Triager.(*jdtriage.Triager).D.(*decide.Router); !ok {
		t.Fatalf("disabled: decider is %T, want the plain *decide.Router", r.Triager.(*jdtriage.Triager).D)
	}

	cfg.Decide.Escalate = &config.Escalate{Enabled: false}
	r, _ = runnerFor(t, cfg, t.TempDir())
	if _, ok := r.Triager.(*jdtriage.Triager).D.(*decide.Router); !ok {
		t.Fatal("escalate.enabled=false installed a wrapper")
	}

	cfg.Decide.Escalate = &config.Escalate{Enabled: true}
	if err := cfg.Validate(); err == nil { // no cheaper tier to escalate on
		t.Fatal("escalation enabled without llm.triage_model validated")
	}
	cfg.LLM.TriageModel = "claude-haiku-5-5"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r, _ = runnerFor(t, cfg, t.TempDir())
	esc, ok := r.Triager.(*jdtriage.Triager).D.(*escalator)
	if !ok {
		t.Fatalf("enabled: decider is %T, want *escalator", r.Triager.(*jdtriage.Triager).D)
	}
	if _, ok := esc.primary.(*decide.Router); !ok {
		t.Fatalf("the escalator wraps %T, want the router", esc.primary)
	}
	if esc.belowP != config.DefaultEscalateBelowP || esc.maxRun != config.DefaultEscalateMaxPerRun {
		t.Fatalf("defaults: below_p %v max %d", esc.belowP, esc.maxRun)
	}
	below, max := 0.3, 7
	cfg.Decide.Escalate = &config.Escalate{Enabled: true, BelowP: &below, MaxPerRun: &max}
	r, _ = runnerFor(t, cfg, t.TempDir())
	esc = r.Triager.(*jdtriage.Triager).D.(*escalator)
	if esc.belowP != 0.3 || esc.maxRun != 7 {
		t.Fatalf("configured: below_p %v max %d", esc.belowP, esc.maxRun)
	}
}
