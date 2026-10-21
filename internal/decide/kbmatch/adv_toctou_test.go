package kbmatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Lock resolves the path at T1, Save resolves it at T2. If the link is
// retargeted between them, A's lock guards X while A's save writes Y, and a
// second runner can take Y's lock while A still "holds the store lock".
func TestLockAndSaveResolveAtDifferentTimes(t *testing.T) {
	dir := t.TempDir()
	x, y := filepath.Join(dir, "x.json"), filepath.Join(dir, "y.json")
	link := filepath.Join(dir, "store.json")
	if err := os.Symlink(x, link); err != nil {
		t.Fatal(err)
	}
	unlockA, err := Lock(link) // A locks x.json.lock
	if err != nil {
		t.Fatal(err)
	}
	defer unlockA()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(y, link); err != nil {
		t.Fatal(err)
	}
	unlockB, err := Lock(link) // B locks y.json.lock although A holds "the" lock
	if errors.Is(err, ErrBusy) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unlockB()
	if err := NewStore(link).Save(); err != nil { // A's save now lands on y.json
		t.Fatal(err)
	}
	if _, err := os.Stat(y); err != nil {
		t.Fatal(err)
	}
	t.Fatal("two runners hold the store lock at once (x.json.lock, y.json.lock); A's write landed on y.json under B's lock")
}
