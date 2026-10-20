package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/decide"
)

// countingScorer is fakeScorer that counts its calls and can block until ctx
// ends, as a DevPrism that never answers would.
type countingScorer struct {
	fakeScorer
	calls int
	block bool
}

func (c *countingScorer) Gap(ctx context.Context, jd, p string) (resume.Gap, error) {
	c.calls++
	if c.block {
		<-ctx.Done()
		return resume.Gap{}, ctx.Err()
	}
	return c.fakeScorer.Gap(ctx, jd, p)
}

func (c *countingScorer) Score(ctx context.Context, jd, p string) (resume.Score, error) {
	c.calls++
	return c.fakeScorer.Score(ctx, jd, p)
}

var kept = decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}

// unscoredRunner runs a posting with description desc through the pipeline
// with scoring off, as a run with devprism.binary empty did, stopping at
// status stop (Scored or Explained). It then switches scoring on with sc.
func unscoredRunner(t *testing.T, desc string, stop posting.Status, sc resume.Scorer) (*Runner, *ledger.File, posting.Key) {
	t.Helper()
	l := openLedger(t)
	p := ghPost(t, "1", desc)
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Ledger: l, Triager: &fakeTriager{ans: kept}, Explainer: fakeExplainer{text: "good fit"},
		Policy: policy, SkipScoring: true, Now: func() time.Time { return t0.Add(time.Minute) }}
	if stop == posting.Scored {
		r.Explainer = fakeExplainer{err: errors.New("stop before explaining")}
	}
	_ = r.Advance(ctx, p.Key)
	if got := rec(t, l, p.Key); got.Status != stop || got.Annotations[ledger.AnnRank] != "" {
		t.Fatalf("setup: status %s rank %q, want %s and no rank", got.Status, got.Annotations[ledger.AnnRank], stop)
	}
	r.SkipScoring, r.Scorer = false, sc
	r.Explainer = fakeExplainer{text: "good fit"}
	r.Now = func() time.Time { return t0.Add(time.Hour) }
	return r, l, p.Key
}

// unchanged fails the test when the record moved since before.
func unchanged(t *testing.T, l ledger.Ledger, before ledger.Record) {
	t.Helper()
	got := rec(t, l, before.Key)
	if got.Status != before.Status || !got.LastEvent.Equal(before.LastEvent) || got.Annotations[ledger.AnnRank] != "" {
		t.Fatalf("record changed: status %s->%s, last event %v->%v, rank %q",
			before.Status, got.Status, before.LastEvent, got.LastEvent, got.Annotations[ledger.AnnRank])
	}
}

func TestUnscored(t *testing.T) {
	ranked := map[string]string{ledger.AnnRank: "62.5"}
	for _, c := range []struct {
		st   posting.Status
		ann  map[string]string
		want bool
	}{
		{posting.Scored, nil, true},
		{posting.Explained, map[string]string{ledger.AnnExplanation: "x"}, true},
		{posting.Explained, ranked, false},
		{posting.Scored, ranked, false},
		{posting.Digested, nil, false},
		{posting.Dropped, nil, false},
		{posting.Dead, nil, false},
		{posting.Duplicate, nil, false},
		{posting.Triaged, nil, false},
		{posting.Unsure, nil, false},
		{posting.Live, nil, false},
	} {
		if got := Unscored(ledger.Record{Status: c.st, Annotations: c.ann}); got != c.want {
			t.Errorf("Unscored(%s, %v) = %v, want %v", c.st, c.ann, got, c.want)
		}
	}
}

// An explained posting that was never scored gets the scoring annotations and
// keeps its status and explanation: no re-explain, no change of status. A
// second pass writes nothing.
func TestRescoreKeepsStatusAndExplanation(t *testing.T) {
	sc := &countingScorer{}
	r, l, k := unscoredRunner(t, "jd text", posting.Explained, sc)
	if err := r.Rescore(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Explained {
		t.Fatalf("status %s, want explained", got.Status)
	}
	want := map[string]string{"rank": "62.5", "gap.coverage_pct": "62.5", "score.blocks": "2",
		"explanation": "good fit", "triage.remote": "remote"}
	for key, v := range want {
		if got.Annotations[key] != v {
			t.Errorf("%s = %q, want %q", key, got.Annotations[key], v)
		}
	}
	if Unscored(got) {
		t.Fatal("still unscored after a rescore")
	}

	calls, last := sc.calls, got.LastEvent
	r.Now = func() time.Time { return t0.Add(2 * time.Hour) }
	if err := r.Rescore(ctx, k); err != nil {
		t.Fatal(err)
	}
	if sc.calls != calls || !rec(t, l, k).LastEvent.Equal(last) {
		t.Fatalf("a second rescore asked DevPrism (%d calls, was %d) or wrote an event", sc.calls, calls)
	}
}

// A posting parked at Scored stays Scored when rescored; the next Advance
// explains it as usual, with the score in place.
func TestRescoredScoredPostingIsExplainedNext(t *testing.T) {
	r, l, k := unscoredRunner(t, "jd text", posting.Scored, &countingScorer{})
	if err := r.Rescore(ctx, k); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, k); got.Status != posting.Scored || got.Annotations[ledger.AnnRank] != "62.5" {
		t.Fatalf("status %s rank %q", got.Status, got.Annotations[ledger.AnnRank])
	}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, k); got.Status != posting.Explained || got.Annotations[ledger.AnnRank] != "62.5" || got.Annotations[ledger.AnnExplanation] != "good fit" {
		t.Fatalf("after advance: status %s ann %v", got.Status, got.Annotations)
	}
}

// The employer boost applies at rescore exactly as at a first score.
func TestRescoreAppliesTheEmployerBoost(t *testing.T) {
	r, l, k := unscoredRunner(t, "jd text", posting.Explained, &countingScorer{})
	r.Employer, r.PreferEmployers = acmeCapExempt, []string{"cap_exempt_likely"}
	if err := r.Rescore(ctx, k); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, k); got.Annotations[ledger.AnnRank] != "72.5" || got.Annotations[ledger.AnnCoverage] != "62.5" {
		t.Fatalf("rank %q coverage %q, want 72.5 and 62.5", got.Annotations[ledger.AnnRank], got.Annotations[ledger.AnnCoverage])
	}
}

// DevPrism down, a failing score call, a call that times out, a cancelled run
// and a failed ledger write each write nothing: a posting is never marked
// rescored when it was not.
func TestRescoreFailuresWriteNothing(t *testing.T) {
	down := errors.New("devprism: exec: no such file")
	cases := []struct {
		name  string
		sc    *countingScorer
		ctx   func() (context.Context, context.CancelFunc)
		wrap  func(ledger.Ledger) ledger.Ledger
		want  error
		calls int // DevPrism calls expected; -1 means any
	}{
		{name: "devprism down", sc: &countingScorer{fakeScorer: fakeScorer{gapErr: down}}, want: down, calls: 1},
		{name: "score fails", sc: &countingScorer{fakeScorer: fakeScorer{scoreErr: down}}, want: down, calls: 2},
		{name: "timeout", sc: &countingScorer{block: true}, want: context.DeadlineExceeded, calls: 1,
			ctx: func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 20*time.Millisecond) }},
		{name: "cancelled", sc: &countingScorer{}, want: context.Canceled, calls: 0,
			ctx: func() (context.Context, context.CancelFunc) {
				c, cancel := context.WithCancel(ctx)
				cancel()
				return c, cancel
			}},
		{name: "append fails", sc: &countingScorer{}, want: errAppend, calls: 2,
			wrap: func(l ledger.Ledger) ledger.Ledger { return &failingAppend{Ledger: l, n: 1} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, l, k := unscoredRunner(t, "jd text", posting.Explained, c.sc)
			if c.wrap != nil {
				r.Ledger = c.wrap(l)
			}
			before := rec(t, l, k)
			cctx := ctx
			if c.ctx != nil {
				var cancel context.CancelFunc
				cctx, cancel = c.ctx()
				defer cancel()
			}
			err := r.Rescore(cctx, k)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.sc.calls != c.calls {
				t.Errorf("%d DevPrism calls, want %d", c.sc.calls, c.calls)
			}
			unchanged(t, l, before)
		})
	}
}

// A posting with no description (or only whitespace) is not scored: an empty
// JD would score near zero and sink it below the unknown-coverage prior.
func TestRescoreSkipsAnEmptyDescription(t *testing.T) {
	for _, desc := range []string{"", " \n\t "} {
		sc := &countingScorer{}
		r, l, k := unscoredRunner(t, desc, posting.Explained, sc)
		before := rec(t, l, k)
		if Described(before) {
			t.Fatalf("Described(%q) = true", desc)
		}
		if err := r.Rescore(ctx, k); !errors.Is(err, ErrNoDescription) {
			t.Fatalf("desc %q: err = %v, want ErrNoDescription", desc, err)
		}
		if sc.calls != 0 {
			t.Errorf("desc %q: DevPrism asked %d times", desc, sc.calls)
		}
		unchanged(t, l, before)
	}
}

// With scoring still off, or no scorer at all, Rescore refuses and writes
// nothing; an unknown key is an error; a record past the pipeline is left
// alone without an error.
func TestRescoreRefusals(t *testing.T) {
	r, l, k := unscoredRunner(t, "jd text", posting.Explained, &countingScorer{})
	before := rec(t, l, k)
	r.SkipScoring = true
	if err := r.Rescore(ctx, k); !errors.Is(err, ErrScoringDisabled) {
		t.Fatalf("skip scoring: err = %v", err)
	}
	r.SkipScoring, r.Scorer = false, nil
	if err := r.Rescore(ctx, k); !errors.Is(err, ErrStageMissing) {
		t.Fatalf("nil scorer: err = %v", err)
	}
	unchanged(t, l, before)
	if err := r.Rescore(ctx, "gh:acme:nope"); err == nil {
		t.Fatal("unknown key accepted")
	}

	sc := &countingScorer{}
	r.Scorer = sc
	if err := r.MarkDigested(ctx, []posting.Key{k}); err != nil {
		t.Fatal(err)
	}
	digested := rec(t, l, k)
	if err := r.Rescore(ctx, k); err != nil || sc.calls != 0 {
		t.Fatalf("digested: err %v, %d DevPrism calls", err, sc.calls)
	}
	unchanged(t, l, digested)
}
