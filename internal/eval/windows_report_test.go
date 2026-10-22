package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A changed label is reported, and the report says which labels it used.
func TestWindowReportSaysLabelsChangedAfterFiling(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 20, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 20, 0)...)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "a000", Labels: map[string]string{"work_mode": "onsite"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := BuildWindows(recs, s, Names{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Changed["window-1"]; got != 1 {
		t.Fatalf("changed %d, want 1", got)
	}
	if !strings.Contains(rep.Markdown(), "1 labels changed after window-1 was filed; verdict uses the labels as filed") {
		t.Fatalf("report does not say so:\n%s", rep.Markdown())
	}
}

// The permanent-failure wording names the count and the floor.
func TestWindowReportCannotPromoteNamesTheCount(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 5, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 20, 0)...)
	v := windowVerdict(t, s, recs)
	if v.Promotable || !v.Final || !strings.Contains(v.Why, "cannot promote: window closed with 5 discordant (needs 10)") {
		t.Fatalf("%+v", v)
	}
}

// BuildWindows never writes windows.json, however many postings it files in
// memory.
func TestWindowBuildWritesNothing(t *testing.T) {
	s, labelDir, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 40, 5, 0)
	if _, err := BuildWindows(recs, s, Names{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(labelDir, WindowsFile)); !os.IsNotExist(err) {
		t.Fatalf("BuildWindows wrote %s: %v", WindowsFile, err)
	}
}
