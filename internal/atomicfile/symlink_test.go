package atomicfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestWriteDoesNotFollowLeafSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return atomicfile.Write(path, []byte("new")) })
}

// Resolve once, then Write the result: the operator-chosen path (the config
// file) is written through its link, and the link is kept.
func TestWriteOfResolvedPathFollowsLeafSymlinks(t *testing.T) {
	symlinktest.RunFollow(t, func(path string) error {
		target, err := atomicfile.Resolve(path)
		if err != nil {
			return err
		}
		return atomicfile.Write(target, []byte("new"))
	})
}

// An append-only log behind a leaf link must not be replaced by a new empty
// file: the append is refused, and the link and its target are left alone.
func TestOpenAppendRefusesLeafSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "events")
	if err := os.WriteFile(target, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, _, err := atomicfile.OpenAppend(link, os.O_APPEND)
	if err == nil {
		f.Close()
		t.Fatal("append through a link to an existing log was accepted")
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("error = %v, want ErrLinkedLeaf naming the symbolic link", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "a\n" {
		t.Errorf("target content changed: %q", b)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link was removed or replaced: %v %v", st, err)
	}
	if got, _ := os.Readlink(link); got != target {
		t.Errorf("link now points at %q", got)
	}

	// Dangling: refused, and nothing is created at the target or in its place.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(realDir, "new"), dangling); err != nil {
		t.Fatal(err)
	}
	if f, _, err := atomicfile.OpenAppend(dangling, 0); err == nil || !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("dangling link: err = %v, want ErrLinkedLeaf", err)
	}
	if _, err := os.Lstat(filepath.Join(realDir, "new")); !os.IsNotExist(err) {
		t.Errorf("a file was created through the dangling link: %v", err)
	}
	if st, err := os.Lstat(dangling); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling link removed or replaced: %v %v", st, err)
	}

	// A loop at the leaf is refused the same way.
	loopA, loopB := filepath.Join(dir, "la"), filepath.Join(dir, "lb")
	_ = os.Symlink(loopB, loopA)
	_ = os.Symlink(loopA, loopB)
	if f, _, err := atomicfile.OpenAppend(loopA, 0); err == nil || !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("loop leaf: err = %v, want ErrLinkedLeaf", err)
	}
}

func TestOpenAppendThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	f, created, err := atomicfile.OpenAppend(filepath.Join(linkDir, "events"), os.O_APPEND)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	f.Close()
	if _, err := os.Stat(filepath.Join(realDir, "events")); err != nil {
		t.Errorf("file not in the real directory: %v", err)
	}
}

func TestResolve(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(realDir, filepath.Join(root, "ldir"))
	_ = os.Symlink(filepath.Join(realDir, "f"), filepath.Join(root, "lf"))
	_ = os.Symlink("real/nope", filepath.Join(root, "dangle"))
	_ = os.Symlink("dangle", filepath.Join(root, "chain"))
	_ = os.Symlink("ldir/gone/x", filepath.Join(root, "deep"))
	for name, tc := range map[string]struct{ in, want string }{
		"plain existing":        {filepath.Join(realDir, "f"), filepath.Join(realDir, "f")},
		"file link":             {filepath.Join(root, "lf"), filepath.Join(realDir, "f")},
		"dir link":              {filepath.Join(root, "ldir", "f"), filepath.Join(realDir, "f")},
		"missing under dirlink": {filepath.Join(root, "ldir", "new"), filepath.Join(realDir, "new")},
		"missing, parent too":   {filepath.Join(root, "a", "b", "c"), filepath.Join(root, "a", "b", "c")},
		"dangling":              {filepath.Join(root, "dangle"), filepath.Join(realDir, "nope")},
		"dangling chain":        {filepath.Join(root, "chain"), filepath.Join(realDir, "nope")},
		"dangling via dir link": {filepath.Join(root, "deep"), filepath.Join(realDir, "gone", "x")},
	} {
		got, err := atomicfile.Resolve(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: Resolve(%s) = %q, %v; want %q", name, tc.in, got, err, tc.want)
		}
	}
}

// ResolveLeaf follows the directory part only: the leaf comes back as spelled,
// in the resolved directory, and resolving the result changes nothing.
func TestResolveLeaf(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(realDir, filepath.Join(root, "ldir"))
	_ = os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(realDir, "lf"))
	for name, tc := range map[string]struct{ in, want string }{
		"leaf link kept":        {filepath.Join(realDir, "lf"), filepath.Join(realDir, "lf")},
		"dir link, leaf link":   {filepath.Join(root, "ldir", "lf"), filepath.Join(realDir, "lf")},
		"missing under dirlink": {filepath.Join(root, "ldir", "new"), filepath.Join(realDir, "new")},
		"missing parent":        {filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b")},
	} {
		got, err := atomicfile.ResolveLeaf(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: ResolveLeaf(%s) = %q, %v; want %q", name, tc.in, got, err, tc.want)
			continue
		}
		if again, err := atomicfile.ResolveLeaf(got); err != nil || again != got {
			t.Errorf("%s: not idempotent: %q, %v", name, again, err)
		}
	}
	if _, err := atomicfile.ResolveLeaf(realDir); err == nil {
		t.Error("a directory leaf was accepted")
	}
}

func TestResolveBoundsLinkHops(t *testing.T) {
	dir := t.TempDir()
	// 40 dangling links in a chain: more hops than the bound, no loop.
	prev := filepath.Join(dir, "end")
	for i := range 40 {
		l := filepath.Join(dir, "l"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.Symlink(prev, l); err != nil {
			t.Fatal(err)
		}
		prev = l
	}
	if got, err := atomicfile.Resolve(prev); err == nil {
		t.Fatalf("Resolve followed 40 dangling links: %q", got)
	}
}
