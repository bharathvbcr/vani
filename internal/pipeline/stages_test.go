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
	"github.com/bharathvbcr/decide"
)

func TestSkipFlagsAdvanceWithoutStages(t *testing.T) {
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	r.Scorer, r.Explainer = nil, nil
	r.SkipScoring, r.SkipExplain = true, true
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Explained {
		t.Fatalf("status %s, want explained", got.Status)
	}
	if got.Annotations[ledger.AnnRank] != "" || got.Annotations[ledger.AnnExplanation] != "" {
		t.Fatalf("skipped stages must not write results: %v", got.Annotations)
	}
}

func TestNilStageWithoutSkipIsAnError(t *testing.T) {
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	r.Scorer = nil
	if err := r.Advance(ctx, k); !errors.Is(err, ErrStageMissing) {
		t.Fatalf("err = %v, want ErrStageMissing", err)
	}
	if st := rec(t, l, k).Status; st != posting.Triaged {
		t.Fatalf("status %s, want triaged (left in place)", st)
	}
}

type gapScorer struct{ gap resume.Gap }

func (g gapScorer) Gap(context.Context, string, string) (resume.Gap, error) { return g.gap, nil }
func (g gapScorer) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{}, nil
}

func TestMissingRequirementsAnnotation(t *testing.T) {
	long := strings.Repeat("x", 100)
	g := resume.Gap{CoveragePct: 40, MustHaves: []resume.Coverage{
		{Requirement: "Go", Status: "covered"},
		{Requirement: "CUDA", Status: "weak"},
		{Requirement: "Kubernetes", Status: "missing"},
		{Requirement: "", Status: "missing"},
		{Requirement: long, Status: "missing"},
	}}
	got := missingRequirements(g)
	want := "Kubernetes; " + strings.Repeat("x", 60) + "; CUDA (weak)"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	many := resume.Gap{}
	for i := 0; i < 10; i++ {
		many.MustHaves = append(many.MustHaves, resume.Coverage{Requirement: "r", Status: "missing"})
	}
	if n := len(strings.Split(missingRequirements(many), "; ")); n != maxMissing {
		t.Fatalf("kept %d, want %d", n, maxMissing)
	}

	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	r.Scorer = gapScorer{gap: g}
	r.Now = func() time.Time { return t0.Add(time.Hour) }
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if rec(t, l, k).Annotations[ledger.AnnMissing] != want {
		t.Fatalf("annotation = %q", rec(t, l, k).Annotations[ledger.AnnMissing])
	}
}

type fakeBadger map[string]string

func (f fakeBadger) Badge(c string) string { return f[c] }

func TestBadgerAnnotatesAtTriage(t *testing.T) {
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, ans, nil)
	r.Badger = fakeBadger{"Acme": "12 certified H-1B LCAs FY2024 (exact match)"}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, k).Annotations[ledger.AnnLCA]; got != "12 certified H-1B LCAs FY2024 (exact match)" {
		t.Fatalf("badge %q", got)
	}
	if rec(t, l, k).Status == posting.Dropped {
		t.Fatal("a badge must never drop a posting")
	}
}
