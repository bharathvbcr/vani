package marks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

var t0 = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

const k1 posting.Key = "ab:cohere:87e9fd4e"

func TestMarkNoteAndClear(t *testing.T) {
	dir := Dir(t.TempDir())
	if m, err := Read(dir); err != nil || len(m) != 0 {
		t.Fatalf("a store never written reads empty: %v %v", m, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a read created the store")
	}
	e, err := SetMark(dir, k1, Saved, t0)
	if err != nil || e.Mark != Saved || !e.MarkedAt.Equal(t0) {
		t.Fatalf("save: %+v %v", e, err)
	}
	e, err = SetNote(dir, k1, "ask about on-call\nand the team", t0.Add(time.Minute))
	if err != nil || e.Mark != Saved || e.Note == "" {
		t.Fatalf("a note keeps the mark: %+v %v", e, err)
	}
	e, err = SetMark(dir, k1, Applied, t0.Add(time.Hour))
	if err != nil || e.Mark != Applied || !e.MarkedAt.Equal(t0.Add(time.Hour)) || e.Note == "" {
		t.Fatalf("apply replaces save, keeps the note: %+v %v", e, err)
	}
	m, err := Read(dir)
	if err != nil || m[k1] != e {
		t.Fatalf("read %+v, want %+v (%v)", m[k1], e, err)
	}
	SetMark(dir, k1, None, t0.Add(2*time.Hour))
	SetNote(dir, k1, "", t0.Add(2*time.Hour))
	if m, _ := Read(dir); len(m) != 0 {
		t.Fatalf("cleared mark and note leave no entry: %+v", m)
	}
	for _, p := range []string{dir, filepath.Join(dir, fileName)} {
		st, _ := os.Stat(p)
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v, want owner-only", p, st.Mode().Perm())
		}
	}
}

func TestRefusesBadInput(t *testing.T) {
	dir := Dir(t.TempDir())
	cases := map[string]error{
		"empty key": func() error { _, err := SetMark(dir, "", Saved, t0); return err }(),
		"no ats":    func() error { _, err := SetMark(dir, "nokey", Saved, t0); return err }(),
		"long key": func() error {
			_, err := SetMark(dir, posting.Key("gh:"+strings.Repeat("a", MaxKeyBytes)), Saved, t0)
			return err
		}(),
		"control key":  func() error { _, err := SetMark(dir, "gh:a:1\x1b[2J", Saved, t0); return err }(),
		"unknown mark": func() error { _, err := SetMark(dir, k1, Mark("starred"), t0); return err }(),
		"zero time":    func() error { _, err := SetMark(dir, k1, Saved, time.Time{}); return err }(),
		"long note":    func() error { _, err := SetNote(dir, k1, strings.Repeat("x", MaxNoteBytes+1), t0); return err }(),
		"control note": func() error { _, err := SetNote(dir, k1, "a\x00b", t0); return err }(),
		"bad utf8":     func() error { _, err := SetNote(dir, k1, "a\xffb", t0); return err }(),
		"line sep":     func() error { _, err := SetNote(dir, k1, "a b", t0); return err }(),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if m, err := Read(dir); err != nil || len(m) != 0 {
		t.Fatalf("refused writes left state: %+v %v", m, err)
	}
	if _, err := SetNote(dir, k1, strings.Repeat("é", MaxNoteBytes/2), t0); err != nil {
		t.Fatalf("a note at the limit: %v", err)
	}
}

func TestDuplicateMarkIsIdempotent(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Hidden, t0)
	e, err := SetMark(dir, k1, Hidden, t0.Add(time.Second))
	if err != nil || e.Mark != Hidden {
		t.Fatalf("%+v %v", e, err)
	}
	if m, _ := Read(dir); len(m) != 1 {
		t.Fatalf("entries %d", len(m))
	}
}

func TestTornTailIsSkippedThenCut(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	path := filepath.Join(dir, fileName)
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"v":1,"at":"2026-10-19T09:00:00Z","key":"gh:a:2","ma`)
	fh.Close()
	m, err := Read(dir)
	if err != nil || len(m) != 1 {
		t.Fatalf("torn tail: %+v %v", m, err)
	}
	if _, err := SetMark(dir, "gh:a:3", Saved, t0); err != nil {
		t.Fatal(err)
	}
	m, err = Read(dir) // would be mid-file corruption if the tail were not cut
	if err != nil || len(m) != 2 {
		t.Fatalf("after cut: %+v %v", m, err)
	}
}

func TestMidFileCorruptionIsRefused(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	path := filepath.Join(dir, fileName)
	good, _ := os.ReadFile(path)
	os.WriteFile(path, append(append([]byte("{garbage}\n"), good...), good...), 0o600)
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("read: %v", err)
	}
	if _, err := SetMark(dir, k1, Applied, t0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a write over a corrupt store must refuse: %v", err)
	}
	unknown := `{"v":1,"at":"2026-10-19T09:00:00Z","key":"gh:a:1","mark":"saved","extra":1}` + "\n"
	os.WriteFile(path, []byte(unknown+string(good)), 0o600)
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestConcurrentWritersLoseNothing(t *testing.T) {
	dir := Dir(t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				k := posting.Key(fmt.Sprintf("gh:c:%d-%d", g, i))
				if _, err := SetMark(dir, k, Saved, t0); err != nil {
					errs <- err
				}
				if _, err := Read(dir); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if m, err := Read(dir); err != nil || len(m) != 64 {
		t.Fatalf("entries %d, %v", len(m), err)
	}
}

// Readers that overlap without a gap must not starve a writer. A relay of
// shared locks, each taken before the last is let go, keeps the store read
// for longer than lockWait; a writer still gets in, in its turn. (The flock
// polled with LOCK_NB never saw a moment free of readers, so the writer gave
// up with ErrBusy: the "store is busy" flake under load.)
func TestOverlappingReadersDoNotStarveAWriter(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	type held interface{ Close() error }
	acquire := func() held {
		l, err := lock(dir, false, false)
		if err != nil || l == nil {
			t.Errorf("shared lock: %v", err)
			return nil
		}
		return l
	}
	stop := make(chan struct{})
	relayDone := make(chan struct{})
	first := make(chan struct{})
	go func() {
		defer close(relayDone)
		cur := acquire()
		close(first)
		deadline := time.Now().Add(lockWait + time.Second)
		for cur != nil && time.Now().Before(deadline) {
			select {
			case <-stop:
				cur.Close()
				return
			default:
			}
			next := make(chan held, 1)
			go func() { next <- acquire() }()
			select {
			case n := <-next: // overlap: the next reader is in before this one leaves
				cur.Close()
				cur = n
			case <-time.After(50 * time.Millisecond):
				cur.Close()
				cur = <-next
			}
		}
		if cur != nil {
			cur.Close()
		}
	}()
	<-first
	start := time.Now()
	_, err := SetMark(dir, k1, Applied, t0.Add(time.Minute))
	close(stop)
	<-relayDone
	if err != nil {
		t.Fatalf("writer behind overlapping readers: %v after %v", err, time.Since(start).Round(time.Millisecond))
	}
	if m, err := Read(dir); err != nil || m[k1].Mark != Applied {
		t.Fatalf("after write: %+v %v", m[k1], err)
	}
}

// A writer that cannot have the store within lockWait still says so: the
// in-process turn is bounded like the flock.
func TestBusyIsStillBounded(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	l, err := lock(dir, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	start := time.Now()
	if _, err := SetMark(dir, k1, Applied, t0); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if d := time.Since(start); d < lockWait || d > lockWait+2*time.Second {
		t.Fatalf("gave up after %v, want about %v", d, lockWait)
	}
}

// A store whose live marks fill it refuses the write, even after the
// compaction a full store gets first, and is left exactly as it was.
func TestFullStoreRefusesWrites(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	path := filepath.Join(dir, fileName)
	full := fillWithLiveNotes(t, path)
	k, _ := posting.GreenhouseKey("zzz", "000000") // the same line length as the fill
	if _, err := SetNote(dir, k, strings.Repeat("n", MaxNoteBytes), t0); !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, full) {
		t.Fatal("a refused write changed the store")
	}
}

// A file padded to the cap with bytes that are not marks is corrupt, not
// full: it is refused as corrupt and never compacted.
func TestPaddedStoreAtTheCapIsCorrupt(t *testing.T) {
	dir := Dir(t.TempDir())
	SetMark(dir, k1, Saved, t0)
	path := filepath.Join(dir, fileName)
	f, _ := os.OpenFile(path, os.O_WRONLY, 0)
	f.Truncate(MaxFileBytes) // a sparse file at the limit
	f.Close()
	if _, err := SetMark(dir, k1, Applied, t0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if st, _ := os.Stat(path); st.Size() != MaxFileBytes {
		t.Fatalf("the corrupt store was rewritten: %d bytes", st.Size())
	}
}

// fillWithLiveNotes appends, in key order, one maximal note for each of
// enough distinct postings that one more such line would pass the cap,
// and returns the file's bytes. Every line is live, so compaction keeps
// them all.
func fillWithLiveNotes(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("n", MaxNoteBytes)
	var size int64
	for i := 0; ; i++ {
		k, _ := posting.GreenhouseKey("aaa", fmt.Sprintf("%06d", i))
		line, _ := json.Marshal(event{V: Version, At: t0, Key: k, Note: &note})
		line = append(line, '\n')
		if size+int64(len(line)) > MaxFileBytes {
			break
		}
		f.Write(line)
		size += int64(len(line))
	}
	f.Close()
	b, _ := os.ReadFile(path)
	return b
}
