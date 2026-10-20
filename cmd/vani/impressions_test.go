package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/marks"
)

// A run's digest records what it listed, with sections, positions and scores,
// and never a title: the impressions are what lets an unmarked role count as
// seen-and-passed.
func TestRunRecordsDigestImpressions(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l, n := fixture(), &fakeNotifier{}
	wire(t, l, n, nil)
	at := time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)
	if _, err := marks.SetMark(marks.Dir(stateDir), "gh:a:2", marks.Saved, at); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "run-once")

	got, err := feedback.ReadImpressions(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skipped != 0 || len(got.Rows) == 0 {
		t.Fatalf("impressions %+v", got)
	}
	sections := map[string]int{}
	for i, r := range got.Rows {
		if r.Surface != feedback.SurfaceDigest || r.Position != i+1 {
			t.Fatalf("row %d: %+v", i, r)
		}
		sections[r.Section]++
	}
	if sections[feedback.SectionSaved] != 1 {
		t.Fatalf("sections %v, want the saved role logged", sections)
	}
	raw, err := os.ReadFile(filepath.Join(feedback.Dir(stateDir), feedback.ImpressionsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.All() {
		if r.Posting.Title != "" && strings.Contains(string(raw), r.Posting.Title) {
			t.Fatalf("a title reached the impression log: %q", r.Posting.Title)
		}
	}
}

// Nothing shown records nothing; a log that cannot be written is an error the
// caller reports, never a silent pass.
func TestRecordDigestImpressions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if err := recordDigestImpressions(ctx, dir, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(feedback.Dir(dir)); !os.IsNotExist(err) {
		t.Fatalf("an empty digest created the feedback dir: %v", err)
	}
	shown := make([]digest.Shown, feedback.MaxAppend+5)
	for i := range shown {
		shown[i] = digest.Shown{Key: "gh:a:1", Section: digest.SectionRanked, Position: i + 1,
			Result: jobinsight.Result{Score: 50, Relevance: 60, Match: 0.5, Gate: 1, Fresh: 1, Company: 1}}
	}
	if err := recordDigestImpressions(ctx, dir, time.Now(), shown); err != nil {
		t.Fatalf("a digest longer than one append: %v", err)
	}
	if got, _ := feedback.ReadImpressions(dir); len(got.Rows) != len(shown) {
		t.Fatalf("logged %d of %d", len(got.Rows), len(shown))
	}
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil { // a file where the state dir should be
		t.Fatal(err)
	}
	if err := recordDigestImpressions(ctx, blocked, time.Now(), shown[:1]); err == nil {
		t.Fatal("an unwritable log reported success")
	}
}
