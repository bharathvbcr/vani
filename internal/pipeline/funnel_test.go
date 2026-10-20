package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/decide"
)

// ---- fakes ----

// seqTriager answers from a list, one per call, and counts calls.
type seqTriager struct {
	mu      sync.Mutex
	answers []decide.Answer
	calls   int
	log     *callLog
	err     error
}

func (s *seqTriager) Triage(context.Context, posting.Posting) (decide.Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.add("triage")
	i := s.calls
	s.calls++
	if s.err != nil {
		return decide.Answer{}, s.err
	}
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	return s.answers[i], nil
}

type callLog struct {
	mu sync.Mutex
	v  []string
}

func (c *callLog) add(s string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.v = append(c.v, s)
	c.mu.Unlock()
}

func (c *callLog) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.v, ",")
}

type logScorer struct {
	log    *callLog
	cov    float64
	gapErr error
}

func (s logScorer) Gap(context.Context, string, string) (resume.Gap, error) {
	s.log.add("gap")
	return resume.Gap{CoveragePct: s.cov}, s.gapErr
}

func (s logScorer) Score(context.Context, string, string) (resume.Score, error) {
	s.log.add("score")
	return resume.Score{}, nil
}

type countExplainer struct {
	mu    sync.Mutex
	calls int
}

func (c *countExplainer) Explain(context.Context, ledger.Record) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return "note", nil
}

type fakeSpend struct{ err error }

func (f fakeSpend) Allow(context.Context, string) error           { return f.err }
func (f fakeSpend) Charge(context.Context, string, float64) error { return nil }

func okAns(slot string, p float64) decide.Answer {
	return decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev(slot, p)}}
}

// dupPost is a posting whose title, location and description are fixed, so two
// of them (different ids) are the same reading.
func dupPost(t *testing.T, id, loc, desc string) posting.Posting {
	t.Helper()
	p := ghPost(t, id, desc)
	p.Title, p.Location = "ML Engineer", loc
	return p
}

func reconcileFunnel(t *testing.T, l ledger.Ledger, ps ...posting.Posting) {
	t.Helper()
	if _, err := Reconcile(ctx, l, board, ps, t0); err != nil {
		t.Fatal(err)
	}
}

func newRunner(l ledger.Ledger, tr Triager, f Funnel) *Runner {
	return &Runner{Ledger: l, Triager: tr, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "n"}, Policy: policy,
		Now: func() time.Time { return t0.Add(time.Minute) }, Funnel: f}
}

const longDesc = "Build and operate ML inference systems at scale."

// ---- 1 + 2: fail-open counters, empty description ----

func TestEmptyDescriptionSkipsPaidCallAndKeeps(t *testing.T) {
	for _, desc := range []string{"", "   \n\t ", "x"} {
		t.Run(fmt.Sprintf("%q", desc), func(t *testing.T) {
			l := openLedger(t)
			p := dupPost(t, "1", "Remote", desc)
			reconcileFunnel(t, l, p)
			tr := &seqTriager{answers: []decide.Answer{okAns("onsite", 0.99)}}
			fo := &FailOpen{}
			r := newRunner(l, tr, Funnel{FailOpen: fo, MinDescriptionRunes: 5})
			if err := r.Advance(ctx, p.Key); err != nil {
				t.Fatal(err)
			}
			if tr.calls != 0 {
				t.Fatalf("paid triage called %d times for an empty description", tr.calls)
			}
			got := rec(t, l, p.Key)
			if got.Status == posting.Dropped {
				t.Fatal("empty description was dropped")
			}
			if got.Annotations[AnnTriageSkipped] != CauseEmptyDescription || got.Annotations["triage.unsure"] == "" {
				t.Fatalf("skip not annotated: %v", got.Annotations)
			}
			if _, has := got.Annotations["triage.backend"]; has {
				t.Fatal("skipped posting carries a backend reading")
			}
			if c := fo.Counts(); c.EmptyDescription != 1 || c.Total() != 1 {
				t.Fatalf("counts %+v", c)
			}
		})
	}
}

func TestFailOpenCausesEachCountAndAPassDoesNot(t *testing.T) {
	// Every cause increments its own counter, and an unknown one is not lost.
	fo := &FailOpen{}
	for _, c := range []string{CauseEmptyDescription, CauseTitleUnread, CauseLocationUnreadable, CausePrescoreFailed, "novel"} {
		before := fo.Counts().Total()
		fo.Note(c)
		if fo.Counts().Total() != before+1 {
			t.Fatalf("%s did not count", c)
		}
	}
	if got := fo.Counts(); got != (FailOpenCounts{EmptyDescription: 1, TitleUnread: 1, LocationUnreadable: 1, PrescoreFailed: 1, Other: 1}) {
		t.Fatalf("counts %+v", got)
	}
	var none *FailOpen
	none.Note(CauseTitleUnread) // nil counts nothing, never panics
	if none.Counts().Total() != 0 {
		t.Fatal("nil counter counted")
	}

	// A posting whose checks all ran counts nothing.
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", longDesc)
	reconcileFunnel(t, l, p)
	pol := policy
	pol.ReadTitle = func(string) []TitleReading { return nil }
	f := &FailOpen{}
	r := newRunner(l, &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}, Funnel{FailOpen: f, MinDescriptionRunes: 5, ScoreFirst: true})
	r.Policy = pol
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	if f.Counts().Total() != 0 {
		t.Fatalf("a passing posting was counted: %+v", f.Counts())
	}

	// A blank title with title readings configured could not be read.
	l2 := openLedger(t)
	q := dupPost(t, "2", "Remote", longDesc)
	q.Title = " "
	reconcileFunnel(t, l2, q)
	f2 := &FailOpen{}
	r2 := newRunner(l2, &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}, Funnel{FailOpen: f2})
	r2.Policy = pol
	if err := r2.Advance(ctx, q.Key); err != nil {
		t.Fatal(err)
	}
	if f2.Counts().TitleUnread != 1 {
		t.Fatalf("blank title not counted: %+v", f2.Counts())
	}
}

// ---- 3: reading inheritance ----

func TestIdenticalPostingsShareOneCall(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if tr.calls != 1 {
		t.Fatalf("%d backend calls for two identical postings, want 1", tr.calls)
	}
	ra, rb := rec(t, l, a.Key), rec(t, l, b.Key)
	if ra.Annotations[AnnTriageInheritedFrom] != "" {
		t.Fatal("the asked posting is marked inherited")
	}
	if rb.Annotations[AnnTriageInheritedFrom] != string(a.Key) || rb.Annotations["triage.remote"] != "remote" || rb.Annotations["triage.backend"] != "d1-ultra" {
		t.Fatalf("copy not marked or incomplete: %v", rb.Annotations)
	}
}

func TestInheritanceConcurrentAskedOnce(t *testing.T) {
	l := openLedger(t)
	var keys []posting.Key
	var ps []posting.Posting
	for i := range 8 {
		p := dupPost(t, fmt.Sprint(i+1), "Remote", longDesc)
		ps, keys = append(ps, p), append(keys, p.Key)
	}
	reconcileFunnel(t, l, ps...)
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Advance(ctx, k); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if tr.calls != 1 {
		t.Fatalf("%d backend calls for eight identical concurrent postings, want 1", tr.calls)
	}
}

func TestDifferentLocationNeverInheritsADrop(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Austin, TX", longDesc)
	reconcileFunnel(t, l, a, b)
	// The first reading drops; the second location must be read by itself.
	tr := &seqTriager{answers: []decide.Answer{okAns("onsite", 0.99), okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if rec(t, l, a.Key).Status != posting.Dropped {
		t.Fatalf("first posting %s, want dropped", rec(t, l, a.Key).Status)
	}
	rb := rec(t, l, b.Key)
	if tr.calls != 2 || rb.Status == posting.Dropped || rb.Annotations[AnnTriageInheritedFrom] != "" {
		t.Fatalf("other location: calls %d status %s ann %v", tr.calls, rb.Status, rb.Annotations)
	}
}

func TestInheritedDropRerunsTheDropRule(t *testing.T) {
	// Same description, title and location: the reading is copied, and the
	// drop rule decides again under the policy in force, not the old verdict.
	l := openLedger(t)
	a, b, c := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc), dupPost(t, "3", "Remote", longDesc)
	reconcileFunnel(t, l, a, b, c)
	cache := NewReadCache()
	tr := &seqTriager{answers: []decide.Answer{okAns("onsite", 0.95)}}
	r := newRunner(l, tr, Funnel{Cache: cache, ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if s := rec(t, l, b.Key).Status; s != posting.Dropped || tr.calls != 1 {
		t.Fatalf("same-location copy: status %s calls %d, want a dropped copy of one call", s, tr.calls)
	}
	r.Policy = DropPolicy{Rules: []DropRule{{Slot: "remote", Values: []string{"onsite"}, MinOpenRouterProb: 0.99}}, OpenRouter: "d1-ultra"}
	if err := r.Advance(ctx, c.Key); err != nil {
		t.Fatal(err)
	}
	if s := rec(t, l, c.Key).Status; s == posting.Dropped || tr.calls != 1 {
		t.Fatalf("stricter policy: status %s calls %d, want kept, still one call", s, tr.calls)
	}
}

func TestModelChangeReasks(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	// A fresh run under another model id must not reuse the persisted reading.
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m2"}).Advance(ctx, b.Key); err != nil {
		t.Fatal(err)
	}
	if tr.calls != 2 || rec(t, l, b.Key).Annotations[AnnTriageInheritedFrom] != "" {
		t.Fatalf("model change reused a reading: calls %d", tr.calls)
	}
	if rec(t, l, b.Key).Annotations[AnnTriageModel] != "m2" {
		t.Fatalf("model id not recorded: %v", rec(t, l, b.Key).Annotations)
	}
}

func TestPersistedReadingInheritedAcrossRuns(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, b.Key); err != nil {
		t.Fatal(err)
	}
	if tr.calls != 1 || rec(t, l, b.Key).Annotations[AnnTriageInheritedFrom] != string(a.Key) {
		t.Fatalf("persisted reading not reused: calls %d ann %v", tr.calls, rec(t, l, b.Key).Annotations)
	}
}

func TestUnreadAndUnsureReadingsAreNotInherited(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{
		{Backend: "llm", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Abstained}}}, okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if tr.calls != 2 || rec(t, l, b.Key).Annotations[AnnTriageInheritedFrom] != "" {
		t.Fatalf("an unsure reading was copied: calls %d", tr.calls)
	}
}

func TestTriageFailureReleasesWaiters(t *testing.T) {
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{err: errors.New("down")}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err == nil {
			t.Fatal("expected the backend error")
		}
	}
	if tr.calls != 2 {
		t.Fatalf("a failed read blocked its twin: calls %d", tr.calls)
	}
}

// ---- 4: notes gate and budget ----

func scoredRunner(t *testing.T, cov float64, g *NotesGate) (*Runner, *countExplainer, *ledger.File, posting.Key) {
	t.Helper()
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", longDesc)
	reconcileFunnel(t, l, p)
	ex := &countExplainer{}
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Notes: g})
	r.Scorer = logScorer{cov: cov}
	r.Explainer = ex
	return r, ex, l, p.Key
}

func TestNotesBelowFloorMakeNoCall(t *testing.T) {
	g := &NotesGate{MinRelevance: 40, Relevance: fromRank}
	r, ex, l, k := scoredRunner(t, 20, g)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if ex.calls != 0 || got.Status != posting.Explained || got.Annotations[AnnNotesSkipped] == "" || got.Annotations[ledger.AnnExplanation] != "" {
		t.Fatalf("calls %d status %s ann %v", ex.calls, got.Status, got.Annotations)
	}
	if g.Counts().Below != 1 {
		t.Fatalf("counts %+v", g.Counts())
	}
}

func TestNotesAtOrAboveFloorAsk(t *testing.T) {
	r, ex, l, k := scoredRunner(t, 40, &NotesGate{MinRelevance: 40, Relevance: fromRank})
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if ex.calls != 1 || rec(t, l, k).Annotations[ledger.AnnExplanation] != "note" {
		t.Fatalf("calls %d ann %v", ex.calls, rec(t, l, k).Annotations)
	}
}

func TestNotesUnknownRelevanceDeferredNotDone(t *testing.T) {
	for _, cov := range []float64{0} { // zero coverage is the failed-scoring signature
		g := &NotesGate{MinRelevance: 40, Relevance: fromRank}
		r, ex, l, k := scoredRunner(t, cov, g)
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
		got := rec(t, l, k)
		if ex.calls != 0 || got.Status != posting.Scored || got.Annotations[AnnNotesDeferred] != DeferUnknownRelevance {
			t.Fatalf("calls %d status %s ann %v", ex.calls, got.Status, got.Annotations)
		}
		if !NotesDeferred(got) {
			t.Fatal("NotesDeferred false for a deferred record")
		}
		last := got.LastEvent
		r.Now = func() time.Time { return t0.Add(time.Hour) }
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
		if !rec(t, l, k).LastEvent.Equal(last) {
			t.Fatal("a repeated deferral wrote another ledger event")
		}
	}
	// No scoring at all (SkipScoring) leaves relevance unknown too.
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", longDesc)
	reconcileFunnel(t, l, p)
	ex := &countExplainer{}
	r := newRunner(l, &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}, Funnel{Notes: &NotesGate{MinRelevance: 1, Relevance: fromRank}})
	r.SkipScoring, r.Explainer = true, ex
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, p.Key); ex.calls != 0 || got.Status != posting.Scored {
		t.Fatalf("calls %d status %s", ex.calls, got.Status)
	}
}

func TestNotesBudgetSpentDefersAndTriageStillRuns(t *testing.T) {
	g := &NotesGate{MinRelevance: 10, Relevance: fromRank, Spend: fakeSpend{err: decide.ErrBudget}, SpendKey: "notes"}
	l := openLedger(t)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc+" two")
	reconcileFunnel(t, l, a, b)
	ex := &countExplainer{}
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}}
	r := newRunner(l, tr, Funnel{Notes: g})
	r.Scorer, r.Explainer = logScorer{cov: 80}, ex
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatalf("a spent notes budget failed the posting: %v", err)
		}
	}
	if tr.calls != 2 || ex.calls != 0 {
		t.Fatalf("triage calls %d (want 2), notes calls %d (want 0)", tr.calls, ex.calls)
	}
	for _, k := range []posting.Key{a.Key, b.Key} {
		if got := rec(t, l, k); got.Status != posting.Scored || got.Annotations[AnnNotesDeferred] != DeferBudget {
			t.Fatalf("%s: status %s ann %v", k, got.Status, got.Annotations)
		}
	}
	if g.Counts().BudgetSpent != 2 {
		t.Fatalf("counts %+v", g.Counts())
	}
	// Budget back (a new day): the deferred note is asked for and clears the mark.
	r.Notes = &NotesGate{MinRelevance: 10, Relevance: fromRank, Spend: fakeSpend{}, SpendKey: "notes"}
	if err := r.Advance(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, a.Key); got.Status != posting.Explained || got.Annotations[AnnNotesDeferred] != "" || ex.calls != 1 {
		t.Fatalf("status %s ann %v calls %d", got.Status, got.Annotations, ex.calls)
	}
}

func TestNotesBudgetErrorFailsTheStageNotTheGate(t *testing.T) {
	boom := errors.New("budget file unreadable")
	r, ex, l, k := scoredRunner(t, 80, &NotesGate{MinRelevance: 10, Relevance: fromRank, Spend: fakeSpend{err: boom}})
	if err := r.Advance(ctx, k); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if ex.calls != 0 || rec(t, l, k).Status != posting.Scored {
		t.Fatal("a budget that could not be read let the note through or marked it done")
	}
}

// ---- 5: DevPrism before triage ----

func TestScoreFirstRunsDevPrismBeforeTriageAndDoesNotGate(t *testing.T) {
	for _, cov := range []float64{73, 0} {
		l := openLedger(t)
		p := dupPost(t, "1", "Remote", longDesc)
		reconcileFunnel(t, l, p)
		log := &callLog{}
		tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}, log: log}
		r := newRunner(l, tr, Funnel{ScoreFirst: true})
		r.Scorer = logScorer{log: log, cov: cov}
		if err := r.Advance(ctx, p.Key); err != nil {
			t.Fatal(err)
		}
		if got := log.String(); got != "gap,score,triage" {
			t.Fatalf("coverage %v: call order %q, want gap,score,triage (each scored once)", cov, got)
		}
		got := rec(t, l, p.Key)
		if tr.calls != 1 || got.Status != posting.Explained || got.Annotations[ledger.AnnCoverage] == "" {
			t.Fatalf("coverage %v: status %s calls %d ann %v", cov, got.Status, tr.calls, got.Annotations)
		}
	}
}

func TestScoreFirstFailureDoesNotStopTriage(t *testing.T) {
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", longDesc)
	reconcileFunnel(t, l, p)
	log := &callLog{}
	tr := &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}, log: log}
	fo := &FailOpen{}
	r := newRunner(l, tr, Funnel{ScoreFirst: true, FailOpen: fo})
	r.Scorer = logScorer{log: log, gapErr: errors.New("devprism down")}
	if err := r.Advance(ctx, p.Key); err == nil {
		t.Fatal("the score stage should still report the failure")
	}
	if tr.calls != 1 || rec(t, l, p.Key).Status != posting.Triaged {
		t.Fatalf("triage gated on DevPrism: calls %d status %s", tr.calls, rec(t, l, p.Key).Status)
	}
	if fo.Counts().PrescoreFailed != 1 {
		t.Fatalf("counts %+v", fo.Counts())
	}
}

func TestDroppedPostingKeepsNoPreScore(t *testing.T) {
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", longDesc)
	reconcileFunnel(t, l, p)
	r := newRunner(l, &seqTriager{answers: []decide.Answer{okAns("onsite", 0.99)}}, Funnel{ScoreFirst: true})
	r.Scorer = logScorer{cov: 50}
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, p.Key)
	if got.Status != posting.Dropped || got.Annotations[ledger.AnnRank] != "" {
		t.Fatalf("status %s ann %v", got.Status, got.Annotations)
	}
}

func TestBlankDescriptionIsNotScored(t *testing.T) {
	l := openLedger(t)
	p := dupPost(t, "1", "Remote", "")
	reconcileFunnel(t, l, p)
	log := &callLog{}
	r := newRunner(l, &seqTriager{answers: []decide.Answer{okAns("remote", 0.99)}, log: log}, Funnel{MinDescriptionRunes: 5})
	r.Scorer = logScorer{log: log, gapErr: errors.New("empty jd")}
	r.SkipExplain = true
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatalf("a blank description failed the score stage: %v", err)
	}
	if got := rec(t, l, p.Key); log.String() != "" || got.Annotations[ledger.AnnRank] != "" {
		t.Fatalf("calls %q ann %v", log.String(), got.Annotations)
	}
}
