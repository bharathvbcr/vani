package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func sizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func TestReadStateFileIsBounded(t *testing.T) {
	dir := t.TempDir()
	if _, err := readStateFile(filepath.Join(dir, "none"), 10); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := readStateFile(p, 10); err != nil || string(b) != "0123456789" {
		t.Errorf("exactly the limit: %q %v", b, err)
	}
	_, err := readStateFile(p, 9)
	if err == nil || !strings.Contains(err.Error(), "10 bytes") || !strings.Contains(err.Error(), "9-byte limit") {
		t.Errorf("over the limit must name size and limit: %v", err)
	}
	if b, err := readStateFile(p, 0); err == nil {
		t.Errorf("limit 0 read %q", b)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := readStateFile(filepath.Join(dir, "empty"), 10); err != nil || len(b) != 0 {
		t.Errorf("empty: %q %v", b, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readStateFile(link, 100); !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Errorf("symlink: %v", err)
	}
}

// An oversized pin list is not read: it pins nothing and says so, with its size
// and the limit. A missing or malformed one stays quiet (embedLabelled rewrites it).
func TestPinLabelledOversizedListFailsLoudly(t *testing.T) {
	state := t.TempDir()
	var warn bytes.Buffer
	old := labelPinWarn
	labelPinWarn = &warn
	t.Cleanup(func() { labelPinWarn = old })
	ranks := map[string]float64{"a": 0.5}

	if got := pinLabelled(ranks, state); len(got) != 1 || warn.Len() != 0 {
		t.Fatalf("missing list: %v, warned %q", got, warn.String())
	}
	path := filepath.Join(semanticDir(state), labelledPinsName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pinLabelled(ranks, state); len(got) != 1 || warn.Len() != 0 {
		t.Fatalf("malformed list: %v, warned %q", got, warn.String())
	}
	sizedFile(t, path, maxStateFileBytes+1)
	got := pinLabelled(ranks, state)
	if len(got) != 1 || got["a"] != 0.5 {
		t.Errorf("oversized list changed the ranks: %v", got)
	}
	w := warn.String()
	if !strings.Contains(w, fmt.Sprint(maxStateFileBytes+1, " bytes")) || !strings.Contains(w, fmt.Sprint(maxStateFileBytes, "-byte limit")) {
		t.Errorf("warning does not name size and limit: %q", w)
	}
}

func pinTree(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// A failed write leaves the old pin list and no temporary file; a sync
// failure is reported, so a pin list that may not be on disk never looks written.
func TestWriteLabelledPinsFailureKeepsOldListAndLeavesNoTemp(t *testing.T) {
	state := t.TempDir()
	if err := writeLabelledPins(state, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	dir := semanticDir(state)
	want, err := os.ReadFile(filepath.Join(dir, labelledPinsName))
	if err != nil {
		t.Fatal(err)
	}

	restore := atomicfile.SetSyncForTest(func(string, bool) error { return errors.New("disk said no") })
	err = writeLabelledPins(state, []string{"new"})
	restore()
	if err == nil {
		t.Fatal("a failed sync was reported as a written pin list")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, labelledPinsName)); !bytes.Equal(got, want) {
		t.Errorf("old list changed: %q", got)
	}
	if names := pinTree(t, dir); len(names) != 1 || names[0] != labelledPinsName {
		t.Errorf("leftovers after a failed sync: %v", names)
	}

	// A rename that cannot land (a directory in the list's place) leaves no temp.
	state2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(semanticDir(state2), labelledPinsName, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeLabelledPins(state2, []string{"new"}); err == nil {
		t.Fatal("wrote over a directory")
	}
	if names := pinTree(t, semanticDir(state2)); len(names) != 1 || names[0] != labelledPinsName {
		t.Errorf("leftovers after a failed rename: %v", names)
	}

	// And it still writes and replaces.
	if err := writeLabelledPins(state, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if got := pinLabelled(map[string]float64{}, state); len(got) != 1 {
		t.Errorf("new list not read: %v", got)
	}
}

// An earlier removal's record over the read bound stops the removal before
// anything is changed, and says why.
func TestWatchlistRemoveRefusesAnOversizedEarlierRecord(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")
	tomb := pipeline.RemovedBoardPath(stateDir, sources.Board{ATS: posting.ATS("gh"), Token: "Acme"}.KeyPrefix())
	sizedFile(t, tomb, maxTombstoneReadBytes+1)
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, e := mustCode(t, 1, "watchlist", "remove", "--ats", "gh", "--token", "Acme")
	if !strings.Contains(e, fmt.Sprint(maxTombstoneReadBytes+1, " bytes")) || !strings.Contains(e, fmt.Sprint(maxTombstoneReadBytes, "-byte limit")) {
		t.Errorf("error does not name size and limit: %q", e)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(after, cfgBefore) {
		t.Error("the config changed although the removal was refused")
	}
	if st, err := os.Stat(tomb); err != nil || st.Size() != maxTombstoneReadBytes+1 {
		t.Errorf("the record was touched: %v %v", st, err)
	}
}

// A full title index is said once, through the caller's warn path, without a count.
func TestFillTitleIndexSaysOnceWhenFull(t *testing.T) {
	recs := func(n int) []ledger.Record {
		var out []ledger.Record
		for i := 0; i < n; i++ {
			out = append(out, ledger.Record{Posting: posting.Posting{Title: fmt.Sprintf("T%d", i), Description: fmt.Sprintf("description number %d", i)}})
		}
		return out
	}
	var lines []string
	warn := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }

	fillTitleIndex(semantic.NewBoundedTitleIndex(3), recs(10), warn)
	if len(lines) != 1 || !strings.Contains(lines[0], "title index is full") || strings.ContainsAny(lines[0], "0123456789") {
		t.Errorf("full index: %q", lines)
	}
	lines = nil
	fillTitleIndex(semantic.NewBoundedTitleIndex(3), recs(3), warn)
	fillTitleIndex(semantic.NewBoundedTitleIndex(3), nil, warn)
	if len(lines) != 0 {
		t.Errorf("an index at its bound with nothing refused warned: %q", lines)
	}
}
