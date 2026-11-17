package rankfit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const w4dShares = `{"fit":0.1,"coverage":0.1,"semantic":0.05,"keyword":0.6,"title":0.05,"employer":0.05,"kb_match":0.05}`

func w4dLoad(t *testing.T, body string) (File, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "w.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func w4dFile(parts ...string) string {
	return `{"v":1,"fitted_at":"2026-10-18T00:00:00Z",` + strings.Join(parts, ",") + `}`
}

// A report field this binary does not know (a newer vani wrote it) is
// provenance: the file still loads, ranks by its shares, and the field is noted.
func TestW4dLoadToleratesUnknownReportFields(t *testing.T) {
	for name, report := range map[string]string{
		"top level":        `{"labels":5,"new_field":1}`,
		"nested in weight": `{"labels":5,"weights":{"held_out_pairs":9,"newer":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":`+report))
			if err != nil {
				t.Fatalf("a file with an extra report field was refused: %v", err)
			}
			if f.Shares == nil || f.Shares.Keyword != 0.6 || f.Report == nil || f.Report.Labels != 5 {
				t.Errorf("parts not decoded: %+v", f)
			}
			if !strings.Contains(f.ReportIgnored, "unknown field") {
				t.Errorf("ReportIgnored %q does not record the ignored field", f.ReportIgnored)
			}
		})
	}
	f, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":{"labels":5}`))
	if err != nil || f.ReportIgnored != "" {
		t.Errorf("a clean report: err %v ignored %q", err, f.ReportIgnored)
	}
}

// Unknown fields anywhere in the parts that rank, and at the top level, are
// still refused, and the refusal names the part.
func TestW4dLoadRefusesUnknownFieldsInRankingParts(t *testing.T) {
	cal := `{"d1":{"a":0,"b":1}}`
	for name, tc := range map[string]struct{ body, part string }{
		"shares":                {w4dFile(`"shares":` + strings.Replace(w4dShares, `"fit":0.1`, `"fit":0.1,"extra":1`, 1)), "shares"},
		"calibration":           {w4dFile(`"calibration":` + strings.Replace(cal, `"a":0`, `"a":0,"extra":1`, 1)), "calibration"},
		"top level":             {w4dFile(`"shares":`+w4dShares, `"surprise":1`), "file"},
		"type error in shares":  {w4dFile(`"shares":{"fit":"x"}`), "shares"},
		"malformed report type": {w4dFile(`"shares":`+w4dShares, `"report":{"labels":"x"}`), "report"},
		"report not an object":  {w4dFile(`"shares":`+w4dShares, `"report":3`), "report"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := w4dLoad(t, tc.body)
			if err == nil {
				t.Fatal("loaded")
			}
			var pe *PartError
			if !errors.As(err, &pe) || pe.Part != tc.part {
				t.Errorf("err %v: part %v, want %q", err, pe, tc.part)
			}
		})
	}
	// Validate failures name their part too.
	_, err := w4dLoad(t, w4dFile(`"shares":`+strings.Replace(w4dShares, `"fit":0.1`, `"fit":-1`, 1)))
	var pe *PartError
	if !errors.As(err, &pe) || pe.Part != "shares" {
		t.Errorf("negative share: %v", err)
	}
}

// A null report is no report, and data after the file is still refused.
func TestW4dLoadNullReportAndTrailingData(t *testing.T) {
	if f, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":null`)); err != nil || f.Report != nil {
		t.Errorf("null report: %v %+v", err, f.Report)
	}
	if _, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares)+` {}`); err == nil {
		t.Error("data after the file loaded")
	}
}

// What a file records of an ignored report field is not written back: the file
// format is unchanged.
func TestW4dIgnoredNoteIsNotSerialized(t *testing.T) {
	f, err := w4dLoad(t, w4dFile(`"shares":`+w4dShares, `"report":{"labels":5,"new_field":1}`))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "out.json")
	if err := Write(p, f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "new_field") || strings.Contains(string(b), "ignored") {
		t.Errorf("note leaked into the file:\n%s", b)
	}
}
