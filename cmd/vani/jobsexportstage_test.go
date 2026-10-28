package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/xlsx"
)

// exportCell is the workbook's text for column name of one job's row: the sheet
// is written and read back, so what is checked is the file.
func exportCell(t *testing.T, j jobview.Job, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := xlsx.Write(&buf, exportSheet([]jobview.Job{j}, "USD")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "one.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := exportRows(t, path)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	return rows[1][col(name)]
}

// The workbook carries what became of an application: the current stage, the
// furthest one reached and the date applied, beside the mark.
func TestExportSheetCarriesStages(t *testing.T) {
	applied := time.Date(2026, 10, 1, 23, 30, 0, 0, time.FixedZone("x", -8*3600)) // 2026-10-02 in UTC
	cases := []struct {
		name                    string
		j                       jobview.Job
		stage, furthest, onDate string
	}{
		{"rejected after interview", jobview.Job{Mark: marks.Applied, MarkedAt: applied, Stage: marks.StageRejected, FurthestStage: marks.StageInterview},
			"rejected", "interview", "2026-10-02"},
		{"in an interview", jobview.Job{Mark: marks.Applied, MarkedAt: applied, Stage: marks.StageInterview, FurthestStage: marks.StageInterview},
			"interview", "interview", "2026-10-02"},
		// An old mark has no stage: applied, no stage columns, the date still there.
		{"old applied mark", jobview.Job{Mark: marks.Applied, MarkedAt: applied}, "", "", "2026-10-02"},
		// Unstaged: the furthest stage is kept, the current one is empty.
		{"set back to applied", jobview.Job{Mark: marks.Applied, MarkedAt: applied, FurthestStage: marks.StageOffer}, "", "offer", "2026-10-02"},
		// Marked at is when it was saved or hidden, which is not a date applied.
		{"saved", jobview.Job{Mark: marks.Saved, MarkedAt: applied}, "", "", ""},
		{"hidden", jobview.Job{Mark: marks.Hidden, MarkedAt: applied}, "", "", ""},
		{"no mark", jobview.Job{}, "", "", ""},
		{"applied with no date", jobview.Job{Mark: marks.Applied}, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for name, want := range map[string]string{"Stage": c.stage, "Furthest stage": c.furthest, "Applied": c.onDate} {
				if got := exportCell(t, c.j, name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// The company-history notes the list shows ride the export in their own words.
func TestExportSheetCarriesCompanyNotes(t *testing.T) {
	j := jobview.Job{
		RepeatApply: jobview.NewRepeatApplyNote("Acme", 2, 60, "gh:acme:9", "Platform Engineer", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)),
		Repost:      jobview.NewRepostNote("gh:acme:3", "Inference Engineer", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
	}
	if got := exportCell(t, j, "Repeat apply"); got != j.RepeatApply.Text || !strings.Contains(got, "Platform Engineer") {
		t.Errorf("Repeat apply = %q", got)
	}
	if got := exportCell(t, j, "Repost"); got != "likely repost of Inference Engineer first seen 2026-08-01" {
		t.Errorf("Repost = %q", got)
	}
	for _, name := range []string{"Repeat apply", "Repost"} {
		if got := exportCell(t, jobview.Job{}, name); got != "" {
			t.Errorf("%s of a job with no note = %q, want empty", name, got)
		}
	}
	// Posting text in a note stays text: exportRows fails on any formula cell.
	evil := jobview.Job{Repost: jobview.NewRepostNote("k", `=HYPERLINK("http://x")`, time.Now())}
	if got := exportCell(t, evil, "Repost"); !strings.Contains(got, "HYPERLINK") {
		t.Errorf("Repost = %q", got)
	}
}

// The new columns follow the mark, before the note, and are named for people.
func TestExportColumnOrderAroundStages(t *testing.T) {
	var names []string
	for _, c := range exportColumns {
		names = append(names, c.name)
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"|Mark|Stage|Furthest stage|Applied|Note|Repeat apply|Repost|", "|Key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("columns %q lack %q", joined, want)
		}
	}
	if len(exportSheet(nil, "USD").Header) != len(exportColumns) {
		t.Error("header and columns disagree")
	}
}

// End to end: jobs staged with `jobs mark` come out with their stages, and a
// job with an old mark (applied, never staged) and one never marked are plain.
func TestJobsExportWritesStagesFromMarks(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 4)
	before := time.Now().UTC().Format("2006-01-02")
	mustCode(t, 0, "jobs", "mark", "gh:co0:0", "applied")
	mustCode(t, 0, "jobs", "mark", "gh:co0:0", "interview")
	mustCode(t, 0, "jobs", "mark", "gh:co0:0", "rejected")
	mustCode(t, 0, "jobs", "mark", "gh:co1:1", "applied")
	mustCode(t, 0, "jobs", "mark", "gh:co2:2", "saved")
	out := filepath.Join(t.TempDir(), "stages.xlsx")
	exportJSON(t, "--view", "all", "--top", "10", "--out", out)
	rows := exportRows(t, out)
	byKey := map[string][]string{}
	for _, r := range rows[1:] {
		byKey[r[col("Key")]] = r
	}
	after := time.Now().UTC().Format("2006-01-02")
	const today = "today"
	for key, want := range map[string][4]string{
		"gh:co0:0": {"applied", "rejected", "interview", today},
		"gh:co1:1": {"applied", "", "", today},
		"gh:co2:2": {"saved", "", "", ""},
		"gh:co3:3": {"", "", "", ""},
	} {
		r, ok := byKey[key]
		if !ok {
			t.Fatalf("%s missing from %v", key, rows)
		}
		got := [4]string{r[col("Mark")], r[col("Stage")], r[col("Furthest stage")], r[col("Applied")]}
		if want[3] == today && (got[3] == before || got[3] == after) {
			got[3] = today // the day the marks were made, across a midnight
		}
		if got != want {
			t.Errorf("%s: mark/stage/furthest/applied = %v, want %v", key, got, want)
		}
	}
}
