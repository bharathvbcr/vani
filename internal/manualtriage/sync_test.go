package manualtriage

import (
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
)

// Each reading written is one file sync; the store's first write also syncs
// the directory that names the new file, once.
func TestEachWriteSyncsTheFileOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	log := synctest.Record(t, dir)
	must(t, Set(dir, reading("gh:acme:1", t0)))
	must(t, Set(dir, reading("gh:acme:2", t0)))
	if _, err := Clear(dir, "gh:acme:1", t0); err != nil {
		t.Fatal(err)
	}
	if n, d := len(log.Files()), len(log.Dirs()); n != 3 || d != 1 {
		t.Fatalf("3 writes: %d file syncs and %d directory syncs, want 3 and 1", n, d)
	}
}
