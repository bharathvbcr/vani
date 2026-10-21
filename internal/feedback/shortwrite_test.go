package feedback

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestShortWriteIsCutBackToThePreWriteSize(t *testing.T) {
	dir := t.TempDir()
	appendN(t, dir, imp("gh:a:1", 1))
	path := filepath.Join(Dir(dir), ImpressionsFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	old := writeAll
	writeAll = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2]) // half the row reaches the file
		return n, boom
	}
	t.Cleanup(func() { writeAll = old })

	if _, err := AppendImpressions(t.Context(), dir, []Impression{imp("gh:a:2", 1)}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write error", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the failed append left bytes behind: before %d, after %d, %v", len(before), len(after), err)
	}
	writeAll = old
	appendN(t, dir, imp("gh:a:3", 1))
	got, err := ReadImpressions(dir)
	if err != nil || len(got.Rows) != 2 || got.Skipped != 0 {
		t.Fatalf("rows %d skipped %d err %v", len(got.Rows), got.Skipped, err)
	}
}

func TestShortWriteAfterTornTailRestoresTheFragmentOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(Dir(dir), ImpressionsFile)
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("frag"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := writeAll
	writeAll = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:3])
		return n, errors.New("io")
	}
	t.Cleanup(func() { writeAll = old })
	if _, err := AppendImpressions(t.Context(), dir, []Impression{imp("gh:a:2", 1)}); err == nil {
		t.Fatal("want an error")
	}
	if b, _ := os.ReadFile(path); string(b) != "frag" {
		t.Fatalf("file = %q, want the original fragment", b)
	}
}
