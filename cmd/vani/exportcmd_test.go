package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func seedExportLedger(t *testing.T, state string, descs map[string]string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for k, d := range descs {
		p := posting.Posting{Key: posting.Key(k), ATS: "gh", Company: "Acme", Title: "Engineer", Description: d}
		ann := map[string]string{"note": "FAKE-PERSONAL-NOTE Jane Doe +1-555-0100"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func exportFile(state string) string {
	return filepath.Join(state, "export", "heldout", "owner_labels.jsonl")
}

func TestExportHeldoutWritesKeysAndHashesOnly(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "Build  things", "gh:2": "Other", "gh:3": "unlabelled"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	putEntry(t, state, eval.Entry{Key: "gh:2", Labels: map[string]string{eval.WouldApply: "yes"}, Source: "decile:3", PSelect: 0.2})

	o, _ := mustCode(t, 0, "export", "heldout")
	if !strings.Contains(o, "exported 2") {
		t.Fatalf("stdout %q", o)
	}
	b, err := os.ReadFile(exportFile(state))
	if err != nil {
		t.Fatal(err)
	}
	want1, _ := eval.NormalizedTextSHA256("Build things")
	if !strings.Contains(string(b), `"key":"gh:1","text_sha256":"`+want1+`"`) || strings.Contains(string(b), "gh:3") {
		t.Fatalf("export:\n%s", b)
	}
	for _, leak := range []string{"FAKE-PERSONAL", "Jane", "555", "Build", "strong_fit", "decile", "Acme"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("export leaks %q", leak)
		}
	}
	// Idempotent: the same labels give the same bytes.
	mustCode(t, 0, "export", "heldout")
	b2, _ := os.ReadFile(exportFile(state))
	if !bytes.Equal(b, b2) {
		t.Fatalf("re-export differs:\n%s\n%s", b, b2)
	}
}

func TestExportHeldoutCustomOutReplacesAtomicallyAndNoFollow(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "text"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "owner.jsonl")
	if err := os.Symlink(victim, out); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "export", "heldout", "--out", out)
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("export wrote through a symlink: %q", b)
	}
	if fi, err := os.Lstat(out); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("out is not a regular file: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestExportHeldoutReportsMissingLedgerText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "text"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	putEntry(t, state, eval.Entry{Key: "gh:ghost", Labels: map[string]string{"role_fit": "strong_fit"}})
	o, e := mustCode(t, 1, "export", "heldout")
	if !strings.Contains(e, "gh:ghost") || !strings.Contains(o, "exported 1") {
		t.Fatalf("stdout %q stderr %q", o, e)
	}
	b, _ := os.ReadFile(exportFile(state))
	if strings.Contains(string(b), "ghost") {
		t.Fatalf("missing key exported: %s", b)
	}
}

func TestExportUsage(t *testing.T) {
	setup(t)
	mustCode(t, 2, "export")
	mustCode(t, 2, "export", "bogus")
	mustCode(t, 2, "export", "heldout", "extra")
}

// The export follows a newly written label through the store hook that every
// label path (label session, audit answer) shares.
func TestReexportOnNewLabel(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "one", "gh:2": "two"})
	e := &env{stateDir: state, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	s, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(exportFile(state))
	if err != nil || strings.Count(string(b1), "\n") != 1 {
		t.Fatalf("after first label: %q %v", b1, err)
	}
	if err := s.Put(eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(exportFile(state))
	if strings.Count(string(b2), "\n") != 2 || !strings.Contains(string(b2), "gh:2") {
		t.Fatalf("after second label: %q", b2)
	}
	// Relabelling the same posting changes no byte.
	if err := s.Put(eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "mismatch_domain"}}); err != nil {
		t.Fatal(err)
	}
	if b3, _ := os.ReadFile(exportFile(state)); !bytes.Equal(b2, b3) {
		t.Fatalf("relabel changed the export")
	}
}
