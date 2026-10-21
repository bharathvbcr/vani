package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/lca"
)

func lcaStateWithIndex(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	x := lca.NewIndex()
	err := x.Merge(lca.Partial{
		Source:    lca.SourceMeta{Name: "t.csv", SHA256: "a"},
		Employers: map[string]map[int]lca.Counts{"ACME": {2025: {H1B: 7}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Save(lcaIndexPath(state)); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLoadLCAWithoutIndexIsNil(t *testing.T) {
	x, err := loadLCA(t.TempDir())
	if x != nil || err != nil {
		t.Fatalf("loadLCA = %v, %v; want nil, nil", x, err)
	}
}

func TestLoadLCAUsesAndRepairsTheView(t *testing.T) {
	var notes bytes.Buffer
	old := lcaNotes
	lcaNotes = &notes
	t.Cleanup(func() { lcaNotes = old })

	state := lcaStateWithIndex(t)
	for i := 0; i < 2; i++ { // the first load derives the view, the second reads it
		x, err := loadLCA(state)
		if err != nil || x.Lookup("Acme Inc").Total != 7 {
			t.Fatalf("load %d: %v %v", i, x, err)
		}
	}
	if _, err := os.Stat(lca.ViewPath(lcaIndexPath(state))); err != nil {
		t.Fatalf("view not written: %v", err)
	}
	if notes.Len() != 0 {
		t.Fatalf("routine rebuild was noisy: %q", notes.String())
	}

	// A garbled view falls back to the index, with a visible note.
	if err := os.WriteFile(lca.ViewPath(lcaIndexPath(state)), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, err := loadLCA(state)
	if err != nil || x.Lookup("Acme Inc").Total != 7 {
		t.Fatalf("after corruption: %v %v", x, err)
	}
	if !strings.Contains(notes.String(), "lca view") || !strings.Contains(notes.String(), "rebuilt") {
		t.Errorf("no visible note for a refused view: %q", notes.String())
	}

	// Re-importing changes the index; the old view must not answer for it.
	idx, err := loadLCAIndex(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Merge(lca.Partial{
		Source:    lca.SourceMeta{Name: "u.csv", SHA256: "b"},
		Employers: map[string]map[int]lca.Counts{"ACME": {2025: {H1B: 5}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Save(lcaIndexPath(state)); err != nil {
		t.Fatal(err)
	}
	if x, err = loadLCA(state); err != nil || x.Lookup("Acme Inc").Total != 12 {
		t.Fatalf("after re-import: %v %v", x, err)
	}
}

func TestLoadLCACorruptIndexIsAnError(t *testing.T) {
	state := t.TempDir()
	p := lcaIndexPath(state)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if x, err := loadLCA(state); err == nil {
		t.Fatalf("corrupt index loaded as %v", x)
	}
}
