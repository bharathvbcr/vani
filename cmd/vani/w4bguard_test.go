package main

import (
	"strings"
	"testing"
)

func w4bguardCSV(n int) string {
	var b strings.Builder
	b.WriteString("CASE_NUMBER,CASE_STATUS,RECEIVED_DATE,DECISION_DATE,VISA_CLASS,EMPLOYER_NAME\n")
	for i := 0; i < n; i++ {
		b.WriteString("I-1,Certified,2024-01-02,2024-01-10,H-1B,\"Acme, Inc.\"\n")
	}
	return b.String()
}

// A smaller file for an imported fiscal year is refused with code "shrinks"
// unless --replace is given; a cumulative (bigger) one needs no flag.
func TestW4bguardLCAImportReplaceFlag(t *testing.T) {
	setup(t)
	big := writeCSV(t, "fy2024-q4.csv", w4bguardCSV(10))
	small := writeCSV(t, "fy2024-q1.csv", w4bguardCSV(2))
	bigger := writeCSV(t, "fy2024-q4-v2.csv", w4bguardCSV(12))
	mustCode(t, 0, "lca", "import", big)

	args := []string{"--json", "lca", "import", small}
	o, _ := mustCode(t, 1, args...)
	msg, code := failureIn(t, args, o)
	if code != codeShrinks || !strings.Contains(msg, "--replace") || !strings.Contains(msg, "fy2024-q4.csv") {
		t.Fatalf("code %q msg %q", code, msg)
	}

	o, _ = mustCode(t, 0, "--json", "lca", "import", "--replace", small)
	var r struct {
		File        string   `json:"file"`
		Employers   int      `json:"employers"`
		FiscalYears []int    `json:"fiscal_years"`
		Replaced    []string `json:"replaced"`
	}
	if decodeOne(t, o, &r); len(r.Replaced) != 1 || r.Replaced[0] != "fy2024-q4.csv" {
		t.Fatalf("replaced = %+v", r)
	}

	o, _ = mustCode(t, 0, "--json", "lca", "import", bigger)
	r.Replaced = nil
	if decodeOne(t, o, &r); len(r.Replaced) != 1 || r.Replaced[0] != "fy2024-q1.csv" {
		t.Fatalf("a cumulative file needs no flag: %+v", r)
	}
}

// The help lists --replace and the replaced key of --json.
func TestW4bguardLCAImportHelp(t *testing.T) {
	setup(t)
	_, e := mustCode(t, 0, "lca", "import", "-h")
	for _, want := range []string{"-replace", `"replaced"`} {
		if !strings.Contains(e, want) {
			t.Errorf("help lacks %q:\n%s", want, e)
		}
	}
}
