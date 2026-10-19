package marks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// randomHistory writes n random mark and note changes over keys postings,
// including clears, and returns the store's directory.
func randomHistory(t *testing.T, seed int64, keys, n int) string {
	t.Helper()
	dir := Dir(t.TempDir())
	rng := rand.New(rand.NewSource(seed))
	marks := []Mark{None, Saved, Applied, Hidden}
	at := t0
	for i := range n {
		k, _ := posting.GreenhouseKey("acme", fmt.Sprint(rng.Intn(keys)))
		at = at.Add(time.Duration(1+rng.Intn(5000)) * time.Millisecond)
		var err error
		if rng.Intn(2) == 0 {
			_, err = SetMark(dir, k, marks[rng.Intn(len(marks))], at)
		} else {
			note := ""
			if rng.Intn(4) != 0 {
				note = fmt.Sprintf("note %d\twith a tab\nand a line", i)
			}
			_, err = SetNote(dir, k, note, at)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func lineCount(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

// Compaction keeps exactly what Read returns, one line per set mark or note,
// and a second compaction changes nothing.
func TestCompactKeepsTheFoldedState(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		dir := randomHistory(t, seed, 12, 120)
		before, err := Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := Compact(dir); err != nil {
			t.Fatal(err)
		}
		after, err := Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("seed %d: state changed:\nbefore %v\n after %v", seed, before, after)
		}
		want := 0
		for _, e := range before {
			if e.Mark != None {
				want++
			}
			if e.Note != "" {
				want++
			}
		}
		if got := lineCount(t, dir); got != want {
			t.Fatalf("seed %d: %d lines after compaction, want %d", seed, got, want)
		}
		first, _ := os.ReadFile(filepath.Join(dir, fileName))
		if err := Compact(dir); err != nil {
			t.Fatal(err)
		}
		if second, _ := os.ReadFile(filepath.Join(dir, fileName)); !bytes.Equal(first, second) {
			t.Fatalf("seed %d: compaction is not idempotent", seed)
		}
	}
}

// Before compaction, a store full of superseded edits refused every write
// with ErrFull. Now the write compacts it and lands.
func TestWriteCompactsAStoreFullOfHistory(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	size := st.Size()
	at := t0
	note := strings.Repeat("draft ", MaxNoteBytes/7)
	for {
		at = at.Add(time.Second)
		line, _ := json.Marshal(event{V: Version, At: at, Key: k1, Note: &note})
		line = append(line, '\n')
		if size+int64(len(line)) > MaxFileBytes {
			break // one more such line would pass the cap
		}
		f.Write(line)
		size += int64(len(line))
	}
	f.Close()

	// The same edit once more: the old write refused it with ErrFull.
	final := strings.Repeat("final ", MaxNoteBytes/7)
	got, err := SetNote(dir, k1, final, at.Add(time.Second))
	if err != nil {
		t.Fatalf("write to a store full of history: %v", err)
	}
	if got.Note != final || got.Mark != Saved {
		t.Fatalf("entry %+v", got)
	}
	m, err := Read(dir)
	if err != nil || m[k1].Note != final || m[k1].Mark != Saved {
		t.Fatalf("read back %+v, %v", m[k1], err)
	}
	if st, _ := os.Stat(path); st.Size() > 4<<10 {
		t.Fatalf("store is %d bytes after compaction", st.Size())
	}
}

// appendLines writes n lines straight to the store: each a note on posting
// key(i), so distinct keys make live lines and one key makes history.
func appendLines(t *testing.T, dir string, n int, key func(int) posting.Key) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	note := strings.Repeat("a note of some length ", 10)
	for i := range n {
		line, _ := json.Marshal(event{V: Version, At: t0.Add(time.Duration(i) * time.Second), Key: key(i), Note: &note})
		f.Write(append(line, '\n'))
	}
}

// Past compactAt, a store that is mostly superseded edits is compacted by
// the next write, so a write's replay follows the live marks.
func TestWriteCompactsMostlyHistoryPastTheThreshold(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	appendLines(t, dir, 6000, func(int) posting.Key { return k1 }) // one posting, edited 6000 times
	path := filepath.Join(dir, fileName)
	if st, _ := os.Stat(path); st.Size() <= compactAt {
		t.Fatalf("fixture is only %d bytes", st.Size())
	}
	before, _ := Read(dir)
	if _, err := SetMark(dir, k1, Applied, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := lineCount(t, dir); n != 3 {
		t.Fatalf("%d lines after the write, want 3 (compacted note and mark, then the new mark)", n)
	}
	after, _ := Read(dir)
	if after[k1].Note != before[k1].Note || after[k1].Mark != Applied {
		t.Fatalf("entry %+v", after[k1])
	}
}

// A store past compactAt whose lines are mostly live is not rewritten on
// every write: compaction would not shrink it.
func TestWriteLeavesAMostlyLiveStoreAlone(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	appendLines(t, dir, 6000, func(i int) posting.Key {
		k, _ := posting.GreenhouseKey("acme", fmt.Sprint(i))
		return k
	})
	path := filepath.Join(dir, fileName)
	st0, _ := os.Stat(path)
	if st0.Size() <= compactAt {
		t.Fatalf("fixture is only %d bytes", st0.Size())
	}
	if _, err := SetMark(dir, k1, Applied, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := lineCount(t, dir); n != 6002 {
		t.Fatalf("%d lines, want the 6001 there were plus one appended", n)
	}
	st1, _ := os.Stat(path)
	if !os.SameFile(st0, st1) {
		t.Fatal("the store was replaced (compacted) though it is mostly live")
	}
}

// A store with a bad line before its last is corrupt: compaction refuses it
// and leaves every byte, so nothing the user wrote is lost.
func TestCompactRefusesACorruptStore(t *testing.T) {
	dir := randomHistory(t, 3, 5, 20)
	path := filepath.Join(dir, fileName)
	b, _ := os.ReadFile(path)
	bad := append([]byte(`{"v":1,"nope":true}`+"\n"), b...)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Compact(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, bad) {
		t.Fatal("a corrupt store was rewritten")
	}
}

// A torn last line is dropped by compaction, as every reader drops it.
func TestCompactDropsATornTail(t *testing.T) {
	dir := randomHistory(t, 4, 5, 20)
	path := filepath.Join(dir, fileName)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(`{"v":1,"at":"2026-10-19T09:00:00Z","key":"gh:acme:1","mark":"sav`)
	f.Close()
	before, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	after, err := Read(dir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: %v", err)
	}
	if b, _ := os.ReadFile(path); len(b) > 0 && b[len(b)-1] != '\n' {
		t.Fatal("the torn tail survived")
	}
}

func TestCompactOfANeverWrittenStoreCreatesNothing(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Compact created %s", dir)
	}
}

// The rewrite goes to a temp file first: when it cannot be made, the store
// is left as it was and still reads.
func TestFailedCompactionLeavesTheStore(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := randomHistory(t, 5, 5, 40)
	path := filepath.Join(dir, fileName)
	before, _ := os.ReadFile(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := Compact(dir); err == nil {
		t.Fatal("compaction into a read-only directory reported success")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, before) {
		t.Fatal("a failed compaction changed the store")
	}
	if _, err := Read(dir); err != nil {
		t.Fatalf("the store no longer reads: %v", err)
	}
}

// A store's first write creates the lock file and then the data file. A
// reader that looked for the lock just before and for the data just after
// called the store corrupt. Read now looks for the lock again first.
func TestReadDuringTheFirstWriteIsNotCorrupt(t *testing.T) {
	// Deterministic: the first write lands right after Read found no lock.
	dir := Dir(t.TempDir())
	afterNoLock = func() {
		afterNoLock = func() {}
		if _, err := SetMark(dir, k1, Saved, t0); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterNoLock = func() {} })
	m, err := Read(dir)
	if err != nil {
		t.Fatalf("read during the first write: %v", err)
	}
	if m[k1].Mark != Saved {
		t.Fatalf("read %v, want the mark the write made", m)
	}
	// And the same race left to the scheduler, across fresh stores.
	for i := range 300 {
		dir := Dir(t.TempDir())
		done := make(chan error, 1)
		go func() {
			_, err := SetMark(dir, k1, Saved, t0)
			done <- err
		}()
		for {
			m, err := Read(dir)
			if err != nil {
				t.Fatalf("store %d: read during its first write: %v", i, err)
			}
			if len(m) == 1 {
				break
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// Writers and compactions interleave under the same lock: every mark made
// survives, and no reader ever sees a broken store.
func TestCompactUnderConcurrentWriters(t *testing.T) {
	dir := Dir(t.TempDir())
	const writers, each = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each+100)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				k, _ := posting.GreenhouseKey(fmt.Sprintf("w%d", w), fmt.Sprint(i))
				if _, err := SetMark(dir, k, Applied, t0.Add(time.Duration(i)*time.Second)); err != nil {
					errs <- err
				}
			}
		}()
	}
	stop := make(chan struct{})
	var cw sync.WaitGroup
	cw.Add(1)
	go func() {
		defer cw.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := Compact(dir); err != nil {
				errs <- err
			}
			if _, err := Read(dir); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(stop)
	cw.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != writers*each {
		t.Fatalf("%d marks survived, want %d", len(m), writers*each)
	}
}
