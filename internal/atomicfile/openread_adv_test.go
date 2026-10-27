package atomicfile_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func readHandle(t *testing.T, f *os.File) []byte {
	t.Helper()
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOpenReadRegularReturnsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	payload := []byte("row-one\n\x00\xffrow-two\n")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	got := readHandle(t, f)
	if !bytes.Equal(got, payload) {
		t.Fatalf("OpenRead = %q, want %q", got, payload)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadMissingDoesNotCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such")
	f, err := atomicfile.OpenRead(path)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenRead = %v, want os.ErrNotExist", err)
	}
	if _, lerr := os.Lstat(path); !errors.Is(lerr, os.ErrNotExist) {
		t.Fatalf("missing leaf was created: %v", lerr)
	}
	nested := filepath.Join(dir, "a", "b", "c")
	if f, err := atomicfile.OpenRead(nested); !errors.Is(err, os.ErrNotExist) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("nested missing = %v, want os.ErrNotExist", err)
	}
	if _, lerr := os.Lstat(filepath.Join(dir, "a")); !errors.Is(lerr, os.ErrNotExist) {
		t.Fatalf("OpenRead created a parent: %v", lerr)
	}
}

func TestOpenReadRefusesDanglingAndLoop(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("not-there", dangling); err != nil {
		t.Fatal(err)
	}
	if f, err := atomicfile.OpenRead(dangling); !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("dangling = %v, want ErrLinkedLeaf", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "not-there")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling read created the target: %v", err)
	}
	if st, err := os.Lstat(dangling); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling link = %v %v", st, err)
	}

	loopA, loopB := filepath.Join(dir, "la"), filepath.Join(dir, "lb")
	if err := os.Symlink("lb", loopA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("la", loopB); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f, err := atomicfile.OpenRead(loopA)
	if time.Since(start) > 800*time.Millisecond {
		t.Fatal("symlink loop did not return")
	}
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("loop = %v, want ErrLinkedLeaf", err)
	}
	if got, _ := os.Readlink(loopA); got != "lb" {
		t.Fatalf("loop link now %q", got)
	}

	// A directory component that loops must fail closed, not hang.
	if err := os.Symlink("b", filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	f, err = atomicfile.OpenRead(filepath.Join(dir, "a", "leaf"))
	if time.Since(start) > 800*time.Millisecond {
		t.Fatal("directory symlink loop did not return")
	}
	if f != nil {
		f.Close()
		t.Fatal("directory loop opened a file")
	}
	if err == nil || errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("directory loop = %v, want a resolve error", err)
	}
}

func TestOpenReadAndAppendThroughSymlinkedDir(t *testing.T) {
	root := evalRoot(t)
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("through-dir\n")
	if err := os.WriteFile(filepath.Join(real, "log"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f, err := atomicfile.OpenRead(filepath.Join(link, "log"))
	if err != nil {
		t.Fatal(err)
	}
	got := readHandle(t, f)
	if !bytes.Equal(got, payload) {
		t.Fatalf("OpenRead via dir symlink = %q", got)
	}

	af, created, err := atomicfile.OpenAppend(filepath.Join(link, "fresh"), 0)
	if err != nil || !created {
		t.Fatalf("OpenAppend via dir symlink created=%v err=%v", created, err)
	}
	if _, err := af.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	_ = af.Close()
	if b, err := os.ReadFile(filepath.Join(real, "fresh")); err != nil || string(b) != "new\n" {
		t.Fatalf("file not in the real directory: %q %v", b, err)
	}
	if err := atomicfile.Write(filepath.Join(link, "log"), []byte("replaced\n")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(real, "log")); err != nil || string(b) != "replaced\n" {
		t.Fatalf("Write via dir symlink = %q %v", b, err)
	}
}

func TestOpenReadRefusesLeafSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	const secret = "secret-bytes\n"
	if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "log")
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, link); err != nil {
		t.Fatal(err)
	}
	f, err := atomicfile.OpenRead(link)
	if f != nil {
		b := readHandle(t, f)
		t.Fatalf("followed the leaf link, read %q", b)
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("OpenRead = %v, want ErrLinkedLeaf", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != secret {
		t.Fatalf("target = %q %v", b, err)
	}
}

func TestUnicodeAndOddDirectoryNames(t *testing.T) {
	root := evalRoot(t)
	names := []string{
		"状态",
		"café",
		"cafe\u0301",
		"a b",
		"emoji-📦",
		"a\nb",
	}
	for _, name := range names {
		if !utf8.ValidString(name) {
			t.Fatalf("fixture %q is not UTF-8", name)
		}
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				t.Logf("OS folded %q onto an existing directory", name)
				continue
			}
			t.Fatalf("mkdir %q: %v", name, err)
		}
		path := filepath.Join(dir, "log")
		payload := []byte("u:" + name + "\n")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := atomicfile.OpenRead(path)
		if err != nil {
			t.Fatalf("OpenRead %q: %v", name, err)
		}
		got := readHandle(t, f)
		if !bytes.Equal(got, payload) {
			t.Fatalf("OpenRead %q = %q", name, got)
		}
		af, created, err := atomicfile.OpenAppend(filepath.Join(dir, "app"), 0)
		if err != nil || !created {
			t.Fatalf("OpenAppend %q created=%v err=%v", name, created, err)
		}
		_ = af.Close()
	}

	odd := string([]byte{'o', 'd', 'd', 0xff, 0xfe})
	dir := filepath.Join(root, odd)
	if err := syscall.Mkdir(dir, 0o700); err != nil {
		t.Logf("OS rejected non-UTF-8 directory name: %v", err)
		return
	}
	path := filepath.Join(dir, "log")
	payload := []byte("odd\n")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	got := readHandle(t, f)
	if !bytes.Equal(got, payload) {
		t.Fatalf("OpenRead odd dir = %q", got)
	}
}
