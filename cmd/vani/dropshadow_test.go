package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const dropShadowName = "local-d3-mini-per_slot"

var dropPol = pipeline.DropPolicy{OpenRouter: "d1-ultra", Rules: []pipeline.DropRule{
	{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
}}

// dropShadowFake is the shadow backend: it records which slots it was asked and
// answers with reply (default: strong_fit on role_fit, i.e. a disagreement).
type dropShadowFake struct {
	mu     sync.Mutex
	asked  [][]string
	calls  atomic.Int64
	inNow  atomic.Int64
	maxNow atomic.Int64
	delay  time.Duration
	reply  func(decide.Question) (decide.Answer, error)
}

func (f *dropShadowFake) Name() string       { return dropShadowName }
func (f *dropShadowFake) Serves(string) bool { return true }
func (f *dropShadowFake) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	f.calls.Add(1)
	n := f.inNow.Add(1)
	defer f.inNow.Add(-1)
	for {
		m := f.maxNow.Load()
		if n <= m || f.maxNow.CompareAndSwap(m, n) {
			break
		}
	}
	names := make([]string, len(q.Slots))
	for i, s := range q.Slots {
		names[i] = s.Name
	}
	f.mu.Lock()
	f.asked = append(f.asked, names)
	f.mu.Unlock()
	if f.delay > 0 {
		if err := sleepCtx(ctx, f.delay); err != nil {
			return decide.Answer{}, err
		}
	}
	if f.reply != nil {
		return f.reply(q)
	}
	a := decide.Answer{Backend: dropShadowName, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: "strong_fit"}
	}
	return a, nil
}

func testDropShadower(primary decide.Decider, f *dropShadowFake, inFlight int) *dropShadower {
	return newDropShadower(primary, decide.NewRouter(f), f.Name(), f.Serves, dropPol, inFlight)
}

// dropPrimary answers role_fit mismatch_domain at p and every other slot at its
// first option at 0.95.
func dropPrimary(q decide.Question, p float64) decide.Answer {
	a := primaryFor(q, nil)
	for _, s := range q.Slots {
		if s.Name == "role_fit" {
			rd := jevReading(s, p)
			rd.Value = "mismatch_domain"
			a.Slots[s.Name] = rd
		}
	}
	return a
}

// The shadow is asked about a drop candidate and only that slot, on every
// posting (no sampling), before the drop: the reading lands in Shadow tagged
// with its backend, the primary's answer is untouched, and the policy still
// drops exactly as it would without the shadow even though the shadow disagrees.
func TestDropShadowAsksEveryCandidateAndTheDropStillFollowsTheRule(t *testing.T) {
	f := &dropShadowFake{}
	for i := range 40 {
		q := triageQuestion(fmt.Sprintf("gh:acme:%d", i))
		base := dropPrimary(q, 0.95)
		d := testDropShadower(&primaryStub{ans: base}, f, 2)
		got, err := d.Decide(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		sh, ok := got.Shadow["role_fit"]
		if !ok || sh.Reading != decide.Answered || sh.Value != "strong_fit" || !strings.Contains(sh.Detail, "backend="+dropShadowName) {
			t.Fatalf("posting %d shadow %+v", i, got.Shadow)
		}
		if len(got.Shadow) != 1 {
			t.Fatalf("shadow read non-candidate slots: %+v", got.Shadow)
		}
		if got.Slots["role_fit"] != base.Slots["role_fit"] || got.Backend != base.Backend {
			t.Fatalf("primary answer changed: %+v", got)
		}
		ps, pr := dropPol.Evaluate(base)
		gs, gr := dropPol.Evaluate(got)
		if ps != posting.Dropped || gs != ps || gr != pr {
			t.Fatalf("plain %s (%s), shadowed %s (%s)", ps, pr, gs, gr)
		}
	}
	if f.calls.Load() != 40 {
		t.Fatalf("%d shadow calls for 40 candidates", f.calls.Load())
	}
	for _, names := range f.asked {
		if len(names) != 1 || names[0] != "role_fit" {
			t.Fatalf("asked %v", names)
		}
	}
}

// Postings that are not drop candidates are never sent to the shadow: below
// the rule's p, another value, an outage reading, a failed primary.
func TestDropShadowSkipsNonCandidates(t *testing.T) {
	f := &dropShadowFake{}
	q := triageQuestion("k")
	cases := map[string]decide.Answer{
		"below p": dropPrimary(q, 0.89),
	}
	other := dropPrimary(q, 0.99)
	rd := other.Slots["role_fit"]
	rd.Value = "strong_fit"
	other.Slots["role_fit"] = rd
	cases["other value"] = other
	out := dropPrimary(q, 0.99)
	rd = out.Slots["role_fit"]
	rd.Reading = decide.Unavailable
	out.Slots["role_fit"] = rd
	cases["unavailable"] = out
	for name, a := range cases {
		got, err := testDropShadower(&primaryStub{ans: a}, f, 1).Decide(context.Background(), q)
		if err != nil || got.Shadow != nil {
			t.Fatalf("%s: err %v shadow %+v", name, err, got.Shadow)
		}
	}
	boom := errors.New("down")
	if _, err := testDropShadower(&primaryStub{ans: dropPrimary(q, 0.99), err: boom}, f, 1).Decide(context.Background(), q); !errors.Is(err, boom) {
		t.Fatalf("primary error not returned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := testDropShadower(&primaryStub{ans: dropPrimary(q, 0.99)}, f, 1).Decide(ctx, q); got.Shadow != nil {
		t.Fatal("asked the shadow on a done context")
	}
	if f.calls.Load() != 0 {
		t.Fatalf("%d shadow calls, want 0", f.calls.Load())
	}
}

// A shadow that errors, panics, times out in the queue, returns nothing for the
// slot or an unusable value is recorded as not Answered: never an agreement, so
// no .disagree key and no agreement count, and the drop is unchanged.
func TestDropShadowUnavailableIsNeverAgreement(t *testing.T) {
	q := triageQuestion("k")
	for name, f := range map[string]*dropShadowFake{
		"error":    {reply: func(decide.Question) (decide.Answer, error) { return decide.Answer{}, errors.New("connection refused") }},
		"panic":    {reply: func(decide.Question) (decide.Answer, error) { panic("boom") }},
		"no slots": {reply: func(decide.Question) (decide.Answer, error) { return decide.Answer{Backend: dropShadowName}, nil }},
		"bad value": {reply: func(decide.Question) (decide.Answer, error) {
			return decide.Answer{Backend: dropShadowName, Slots: map[string]decide.SlotReading{"role_fit": {Reading: decide.Answered, Value: "zzz"}}}, nil
		}},
	} {
		t.Run(name, func(t *testing.T) {
			base := dropPrimary(q, 0.95)
			d := testDropShadower(&primaryStub{ans: base}, f, 1)
			got, err := d.Decide(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			sh, ok := got.Shadow["role_fit"]
			if !ok || sh.Reading == decide.Answered || sh.Value != "" || !strings.Contains(sh.Detail, "backend="+dropShadowName) {
				t.Fatalf("shadow %+v", sh)
			}
			if st, _ := dropPol.Evaluate(got); st != posting.Dropped {
				t.Fatalf("drop changed: %s", st)
			}
			if d.Candidates() != 1 || d.Answered() != 0 {
				t.Fatalf("candidates %d answered %d", d.Candidates(), d.Answered())
			}
		})
	}
	t.Run("waiting for the shadow ends with the context", func(t *testing.T) {
		f := &dropShadowFake{delay: time.Hour}
		d := testDropShadower(&primaryStub{ans: dropPrimary(q, 0.95)}, f, 1)
		d.slots <- struct{}{} // the one slot is held
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		got, err := d.Decide(ctx, q)
		if err != nil || got.Shadow["role_fit"].Reading != decide.Unavailable || f.calls.Load() != 0 {
			t.Fatalf("err %v shadow %+v calls %d", err, got.Shadow, f.calls.Load())
		}
	})
}

// Concurrent postings never put more than inFlight calls on the shadow, and none
// is skipped for it: every one of them carries a reading.
func TestDropShadowBoundsInFlightWithoutSkipping(t *testing.T) {
	const n, inFlight = 24, 2
	f := &dropShadowFake{delay: 3 * time.Millisecond}
	var wg sync.WaitGroup
	var answered atomic.Int64
	shared := testDropShadower(&primaryStub{ans: dropPrimary(triageQuestion("x"), 0.95)}, f, inFlight)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := shared.Decide(context.Background(), triageQuestion(fmt.Sprintf("gh:acme:%d", i)))
			if err == nil && got.Shadow["role_fit"].Reading == decide.Answered {
				answered.Add(1)
			}
		}()
	}
	wg.Wait()
	if answered.Load() != n || f.maxNow.Load() > inFlight || f.maxNow.Load() < 1 {
		t.Fatalf("answered %d of %d, peak in flight %d (limit %d)", answered.Load(), n, f.maxNow.Load(), inFlight)
	}
}

// Escalation and the drop-candidate shadow coexist: on one posting the
// high-p drop candidate is read by the shadow and a low-p slot by the
// escalation tier, both land in Shadow under their own backend names, and the
// drop policy ignores both.
func TestEscalateAndDropShadowCoexist(t *testing.T) {
	q := triageQuestion("k")
	base := dropPrimary(q, 0.95)
	var seniority decide.Slot
	for _, s := range q.Slots {
		if s.Name == "seniority" {
			seniority = s
		}
	}
	base.Slots["seniority"] = jevReading(seniority, 0.3) // below escalate below_p, not a drop rule slot
	ds := testDropShadower(&primaryStub{ans: base}, &dropShadowFake{}, 1)
	esc := &escFake{}
	e, _, _ := testEscalator(t, ds, esc, 0.5, 200)
	got, err := e.Decide(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if rf := got.Shadow["role_fit"]; rf.Reading != decide.Answered || !strings.Contains(rf.Detail, "backend="+dropShadowName) {
		t.Fatalf("role_fit shadow %+v", rf)
	}
	if sn := got.Shadow["seniority"]; sn.Reading != decide.Answered || !strings.Contains(sn.Detail, "backend="+escalateName) {
		t.Fatalf("seniority shadow %+v", sn)
	}
	if len(got.Shadow) != 2 || esc.calls.Load() != 1 || len(esc.asked[0]) != 1 || esc.asked[0][0] != "seniority" {
		t.Fatalf("shadow %+v escalation asked %v", got.Shadow, esc.asked)
	}
	if ps, pr := dropPol.Evaluate(base); func() bool { s, r := dropPol.Evaluate(got); return s != ps || r != pr || s != posting.Dropped }() {
		t.Fatalf("drop changed from %s (%s)", ps, pr)
	}
}

// End to end through the runner: a dropped posting carries the shadow's
// annotations, including triage.role_fit.disagree, and is still dropped; with
// the shadow down the posting is dropped the same and records unavailable.
func TestDropShadowAnnotatesDroppedPostings(t *testing.T) {
	for name, tc := range map[string]struct {
		f        *dropShadowFake
		reading  string
		disagree string
	}{
		"disagrees": {&dropShadowFake{}, "answered", "true"},
		"agrees": {&dropShadowFake{reply: func(q decide.Question) (decide.Answer, error) {
			return decide.Answer{Backend: dropShadowName, Slots: map[string]decide.SlotReading{"role_fit": {Reading: decide.Answered, Value: "mismatch_domain"}}}, nil
		}}, "answered", ""},
		"down": {&dropShadowFake{reply: func(decide.Question) (decide.Answer, error) { return decide.Answer{}, errors.New("down") }}, "unavailable", ""},
	} {
		t.Run(name, func(t *testing.T) {
			l := benchLedger(t, 3)
			primary := &jevPrimary{p: 0.95}
			d := testDropShadower(decide.NewRouter(primary), tc.f, 2)
			r := &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: d}, Policy: dropPol, SkipScoring: true, SkipExplain: true}
			if _, err := advanceAll(context.Background(), l.All(), nil, 3, 2, nil, r.Advance, func(string, ...any) {}); err != nil {
				t.Fatal(err)
			}
			for _, rec := range l.All() {
				if rec.Status != posting.Dropped {
					t.Fatalf("%s status %s, want dropped", rec.Key, rec.Status)
				}
				pre := ledger.AnnTriagePrefix + "role_fit"
				if rec.Annotations[pre+ledger.AnnTriageSuffixShadowReading] != tc.reading ||
					rec.Annotations[pre+ledger.AnnTriageSuffixShadowBackend] != dropShadowName ||
					rec.Annotations[pre+ledger.AnnTriageSuffixDisagree] != tc.disagree {
					t.Fatalf("%s annotations %v", rec.Key, rec.Annotations)
				}
			}
			if note := dropShadowNote(r); !strings.Contains(note, "of 3 drop candidate") {
				t.Fatalf("note %q", note)
			}
		})
	}
}

// jevPrimary is a d1-ultra backend reading role_fit mismatch_domain at p and
// every other slot at its first option.
type jevPrimary struct{ p float64 }

func (j *jevPrimary) Name() string       { return "d1-ultra" }
func (j *jevPrimary) Serves(string) bool { return true }
func (j *jevPrimary) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	a := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		v := s.Options[0]
		if s.Name == "role_fit" {
			v = "mismatch_domain"
		}
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: v, Jev: &decide.JevCalib{Probability: j.p}}
	}
	return a, nil
}

// decide.shadow_mode picks the wiring: sample keeps the gated router shadow;
// drop_candidates leaves the router without one and wraps it.
func TestShadowModeWiring(t *testing.T) {
	cfg := reasonsConfig(t)
	cfg.Decide.Shadow = "local"
	cfg.Decide.Local = &config.Local{Endpoint: "http://127.0.0.1:1/v1/systemone", Model: "d3-mini", Questions: "per_slot"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r, _ := runnerFor(t, cfg, t.TempDir())
	rt, ok := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if !ok || rt.Shadow == nil || rt.ShadowSample == nil {
		t.Fatalf("sample mode: decider %T", r.Triager.(*jdtriage.Triager).D)
	}
	cfg.Decide.ShadowMode = config.ShadowModeDropCandidates
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r, _ = runnerFor(t, cfg, t.TempDir())
	ds, ok := r.Triager.(*jdtriage.Triager).D.(*dropShadower)
	if !ok {
		t.Fatalf("drop_candidates mode: decider %T", r.Triager.(*jdtriage.Triager).D)
	}
	if rt, ok := ds.primary.(*decide.Router); !ok || rt.Shadow != nil || rt.ShadowSample != nil {
		t.Fatalf("drop_candidates mode left a sampled shadow on the router: %T", ds.primary)
	}
	if !strings.HasPrefix(ds.name, "local-") {
		t.Fatalf("shadow name %q", ds.name)
	}
}
