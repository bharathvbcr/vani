package pipeline

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// countGap is a Scorer returning a fixed Gap and counting Gap calls.
type countGap struct {
	gap   resume.Gap
	calls *int
}

func (c countGap) Gap(context.Context, string, string) (resume.Gap, error) {
	*c.calls++
	return c.gap, nil
}
func (c countGap) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{BlockIDs: []string{"b1", "b2"}}, nil
}

// DevPrism reports 0% coverage when it extracts no must-have (0 of 0). That is
// unknown: no rank, no coverage, counted in the fail-open tally.
func TestNoMustHavesIsUnknownCoverageNotZero(t *testing.T) {
	calls := 0
	fo := &FailOpen{}
	r := &Runner{Funnel: Funnel{FailOpen: fo}, Scorer: countGap{gap: resume.Gap{CoveragePct: 0, NoMustHaves: true}, calls: &calls}}
	rec := ledger.Record{Key: "gh:acme:1", Posting: posting.Posting{Company: "Acme", Description: "x"}}
	ann, err := r.scoreAnnotations(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := ann[ledger.AnnCoverage]; !ok || v != "" {
		t.Fatalf("coverage must be cleared, got %q (present %v)", v, ok)
	}
	if v, ok := ann[ledger.AnnRank]; !ok || v != "" {
		t.Fatalf("rank must be cleared, got %q (present %v)", v, ok)
	}
	if ann[AnnCoverageUnknown] != CoverageNoMustHaves || ann[ledger.AnnBlocks] != "2" {
		t.Fatalf("annotations %v", ann)
	}
	if got := fo.Counts(); got.CoverageUnknown != 1 || got.Total() != 1 {
		t.Fatalf("fail-open counts %+v", got)
	}
}

// A real measurement of zero (must-haves found, none covered) stays a zero.
func TestMeasuredZeroCoverageStaysZero(t *testing.T) {
	calls := 0
	fo := &FailOpen{}
	g := resume.Gap{CoveragePct: 0, MustHaves: []resume.Coverage{{Requirement: "cuda", Status: "missing"}}}
	r := &Runner{Funnel: Funnel{FailOpen: fo}, Scorer: countGap{gap: g, calls: &calls}}
	ann, err := r.scoreAnnotations(context.Background(), ledger.Record{Key: "gh:a:1", Posting: posting.Posting{Description: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if ann[ledger.AnnCoverage] != "0" || ann[ledger.AnnRank] != "0" || ann[AnnCoverageUnknown] != "" {
		t.Fatalf("annotations %v", ann)
	}
	if fo.Counts().Total() != 0 {
		t.Fatalf("counts %+v", fo.Counts())
	}
}

// A record scored as unknown is not Unscored, so a rescore pass does not ask
// DevPrism about it again and again.
func TestUnknownCoverageRecordIsNotUnscored(t *testing.T) {
	rec := ledger.Record{Status: posting.Scored, Annotations: map[string]string{AnnCoverageUnknown: CoverageNoMustHaves}}
	if Unscored(rec) {
		t.Fatal("unknown-coverage record reported Unscored")
	}
	if !Unscored(ledger.Record{Status: posting.Scored, Annotations: map[string]string{}}) {
		t.Fatal("record with neither rank nor marker must be Unscored")
	}
}

// The score stage does not re-ask for a posting already pre-scored as unknown.
func TestScoreStageSkipsPreScoredUnknown(t *testing.T) {
	calls := 0
	r := &Runner{Funnel: Funnel{ScoreFirst: true}, Scorer: countGap{gap: resume.Gap{NoMustHaves: true}, calls: &calls}}
	rec := ledger.Record{Key: "gh:a:1", Posting: posting.Posting{Description: "x"},
		Annotations: map[string]string{AnnCoverageUnknown: CoverageNoMustHaves}}
	if _, reason, err := r.scoreStage(context.Background(), rec); err != nil || reason != "scored" || calls != 0 {
		t.Fatalf("reason %q err %v calls %d", reason, err, calls)
	}
}
