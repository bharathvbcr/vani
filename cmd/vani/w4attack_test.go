package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func pinWarnBuf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := labelPinWarn
	labelPinWarn = &b
	t.Cleanup(func() { labelPinWarn = old })
	return &b
}

// Every readable pin list shape still pins; every unreadable shape returns the
// ranks unchanged without hanging.
func TestAttackPinLabelledShapes(t *testing.T) {
	warn := pinWarnBuf(t)
	state := t.TempDir()
	if err := writeLabelledPins(state, []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	ranks := map[string]float64{"live": 0.4}
	if got := pinLabelled(ranks, state); len(got) != 3 || got["h1"] < 1e300 {
		t.Fatalf("valid list: %v", got)
	}
	if len(ranks) != 1 {
		t.Fatal("pinLabelled mutated its input")
	}
	if got := pinLabelled(nil, state); got != nil {
		t.Fatalf("nil ranks: %v", got)
	}
	path := filepath.Join(semanticDir(state), labelledPinsName)
	for name, body := range map[string]string{"empty": "", "v2": `{"v":2,"text_hashes":["h"]}`, "nohashes": `{"v":1,"text_hashes":[]}`, "array": "[]", "null": "null", "trailing": `{"v":1,"text_hashes":["h"]} junk`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := pinLabelled(ranks, state)
		if name == "trailing" {
			continue // json.Unmarshal refuses trailing junk: pins nothing, like main
		}
		if len(got) != 1 {
			t.Errorf("%s: pinned %v", name, got)
		}
	}
	// A symlink, a FIFO and a directory in the list's place: no pins, no hang.
	for name, mk := range map[string]func() error{
		"symlink": func() error { return os.Symlink(filepath.Join(state, "elsewhere"), path) },
		"fifo":    func() error { return syscall.Mkfifo(path, 0o600) },
		"dir":     func() error { return os.Mkdir(path, 0o700) },
	} {
		_ = os.Remove(path)
		if err := mk(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		done := make(chan map[string]float64, 1)
		go func() { done <- pinLabelled(ranks, state) }()
		select {
		case got := <-done:
			if len(got) != 1 {
				t.Errorf("%s pinned: %v", name, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: pinLabelled hung", name)
		}
		_ = os.Remove(path)
	}
	if warn.Len() == 0 {
		t.Error("no unreadable shape warned")
	}
}

// A foreign file between 4 KiB (the loader's bound) and 64 KiB at the
// tombstone path does not stop a removal, and is replaced by a valid record.
func TestAttackWatchlistRemoveOverwritesAFourKiBFile(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")
	tomb := pipeline.RemovedBoardPath(stateDir, sources.Board{ATS: posting.ATS("gh"), Token: "Acme"}.KeyPrefix())
	if err := os.MkdirAll(filepath.Dir(tomb), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tomb, []byte(strings.Repeat("x", 60<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "watchlist", "remove", "--ats", "gh", "--token", "Acme")
	if st, err := os.Stat(tomb); err != nil || st.Size() > 4<<10 {
		t.Errorf("record not replaced: %v %v", st, err)
	}
}

// A real record is far under the bound: write one, remove again with
// --keep-postings (reads it back), and the record is deleted.
func TestAttackWatchlistRealRecordIsFarUnderTheBound(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")
	mustCode(t, 0, "watchlist", "remove", "--ats", "gh", "--token", "Acme")
	tomb := pipeline.RemovedBoardPath(stateDir, sources.Board{ATS: posting.ATS("gh"), Token: "Acme"}.KeyPrefix())
	st, err := os.Stat(tomb)
	if err != nil || st.Size() > 1<<10 {
		t.Fatalf("record: %v %v", st, err)
	}
	mustCode(t, 0, "watchlist", "remove", "--ats", "gh", "--token", "Acme", "--keep-postings")
	if _, err := os.Stat(tomb); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--keep-postings left the record: %v", err)
	}
}

// A symlinked record stops the removal before anything changes (documented
// behaviour of the fix); the config is untouched.
func TestAttackWatchlistRemoveRefusesASymlinkedRecord(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")
	tomb := pipeline.RemovedBoardPath(stateDir, sources.Board{ATS: posting.ATS("gh"), Token: "Acme"}.KeyPrefix())
	if err := os.MkdirAll(filepath.Dir(tomb), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(stateDir, "nowhere"), tomb); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfgPath)
	_, e := mustCode(t, 1, "watchlist", "remove", "--ats", "gh", "--token", "Acme")
	if !strings.Contains(e, "symbolic link") {
		t.Errorf("stderr: %q", e)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(after, before) {
		t.Error("config changed")
	}
}
