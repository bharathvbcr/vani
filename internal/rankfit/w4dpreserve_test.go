package rankfit

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestW4dIgnoredPathsSaySheWhere(t *testing.T) {
	f, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares,
		`"report":{"labels":5,"top":1,"platt":{"d1":{"n":3,"extra":2}},"weights":{"held_out_pairs":9,"newer":true,"strata":[{"stratum":"a","zz":1}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"platt.d1.extra", "top", "weights.newer", "weights.strata[].zz"}
	got := slices.Clone(f.ReportIgnoredPaths)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("paths %v, want %v", got, want)
	}
	if !f.ReportIgnoredIn("weights") || !f.ReportIgnoredIn("platt") {
		t.Error("ReportIgnoredIn misses a section holding an ignored field")
	}
	g, _ := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":{"labels":5,"top":1}`))
	if g.ReportIgnoredIn("weights") || g.ReportIgnoredIn("platt") {
		t.Error("ReportIgnoredIn flags a section with no ignored field")
	}
	// Names match case-insensitively, as the decoder reads them: not ignored.
	h, _ := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":{"Labels":5}`))
	if len(h.ReportIgnoredPaths) != 0 {
		t.Errorf("a case variant counted as unknown: %v", h.ReportIgnoredPaths)
	}
}

// A report this binary replaces (a refit) is written as the new one: the old
// report's unknown fields do not leak into it.
func TestW4dReplacedReportDropsOldUnknownFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.json")
	body := w4dFile(`"shares":`+w4dShares, `"report":{"labels":5,"contaminated":true}`)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Update(context.Background(), p, func(cur *File, _ bool) error {
		cur.Report = &Report{Labels: 7}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "contaminated") || !strings.Contains(string(b), `"labels": 7`) {
		t.Errorf("replaced report not written as the new one:\n%s", b)
	}
}

// An unchanged report keeps its unknown fields through a rewrite, and the file
// stays loadable with the same ignored paths.
func TestW4dUnchangedReportSurvivesRewrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.json")
	body := w4dFile(`"shares":`+w4dShares, `"report":{"labels":5,"nest":{"deep":[1,2]},"weights":{"newer":1}}`)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), p, func(cur *File, _ bool) error {
		cur.SpareSeniorityMismatch = []string{"senior"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ReportIgnoredPaths, []string{"nest", "weights.newer"}) || !slices.Equal(f.SpareSeniorityMismatch, []string{"senior"}) {
		t.Errorf("after rewrite: paths %v spare %v", f.ReportIgnoredPaths, f.SpareSeniorityMismatch)
	}
}
