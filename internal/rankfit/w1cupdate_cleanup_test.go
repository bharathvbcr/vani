package rankfit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A refused Update in a directory that existed before leaves that directory (and
// whatever else is in it) alone.
func TestW1cUpdateRefusedKeepsAnExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rank")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "other.json")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = Update(context.Background(), filepath.Join(dir, "w.json"), func(*File, bool) error { return os.ErrInvalid })
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the existing directory's contents were disturbed: %v", err)
	}
}

// Every directory a refused Update created goes, nested ones included, and the
// lock file with them; a directory that already held something else stays.
func TestW1cUpdateRefusedRemovesWhatItCreated(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	for name, fn := range map[string]func(*File, bool) error{
		"fn error": func(*File, bool) error { return os.ErrInvalid },
		"invalid":  func(cur *File, _ bool) error { *cur = File{V: 99}; return nil },
	} {
		if err := Update(context.Background(), filepath.Join(deep, "w.json"), fn); err == nil {
			t.Fatalf("%s: Update succeeded", name)
		}
		if _, err := os.Stat(filepath.Join(root, "a")); err == nil {
			t.Errorf("%s: a refused Update left %s", name, filepath.Join(root, "a"))
		}
	}
	// A directory something else filled meanwhile is not removed.
	_ = Update(context.Background(), filepath.Join(deep, "w.json"), func(*File, bool) error {
		_ = os.WriteFile(filepath.Join(deep, "other"), []byte("x"), 0o644)
		return os.ErrInvalid
	})
	if _, err := os.Stat(filepath.Join(deep, "other")); err != nil {
		t.Errorf("a directory holding another file was removed: %v", err)
	}
}

// A successful Update keeps its directory, file and lock file.
func TestW1cUpdateSuccessKeepsTheDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rank", "w.json")
	if err := Update(context.Background(), path, func(cur *File, _ bool) error { *cur = goodFile(); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

// A cancelled ctx returns the context's error.
func TestW1cUpdateCancelReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Update(ctx, filepath.Join(t.TempDir(), "w.json"), func(cur *File, _ bool) error { *cur = goodFile(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
}
