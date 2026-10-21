package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
)

func TestWriteThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return atomicfile.Write(path, []byte("new")) })
}

func TestOpenAppendThroughSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "events")
	if err := os.WriteFile(target, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, created, err := atomicfile.OpenAppend(link, os.O_APPEND)
	if err != nil || created {
		t.Fatalf("existing target: created=%v err=%v", created, err)
	}
	_, _ = f.WriteString("b\n")
	f.Close()
	if b, _ := os.ReadFile(target); string(b) != "a\nb\n" {
		t.Errorf("target = %q", b)
	}

	// Dangling: created at the target, link kept, SyncNew syncs the target's dir.
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
	if want, _ := filepath.EvalSymlinks(realDir); len(synced) != 1 || synced[0] != want {
		t.Errorf("synced %v, want the target's directory %s", synced, want)
	}
	if st, err := os.Lstat(dangling); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link not kept: %v %v", st, err)
	}

	loopA, loopB := filepath.Join(dir, "la"), filepath.Join(dir, "lb")
	_ = os.Symlink(loopB, loopA)
	_ = os.Symlink(loopA, loopB)
	if f, _, err := atomicfile.OpenAppend(loopA, 0); err == nil {
		f.Close()
		t.Fatal("a symlink loop opened")
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
