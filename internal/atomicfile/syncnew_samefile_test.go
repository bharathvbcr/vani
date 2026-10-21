package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// A path that was replaced by another (still empty) file since OpenAppend is
// not the file this creator made, so a failed SyncNew must leave it alone.
func TestSyncNewLeavesAReplacedEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.jsonl")
	a, created, err := OpenAppend(p, os.O_APPEND)
	if err != nil || !created {
		t.Fatal(err)
	}
	defer a.Close()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncNew(p, func(string) error { return os.ErrInvalid }); err == nil {
		t.Fatal("want the sync error")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("another file at the path was removed: %v", err)
	}
}
