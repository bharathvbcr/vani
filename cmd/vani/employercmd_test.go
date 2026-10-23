package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/employer"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

const testIPEDS = "UNITID,INSTNM,IALIAS,CONTROL,DEGGRANT\n" +
	"100001,State University of Nowhere,Nowhere State U,1,1\n" +
	"100002,Profit Tech Institute,,3,1\n"

const testBMF = "EIN,NAME,SUBSECTION,STATUS\n000000001,QUILLWORT FOUNDATION,03,01\n"

func writeCSV(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEmployerImportLookup(t *testing.T) {
	_, stateDir := setup(t)
	_, e := mustCode(t, 1, "employer", "lookup", "Anything")
	if !strings.Contains(e, "no employer lists imported") {
		t.Fatalf("lookup before import: %q", e)
	}
	o, _ := mustCode(t, 0, "employer", "import", writeCSV(t, "hd2024.csv", testIPEDS))
	if !strings.Contains(o, "ipeds_hd") || !strings.Contains(o, "1 of 2 rows counted") {
		t.Fatalf("import: %q", o)
	}
	mustCode(t, 0, "employer", "import", writeCSV(t, "eo_zz.csv", testBMF))
	if _, err := os.Stat(employerIndexPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	for company, want := range map[string]string{
		"State University of Nowhere": "likely H-1B cap-exempt",
		"Quillwort Foundation":        "likely non-profit (IRS",
		"Profit Tech Institute":       "no employer-list match",
	} {
		o, _ := mustCode(t, 0, "employer", "lookup", company)
		if !strings.Contains(o, want) {
			t.Errorf("lookup %q = %q, want %q", company, o, want)
		}
	}
	// Import wrote the lookup view, so a lookup does not rebuild it.
	viewPath := employer.ViewPath(employerIndexPath(stateDir))
	if _, e := mustCode(t, 0, "employer", "lookup", "Quillwort Foundation"); strings.Contains(e, "rebuilt") {
		t.Fatalf("lookup rebuilt a fresh view: %q", e)
	}
	// Malformed and unrecognised files are refused and leave the index and
	// its view alone.
	before, _ := os.ReadFile(employerIndexPath(stateDir))
	beforeView, err := os.ReadFile(viewPath)
	if err != nil {
		t.Fatal(err)
	}
	mustCode(t, 1, "employer", "import", writeCSV(t, "bad.csv", "A,B\n1,2\n"))
	mustCode(t, 1, "employer", "import", writeCSV(t, "empty.csv", ""))
	mustCode(t, 1, "employer", "import", filepath.Join(t.TempDir(), "missing.csv"))
	if after, _ := os.ReadFile(employerIndexPath(stateDir)); string(after) != string(before) {
		t.Fatal("a refused import changed the index")
	}
	if after, _ := os.ReadFile(viewPath); string(after) != string(beforeView) {
		t.Fatal("a refused import changed the view")
	}
	// A missing view is rebuilt from the index, said so, and written back.
	if err := os.Remove(viewPath); err != nil {
		t.Fatal(err)
	}
	o, e = mustCode(t, 0, "employer", "lookup", "Quillwort Foundation")
	if !strings.Contains(o, "likely non-profit (IRS") || !strings.Contains(e, "lookup view rebuilt") {
		t.Fatalf("lookup without a view: %q %q", o, e)
	}
	if after, _ := os.ReadFile(viewPath); string(after) != string(beforeView) {
		t.Fatal("rebuilt view differs from the one import wrote")
	}
	// A corrupt index is an error, never "no data".
	if err := os.WriteFile(employerIndexPath(stateDir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, e = mustCode(t, 1, "employer", "lookup", "Quillwort Foundation")
	if !strings.Contains(e, "corrupt") {
		t.Fatalf("corrupt index: %q", e)
	}
	mustCode(t, 2, "employer")
	mustCode(t, 2, "employer", "wat")
	mustCode(t, 2, "employer", "import")
	mustCode(t, 2, "employer", "lookup")
}

func TestEmployerLookupAdapterOnlyConfidentAttributes(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "employer", "import", writeCSV(t, "hd.csv", testIPEDS))
	// Import wrote a fresh view, so loading it must not warn.
	x, err := loadEmployerView(stateDir, func(format string, a ...any) { t.Errorf("unexpected warning: "+format, a...) })
	if err != nil || x == nil {
		t.Fatalf("load: %v", err)
	}
	l := employerLookup{x}
	if got := l.Employer("State University of Nowhere"); len(got.Attributes) != 1 || got.Attributes[0] != "cap_exempt_likely" {
		t.Fatalf("exact: %+v", got)
	}
	if got := l.Employer("State University"); len(got.Attributes) != 0 || !strings.Contains(got.Badge, "unique prefix") {
		t.Fatalf("prefix match must carry no attributes: %+v", got)
	}
}

// The pipeline loads the view too: a missing view is rebuilt with a warning
// in the run's log, and ranking still gets confident attributes.
func TestRunnerLoadsEmployerViewAndWarnsOnRebuild(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	_, stateDir := setup(t)
	mustCode(t, 0, "employer", "import", writeCSV(t, "hd.csv", testIPEDS))
	cfg := config.Default()
	cfg.LLM = testGeminiLLM
	cfg.Decide.OpenRouterModel = ""
	cfg.EmployerPreferences = []string{"cap_exempt_likely"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	runner := func() (*pipeline.Runner, string) {
		t.Helper()
		var warn bytes.Buffer
		w := &wired{cfg: cfg, stateDir: stateDir, warn: &warn}
		r, err := w.runner(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.Employer == nil {
			t.Fatal("runner has no employer lookup")
		}
		if got := r.Employer.Employer("State University of Nowhere"); len(got.Attributes) != 1 || got.Attributes[0] != "cap_exempt_likely" {
			t.Fatalf("runner lookup: %+v", got)
		}
		return r, warn.String()
	}
	if _, warn := runner(); strings.Contains(warn, "employer:") {
		t.Fatalf("fresh view warned: %q", warn)
	}
	if err := os.Remove(employer.ViewPath(employerIndexPath(stateDir))); err != nil {
		t.Fatal(err)
	}
	if _, warn := runner(); !strings.Contains(warn, "vani: employer: lookup view rebuilt") {
		t.Fatalf("rebuild not warned: %q", warn)
	}
	if _, warn := runner(); strings.Contains(warn, "employer:") {
		t.Fatalf("rebuilt view was not written back: %q", warn)
	}
}
