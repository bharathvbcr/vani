package atomicfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func evalRoot(t *testing.T) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A link planted in the state dir that points outside it is replaced, never
// written through: the outside file is untouched.
func TestWriteDoesNotFollowOutOfTreeLink(t *testing.T) {
	root := evalRoot(t)
	state := filepath.Join(root, "state")
	outside := filepath.Join(root, "ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(state, "cache.json")); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(state, "cache.json"), []byte("JD-TEXT")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Fatalf("outside file was written through the link: %q", b)
	}
	leaf := filepath.Join(state, "cache.json")
	if st, err := os.Lstat(leaf); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("leaf not replaced by a regular file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(leaf); string(b) != "JD-TEXT" {
		t.Fatalf("leaf = %q", b)
	}
}

// A dangling link at a cache name must not create a file outside the tree.
func TestWriteDoesNotCreateOutsideFileThroughDanglingLink(t *testing.T) {
	root := evalRoot(t)
	state := filepath.Join(root, "state")
	outside := filepath.Join(root, "elsewhere", "new")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(state, "x")); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(state, "x"), []byte("D")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file was created outside the state dir: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(outside)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a directory was created outside the state dir: %v", err)
	}
	if st, err := os.Lstat(filepath.Join(state, "x")); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("leaf not replaced by a regular file: %v %v", st, err)
	}
}

func TestResolveRelativeChainAcrossDirs(t *testing.T) {
	root := evalRoot(t)
	for _, d := range []string{"a", "b", "c"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../b/m", filepath.Join(root, "a", "l")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../c/final", filepath.Join(root, "b", "m")); err != nil {
		t.Fatal(err)
	}
	got, err := atomicfile.Resolve(filepath.Join(root, "a", "l"))
	want := filepath.Join(root, "c", "final")
	if err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
	}
	// Dangling link inside a symlinked directory: the relative target is taken
	// from the real directory of the link.
	if err := os.Symlink("../c/new", filepath.Join(root, "b", "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "bdir")); err != nil {
		t.Fatal(err)
	}
	got, err = atomicfile.Resolve(filepath.Join(root, "bdir", "d"))
	if err != nil || got != filepath.Join(root, "c", "new") {
		t.Fatalf("via dir link: %q, %v", got, err)
	}
}

// A leaf link to a directory is replaced like any other leaf link; the
// directory it named is untouched.
func TestWriteOntoDirectoryLinkReplacesLink(t *testing.T) {
	root := evalRoot(t)
	real := filepath.Join(root, "realdir")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "cfg.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(link, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if st, e := os.Lstat(real); e != nil || !st.IsDir() {
		t.Fatalf("directory damaged: %v", e)
	}
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Fatalf("directory behind the link was written into: %v", ents)
	}
	if st, e := os.Lstat(link); e != nil || !st.Mode().IsRegular() {
		t.Fatalf("link not replaced: %v %v", st, e)
	}
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp left: %s", e.Name())
		}
	}
}

func TestResolveHopCountBoundary(t *testing.T) {
	build := func(n int) string {
		dir := evalRoot(t)
		prev := filepath.Join(dir, "end")
		for i := range n {
			l := filepath.Join(dir, "l"+string(rune('a'+i%26))+string(rune('a'+i/26)))
			if err := os.Symlink(prev, l); err != nil {
				t.Fatal(err)
			}
			prev = l
		}
		return prev
	}
	if _, err := atomicfile.Resolve(build(16)); err != nil {
		t.Errorf("16 dangling hops should resolve: %v", err)
	}
	if _, err := atomicfile.Resolve(build(17)); err == nil {
		t.Error("17 dangling hops should error")
	}
}

func TestResolveLongPath(t *testing.T) {
	root := evalRoot(t)
	p := filepath.Join(root, strings.Repeat("d", 200), strings.Repeat("e", 200), strings.Repeat("f", 200), strings.Repeat("g", 200), strings.Repeat("h", 200), "file")
	got, err := atomicfile.Resolve(p)
	t.Logf("len=%d unchanged=%v err=%v", len(p), got == p, err)
	if err == nil {
		if werr := atomicfile.Write(p, []byte("x")); werr == nil {
			t.Fatal("write succeeded on a path over PATH_MAX")
		}
	}
}

func TestResolveUnicodeNFDAndCase(t *testing.T) {
	root := evalRoot(t)
	nfc := "café.json"
	nfd := "café.json"
	if err := os.WriteFile(filepath.Join(root, nfc), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nfd, filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	got, err := atomicfile.Resolve(filepath.Join(root, "l"))
	t.Logf("NFD link to NFC file: %q err=%v", got, err)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("config.json", filepath.Join(root, "Config.json")); err != nil {
		t.Logf("case-insensitive FS: symlink onto existing name refused: %v", err)
		return
	}
	if err := atomicfile.Write(filepath.Join(root, "Config.json"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "config.json")); string(b) != "x" {
		t.Errorf("config.json was written through the leaf link: %q", b)
	}
}

// A state dir linked to another volume: temp file and fsync are beside the real
// directory, the link's directory gains nothing.
func TestWriteKeepsTempBesideRealDirAcrossDirs(t *testing.T) {
	a, b := evalRoot(t), evalRoot(t)
	link := filepath.Join(a, "state")
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(link, "t.json"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "t.json")); string(got) != "v" {
		t.Fatalf("real dir file = %q", got)
	}
	if ents, _ := os.ReadDir(a); len(ents) != 1 {
		t.Errorf("link dir has stray entries: %v", ents)
	}
	if ents, _ := os.ReadDir(b); len(ents) != 1 {
		t.Errorf("real dir has stray entries: %v", ents)
	}
}

func TestSyncNewSurfacesSyncError(t *testing.T) {
	root := evalRoot(t)
	p := filepath.Join(root, "ev")
	f, created, err := atomicfile.OpenAppend(p, 0)
	if err != nil || !created {
		t.Fatal(err)
	}
	_ = f.Close()
	boom := errors.New("boom")
	if err := atomicfile.SyncNew(p, func(string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("sync error swallowed: %v", err)
	}
}
