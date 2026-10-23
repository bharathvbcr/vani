package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// SyncNew removes by path, so another opener that found
// the file after creation and appended loses its data when the creator's
// directory sync fails. (Both in-repo callers hold a directory lock across
// OpenAppend..SyncNew, which is what keeps this unreachable in production.)
func TestSyncNewDoesNotDeleteAnotherOpenersData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.jsonl")
	a, created, err := OpenAppend(p, os.O_APPEND)
	if err != nil || !created {
		t.Fatal(err)
	}
	defer a.Close()
	b, created, err := OpenAppend(p, os.O_APPEND)
	if err != nil || created {
		t.Fatalf("B: created=%v err=%v", created, err)
	}
	defer b.Close()
	if _, err := b.WriteString("B-row\n"); err != nil {
		t.Fatal(err)
	}
	_ = b.Sync()
	_ = SyncNew(p, func(string) error { return os.ErrInvalid })
	if got, err := os.ReadFile(p); err != nil || string(got) != "B-row\n" {
		t.Fatalf("B's synced row was deleted by A's failed SyncNew: %q %v", got, err)
	}
}

// TornTail turns an I/O error into "not torn". A check that could not
// run reads the same as one that passed, and the caller then glues its row
// onto the fragment.
func TestTornTailFailureIsNotReportedAsClean(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("a\nfrag"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if torn, err := TornTail(f); err == nil { // write-only fd: ReadAt fails (EBADF)
		t.Fatalf("TornTail returned torn=%v, nil error on a file whose tail it could not read", torn)
	}
}
