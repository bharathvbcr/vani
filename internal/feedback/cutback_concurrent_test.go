package feedback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the cut-back truncates to the pre-write size, so a row another
// appender (one that does not take the feedback lock) added meanwhile is cut
// off with our fragment.
func TestCutBackDoesNotEraseAnotherAppendersRow(t *testing.T) {
	dir := t.TempDir()
	appendN(t, dir, imp("gh:a:1", 1))
	path := filepath.Join(Dir(dir), ImpressionsFile)
	old := writeAll
	t.Cleanup(func() { writeAll = old })
	writeAll = func(f *os.File, b []byte) (int, error) {
		o, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = o.WriteString("OTHER-APPENDER-ROW\n")
		o.Close()
		n, _ := f.Write(b[:len(b)/2])
		return n, fmt.Errorf("disk full")
	}
	if _, err := AppendImpressions(t.Context(), dir, []Impression{imp("gh:a:2", 1)}); err == nil {
		t.Fatal("want error")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "OTHER-APPENDER-ROW") {
		t.Fatalf("the other appender's row was truncated away: %q", b)
	}
}
