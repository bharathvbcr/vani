package atomicfile_test

import (
	"os"
	"path/filepath"
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

func TestOpenAppendDoesNotFollowLeafSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "events")
	if err := os.WriteFile(target, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Existing target: the link is replaced by a new empty regular file.
	f, created, err := atomicfile.OpenAppend(link, os.O_APPEND)
	if err != nil || !created {
		t.Fatalf("link to an existing file: created=%v err=%v", created, err)
	}
	_, _ = f.WriteString("b\n")
	f.Close()
	if b, _ := os.ReadFile(target); string(b) != "a\n" {
		t.Errorf("the file behind the link was appended to: %q", b)
	}
	if st, err := os.Lstat(link); err != nil || !st.Mode().IsRegular() {
		t.Errorf("link not replaced: %v %v", st, err)
	}
	if b, _ := os.ReadFile(link); string(b) != "b\n" {
		t.Errorf("link's replacement = %q", b)
	}

	// Dangling: nothing is created at the target; SyncNew syncs the leaf's
	// own directory.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(realDir, "new"), dangling); err != nil {
		t.Fatal(err)
	}
	f, created, err = atomicfile.OpenAppend(dangling, 0)
	if err != nil || !created {
		t.Fatalf("dangling: created=%v err=%v", created, err)
	}
	f.Close()
	var synced []string
	if err := atomicfile.SyncNew(dangling, func(d string) error { synced = append(synced, d); return nil }); err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(dir); len(synced) != 1 || synced[0] != want {
		t.Errorf("synced %v, want the leaf's directory %s", synced, want)
	}
	if _, err := os.Lstat(filepath.Join(realDir, "new")); !os.IsNotExist(err) {
		t.Errorf("a file was created through the dangling link: %v", err)
	}
	if st, err := os.Lstat(dangling); err != nil || !st.Mode().IsRegular() {
		t.Errorf("dangling link not replaced: %v %v", st, err)
	}

	// A loop at the leaf is only a link to replace.
	loopA, loopB := filepath.Join(dir, "la"), filepath.Join(dir, "lb")
	_ = os.Symlink(loopB, loopA)
	_ = os.Symlink(loopA, loopB)
	f, created, err = atomicfile.OpenAppend(loopA, 0)
	if err != nil || !created {
		t.Fatalf("loop leaf: created=%v err=%v", created, err)
	}
	f.Close()
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
