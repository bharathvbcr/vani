package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenAppendReportsCreationOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.jsonl")
	f, created, err := OpenAppend(p, os.O_APPEND)
	if err != nil || !created {
		t.Fatalf("first open: created=%v err=%v", created, err)
	}
	f.Close()
	f, created, err = OpenAppend(p, os.O_APPEND)
	if err != nil || created {
		t.Fatalf("second open: created=%v err=%v", created, err)
	}
	f.Close()
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestOpenAppendRacingOpenersExactlyOneCreator(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.jsonl")
	var creators atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, created, err := OpenAppend(p, 0)
			if err != nil {
				t.Error(err)
				return
			}
			f.Close()
			if created {
				creators.Add(1)
			}
		}()
	}
	wg.Wait()
	if creators.Load() != 1 {
		t.Fatalf("%d openers believed they created the file, want exactly 1", creators.Load())
	}
}

func TestOpenAppendMissingDirErrors(t *testing.T) {
	if _, _, err := OpenAppend(filepath.Join(t.TempDir(), "no", "such", "f"), 0); err == nil {
		t.Fatal("a missing parent directory must be an error")
	}
}

func TestSyncNewRemovesFileWhenSyncFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.jsonl")
	f, _, err := OpenAppend(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	boom := errors.New("boom")
	if err := SyncNew(p, func(string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unsynced new file should be removed: %v", err)
	}
}

func TestTornTail(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"empty":           {"", false},
		"clean":           {"a\n", false},
		"partial only":    {"abc", true},
		"clean then part": {"a\nb", true},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if got, err := TornTail(f); err != nil || got != tc.want {
				t.Fatalf("TornTail = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
