package atomicfile_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// replaceByRename does what atomicfile.Write's rename does, with no fsync, so a
// test can land it inside OpenRead's open-to-check window.
func replaceByRename(t *testing.T, path string, data string) {
	t.Helper()
	tmp := path + ".swap"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Error(err)
	}
}

func TestOpenReadRetriesWhenRenameLandsDuringOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	fired := 0
	defer atomicfile.SetAfterOpenHookForTest(func() {
		if fired == 0 {
			fired++
			replaceByRename(t, path, "new")
		}
	})()
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		t.Fatalf("OpenRead lost to a rename: %v", err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != "new" {
		t.Fatalf("read %q, want the replacement %q", got, "new")
	}
}

func TestOpenReadChangingEveryAttemptIsErrChanging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	defer atomicfile.SetAfterOpenHookForTest(func() {
		calls++
		replaceByRename(t, path, "v")
	})()
	f, err := atomicfile.OpenRead(path)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, atomicfile.ErrChanging) {
		t.Fatalf("OpenRead = %v, want ErrChanging", err)
	}
	if calls != atomicfile.OpenReadAttemptsForTest {
		t.Fatalf("attempts = %d, want the bound %d", calls, atomicfile.OpenReadAttemptsForTest)
	}
}

// The identity check exists so a link planted after the Lstat cannot redirect
// the read. It still refuses at once, without a retry.
func TestOpenReadSymlinkSwappedInDuringOpenIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	secret := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(path, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	defer atomicfile.SetAfterOpenHookForTest(func() {
		calls++
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(secret, path); err != nil {
			t.Error(err)
		}
	})()
	f, err := atomicfile.OpenRead(path)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("OpenRead = %v, want ErrLinkedLeaf", err)
	}
	if calls != 1 {
		t.Fatalf("a refused link was retried: %d attempts", calls)
	}
}

// A writer looping Write and a reader looping OpenRead: every open succeeds
// and reads one whole version.
func TestOpenReadRacingAtomicWriteNeverFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	a, b := []byte("aaaaaaaaaaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := atomicfile.Write(path, a); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			d := a
			if i%2 == 0 {
				d = b
			}
			if err := atomicfile.Write(path, d); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 400; i++ {
		f, err := atomicfile.OpenRead(path)
		if err != nil {
			t.Errorf("OpenRead during a rewrite: %v", err)
			break
		}
		got, rerr := io.ReadAll(f)
		f.Close()
		if rerr != nil || (string(got) != string(a) && string(got) != string(b)) {
			t.Errorf("read %q, %v: not one whole version", got, rerr)
			break
		}
	}
	close(stop)
	wg.Wait()
}
