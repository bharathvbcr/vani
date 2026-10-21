// Package symlinktest is the shared table for "this writer must write through
// a symlinked path". Every atomic writer in Vani goes through atomicfile.Write;
// each package's test runs the same scenarios against its own entry point so a
// wrapper that does its own path handling cannot regress the property.
package symlinktest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const old = "OLD-CONTENT"

func isLink(t *testing.T, p string) bool {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("lstat %s: %v", p, err)
	}
	return st.Mode()&os.ModeSymlink != 0
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left in %s: %s", dir, e.Name())
		}
	}
}

// Run drives write against a file path spelled through symlinks: a link to an
// existing file (absolute and relative), a chain of links, a dangling link, a
// symlinked parent directory, and a loop. write must replace the file at the
// path it is given.
func Run(t *testing.T, write func(path string) error) {
	t.Helper()
	t.Run("file symlink", func(t *testing.T) {
		for _, rel := range []bool{false, true} {
			dir := t.TempDir()
			target := filepath.Join(dir, "real", "target.json")
			if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "link.json")
			dest := target
			if rel {
				dest = filepath.Join("real", "target.json")
			}
			if err := os.Symlink(dest, link); err != nil {
				t.Fatal(err)
			}
			if err := write(link); err != nil {
				t.Fatalf("relative=%v: %v", rel, err)
			}
			if !isLink(t, link) {
				t.Errorf("relative=%v: the link was replaced by a regular file", rel)
			}
			if b, err := os.ReadFile(target); err != nil || string(b) == old || len(b) == 0 {
				t.Errorf("relative=%v: target not updated: %q %v", rel, b, err)
			}
			noTemps(t, filepath.Dir(target))
			noTemps(t, dir)
		}
	})
	t.Run("link chain", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		mid := filepath.Join(dir, "mid")
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, mid); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("mid", link); err != nil {
			t.Fatal(err)
		}
		if err := write(link); err != nil {
			t.Fatal(err)
		}
		if !isLink(t, link) || !isLink(t, mid) {
			t.Error("a link in the chain was replaced")
		}
		if b, _ := os.ReadFile(target); string(b) == old || len(b) == 0 {
			t.Errorf("target not updated: %q", b)
		}
	})
	t.Run("dangling symlink", func(t *testing.T) {
		for _, rel := range []bool{false, true} {
			dir := t.TempDir()
			realDir := filepath.Join(dir, "real")
			if err := os.Mkdir(realDir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(realDir, "missing.json")
			link := filepath.Join(dir, "link.json")
			dest := target
			if rel {
				dest = filepath.Join("real", "missing.json")
			}
			if err := os.Symlink(dest, link); err != nil {
				t.Fatal(err)
			}
			if err := write(link); err != nil {
				t.Fatalf("relative=%v: %v", rel, err)
			}
			if !isLink(t, link) {
				t.Errorf("relative=%v: the dangling link was replaced by a regular file", rel)
			}
			if st, err := os.Stat(target); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
				t.Errorf("relative=%v: target not written: %v %v", rel, st, err)
			}
			noTemps(t, realDir)
			noTemps(t, dir)
		}
	})
	t.Run("dangling symlink into a missing directory", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "newdir", "missing.json")
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := write(link); err != nil {
			t.Fatal(err)
		}
		if !isLink(t, link) {
			t.Error("the link was replaced")
		}
		if st, err := os.Stat(target); err != nil || st.Size() == 0 {
			t.Errorf("target not written: %v %v", st, err)
		}
	})
	t.Run("symlinked parent directory", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "realdir")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(dir, "linkdir")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		if err := write(filepath.Join(linkDir, "f.json")); err != nil {
			t.Fatal(err)
		}
		if !isLink(t, linkDir) {
			t.Error("the directory link was replaced")
		}
		if st, err := os.Stat(filepath.Join(realDir, "f.json")); err != nil || st.Size() == 0 {
			t.Errorf("file not in the real directory: %v %v", st, err)
		}
		noTemps(t, realDir)
	})
	t.Run("symlink loop", func(t *testing.T) {
		dir := t.TempDir()
		a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
		if err := os.Symlink(b, a); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(a, b); err != nil {
			t.Fatal(err)
		}
		if err := write(a); err == nil {
			t.Fatal("a symlink loop was written without error")
		}
		if !isLink(t, a) || !isLink(t, b) {
			t.Error("a loop link was replaced")
		}
	})
}

// RunDir is Run for writers that choose their own file names under a
// directory: dir is spelled through a symlink, and something must land in the
// real directory, which must keep no temp files.
func RunDir(t *testing.T, write func(dir string) error) {
	t.Helper()
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if err := write(linkDir); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, linkDir) {
		t.Error("the directory link was replaced")
	}
	ents, err := os.ReadDir(realDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".lock") && e.Name() != "lock" {
			n++
		}
	}
	if n == 0 {
		t.Errorf("nothing written in the real directory: %v", ents)
	}
}
