package feedback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeLog(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(Dir(dir), ImpressionsFile)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two (or many) writers racing onto a torn tail: the append lock must make
// only the first one prepend the newline, so exactly one line (the fragment)
// is skipped and no blank line or lost row appears.
func TestConcurrentAppendersOnTornTailSkipExactlyTheFragment(t *testing.T) {
	for round := range 10 {
		dir := t.TempDir()
		writeLog(t, dir, `{"v":1,"at":"2026-10-20T09:00:00Z","surface":"dig`)
		const n = 8
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := AppendImpressions(t.Context(), dir, []Impression{imp(fmt.Sprintf("gh:a:%d", i), 1)}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		got, err := ReadImpressions(dir)
		if err != nil || len(got.Rows) != n || got.Skipped != 1 {
			t.Fatalf("round %d: rows %d skipped %d err %v, want %d and exactly 1", round, len(got.Rows), got.Skipped, err, n)
		}
		b, _ := os.ReadFile(filepath.Join(Dir(dir), ImpressionsFile))
		if strings.Contains(string(b), "\n\n") {
			t.Fatalf("round %d: blank line in the log", round)
		}
	}
}

func TestTornTailVariantsKeepTheNewRowAndSkipOnlyTheFragment(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		skipped int
	}{
		"only newline":       {"\n", 0},
		"only CR":            {"\r", 0},
		"CRLF rows then CR":  {"\r\n\r", 0},
		"multibyte lead":     {"\xe2", 1},
		"partial multibyte":  {"junk\n\xe2\x82", 2},
		"whole junk row":     {"not json\n", 1},
		"blank lines before": {"\n\n\n", 0},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeLog(t, dir, tc.content)
			appendN(t, dir, imp("gh:a:9", 1))
			got, err := ReadImpressions(dir)
			if err != nil || len(got.Rows) != 1 || got.Rows[0].Key != "gh:a:9" {
				t.Fatalf("rows %+v err %v", got.Rows, err)
			}
			if got.Skipped != tc.skipped {
				b, _ := os.ReadFile(filepath.Join(Dir(dir), ImpressionsFile))
				t.Fatalf("skipped %d, want %d; file %q", got.Skipped, tc.skipped, b)
			}
		})
	}
}

// A write that fails after partial bytes AND whose cut-back fails: the error
// must say both, not swallow the truncate failure.
func TestFailedWriteAndFailedCutBackBothReported(t *testing.T) {
	dir := t.TempDir()
	appendN(t, dir, imp("gh:a:1", 1))
	old := writeAll
	t.Cleanup(func() { writeAll = old })
	writeAll = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		_ = f.Close() // the truncate that follows must now fail
		return n, fmt.Errorf("disk full")
	}
	_, err := AppendImpressions(t.Context(), dir, []Impression{imp("gh:a:2", 1)})
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "could not be cut back") {
		t.Fatalf("err = %v, want both the write and the cut-back failure", err)
	}
}
