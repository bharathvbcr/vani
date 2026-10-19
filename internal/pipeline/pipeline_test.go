package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/decide"
)

var (
	t0    = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	board = sources.Board{ATS: posting.Greenhouse, Token: "Acme", Company: "Acme"}
	ctx   = context.Background()
)

func openLedger(t *testing.T) *ledger.File {
	t.Helper()
	l, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func ghPost(t *testing.T, id, desc string) posting.Posting {
	t.Helper()
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Engineer " + id, Location: "Remote", Description: desc}
}

func rec(t *testing.T, l ledger.Ledger, k posting.Key) ledger.Record {
	t.Helper()
	r, ok := l.Get(k)
	if !ok {
		t.Fatalf("no record %s", k)
	}
	return r
}

func TestReconcileNewChangedUnchanged(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "build things")
	s, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0)
	if err != nil || s.New != 1 {
		t.Fatalf("new: %+v %v", s, err)
	}
	r := rec(t, l, p.Key)
	if r.Status != posting.Live || r.Annotations[DescHashKey] != p.DescriptionHash() {
		t.Fatalf("bad new record: %+v", r)
	}

	// Unchanged (whitespace-only re-render): no new event.
	p2 := p
	p2.Description = "build   things\n"
	before := rec(t, l, p.Key).LastEvent
	s, _ = Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
	if s.Unchanged != 1 || !rec(t, l, p.Key).LastEvent.Equal(before) {
		t.Fatalf("unchanged wrote an event: %+v", s)
	}

	// Changed: derived annotations are forgotten.
	l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Explained, Annotations: map[string]string{
		"triage.remote": "yes", "triage.backend": "jev", "score.total": "5", "gap.coverage_pct": "9", "explanation": "e", "other": "keep"}})
	p3 := p
	p3.Description = "something else entirely"
	s, _ = Reconcile(ctx, l, board, []posting.Posting{p3}, t0.Add(2*time.Hour))
	r = rec(t, l, p.Key)
	if s.Changed != 1 || r.Status != posting.Live {
		t.Fatalf("changed: %+v %+v", s, r)
	}
	for _, k := range []string{"triage.remote", "triage.backend", "score.total", "gap.coverage_pct", "explanation"} {
		if _, ok := r.Annotations[k]; ok {
			t.Errorf("annotation %s survived a change", k)
		}
	}
	if r.Annotations["other"] != "keep" || r.Annotations[DescHashKey] != p3.DescriptionHash() {
		t.Fatalf("annotations: %v", r.Annotations)
	}
}

func TestAbsentOnceThenTwiceIsDead(t *testing.T) {
	l := openLedger(t)
	a, b := ghPost(t, "1", "a"), ghPost(t, "2", "b")
	Reconcile(ctx, l, board, []posting.Posting{a, b}, t0)

	s, _ := Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(time.Hour))
	if s.Missed != 1 || s.Dead != 0 {
		t.Fatalf("first miss: %+v", s)
	}
	rb := rec(t, l, b.Key)
	if rb.MissedRuns != 1 || rb.Status != posting.Live {
		t.Fatalf("after one miss: %+v", rb)
	}
	s, _ = Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(2*time.Hour))
	rb = rec(t, l, b.Key)
	if s.Dead != 1 || rb.Status != posting.Dead || rb.MissedRuns != 2 {
		t.Fatalf("second miss: %+v %+v", s, rb)
	}
	if ra := rec(t, l, a.Key); ra.Status != posting.Live || ra.MissedRuns != 0 {
		t.Fatalf("present posting disturbed: %+v", ra)
	}
	// A third listing without it does nothing more to a dead record.
	s, _ = Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(3*time.Hour))
	if s.Dead != 0 || s.Missed != 0 {
		t.Fatalf("dead record touched: %+v", s)
	}
}

func TestAbsentDigestedPostingsBecomeDead(t *testing.T) {
	l := openLedger(t)
	a, b := ghPost(t, "1", "a"), ghPost(t, "2", "b")
	Reconcile(ctx, l, board, []posting.Posting{a, b}, t0)
	// Progress b to Digested
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0.Add(time.Minute), Key: b.Key, Status: posting.Digested}); err != nil {
		t.Fatal(err)
	}
	// First run without b: tracks as absent and increments missed runs
	s, err := Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(time.Hour))
	if err != nil || s.Missed != 1 {
		t.Fatalf("first miss: %+v %v", s, err)
	}
	rb := rec(t, l, b.Key)
	if rb.MissedRuns != 1 || rb.Status != posting.Digested {
		t.Fatalf("after one miss: status=%v missed=%d", rb.Status, rb.MissedRuns)
	}
	// Second run without b: transitions to Dead and sets DiedFrom
	s, err = Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(2*time.Hour))
	if err != nil || s.Dead != 1 {
		t.Fatalf("second miss: %+v %v", s, err)
	}
	rb = rec(t, l, b.Key)
	if rb.Status != posting.Dead || rb.MissedRuns != 2 {
		t.Fatalf("after two misses: status=%v missed=%d", rb.Status, rb.MissedRuns)
	}
	if rb.Annotations[DiedFromKey] != string(posting.Digested) {
		t.Fatalf("died from annotation: %q", rb.Annotations[DiedFromKey])
	}
}

func TestAbsentIgnoresOtherBoards(t *testing.T) {
	l := openLedger(t)
	other := sources.Board{ATS: posting.Greenhouse, Token: "acmecorp"} // shares the "acme" prefix text
	k, _ := posting.GreenhouseKey("acmecorp", "9")
	Reconcile(ctx, l, other, []posting.Posting{{Key: k, ATS: posting.Greenhouse, Company: "AcmeCorp", Title: "t", Description: "d"}}, t0)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
	if r := rec(t, l, k); r.Status != posting.Live || r.MissedRuns != 0 {
		t.Fatalf("another board's posting was aged: %+v", r)
	}
}

func TestReappearanceResetsMissedRuns(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	Reconcile(ctx, l, board, []posting.Posting{a}, t0)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	if rec(t, l, a.Key).MissedRuns != 1 {
		t.Fatal("miss not recorded")
	}
	s, _ := Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(2*time.Hour))
	if s.Reappeared != 1 || rec(t, l, a.Key).MissedRuns != 0 {
		t.Fatalf("not reset: %+v %+v", s, rec(t, l, a.Key))
	}
	// So one more miss is only a first miss again.
	Reconcile(ctx, l, board, nil, t0.Add(3*time.Hour))
	if r := rec(t, l, a.Key); r.Status == posting.Dead || r.MissedRuns != 1 {
		t.Fatalf("miss count not restarted: %+v", r)
	}
}

func TestDeadPostingListedAgainIsLive(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	Reconcile(ctx, l, board, []posting.Posting{a}, t0)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
	if rec(t, l, a.Key).Status != posting.Dead {
		t.Fatal("setup")
	}
	Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(3*time.Hour))
	if r := rec(t, l, a.Key); r.Status != posting.Live || r.MissedRuns != 0 {
		t.Fatalf("not revived: %+v", r)
	}
}

// digestPosting lists p on board and marks it digested, as a published digest does.
func digestPosting(t *testing.T, l ledger.Ledger, p posting.Posting) {
	t.Helper()
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Digested, Reason: "digest"}); err != nil {
		t.Fatal(err)
	}
}

// A digested posting the employer takes down ages like any other listed one:
// one miss is counted, the second kills it.
func TestDigestedAbsentTwiceIsDead(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	digestPosting(t, l, a)

	s, err := Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	if err != nil || s.Missed != 1 || s.Dead != 0 {
		t.Fatalf("first miss: %+v %v", s, err)
	}
	if r := rec(t, l, a.Key); r.Status != posting.Digested || r.MissedRuns != 1 {
		t.Fatalf("after one miss: %+v", r)
	}
	s, err = Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
	if err != nil || s.Dead != 1 {
		t.Fatalf("second miss: %+v %v", s, err)
	}
	if r := rec(t, l, a.Key); r.Status != posting.Dead || r.MissedRuns != 2 {
		t.Fatalf("after two misses: %+v", r)
	}
}

func TestDigestedReappearanceResetsMissedRuns(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	digestPosting(t, l, a)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	if r := rec(t, l, a.Key); r.MissedRuns != 1 {
		t.Fatalf("miss not recorded: %+v", r)
	}
	s, _ := Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(2*time.Hour))
	if r := rec(t, l, a.Key); s.Reappeared != 1 || r.Status != posting.Digested || r.MissedRuns != 0 {
		t.Fatalf("reappearance: %+v %+v", s, r)
	}
	Reconcile(ctx, l, board, nil, t0.Add(3*time.Hour))
	if r := rec(t, l, a.Key); r.Status != posting.Digested || r.MissedRuns != 1 {
		t.Fatalf("miss count not restarted: %+v", r)
	}
}

// A posting that died and is listed again with the same content returns to the
// status it died in, with its annotations, so a digested posting is neither
// triaged again (tokens) nor sent in a second digest.
func TestDeadListedAgainRestoresItsStatus(t *testing.T) {
	for _, st := range []posting.Status{posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested} {
		t.Run(string(st), func(t *testing.T) {
			l := openLedger(t)
			a := ghPost(t, "1", "a")
			Reconcile(ctx, l, board, []posting.Posting{a}, t0)
			if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: a.Key, Status: st,
				Annotations: map[string]string{"triage.remote": "yes"}, Reason: "setup"}); err != nil {
				t.Fatal(err)
			}
			Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
			Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
			if r := rec(t, l, a.Key); r.Status != posting.Dead || r.Annotations[DiedFromKey] != string(st) {
				t.Fatalf("setup: %+v", r)
			}
			s, err := Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(3*time.Hour))
			if err != nil || s.Reappeared != 1 {
				t.Fatalf("relist: %+v %v", s, err)
			}
			r := rec(t, l, a.Key)
			if r.Status != st || r.MissedRuns != 0 || r.Annotations["triage.remote"] != "yes" {
				t.Fatalf("not restored to %s: %+v", st, r)
			}
			if _, ok := r.Annotations[DiedFromKey]; ok {
				t.Fatalf("%s survived the revival: %v", DiedFromKey, r.Annotations)
			}
		})
	}
}

// A dead record with no usable died_from (written before it existed, or not a
// status a listed posting can hold) is revived to live, as before.
func TestDeadWithoutUsableDiedFromIsLive(t *testing.T) {
	for name, from := range map[string]string{"none": "", "closed": string(posting.Dead), "unknown": "archived", "duplicate": string(posting.Duplicate)} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			a := ghPost(t, "1", "a")
			Reconcile(ctx, l, board, []posting.Posting{a}, t0)
			ann := map[string]string{}
			if from != "" {
				ann[DiedFromKey] = from
			}
			if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: a.Key, Status: posting.Dead, Annotations: ann, Reason: "setup"}); err != nil {
				t.Fatal(err)
			}
			Reconcile(ctx, l, board, []posting.Posting{a}, t0.Add(time.Hour))
			if r := rec(t, l, a.Key); r.Status != posting.Live {
				t.Fatalf("revived to %s, want live: %+v", r.Status, r)
			}
		})
	}
}

// A dead posting relisted with new content is a changed posting: it goes live
// and forgets both its derived results and where it died.
func TestDeadListedAgainChangedIsLive(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	digestPosting(t, l, a)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
	a2 := a
	a2.Description = "rewritten"
	s, _ := Reconcile(ctx, l, board, []posting.Posting{a2}, t0.Add(3*time.Hour))
	r := rec(t, l, a.Key)
	if s.Changed != 1 || r.Status != posting.Live {
		t.Fatalf("changed relist: %+v %+v", s, r)
	}
	if _, ok := r.Annotations[DiedFromKey]; ok {
		t.Fatalf("%s survived a change: %v", DiedFromKey, r.Annotations)
	}
}

// A closed record (dead, duplicate, dropped) is not aged by absence: it gets
// no event at all.
func TestAbsenceLeavesClosedRecordsAlone(t *testing.T) {
	for _, st := range []posting.Status{posting.Dead, posting.Duplicate, posting.Dropped} {
		t.Run(string(st), func(t *testing.T) {
			l := openLedger(t)
			a := ghPost(t, "1", "a")
			Reconcile(ctx, l, board, []posting.Posting{a}, t0)
			if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: a.Key, Status: st, Reason: "setup"}); err != nil {
				t.Fatal(err)
			}
			before := rec(t, l, a.Key)
			for i := 1; i <= 3; i++ {
				s, err := Reconcile(ctx, l, board, nil, t0.Add(time.Duration(i)*time.Hour))
				if err != nil || s.Missed != 0 || s.Dead != 0 {
					t.Fatalf("run %d: %+v %v", i, s, err)
				}
			}
			r := rec(t, l, a.Key)
			if r.Status != st || r.MissedRuns != before.MissedRuns || !r.LastEvent.Equal(before.LastEvent) {
				t.Fatalf("closed record touched: %+v", r)
			}
		})
	}
}

// Dedupe still treats a digested weak record as settled: an ATS twin arriving
// later does not retroactively mark what the user was already shown.
func TestDigestedWeakRecordIsNotMarkedDuplicate(t *testing.T) {
	l := openLedger(t)
	fk, _ := posting.FallbackKey("Acme", "Engineer 1", "Remote")
	aggBoard := sources.Board{ATS: posting.Other, Token: "aggregator"}
	Reconcile(ctx, l, aggBoard, []posting.Posting{{Key: fk, ATS: posting.Other, Company: "Acme", Title: "Engineer 1", Location: "Remote", Description: "x"}}, t0)
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: fk, Status: posting.Digested, Reason: "digest"}); err != nil {
		t.Fatal(err)
	}
	s, err := Reconcile(ctx, l, board, []posting.Posting{ghPost(t, "1", "d")}, t0.Add(time.Hour))
	if err != nil || s.Duplicate != 0 {
		t.Fatalf("dedupe: %+v %v", s, err)
	}
	if r := rec(t, l, fk); r.Status != posting.Digested {
		t.Fatalf("digested weak record re-marked: %+v", r)
	}
}

func TestCrossSourceDedupe(t *testing.T) {
	l := openLedger(t)
	ats := ghPost(t, "1", "desc")
	Reconcile(ctx, l, board, []posting.Posting{ats}, t0)

	fk, _ := posting.FallbackKey("ACME", "engineer 1!", "remote")
	agg := posting.Posting{Key: fk, ATS: posting.Other, Company: "ACME", Title: "engineer 1!", Location: "remote", Description: "x"}
	aggBoard := sources.Board{ATS: posting.Other, Token: "aggregator"}
	s, err := Reconcile(ctx, l, aggBoard, []posting.Posting{agg}, t0.Add(time.Hour))
	if err != nil || s.Duplicate != 1 || s.New != 0 {
		t.Fatalf("dedupe: %+v %v", s, err)
	}
	r := rec(t, l, fk)
	if r.Status != posting.Duplicate || r.Annotations[DuplicateOfKey] != string(ats.Key) {
		t.Fatalf("not marked duplicate: %+v", r)
	}
	// Re-listing the duplicate does not resurrect it.
	Reconcile(ctx, l, aggBoard, []posting.Posting{agg}, t0.Add(2*time.Hour))
	if rec(t, l, fk).Status != posting.Duplicate {
		t.Fatal("duplicate resurrected")
	}
	// A non-matching fallback posting is just new.
	fk2, _ := posting.FallbackKey("Acme", "Other role", "")
	s, _ = Reconcile(ctx, l, aggBoard, []posting.Posting{{Key: fk2, ATS: posting.Other, Company: "Acme", Title: "Other role"}}, t0.Add(3*time.Hour))
	if s.New != 1 || rec(t, l, fk2).Status != posting.Live {
		t.Fatalf("non-duplicate: %+v", s)
	}
}

func TestCrossSourceDedupeFallbackFirst(t *testing.T) {
	l := openLedger(t)
	fk, _ := posting.FallbackKey("Acme", "Engineer 1", "Remote")
	aggBoard := sources.Board{ATS: posting.Other, Token: "aggregator"}
	Reconcile(ctx, l, aggBoard, []posting.Posting{{Key: fk, ATS: posting.Other, Company: "Acme", Title: "Engineer 1", Location: "Remote", Description: "x"}}, t0)
	Reconcile(ctx, l, board, []posting.Posting{ghPost(t, "1", "d")}, t0.Add(time.Hour))
	if r := rec(t, l, fk); r.Status != posting.Duplicate {
		t.Fatalf("earlier fallback record not deduped: %+v", r)
	}
}

func TestReconcileSkipsInvalidAndRepeatedKeys(t *testing.T) {
	l := openLedger(t)
	a := ghPost(t, "1", "a")
	s, err := Reconcile(ctx, l, board, []posting.Posting{{Title: "no key"}, {Key: "zz:1"}, a, a}, t0)
	if err != nil || s.Invalid != 2 || s.New != 1 || len(l.All()) != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestReconcileCancelled(t *testing.T) {
	l := openLedger(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Reconcile(c, l, board, []posting.Posting{ghPost(t, "1", "a")}, t0); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// ---- runner ----

type fakeTriager struct {
	ans   decide.Answer
	err   error
	calls int
}

func (f *fakeTriager) Triage(context.Context, posting.Posting) (decide.Answer, error) {
	f.calls++
	return f.ans, f.err
}

type fakeScorer struct{ gapErr, scoreErr error }

func (f fakeScorer) Gap(context.Context, string, string) (resume.Gap, error) {
	return resume.Gap{CoveragePct: 62.5}, f.gapErr
}
func (f fakeScorer) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{BlockIDs: []string{"b1", "b2"}}, f.scoreErr
}

type fakeExplainer struct {
	text string
	err  error
}

func (f fakeExplainer) Explain(context.Context, ledger.Record) (string, error) { return f.text, f.err }

func jev(value string, p float64) decide.SlotReading {
	return decide.SlotReading{Reading: decide.Answered, Value: value, Jev: &decide.JevCalib{Probability: p}}
}

// tagged marks a reading the way decide.Router does when it merges slots
// answered by different backends.
func tagged(sr decide.SlotReading, backend string) decide.SlotReading {
	if sr.Detail != "" {
		sr.Detail += ";"
	}
	sr.Detail += "backend=" + backend
	return sr
}

var policy = DropPolicy{Rules: []DropRule{{Slot: "remote", Values: []string{"onsite"}, MinOpenRouterProb: 0.9, MinLappiScore: 0.5}}, OpenRouter: "d1-ultra"}

func runnerWith(t *testing.T, ans decide.Answer, terr error) (*Runner, *ledger.File, posting.Key) {
	t.Helper()
	l := openLedger(t)
	p := ghPost(t, "1", "jd text")
	Reconcile(ctx, l, board, []posting.Posting{p}, t0)
	r := &Runner{Ledger: l, Triager: &fakeTriager{ans: ans, err: terr}, Scorer: fakeScorer{},
		Explainer: fakeExplainer{text: "good fit"}, Policy: policy, Now: func() time.Time { return t0.Add(time.Minute) }}
	return r, l, p.Key
}

func TestAdvanceFullPath(t *testing.T) {
	ans := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Explained {
		t.Fatalf("status %s", got.Status)
	}
	want := map[string]string{"triage.remote": "remote", "triage.remote.reading": "answered", "triage.backend": "d1-ultra",
		"rank": "62.5", "gap.coverage_pct": "62.5", "score.blocks": "2", "explanation": "good fit"}
	for k, v := range want {
		if got.Annotations[k] != v {
			t.Errorf("%s = %q, want %q", k, got.Annotations[k], v)
		}
	}
	if err := r.MarkDigested(ctx, []posting.Key{k, k}); err != nil {
		t.Fatal(err)
	}
	if rec(t, l, k).Status != posting.Digested {
		t.Fatal("not digested")
	}
	if err := r.MarkDigested(ctx, []posting.Key{"gh:acme:nope"}); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestDropPolicy(t *testing.T) {
	lappi := func(v string, score float64) decide.SlotReading {
		return decide.SlotReading{Reading: decide.Answered, Value: v, Lappi: &decide.LappiCalib{Score: score}}
	}
	cases := []struct {
		name string
		ans  decide.Answer
		want posting.Status
		sub  string
	}{
		{"d1-ultra above threshold drops", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.95)}}, posting.Dropped, `"remote" = "onsite"`},
		{"d1-ultra at threshold drops", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.9)}}, posting.Dropped, ""},
		{"d1-ultra below threshold triaged", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.89)}}, posting.Triaged, ""},
		{"lappi above drops", decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": lappi("onsite", 0.7)}}, posting.Dropped, "lappi"},
		{"lappi below triaged", decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": lappi("onsite", 0.2)}}, posting.Triaged, ""},
		{"jev calibration on lappi backend does not drop", decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.99)}}, posting.Triaged, ""},
		{"llm never drops", decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Answered, Value: "onsite"}}}, posting.Triaged, ""},
		{"llm with forged calibration never drops", decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}, posting.Triaged, ""},
		{"answered without calibration triaged", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Answered, Value: "onsite"}}}, posting.Triaged, ""},
		{"value outside drop set", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("hybrid", 0.99)}}, posting.Triaged, ""},
		{"abstained unsure", decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Abstained}}}, posting.Unsure, `"remote"`},
		{"refused unsure", decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Refused}}}, posting.Unsure, ""},
		{"unavailable unsure", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Unavailable}}}, posting.Unsure, ""},
		{"failed unsure", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Failed}}}, posting.Unsure, ""},
		{"not asked unsure", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.NotAsked}}}, posting.Unsure, ""},
		{"missing slot unsure", decide.Answer{Backend: "d1-ultra"}, posting.Unsure, "missing"},
		{"abstain on unruled slot is fine", decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99), "seniority": {Reading: decide.Abstained}}}, posting.Triaged, ""},
		// Router-merged answers: Backend is "d1-ultra+llm", each slot carries its own tag.
		{"merged answer: d1-ultra-tagged slot drops", decide.Answer{Backend: "d1-ultra+llm", Slots: map[string]decide.SlotReading{"remote": tagged(jev("onsite", 0.95), "d1-ultra")}}, posting.Dropped, "d1-ultra"},
		{"merged answer: llm-tagged slot with d1-ultra calibration never drops", decide.Answer{Backend: "d1-ultra+llm", Slots: map[string]decide.SlotReading{"remote": tagged(jev("onsite", 1), "llm")}}, posting.Triaged, ""},
		// Only the configured OpenRouter name drops on a probability: the local
		// shadow speaks the same wire shape, and "jev" is the pre-rename name.
		{"local with calibration never drops", decide.Answer{Backend: "local", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}, posting.Triaged, ""},
		{"legacy name jev never drops once renamed", decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}, posting.Triaged, ""},
		{"merged answer: local-tagged slot never drops", decide.Answer{Backend: "d1-ultra+local", Slots: map[string]decide.SlotReading{"remote": tagged(jev("onsite", 1), "local")}}, posting.Triaged, ""},
		{"merged answer: untagged slot cannot drop", decide.Answer{Backend: "d1-ultra+llm", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}, posting.Triaged, ""},
	}
	for _, c := range cases {
		st, reason := policy.Evaluate(c.ans)
		if st != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, st, reason, c.want)
		}
		if c.sub != "" && !strings.Contains(reason, c.sub) {
			t.Errorf("%s: reason %q lacks %q", c.name, reason, c.sub)
		}
	}
	// Zero thresholds never drop.
	zero := DropPolicy{Rules: []DropRule{{Slot: "remote", Values: []string{"onsite"}}}}
	if st, _ := zero.Evaluate(decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}); st == posting.Dropped {
		t.Fatal("unset threshold dropped")
	}
	// Without a configured OpenRouter name no backend drops on a probability.
	noName := DropPolicy{Rules: policy.Rules}
	if st, _ := noName.Evaluate(decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 1)}}); st == posting.Dropped {
		t.Fatal("dropped on a probability with no OpenRouter backend named")
	}
	// Empty policy never drops or flags.
	if st, _ := (DropPolicy{}).Evaluate(decide.Answer{Backend: "llm"}); st != posting.Triaged {
		t.Fatal("empty policy")
	}
}

func TestAdvanceDropStops(t *testing.T) {
	ans := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Dropped || got.Annotations["rank"] != "" {
		t.Fatalf("%+v", got)
	}
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatal("terminal record moved")
	}
}

func TestAdvanceUnsureIsKeptAndFlagged(t *testing.T) {
	ans := decide.Answer{Backend: "lappi", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Abstained}}}
	r, l, k := runnerWith(t, ans, nil)
	r.Explainer = nil
	if err := r.Advance(ctx, k); !errors.Is(err, ErrStageMissing) {
		t.Fatalf("want ErrStageMissing, got %v", err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Scored || got.Annotations["triage.unsure"] == "" {
		t.Fatalf("unsure flag lost: %+v", got)
	}
}

func TestTriagerErrorLeavesStatus(t *testing.T) {
	// Even an answer that would drop is irrelevant when the call errored.
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.99)}}
	r, l, k := runnerWith(t, ans, errors.New("backend down"))
	before := len(l.All())
	err := r.Advance(ctx, k)
	if err == nil || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("error not returned: %v", err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Live || got.LastEvent.After(t0) || len(l.All()) != before {
		t.Fatalf("record moved on triager error: %+v", got)
	}
}

func TestScorerAndExplainerErrorsLeaveStatus(t *testing.T) {
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}

	r, l, k := runnerWith(t, ans, nil)
	r.Scorer = fakeScorer{scoreErr: errors.New("devprism down")}
	if err := r.Advance(ctx, k); err == nil {
		t.Fatal("score error swallowed")
	}
	got := rec(t, l, k)
	if got.Status != posting.Triaged || got.Annotations["rank"] != "" {
		t.Fatalf("after score error: %+v", got)
	}

	r, l, k = runnerWith(t, ans, nil)
	r.Explainer = fakeExplainer{err: errors.New("llm down")}
	if err := r.Advance(ctx, k); err == nil {
		t.Fatal("explain error swallowed")
	}
	if got := rec(t, l, k); got.Status != posting.Scored || got.Annotations["explanation"] != "" {
		t.Fatalf("after explain error: %+v", got)
	}

	r.Explainer = fakeExplainer{text: ""}
	if err := r.Advance(ctx, k); err == nil {
		t.Fatal("empty explanation accepted")
	}
	r.Explainer = fakeExplainer{text: strings.Repeat("é", 5000)}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(rec(t, l, k).Annotations["explanation"])); n != maxExplanationRunes {
		t.Fatalf("explanation not bounded: %d", n)
	}
}

func TestAdvanceUnknownKeyAndCancel(t *testing.T) {
	r, _, k := runnerWith(t, decide.Answer{Backend: "jev"}, nil)
	if err := r.Advance(ctx, "gh:acme:missing"); err == nil {
		t.Fatal("unknown key accepted")
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Advance(c, k); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
