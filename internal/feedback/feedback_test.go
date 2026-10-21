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

func slated(slate, surface, key string, pos int) Impression {
	i := imp(key, pos)
	i.SlateID, i.Surface = slate, surface
	return i
}

func appendN(t *testing.T, dir string, rows ...Impression) int {
	t.Helper()
	n, err := AppendImpressions(context.Background(), dir, rows)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImpressionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if n := appendN(t, dir, imp("gh:a:1", 1), imp("gh:a:2", 2)); n != 2 {
		t.Fatalf("wrote %d", n)
	}
	appendN(t, dir, imp("gh:a:3", 1))
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
	got, err := ReadImpressions(t.TempDir())
	if err != nil || len(got.Rows) != 0 || got.Skipped != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

// One bad row refuses the whole batch: a half-written batch would record a
// slate the owner never saw in full.
func TestAppendRefusesBadRows(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	bad := []Impression{
		{At: at, Surface: SurfaceDigest, Key: "", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: "k\nx", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: posting.Key(strings.Repeat("k", maxKeyBytes+1)), Position: 1},
		{At: at, Surface: "Digest", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, Section: "top open", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, SlateID: "Not A Slate", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, Source: "a b", Key: "k", Position: 1},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 0},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, PSelect: 1.5},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, PSelect: math.NaN()},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Priority: math.NaN()}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Relevance: 101}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: map[string]float64{"fit": math.Inf(1)}}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: map[string]float64{"fit": -0.1}}},
		{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, DropReason: strings.Repeat("r", maxReasonByte+1)},
	}
	for i, b := range bad {
		if _, err := AppendImpressions(ctx, dir, []Impression{imp("gh:a:1", 1), b}); err == nil {
			t.Errorf("bad row %d accepted", i)
		}
	}
	many := map[string]float64{}
	for i := 0; i <= MaxFactors; i++ {
		many["f"+strings.Repeat("x", i)] = 0.5
	}
	if _, err := AppendImpressions(ctx, dir, []Impression{{At: at, Surface: SurfaceDigest, Key: "k", Position: 1, Scores: Scores{Factors: many}}}); err == nil {
		t.Error("too many factors accepted")
	}
	rows := make([]Impression, MaxAppend+1)
	for i := range rows {
		rows[i] = imp("gh:a:1", i+1)
	}
	if _, err := AppendImpressions(ctx, dir, rows); err == nil {
		t.Error("oversized batch accepted")
	}
	if got, _ := ReadImpressions(dir); len(got.Rows) != 0 {
		t.Fatalf("a refused batch wrote %d rows", len(got.Rows))
	}
	if _, err := AppendImpressions(ctx, "", []Impression{imp("k", 1)}); err == nil {
		t.Error("empty state dir accepted")
	}
	if n, err := AppendImpressions(ctx, dir, nil); err != nil || n != 0 {
		t.Errorf("empty batch: %d %v", n, err)
	}
	if _, err := os.Stat(Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("refused and empty batches created the feedback dir: %v", err)
	}
}

// A key is logged once per (slate, surface): a surface that redraws or
// refreshes the same slate adds nothing; a new slate, or the same key on
// another surface, is a new impression; a row with no slate is never deduped.
func TestSlateDedupe(t *testing.T) {
	dir := t.TempDir()
	if n := appendN(t, dir, slated("2026-10-20", SurfaceJobs, "gh:a:1", 1), slated("2026-10-20", SurfaceJobs, "gh:a:2", 2), slated("2026-10-20", SurfaceJobs, "gh:a:1", 3)); n != 2 {
		t.Fatalf("a repeated key inside one batch wrote %d rows, want 2", n)
	}
	if n := appendN(t, dir, slated("2026-10-20", SurfaceJobs, "gh:a:1", 1), slated("2026-10-20", SurfaceJobs, "gh:a:3", 3)); n != 1 {
		t.Fatalf("a refresh wrote %d rows, want only the new key", n)
	}
	if n := appendN(t, dir, slated("2026-10-21", SurfaceJobs, "gh:a:1", 1), slated("2026-10-20", SurfaceAudit, "gh:a:1", 1)); n != 2 {
		t.Fatalf("a new slate and another surface wrote %d rows, want 2", n)
	}
	if n := appendN(t, dir, imp("gh:a:1", 1)); n != 1 {
		t.Fatalf("a row with no slate wrote %d", n)
	}
	if n := appendN(t, dir, imp("gh:a:1", 1)); n != 1 {
		t.Fatalf("a row with no slate was deduped")
	}
	got, _ := ReadImpressions(dir)
	if len(got.Rows) != 7 {
		t.Fatalf("%d rows, want 7", len(got.Rows))
	}
}

// The log is bounded: past the cap the live file becomes the one rotated file
// (replacing the one before), so the directory holds two generations at most,
// and dedupe still sees the rotated slate.
func TestLogRotatesAndStaysBounded(t *testing.T) {
	old := maxLogBytes
	maxLogBytes = 2000
	defer func() { maxLogBytes = old }()
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		appendN(t, dir, slated("s"+strings.Repeat("x", i%3), SurfaceJobs, "gh:a:1", 1), imp("gh:a:2", 1))
	}
	entries, err := os.ReadDir(Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	var logs int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "impressions") {
			logs++
			if fi, _ := e.Info(); fi.Size() > 2*maxLogBytes {
				t.Errorf("%s is %d bytes, over twice the cap", e.Name(), fi.Size())
			}
		}
	}
	if logs != 2 {
		t.Fatalf("%d impression files, want the live and one rotated", logs)
	}
	if n := appendN(t, dir, slated("sxx", SurfaceJobs, "gh:a:1", 1)); n != 0 {
		t.Fatalf("a slate logged before the rotation was logged again (%d)", n)
	}
	got, err := ReadImpressions(dir)
	if err != nil || got.Skipped != 0 || len(got.Rows) == 0 || len(got.Rows) >= 120 {
		t.Fatalf("rows %d skipped %d err %v", len(got.Rows), got.Skipped, err)
	}
}

// A torn, oversized, foreign-version or tampered line is skipped and counted,
// never fatal: the readable history still counts, and the reader says how much
// it could not use.
func TestReadSkipsAndCountsBadLines(t *testing.T) {
	dir := t.TempDir()
	appendN(t, dir, imp("gh:a:1", 1))
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
		if _, err := f.WriteString(j + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.WriteString(`{"v":1,"at":"2026-10-20T09:00:00Z","surface":"dig`); err != nil { // torn last line
		t.Fatal(err)
	}
	f.Close()
	appendN(t, dir, imp("gh:a:2", 1))
	got, err := ReadImpressions(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The append after the torn line starts on a fresh line, so only the torn
	// fragment itself is junk: the new row is kept.
	if len(got.Rows) != 2 || got.Skipped != 5 {
		t.Fatalf("rows %d skipped %d, want 2 and 5", len(got.Rows), got.Skipped)
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
				if _, err := AppendImpressions(context.Background(), dir, []Impression{imp("gh:a:1", i+1), imp("gh:a:2", i+1)}); err != nil {
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

// Two processes reporting the same slate at once log each key once.
func TestConcurrentSameSlateLogsOnce(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := AppendImpressions(context.Background(), dir, []Impression{slated("s1", SurfaceJobs, "gh:a:1", 1), slated("s1", SurfaceJobs, "gh:a:2", 2)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, _ := ReadImpressions(dir); len(got.Rows) != 2 {
		t.Fatalf("%d rows, want 2", len(got.Rows))
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
	if _, err := AppendImpressions(ctx, dir, []Impression{imp("k", 1)}); err == nil {
		t.Fatal("append went ahead while the lock was held and the context cancelled")
	}
}
