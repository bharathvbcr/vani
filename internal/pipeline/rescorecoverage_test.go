package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// readsScorer is a DevPrism whose parser either finds no must-have in a JD
// (reads false, as before a parser fix) or finds one missing requirement at
// 40% coverage (reads true). It counts its calls.
type readsScorer struct {
	reads           bool
	gapErr, scorErr error
	calls           int
}

func (s *readsScorer) Gap(ctx context.Context, jd, p string) (resume.Gap, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return resume.Gap{}, err
	}
	if s.gapErr != nil {
		return resume.Gap{}, s.gapErr
	}
	if !s.reads {
		return resume.Gap{NoMustHaves: true}, nil
	}
	return resume.Gap{CoveragePct: 40, MustHaves: []resume.Coverage{{Requirement: "Go", Status: "missing"},
		{Requirement: "CUDA", Status: "covered"}}}, nil
}

func (s *readsScorer) Score(ctx context.Context, jd, p string) (resume.Score, error) {
	s.calls++
	if s.scorErr != nil {
		return resume.Score{}, s.scorErr
	}
	return resume.Score{BlockIDs: []string{"b1", "b2", "b3"}}, nil
}

// unknownRunner advances one posting with a DevPrism that finds no must-have,
// so it is explained with the coverage-unknown marker, then moves it to stop
// (Explained, Digested, Dropped, Dead or Duplicate). The runner's scorer is
// then sc.
func unknownRunner(t *testing.T, stop posting.Status, sc resume.Scorer) (*Runner, *ledger.File, posting.Key) {
	t.Helper()
	l := openLedger(t)
	p := ghPost(t, "1", "jd text")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Ledger: l, Triager: &fakeTriager{ans: kept}, Explainer: fakeExplainer{text: "good fit"},
		Policy: policy, Scorer: &readsScorer{}, Now: func() time.Time { return t0.Add(time.Minute) }}
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, p.Key)
	if got.Status != posting.Explained || !ledger.CoverageUnknown(got) || got.Annotations[ledger.AnnRank] != "" {
		t.Fatalf("setup: status %s ann %v, want explained with unknown coverage", got.Status, got.Annotations)
	}
	switch stop {
	case posting.Explained:
	case posting.Digested:
		if err := r.MarkDigested(ctx, []posting.Key{p.Key}); err != nil {
			t.Fatal(err)
		}
	default:
		if err := r.emit(ctx, p.Key, stop, "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec(t, l, p.Key); got.Status != stop || !ledger.CoverageUnknown(got) {
		t.Fatalf("setup: status %s, want %s with unknown coverage", got.Status, stop)
	}
	r.Scorer = sc
	r.Now = func() time.Time { return t0.Add(time.Hour) }
	return r, l, p.Key
}

// A shown posting (scored, explained or digested) with the coverage-unknown
// marker and text to read can be measured again; a dropped, dead or duplicate
// one is left alone, as is one with a measured coverage or no text.
func TestCoverageRescorable(t *testing.T) {
	base := ledger.Record{Posting: posting.Posting{Description: "jd text"},
		Annotations: map[string]string{ledger.AnnCoverageUnknown: ledger.CoverageNoMustHaves}}
	for _, c := range []struct {
		status posting.Status
		want   bool
	}{
		{posting.Scored, true}, {posting.Explained, true}, {posting.Digested, true},
		{posting.Dropped, false}, {posting.Dead, false}, {posting.Duplicate, false},
		{posting.Live, false}, {posting.Triaged, false},
	} {
		r := base
		r.Status = c.status
		if got := CoverageRescorable(r); got != c.want {
			t.Errorf("%s: CoverageRescorable = %v, want %v", c.status, got, c.want)
		}
	}
	measured := base
	measured.Status = posting.Digested
	measured.Annotations = map[string]string{ledger.AnnRank: "50", ledger.AnnCoverage: "50"}
	if CoverageRescorable(measured) {
		t.Error("a measured coverage is rescorable")
	}
	blank := base
	blank.Status = posting.Digested
	blank.Posting.Description = " \n\t"
	if CoverageRescorable(blank) {
		t.Error("a posting with no text is rescorable")
	}
}

// When DevPrism now finds must-haves, the posting gets the same score
// annotations a first score writes, keeps its status and explanation, and
// loses the marker. A second pass asks nothing and writes nothing.
func TestRescoreUnknownMeasuresInPlace(t *testing.T) {
	for _, st := range []posting.Status{posting.Explained, posting.Digested} {
		t.Run(string(st), func(t *testing.T) {
			sc := &readsScorer{reads: true}
			r, l, k := unknownRunner(t, st, sc)
			wrote, err := r.RescoreUnknown(ctx, k)
			if err != nil || !wrote {
				t.Fatalf("RescoreUnknown = %v, %v; want true, nil", wrote, err)
			}
			got := rec(t, l, k)
			if got.Status != st {
				t.Fatalf("status %s, want %s", got.Status, st)
			}
			want := map[string]string{ledger.AnnRank: "40", ledger.AnnCoverage: "40", ledger.AnnBlocks: "3",
				ledger.AnnMissing: "Go", ledger.AnnExplanation: "good fit", ledger.AnnCoverageUnknown: ""}
			for key, v := range want {
				if got.Annotations[key] != v {
					t.Errorf("%s = %q, want %q", key, got.Annotations[key], v)
				}
			}
			if ledger.CoverageUnknown(got) || CoverageRescorable(got) {
				t.Fatal("still marked unknown after a measurement")
			}
			calls, last := sc.calls, got.LastEvent
			r.Now = func() time.Time { return t0.Add(2 * time.Hour) }
			if wrote, err := r.RescoreUnknown(ctx, k); err != nil || wrote {
				t.Fatalf("second pass = %v, %v; want false, nil", wrote, err)
			}
			if sc.calls != calls || !rec(t, l, k).LastEvent.Equal(last) {
				t.Fatalf("a second pass asked DevPrism (%d calls, was %d) or wrote an event", sc.calls, calls)
			}
		})
	}
}

// The preferred-employer boost applies as at a first score.
func TestRescoreUnknownAppliesTheEmployerBoost(t *testing.T) {
	r, l, k := unknownRunner(t, posting.Digested, &readsScorer{reads: true})
	r.Employer, r.PreferEmployers = acmeCapExempt, []string{"cap_exempt_likely"}
	if _, err := r.RescoreUnknown(ctx, k); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, k); got.Annotations[ledger.AnnRank] != "50" || got.Annotations[ledger.AnnCoverage] != "40" {
		t.Fatalf("rank %q coverage %q, want 50 and 40", got.Annotations[ledger.AnnRank], got.Annotations[ledger.AnnCoverage])
	}
}

// A DevPrism that still finds no must-have leaves the record exactly as it
// was: no event (the ledger is append-only), the marker kept.
func TestRescoreUnknownStillUnknownWritesNothing(t *testing.T) {
	sc := &readsScorer{}
	r, l, k := unknownRunner(t, posting.Digested, sc)
	before := rec(t, l, k)
	wrote, err := r.RescoreUnknown(ctx, k)
	if err != nil || wrote {
		t.Fatalf("RescoreUnknown = %v, %v; want false, nil", wrote, err)
	}
	if sc.calls == 0 {
		t.Fatal("DevPrism was not asked")
	}
	got := rec(t, l, k)
	if !got.LastEvent.Equal(before.LastEvent) || !ledger.CoverageUnknown(got) || got.Status != posting.Digested {
		t.Fatalf("record changed: last event %v->%v, ann %v", before.LastEvent, got.LastEvent, got.Annotations)
	}
}

// DevPrism down, a failing score call, a cancelled context and a failed append
// write nothing and return the error.
func TestRescoreUnknownFailuresWriteNothing(t *testing.T) {
	down := errors.New("devprism: exec: no such file")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []struct {
		name string
		sc   *readsScorer
		ctx  context.Context
		wrap func(ledger.Ledger) ledger.Ledger
		want error
	}{
		{"devprism down", &readsScorer{reads: true, gapErr: down}, ctx, nil, down},
		{"score fails", &readsScorer{reads: true, scorErr: down}, ctx, nil, down},
		{"cancelled", &readsScorer{reads: true}, cancelled, nil, context.Canceled},
		{"append fails", &readsScorer{reads: true}, ctx, func(l ledger.Ledger) ledger.Ledger { return &failingAppend{Ledger: l, n: 1} }, errAppend},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, l, k := unknownRunner(t, posting.Digested, c.sc)
			if c.wrap != nil {
				r.Ledger = c.wrap(l)
			}
			before := rec(t, l, k)
			wrote, err := r.RescoreUnknown(c.ctx, k)
			if !errors.Is(err, c.want) || wrote {
				t.Fatalf("RescoreUnknown = %v, %v; want false, %v", wrote, err, c.want)
			}
			got := rec(t, l, k)
			if !got.LastEvent.Equal(before.LastEvent) || !ledger.CoverageUnknown(got) {
				t.Fatalf("record changed: last event %v->%v", before.LastEvent, got.LastEvent)
			}
		})
	}
}

// A dropped, dead or duplicate posting is never asked about or written, even
// with the marker: a drop stays a drop (Rule 7).
func TestRescoreUnknownLeavesClosedPostingsAlone(t *testing.T) {
	for _, st := range []posting.Status{posting.Dropped, posting.Dead, posting.Duplicate} {
		t.Run(string(st), func(t *testing.T) {
			sc := &readsScorer{reads: true}
			r, l, k := unknownRunner(t, st, sc)
			before := rec(t, l, k)
			if wrote, err := r.RescoreUnknown(ctx, k); err != nil || wrote {
				t.Fatalf("RescoreUnknown = %v, %v; want false, nil", wrote, err)
			}
			if sc.calls != 0 || !rec(t, l, k).LastEvent.Equal(before.LastEvent) {
				t.Fatalf("asked DevPrism %d times or wrote an event", sc.calls)
			}
		})
	}
}

// Scoring off refuses, as Rescore does.
func TestRescoreUnknownScoringOff(t *testing.T) {
	r, _, k := unknownRunner(t, posting.Digested, &readsScorer{reads: true})
	r.SkipScoring = true
	if _, err := r.RescoreUnknown(ctx, k); !errors.Is(err, ErrScoringDisabled) {
		t.Fatalf("err = %v, want ErrScoringDisabled", err)
	}
}
