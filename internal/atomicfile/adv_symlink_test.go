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

// Characterization: a link planted in the state dir that points outside it is
// written through. Before b9bcfd8 the link was replaced (contained).
func TestWriteFollowsOutOfTreeLink(t *testing.T) {
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
	b, _ := os.ReadFile(outside)
	if string(b) != "JD-TEXT" {
		t.Fatalf("expected write-through, got %q", b)
	}
}

// A dangling link at a cache name creates a file outside the tree.
func TestWriteCreatesOutsideFileThroughDanglingLink(t *testing.T) {
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
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("expected outside file created: %v", err)
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

func TestWriteOntoDirectoryLinkFails(t *testing.T) {
	root := evalRoot(t)
	real := filepath.Join(root, "realdir")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "cfg.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(link, []byte("x")); err == nil {
		t.Fatal("wrote a file over a directory reached by link")
	}
	if st, e := os.Lstat(real); e != nil || !st.IsDir() {
		t.Fatalf("directory damaged: %v", e)
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
	if b, _ := os.ReadFile(filepath.Join(root, "config.json")); string(b) != "new" {
		t.Errorf("config.json = %q", b)
	}
}

// Link and target on different directories (volumes): temp file and fsync are
// beside the target, the link's directory gains nothing.
func TestWriteKeepsTempBesideTargetAcrossDirs(t *testing.T) {
	a, b := evalRoot(t), evalRoot(t)
	target := filepath.Join(b, "t.json")
	link := filepath.Join(a, "l.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(link, []byte("v")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "v" {
		t.Fatalf("target = %q", got)
	}
	if ents, _ := os.ReadDir(a); len(ents) != 1 {
		t.Errorf("link dir has stray entries: %v", ents)
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
