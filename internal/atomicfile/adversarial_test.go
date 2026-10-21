package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAppendSymlinkAndDirectoryPaths(t *testing.T) {
	dir := t.TempDir()

	// A dangling symlink: O_EXCL does not follow it (EEXIST) and the plain open
	// does (ENOENT), which used to be an error. OpenAppend resolves the link
	// first, so it creates the target (once: created is true only the first
	// time) and keeps the link.
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	for i, wantCreated := range []bool{true, false} {
		f, created, err := OpenAppend(dangling, 0)
		if err != nil || created != wantCreated {
			t.Fatalf("dangling symlink, open %d: created=%v err=%v, want created=%v", i, created, err, wantCreated)
		}
		f.Close()
	}
	if st, err := os.Lstat(dangling); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link not kept: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nowhere")); err != nil {
		t.Fatalf("target not created: %v", err)
	}

	// A symlink to an existing file opens the target and is not a creation.
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, created, err := OpenAppend(link, 0)
	if err != nil || created {
		t.Fatalf("symlink to file: created=%v err=%v", created, err)
	}
	f.Close()

	// A directory at the path is an error.
	d := filepath.Join(dir, "adir")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if f, _, err := OpenAppend(d, 0); err == nil {
		f.Close()
		t.Fatal("a directory opened read-write")
	}
}

func TestOpenAppendReadOnlyParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	f, created, err := OpenAppend(filepath.Join(dir, "log"), 0)
	if err == nil {
		f.Close()
		t.Fatalf("created in a read-only dir: %v", created)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want permission", err)
	}
}

func TestTornTailEdgeBytes(t *testing.T) {
	for name, tc := range map[string]struct {
		content []byte
		want    bool
	}{
		"just newline":      {[]byte("\n"), false},
		"just CR":           {[]byte("\r"), true}, // a lone CR is not a row boundary
		"CRLF row":          {[]byte("a\r\n"), false},
		"CR tail":           {[]byte("a\n b\r"), true},
		"multibyte lead":    {[]byte("a\n\xe2"), true},
		"multibyte partial": {[]byte("a\n\xe2\x82"), true},
		"complete rune":     {[]byte("a\n\xe2\x82\xac"), true}, // no newline: torn
		"newline in middle": {[]byte("a\nb\nc"), true},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if got, err := TornTail(f); err != nil || got != tc.want {
				t.Fatalf("TornTail(%q) = %v, %v, want %v", tc.content, got, err, tc.want)
			}
		})
	}
}

// TornTail must cost one byte of I/O however large the file is: a sparse 8 GiB
// file answers at once.
func TestTornTailReadsOnlyTheLastByte(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = 8 << 30
	if err := f.Truncate(size); err != nil {
		t.Skipf("no sparse files here: %v", err)
	}
	if _, err := f.WriteAt([]byte("\n"), size-1); err != nil {
		t.Fatal(err)
	}
	if torn, err := TornTail(f); err != nil || torn {
		t.Fatalf("file ending in newline: torn=%v err=%v", torn, err)
	}
	if _, err := f.WriteAt([]byte("x"), size-1); err != nil {
		t.Fatal(err)
	}
	if torn, err := TornTail(f); err != nil || !torn {
		t.Fatalf("file ending in x: torn=%v err=%v", torn, err)
	}
}
