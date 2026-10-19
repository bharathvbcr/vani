package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLCAImportAndLookup(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	_, e := mustCode(t, 1, "lca", "lookup", "Acme")
	if !strings.Contains(e, "no LCA data imported") {
		t.Fatalf("lookup before import: %q", e)
	}
	csv := "CASE_NUMBER,CASE_STATUS,RECEIVED_DATE,DECISION_DATE,VISA_CLASS,EMPLOYER_NAME\n" +
		"I-1,Certified,2024-01-02,2024-01-10,H-1B,\"Acme, Inc.\"\n" +
		"I-2,Certified,2024-02-02,2024-02-10,H-1B,ACME INC\n" +
		"I-3,Denied,2024-02-02,2024-02-10,H-1B,ACME INC\n"
	path := filepath.Join(t.TempDir(), "lca.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "lca", "import", path)
	if !strings.Contains(o, "1 employers") {
		t.Fatalf("import output %q", o)
	}
	if _, err := os.Stat(lcaIndexPath(stateDir)); err != nil {
		t.Fatalf("index not written: %v", err)
	}
	o, _ = mustCode(t, 0, "lca", "lookup", "Acme")
	if !strings.Contains(o, "2 certified") {
		t.Fatalf("lookup %q", o)
	}
	// Re-importing the same file must not double-count.
	mustCode(t, 0, "lca", "import", path)
	o, _ = mustCode(t, 0, "lca", "lookup", "Acme")
	if !strings.Contains(o, "2 certified") {
		t.Fatalf("lookup after re-import %q", o)
	}
	mustCode(t, 2, "lca", "import")
}

func TestEvalAndLabelOnEmptyLedger(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "eval")
	if !strings.Contains(o, "n/a") && !strings.Contains(strings.ToLower(o), "0") {
		t.Fatalf("eval output %q", o)
	}
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("")
	o, _ = mustCode(t, 0, "label", "-n", "5")
	if !strings.Contains(o, "nothing to label") {
		t.Fatalf("label output %q", o)
	}
	mustCode(t, 2, "label", "-n", "0")
}
