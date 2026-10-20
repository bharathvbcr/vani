package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func readImpressions(t *testing.T, stateDir string) feedback.Read {
	t.Helper()
	got, err := feedback.ReadImpressions(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skipped != 0 {
		t.Fatalf("impressions skipped %d lines", got.Skipped)
	}
	return got
}

func withStdin(t *testing.T, s string) {
	t.Helper()
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(s)
}

// A run's digest records what it listed, with sections, positions, scores and
// the digest's date as the slate, and never a title: the impressions are what
// lets an unmarked role count as seen-and-passed.
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

	got := readImpressions(t, stateDir)
	if len(got.Rows) == 0 {
		t.Fatal("the digest recorded nothing")
	}
	sections := map[string]int{}
	for i, r := range got.Rows {
		if r.Surface != feedback.SurfaceDigest || r.Position != i+1 || r.SlateID == "" {
			t.Fatalf("row %d: %+v", i, r)
		}
		sections[r.Section]++
	}
	logged := map[posting.Key]bool{}
	for _, r := range got.Rows {
		logged[r.Key] = true
	}
	if !logged["gh:a:1"] || !logged["gh:a:2"] || logged["gh:a:3"] {
		t.Fatalf("logged %v (sections %v): want the listed roles (the dropped one only as Explore), never the dead one", logged, sections)
	}
	raw, err := os.ReadFile(filepath.Join(feedback.Dir(stateDir), feedback.ImpressionsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.All() {
		for _, text := range []string{r.Posting.Title, r.Posting.Company, r.Posting.Description} {
			if text != "" && strings.Contains(string(raw), text) {
				t.Fatalf("posting text reached the impression log: %q", text)
			}
		}
	}
}

// Nothing shown records nothing; the same slate recorded twice counts once; a
// log that cannot be written is an error the caller reports, never a silent pass.
func TestRecordImpressions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if err := recordImpressions(ctx, dir, time.Now(), "2026-10-20", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(feedback.Dir(dir)); !os.IsNotExist(err) {
		t.Fatalf("an empty digest created the feedback dir: %v", err)
	}
	shown := make([]digest.Shown, feedback.MaxAppend+5)
	for i := range shown {
		shown[i] = digest.Shown{Key: posting.Key(fmt.Sprintf("gh:a:%04d", i)), Section: digest.SectionRanked, Position: i + 1,
			Result: jobinsight.Result{Score: 50, Relevance: 60, Match: 0.5, Gate: 1, Fresh: 1, Company: 1}}
	}
	shown[3].Section, shown[3].Source, shown[3].PSelect = digest.SectionExplore, digest.ExploreSourceDropped, 0.25
	if err := recordImpressions(ctx, dir, time.Now(), "2026-10-20", shown); err != nil {
		t.Fatalf("a digest longer than one append: %v", err)
	}
	if err := recordImpressions(ctx, dir, time.Now(), "2026-10-20", shown); err != nil {
		t.Fatal(err)
	}
	got, _ := feedback.ReadImpressions(dir)
	if len(got.Rows) != len(shown) {
		t.Fatalf("logged %d of %d (a second recording of the slate must add nothing)", len(got.Rows), len(shown))
	}
	if r := got.Rows[3]; r.Source != digest.ExploreSourceDropped || r.PSelect != 0.25 {
		t.Fatalf("an Explore row lost its stratum or p_select: %+v", r)
	}
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil { // a file where the state dir should be
		t.Fatal(err)
	}
	if err := recordImpressions(ctx, blocked, time.Now(), "2026-10-20", shown[:1]); err == nil {
		t.Fatal("an unwritable log reported success")
	}
}

// `impressions log` is what the app calls with the rows it shows: keys and
// positions on stdin, once per slate, nothing else kept.
func TestImpressionsLogCommand(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	req := `{"rows":[{"key":"gh:a:1","position":1},{"key":"gh:a:2","position":2}]}`

	withStdin(t, req)
	o, _ := mustCode(t, 0, "impressions", "log", "--surface", "jobs", "--slate", "all-1", "--section", "all", "--json")
	if !strings.Contains(o, `"logged":2`) {
		t.Fatalf("first report: %s", o)
	}
	// A refresh of the same slate adds only what is new.
	withStdin(t, `{"rows":[{"key":"gh:a:1","position":1},{"key":"gh:a:3","position":2}]}`)
	o, _ = mustCode(t, 0, "impressions", "log", "--surface", "jobs", "--slate", "all-1", "--section", "all", "--json")
	if !strings.Contains(o, `"logged":1`) {
		t.Fatalf("refresh: %s", o)
	}
	// A new slate is a new impression.
	withStdin(t, req)
	o, _ = mustCode(t, 0, "impressions", "log", "--surface", "jobs", "--slate", "all-2", "--json")
	if !strings.Contains(o, `"logged":2`) {
		t.Fatalf("new slate: %s", o)
	}
	got := readImpressions(t, state)
	if len(got.Rows) != 5 {
		t.Fatalf("%d rows, want 5", len(got.Rows))
	}
	for _, r := range got.Rows {
		if r.Surface != feedback.SurfaceJobs || r.SlateID == "" || r.Priority != 0 {
			t.Errorf("row %+v", r)
		}
	}
}

// Malformed, oversized, over-long and personal-looking requests are refused
// with nothing written; a usage mistake exits 2.
func TestImpressionsLogRefusesBadRequests(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	rows := strings.Repeat(`{"key":"gh:a:1","position":1},`, maxImpressionRows+1)
	for name, req := range map[string]string{
		"not json":      "nope",
		"empty":         "",
		"unknown field": `{"rows":[{"key":"gh:a:1","position":1,"title":"Staff Engineer at Acme"}]}`,
		"extra field":   `{"rows":[],"name":"x"}`,
		"bad position":  `{"rows":[{"key":"gh:a:1","position":0}]}`,
		"empty key":     `{"rows":[{"key":"","position":1}]}`,
		"too many rows": `{"rows":[` + strings.TrimSuffix(rows, ",") + `]}`,
		"oversized":     `{"rows":[],"pad":"` + strings.Repeat("x", maxImpressionsStdin) + `"}`,
	} {
		withStdin(t, req)
		code, _, e := do(t, "impressions", "log", "--surface", "jobs", "--slate", "s1")
		if code != 1 {
			t.Errorf("%s: exit %d, want 1 (%s)", name, code, e)
		}
	}
	if _, err := os.Stat(feedback.Dir(state)); !os.IsNotExist(err) {
		t.Errorf("a refused request created the feedback dir: %v", err)
	}
	for _, args := range [][]string{
		{"impressions"},
		{"impressions", "wat"},
		{"impressions", "log", "--slate", "s1"},
		{"impressions", "log", "--surface", "digest", "--slate", "s1"},
		{"impressions", "log", "--surface", "jobs"},
		{"impressions", "log", "--surface", "jobs", "--slate", "s1", "extra"},
	} {
		withStdin(t, `{"rows":[]}`)
		mustCode(t, 2, args...)
	}
}

// The audit session records the cards it showed: each answered card and the
// one it stopped on, as one slate named by the draw's seed; --list and --json
// write nothing.
func TestAuditRecordsTheCardsShown(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 6, 6)
	mustCode(t, 0, "audit", "--list", "--seed", "W1")
	mustCode(t, 0, "audit", "--json", "--seed", "W1")
	if _, err := os.Stat(feedback.Dir(state)); !os.IsNotExist(err) {
		t.Fatalf("--list or --json wrote impressions: %v", err)
	}
	withStdin(t, "1\ns\nq\n") // two answered, quit on the third card
	mustCode(t, 0, "audit", "--seed", "W1")
	got := readImpressions(t, state)
	if len(got.Rows) != 3 {
		t.Fatalf("%d cards recorded, want 3", len(got.Rows))
	}
	for i, r := range got.Rows {
		if r.Surface != feedback.SurfaceAudit || r.SlateID != "w1" || r.Position != i+1 || r.Source == "" || !(r.PSelect > 0 && r.PSelect <= 1) {
			t.Errorf("row %d: %+v", i, r)
		}
	}
}
