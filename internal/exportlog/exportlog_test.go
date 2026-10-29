package exportlog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var t0 = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func keys(ks ...string) []posting.Key {
	out := make([]posting.Key, len(ks))
	for i, k := range ks {
		out[i] = posting.Key(k)
	}
	return out
}

func TestNeverWrittenIsEmpty(t *testing.T) {
	l, err := Read(Path(t.TempDir()))
	if err != nil || len(l.Jobs) != 0 || l.Prior(keys("gh:a:1")).N != 0 || l.Exported("gh:a:1") {
		t.Fatalf("%+v %v", l, err)
	}
}

// A second export of a key moves its last time and count, keeps its first, and
// Prior names the export that wrote it last, by file.
func TestRecordFoldsAndPriorNamesTheLastExport(t *testing.T) {
	path := Path(t.TempDir())
	ctx := context.Background()
	if _, err := Record(ctx, path, keys("gh:a:1", "gh:a:2", "gh:a:1"), "/x/first.xlsx", t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Hour)
	if _, err := Record(ctx, path, keys("gh:a:2", "gh:a:3"), "/x/second.xlsx", t1); err != nil {
		t.Fatal(err)
	}
	l, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if j := l.Jobs["gh:a:1"]; j.Count != 1 || !j.First.Equal(t0) || !j.Last.Equal(t0) {
		t.Fatalf("a:1 %+v (a repeat inside one export counts once)", j)
	}
	if j := l.Jobs["gh:a:2"]; j.Count != 2 || !j.First.Equal(t0) || !j.Last.Equal(t1) {
		t.Fatalf("a:2 %+v", j)
	}
	if len(l.Recent) != 2 || l.Recent[0].Rows != 2 || l.Recent[1].File != "/x/second.xlsx" {
		t.Fatalf("recent %+v", l.Recent)
	}
	p := l.Prior(keys("gh:a:1", "gh:a:2", "gh:a:2", "gh:new:9"))
	if p.N != 2 || !p.Last.At.Equal(t1) || p.Last.File != "/x/second.xlsx" {
		t.Fatalf("prior %+v", p)
	}
	if p := l.Prior(keys("gh:a:1")); p.Last.File != "/x/first.xlsx" {
		t.Fatalf("prior of a:1 %+v", p)
	}
}

// A damaged record is an error, never an empty one that lets every job through.
func TestCorruptRecordIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       "{",
		"wrong version":  `{"v":2,"jobs":{},"recent":[]}`,
		"unknown field":  `{"v":1,"jobs":{},"recent":[],"x":1}`,
		"two values":     `{"v":1,"jobs":{},"recent":[]}{}`,
		"bad key":        `{"v":1,"jobs":{"nocolon":{"first":"2026-10-27T09:00:00Z","last":"2026-10-27T09:00:00Z","count":1}},"recent":[]}`,
		"zero count":     `{"v":1,"jobs":{"gh:a:1":{"first":"2026-10-27T09:00:00Z","last":"2026-10-27T09:00:00Z","count":0}},"recent":[]}`,
		"last before":    `{"v":1,"jobs":{"gh:a:1":{"first":"2026-10-27T09:00:00Z","last":"2026-10-26T09:00:00Z","count":1}},"recent":[]}`,
		"negative rows":  `{"v":1,"jobs":{},"recent":[{"at":"2026-10-27T09:00:00Z","file":"x","rows":-1}]}`,
		"empty document": "",
	} {
		t.Run(name, func(t *testing.T) {
			path := Path(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(path); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("read: %v", err)
			}
			if _, err := Record(context.Background(), path, keys("gh:a:1"), "f", t0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("record: %v", err)
			}
			if got, _ := os.ReadFile(path); string(got) != body {
				t.Fatal("a corrupt record was written over")
			}
		})
	}
}

func TestOversizedRecordIsRefused(t *testing.T) {
	path := Path(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"v":1,"jobs":{},"recent":[]}`+strings.Repeat(" ", MaxFileBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "over") {
		t.Fatalf("read: %v", err)
	}
}

func TestRecordRefusesBadInput(t *testing.T) {
	path := Path(t.TempDir())
	ctx := context.Background()
	for name, c := range map[string]struct {
		k    []posting.Key
		file string
		at   time.Time
	}{
		"bad key":   {keys("nocolon"), "f", t0},
		"empty key": {keys(""), "f", t0},
		"zero time": {keys("gh:a:1"), "f", time.Time{}},
		"long file": {keys("gh:a:1"), strings.Repeat("x", MaxFileNameBytes+1), t0},
	} {
		if _, err := Record(ctx, path, c.k, c.file, c.at); err == nil {
			t.Errorf("%s: recorded", name)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused record wrote the file: %v", err)
	}
}

// A lock held past the wait is ErrBusy, and the record is left as it was.
func TestRecordWaitsBoundedForTheLock(t *testing.T) {
	path := Path(t.TempDir())
	lk, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Record(ctx, path, keys("gh:a:1"), "f", t0); !errors.Is(err, ErrBusy) {
		t.Fatalf("record under a held lock: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wrote without the lock")
	}
}

// Exports at once each keep their keys.
func TestConcurrentRecordsLoseNothing(t *testing.T) {
	path := Path(t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Record(context.Background(), path, keys(fmt.Sprintf("gh:c:%d", i), "gh:shared:1"), "f", t0.Add(time.Duration(i)*time.Second))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Jobs) != 9 || l.Jobs["gh:shared:1"].Count != 8 || len(l.Recent) != 8 {
		t.Fatalf("jobs %d, shared %+v, recent %d", len(l.Jobs), l.Jobs["gh:shared:1"], len(l.Recent))
	}
}

// Past MaxJobs the keys exported longest ago are forgotten, and counted; past
// MaxRecent the oldest exports lose their file.
func TestBoundsForgetTheOldest(t *testing.T) {
	path := Path(t.TempDir())
	ctx := context.Background()
	old := make([]posting.Key, MaxJobs)
	for i := range old {
		old[i] = posting.Key(fmt.Sprintf("gh:old:%d", i))
	}
	if _, err := Record(ctx, path, old[:10], "f", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(ctx, path, old[10:], "f", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := Record(ctx, path, keys("gh:new:1", "gh:new:2", "gh:new:3"), "f", t0.Add(2*time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("forgotten %d, %v", n, err)
	}
	l, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Jobs) != MaxJobs || !l.Exported("gh:new:3") || !l.Exported("gh:old:3") || l.Exported("gh:old:0") || l.Exported("gh:old:2") {
		t.Fatalf("jobs %d; the oldest three should be gone", len(l.Jobs))
	}
	for i := range MaxRecent + 5 {
		if _, err := Record(ctx, path, keys("gh:new:1"), fmt.Sprintf("f%d", i), t0.Add(time.Duration(3+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ = Read(path); len(l.Recent) != MaxRecent || l.Recent[MaxRecent-1].File != fmt.Sprintf("f%d", MaxRecent+4) {
		t.Fatalf("recent %d", len(l.Recent))
	}
	// gh:new:2's export has aged out of Recent: Prior keeps its time, without a file.
	if p := l.Prior(keys("gh:new:2")); p.N != 1 || p.Last.File != "" || !p.Last.At.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("prior %+v", p)
	}
}
