package feedback

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var at = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func imp(key string, pos int) Impression {
	return Impression{At: at, Surface: SurfaceDigest, Section: SectionRanked, Key: posting.Key(key), Position: pos,
		Scores: Scores{Priority: 61.2, Relevance: 70, Factors: map[string]float64{"semantic": 0.8, "fit": 0.9}}}
}

func TestImpressionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if err := AppendImpressions(ctx, dir, []Impression{imp("gh:a:1", 1), imp("gh:a:2", 2)}); err != nil {
		t.Fatal(err)
	}
	if err := AppendImpressions(ctx, dir, []Impression{imp("gh:a:3", 1)}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadImpressions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 3 || got.Skipped != 0 || got.Capped != 0 {
		t.Fatalf("read %+v", got)
	}
	if got.Rows[0].V != Version || got.Rows[2].Key != "gh:a:3" || got.Rows[1].Factors["semantic"] != 0.8 {
		t.Fatalf("rows %+v", got.Rows)
	}
	fi, err := os.Stat(filepath.Join(Dir(dir), ImpressionsFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v, %v", fi, err)
	}
}

func TestMissingLogIsEmpty(t *testing.T) {
	got, err := ReadJudgments(t.TempDir())
	if err != nil || len(got.Rows) != 0 || got.Skipped != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// One bad row refuses the whole batch: a half-written batch would record a
// digest the owner never saw in full.
func TestAppendRefusesBadRows(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	bad := []Impression{
		{At: at, Surface: SurfaceDigest, Key: "", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: "k\nx", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: posting.Key(strings.Repeat("k", maxKeyBytes+1)), Position: 1},
		{At: at, Surface: "Digest", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, Section: "top open", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 0},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Priority: math.NaN()}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Relevance: 101}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: map[string]float64{"fit": math.Inf(1)}}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: map[string]float64{"fit": -0.1}}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, DropReason: strings.Repeat("r", maxReasonByte+1)},
	}
	for i, b := range bad {
		if err := AppendImpressions(ctx, dir, []Impression{imp("gh:a:1", 1), b}); err == nil {
			t.Errorf("bad row %d accepted", i)
		}
	}
	many := map[string]float64{}
	for i := 0; i <= MaxFactors; i++ {
		many["f"+strings.Repeat("x", i)] = 0.5
	}
	if err := AppendImpressions(ctx, dir, []Impression{{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: many}}}); err == nil {
		t.Error("too many factors accepted")
	}
	rows := make([]Impression, MaxAppend+1)
	for i := range rows {
		rows[i] = imp("gh:a:1", i+1)
	}
	if err := AppendImpressions(ctx, dir, rows); err == nil {
		t.Error("oversized batch accepted")
	}
	if got, _ := ReadImpressions(dir); len(got.Rows) != 0 {
		t.Fatalf("a refused batch wrote %d rows", len(got.Rows))
	}
	if err := AppendImpressions(ctx, "", []Impression{imp("k", 1)}); err == nil {
		t.Error("empty state dir accepted")
	}
	if err := AppendImpressions(ctx, dir, nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

func TestJudgmentVerdicts(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	for _, v := range []string{VerdictYes, VerdictMaybe, VerdictNo, VerdictSkip} {
		if err := AppendJudgment(ctx, dir, Judgment{At: at, Key: "gh:a:1", Verdict: v, Source: "decile:3"}); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"", "Yes", "y", "apply"} {
		if err := AppendJudgment(ctx, dir, Judgment{At: at, Key: "gh:a:1", Verdict: v}); err == nil {
			t.Errorf("verdict %q accepted", v)
		}
	}
	got, err := ReadJudgments(dir)
	if err != nil || len(got.Rows) != 4 {
		t.Fatalf("%+v %v", got, err)
	}
}

// A torn, oversized, foreign-version or tampered line is skipped and counted,
// never fatal: the readable history still counts, and the reader says how much
// it could not use.
func TestReadSkipsAndCountsBadLines(t *testing.T) {
	dir := t.TempDir()
	if err := AppendImpressions(context.Background(), dir, []Impression{imp("gh:a:1", 1)}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(dir), ImpressionsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := []string{
		"not json",
		`{"v":2,"at":"2026-10-20T09:00:00Z","surface":"digest","key":"k","position":1,"priority":1,"relevance":1}`,
		`{"v":1,"at":"2026-10-20T09:00:00Z","surface":"digest","key":"k","position":1,"priority":500,"relevance":1}`,
		`{"v":1,"surface":"digest","key":"` + strings.Repeat("k", MaxLine) + `","position":1}`,
		"",
	}
	for _, j := range junk {
		f.WriteString(j + "\n")
	}
	f.WriteString(`{"v":1,"at":"2026-10-20T09:00:00Z","surface":"dig`) // torn last line
	f.Close()
	if err := AppendImpressions(context.Background(), dir, []Impression{imp("gh:a:2", 1)}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadImpressions(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The torn line and the next append share a line, so that line is junk too.
	if len(got.Rows) != 1 || got.Skipped != 5 {
		t.Fatalf("rows %d skipped %d, want 1 and 5", len(got.Rows), got.Skipped)
	}
}

func TestConcurrentAppendsInterleaveWholeLines(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := AppendImpressions(context.Background(), dir, []Impression{imp("gh:a:1", i+1), imp("gh:a:2", i+1)}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, err := ReadImpressions(dir)
	if err != nil || len(got.Rows) != 320 || got.Skipped != 0 {
		t.Fatalf("rows %d skipped %d err %v", len(got.Rows), got.Skipped, err)
	}
}

// ScoresOf keeps the known parts and the four multipliers, never a prior
// standing in for an unknown part, and clamps what a hostile Result could hold.
func TestScoresOf(t *testing.T) {
	r := jobinsight.Result{Score: 120, Relevance: math.NaN(), Match: 0.6, Gate: 0.8, Fresh: 0.5, Company: 1.1,
		Factors: []jobinsight.Factor{
			{Name: "fit", Value: 0.9, Known: true},
			{Name: "coverage", Value: 0.4, Known: false},
			{Name: "semantic", Value: 7, Known: true},
			{Name: "fresh", Value: 0.1, Known: true},
		}}
	s := ScoresOf(r)
	if s.Priority != 100 || s.Relevance != 0 {
		t.Fatalf("priority %v relevance %v", s.Priority, s.Relevance)
	}
	if _, ok := s.Factors["coverage"]; ok {
		t.Fatal("an unknown part was kept")
	}
	want := map[string]float64{"fit": 0.9, "semantic": maxFactor, "match": 0.6, "gate": 0.8, "fresh": 0.5, "company": 1.1}
	for k, v := range want {
		if s.Factors[k] != v {
			t.Errorf("%s = %v, want %v", k, s.Factors[k], v)
		}
	}
	if err := s.check(); err != nil {
		t.Fatalf("ScoresOf produced an invalid row: %v", err)
	}
}

func TestAppendHonoursCancelledContextWhenBusy(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lock(context.Background(), Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AppendImpressions(ctx, dir, []Impression{imp("k", 1)}); err == nil {
		t.Fatal("append went ahead while the lock was held and the context cancelled")
	}
}
