package feedback

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Old logs naming not_ranked, new ones naming coverage_unknown, and any other
// well-formed section name parse; a malformed name is skipped and counted,
// never silently accepted.
func TestAdversarialSectionNamesRoundTrip(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := func(section string) string {
		return `{"v":1,"surface":"digest","section":"` + section + `","key":"gh:a:1","position":1}` + "\n"
	}
	log := line(SectionUnranked) + line(SectionCoverageUnknown) + line("some_future_section") + line("Bad Section") + line("") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ImpressionsFile), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadImpressions(state)
	if err != nil {
		t.Fatal(err)
	}
	var secs []string
	for _, r := range got.Rows {
		secs = append(secs, r.Section)
	}
	if strings.Join(secs, ",") != "not_ranked,coverage_unknown,some_future_section," || got.Skipped != 1 {
		t.Errorf("sections %q skipped %d", secs, got.Skipped)
	}
	// Writing the new constant round-trips.
	n, err := AppendImpressions(context.Background(), t.TempDir(), []Impression{{Surface: "digest", Section: SectionCoverageUnknown, Key: "gh:a:2", Position: 1}})
	if err != nil || n != 1 {
		t.Errorf("append coverage_unknown: %d %v", n, err)
	}
}
