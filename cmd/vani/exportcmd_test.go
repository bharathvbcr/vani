package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	// The export lock sits beside the output as <out>.lock; nothing else.
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, ent := range ents {
		names = append(names, ent.Name())
	}
	if len(names) != 3 || names[0] != "owner.jsonl" || names[1] != "owner.jsonl.lock" || names[2] != "victim" {
		t.Errorf("temp files left behind: %v", names)
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

// export posting-fingerprints signs the labelled postings it has text for,
// names each it has not (exit 1), and with --json says the same as one
// document whose count is read back from the file written.
func TestExportPostingFingerprints(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	long := "build the inference stack for large language models on gpus at scale"
	seedExportLedger(t, state, map[string]string{"gh:1": long, "gh:2": long + " with rust"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	putEntry(t, state, eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "strong_fit"}})
	path := filepath.Join(state, "export", "heldout", "signatures.json")

	o, _ := mustCode(t, 0, "export", "posting-fingerprints")
	if !strings.Contains(o, "exported 2 posting fingerprint(s) to "+path) {
		t.Fatalf("stdout %q", o)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []struct {
			Key string `json:"key"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Entries) != 2 || strings.Contains(string(b), "inference") {
		t.Fatalf("export (%v):\n%s", err, b)
	}

	putEntry(t, state, eval.Entry{Key: "gh:ghost", Labels: map[string]string{"role_fit": "strong_fit"}})
	o, e := mustCode(t, 1, "export", "posting-fingerprints")
	if !strings.Contains(o, "exported 2") || !strings.Contains(e, "gh:ghost") || !strings.Contains(e, "1 labelled posting(s) have no text") {
		t.Fatalf("stdout %q stderr %q", o, e)
	}
	o, _ = mustCode(t, 1, "export", "posting-fingerprints", "--json")
	var r heldoutReply
	dec := json.NewDecoder(strings.NewReader(o))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || r.Path != path || r.Exported != 2 || len(r.Missing) != 1 || r.Missing[0] != "gh:ghost" ||
		r.Code != codeRuntime || !strings.Contains(r.Error, "no text") || dec.More() {
		t.Fatalf("reply %+v (%v): %s", r, err, o)
	}
	// Nothing labelled with text is an error, not an empty export.
	_, state2 := setup(t)
	mustCode(t, 0, "init")
	putEntry(t, state2, eval.Entry{Key: "gh:ghost", Labels: map[string]string{"role_fit": "strong_fit"}})
	if _, e := mustCode(t, 1, "export", "posting-fingerprints"); !strings.Contains(e, "no held-out posting text") {
		t.Errorf("stderr %q", e)
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
