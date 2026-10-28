package marks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

// A marks.jsonl that is a symlink is never replaced by an empty history: the
// write fails loudly and link and target stay as they were.
func TestSetMarkRefusesSymlinkedMarksLog(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "history.jsonl")
	const content = "keep me\n"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, fileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := SetMark(dir, k1, Saved, time.Unix(1700000000, 0))
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("SetMark = %v, want ErrLinkedLeaf", err)
	}
	if b, _ := os.ReadFile(target); string(b) != content {
		t.Errorf("target changed: %q", b)
	}
	if st, lerr := os.Lstat(link); lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", st, lerr)
	}
}

func TestCompactedWriteThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error {
		k := posting.Key("greenhouse:acme:1")
		return writeCompacted(path, map[posting.Key]Entry{k: {Key: k, Mark: Saved}})
	})
}

// A symlink planted after compaction and before reopen is not written through.
func TestReopenAfterCompactRefusesSwappedSymlink(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	appendLines(t, dir, 6000, func(int) posting.Key { return k1 })
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	const keep = "keep-target\n"
	if err := os.WriteFile(target, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	old := afterCompact
	t.Cleanup(func() { afterCompact = old })
	afterCompact = func(p string) {
		if err := os.Remove(p); err != nil {
			t.Errorf("remove compacted file: %v", err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Errorf("symlink: %v", err)
		}
	}
	_, err := SetMark(dir, k1, Applied, t0.Add(time.Hour))
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("SetMark = %v, want ErrLinkedLeaf", err)
	}
	if b, rerr := os.ReadFile(target); rerr != nil || string(b) != keep {
		t.Fatalf("target = %q, %v", b, rerr)
	}
	st, lerr := os.Lstat(path)
	if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
	}
}
