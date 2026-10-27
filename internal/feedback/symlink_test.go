package feedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// The feedback lock lives in the store directory: a symlinked spelling of it
// excludes the real one.
func TestLockIsSharedThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(context.Background(), realDir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lock(context.Background(), link); !errors.Is(err, ErrBusy) {
		t.Errorf("lock through the link = %v, want ErrBusy", err)
	}
}

// impressions.jsonl as a symlink is not rotated or appended through, even when
// the target is large enough that a stat-following rotation would rename the link.
func TestAppendRefusesSymlinkedImpressionsLog(t *testing.T) {
	old := maxLogBytes
	maxLogBytes = 32
	t.Cleanup(func() { maxLogBytes = old })

	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	const keep = "keep-target\n"
	if err := os.WriteFile(target, []byte(keep+keep+keep), 0o600); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, ImpressionsFile)
	if err := os.Symlink(target, live); err != nil {
		t.Fatal(err)
	}
	_, err := AppendImpressions(context.Background(), state, []Impression{imp("gh:a:1", 1)})
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("AppendImpressions = %v, want ErrLinkedLeaf", err)
	}
	if b, rerr := os.ReadFile(target); rerr != nil || string(b) != keep+keep+keep {
		t.Fatalf("target = %q, %v", b, rerr)
	}
	st, lerr := os.Lstat(live)
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("live leaf = %v %v, want a symlink", st, lerr)
	}
	if _, err := os.Lstat(filepath.Join(dir, RotatedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation moved the link: %v", err)
	}
}
