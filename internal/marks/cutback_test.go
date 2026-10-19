package marks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A truncate that fails after a failed write is reported with it: the line
// may still be in the file and take effect on the next replay.
func TestCutBackReportsAFailedTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ro, err := os.Open(path) // Truncate fails on a read-only handle
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	cause := errors.New("write failed")
	err = cutBack(ro, 0, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "may still take effect") {
		t.Fatalf("err %v; want the cause and the failed cut", err)
	}

	rw, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if err := cutBack(rw, 0, cause); err != cause {
		t.Fatalf("a clean cut returned %v, want the cause alone", err)
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Errorf("size %d after cutting to 0", st.Size())
	}
}
