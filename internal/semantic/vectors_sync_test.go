package semantic

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
)

// A vector append of any number of records is one file sync; creating the log
// syncs its header and the directory that names it, once each.
func TestVectorAppendSyncsTheLogOnce(t *testing.T) {
	dir := t.TempDir()
	log := synctest.Record(t, dir)
	s := newChunkStore(logPath(dir), "model-x", MaxVectorBytes)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if n, d := len(log.Files()), len(log.Dirs()); n != 1 || d != 1 {
		t.Fatalf("creating the log: %d file syncs and %d directory syncs, want 1 and 1", n, d)
	}
	log.Reset()
	if n, _, err := s.append([]vecRec{primRec(1), primRec(2), primRec(3)}); n != 3 || err != nil {
		t.Fatalf("append: %d, %v", n, err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("a 3-record append: %d file syncs, want 1", n)
	}
}
