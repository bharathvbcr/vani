package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobsViewGolden pins the exact `vani jobs list|stats --json` output of
// the ranking views (every view and sort, the filters, stats, folding), so the
// selection, sort and stats logic can move between packages without a change
// in output. Regenerate with VANI_UPDATE_GOLDEN=1 only for an intended change.
func TestJobsViewGolden(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference engineering in Go and Rust, with Kubernetes.")
	seedInsight(t, state, insightSeeds...)
	mustCode(t, 0, "jobs", "mark", "gh:a:gone", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:b:sales", "hidden")
	cases := []string{
		"list --view new", "list --view all", "list --view saved", "list --view hidden",
		"list --sort priority", "list --sort relevance", "list --sort fresh", "list --sort company", "list --sort salary",
		"list --view all --sort company", "list --view all --sort fresh",
		"list --fit strong_fit", "list --view all --fit none --max-age 10", "list --q engineer",
		"list --view all --open --min-relevance 10", "list --view all --exclude old --company acme",
		"stats --view all", "stats --view new", "stats --view all --fit strong_fit,none",
	}
	var b strings.Builder
	for _, c := range cases {
		o, _ := mustCode(t, 0, append([]string{"jobs"}, append(strings.Fields(c), "--json")...)...)
		b.WriteString("## " + c + "\n" + strings.ReplaceAll(o, state, "<state>") + "\n")
	}
	golden := filepath.Join("testdata", "jobsview.golden")
	if os.Getenv("VANI_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != b.String() {
		t.Fatalf("jobs views changed; compare %s against a VANI_UPDATE_GOLDEN=1 run", golden)
	}
}
