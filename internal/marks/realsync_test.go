package marks

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
)

// The real fsync path, once, end to end: under RunWithoutSync every other test
// here skips it.
func TestRealFsyncRoundTrip(t *testing.T) {
	dir := Dir(t.TempDir())
	synctest.Real(t, dir)
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetNote(dir, k1, "real", t0); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarks(dir)
	if err != nil || len(m) != 1 {
		t.Fatalf("marks %v, %v; want the one written", m, err)
	}
}
