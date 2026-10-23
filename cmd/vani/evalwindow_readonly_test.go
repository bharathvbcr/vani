package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// snapshotDir returns every file under dir (relative name -> bytes).
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// `vani eval --window` reports; it never writes. Labelled, shadow-asked
// postings that no `label --window` has filed yet are filed in memory for the
// report, and the label directory (labels, windows.json, lock files) is
// byte-identical afterwards. Only `vani label --window` persists windows.json.
func TestEvalWindowWritesNothing(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	seedShadowed(t, stateDir, "a", 12, 12, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	dir := filepath.Join(stateDir, "eval", "labels")
	before := snapshotDir(t, dir)
	if _, ok := before[eval.WindowsFile]; ok {
		t.Fatalf("setup wrote %s", eval.WindowsFile)
	}
	o, e := mustCode(t, 0, "eval", "--window")
	if !strings.Contains(o, "### window-1: 12/100 postings (open)") {
		t.Fatalf("the report did not file the labels in memory:\n%s\n%s", o, e)
	}
	after := snapshotDir(t, dir)
	if len(after) != len(before) {
		t.Fatalf("eval --window changed the files in the label dir: before %d after %d", len(before), len(after))
	}
	for name, b := range before {
		if after[name] != b {
			t.Fatalf("eval --window rewrote %s", name)
		}
	}
	if o2, _ := mustCode(t, 0, "eval", "--window"); o2 != o {
		t.Fatalf("eval --window is not repeatable:\n%s\n%s", o, o2)
	}
}
