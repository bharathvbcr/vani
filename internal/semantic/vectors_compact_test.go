package semantic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Compaction of the chunk-vector log: it stops on its context, checks the disk
// before it copies, and leaves the old log in use whenever it does not finish.

// seedRec is a record whose vectors differ by seed, so a parity check can tell
// records apart.
func seedRec(seed int) vecRec {
	vecs := make([][]float32, primN)
	for k := range vecs {
		v := make([]float32, primDim)
		v[(seed+k)%primDim] = 1
		v[(seed*3+k+1)%primDim] += float32(seed%7) / 8
		vecs[k] = v
	}
	return vecRec{h: primHash(seed), vecs: vecs}
}

// cancelAfter is a context that reports Canceled once Err has been asked n times:
// a cancel that lands in the middle of the copy, deterministically.
type cancelAfter struct {
	context.Context
	n     int32
	calls atomic.Int32
}

func (c *cancelAfter) Err() error {
	if c.calls.Add(1) > c.n {
		return context.Canceled
	}
	return nil
}

func filledStore(t *testing.T, records int) *chunkStore {
	t.Helper()
	s := primStore(t, records+4, nil)
	for i := 0; i < records; i++ {
		if n, _, err := s.append(context.Background(), []vecRec{seedRec(i)}); n != 1 || err != nil {
			t.Fatalf("append %d: %d %v", i, n, err)
		}
	}
	return s
}

func logBytes(t *testing.T, s *chunkStore) []byte {
	t.Helper()
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func noTmp(t *testing.T, s *chunkStore) {
	t.Helper()
	if _, err := os.Stat(s.path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a partial compaction file was left: %v", err)
	}
}

// stillTheOldLog asserts the store is in use exactly as before a failed compaction.
func stillTheOldLog(t *testing.T, s *chunkStore, before []byte, gen uint64, idx map[[16]byte]vecLoc) {
	t.Helper()
	if string(logBytes(t, s)) != string(before) {
		t.Error("the log changed")
	}
	if s.gen != gen || !s.valid || s.f == nil || !reflect.DeepEqual(s.idx, idx) {
		t.Errorf("the store left the old log: gen %d (was %d) valid %v open %v", s.gen, gen, s.valid, s.f != nil)
	}
	noTmp(t, s)
	for h := range idx {
		if _, err := s.read(h, primN, primDim); err != nil {
			t.Fatalf("a vector is unreadable after the failed compaction: %v", err)
		}
	}
}

func snapshotIdx(s *chunkStore) map[[16]byte]vecLoc {
	m := make(map[[16]byte]vecLoc, len(s.idx))
	for h, l := range s.idx {
		m[h] = l
	}
	return m
}

func TestCompactStopsCleanlyOnCancel(t *testing.T) {
	for _, after := range []int32{0, 1, 5, 11} { // before anything, then partway through 12 records
		t.Run(fmt.Sprintf("after %d checks", after), func(t *testing.T) {
			s := filledStore(t, 12)
			before, gen, idx := logBytes(t, s), s.gen, snapshotIdx(s)
			ev, err := s.compact(&cancelAfter{Context: context.Background(), n: after}, vecPrio{}, evictTo(s.maxBytes))
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCache) {
				t.Fatalf("compact: %v, want a cancelled ErrCache error", err)
			}
			if ev.Compacted || ev.Evicted != 0 || ev.Dropped != 0 {
				t.Errorf("a cancelled compaction reported work: %+v", ev)
			}
			stillTheOldLog(t, s, before, gen, idx)
			// The old log is still the one in use: it appends and compacts afterwards.
			primAppend(t, s, 100)
			if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted || len(s.idx) != 13 {
				t.Errorf("compact afterwards: %+v %v idx %d", ev, err, len(s.idx))
			}
		})
	}
}

func TestCompactCancelDuringAppendKeepsTheOldLog(t *testing.T) {
	s := primStore(t, 4, nil)
	primAppend(t, s, 1, 2, 3, 4)
	before, gen, idx := logBytes(t, s), s.gen, snapshotIdx(s)
	n, ev, err := s.append(&cancelAfter{Context: context.Background(), n: 0}, []vecRec{primRec(9)})
	if n != 0 || ev.Refused != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("append: %d %+v %v", n, ev, err)
	}
	stillTheOldLog(t, s, before, gen, idx)
}

func TestCompactRefusesWithoutRoom(t *testing.T) {
	s := filledStore(t, 12)
	before, gen, idx := logBytes(t, s), s.gen, snapshotIdx(s)
	needed := s.hdrLen + int64(12)*primSize // the new log, header included (the header is the same width)
	s.freeFn = func(string) (uint64, error) { return uint64(needed - 1), nil }
	_, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes))
	if !errors.Is(err, ErrVectorsNoSpace) || !errors.Is(err, ErrCache) {
		t.Fatalf("compact: %v, want ErrVectorsNoSpace", err)
	}
	for _, w := range []string{fmt.Sprintf("needs %d bytes", needed), fmt.Sprintf("%d free", needed-1)} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not say %q", err, w)
		}
	}
	stillTheOldLog(t, s, before, gen, idx)

	// Exactly enough passes.
	s.freeFn = func(string) (uint64, error) { return uint64(needed), nil }
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted || ev.SpaceUnchecked != "" {
		t.Errorf("with exactly enough room: %+v %v", ev, err)
	}
}

func TestCompactNeedsOnlyWhatItKeeps(t *testing.T) {
	s := filledStore(t, 12)
	keepSlots := int64(6)
	need := s.hdrLen + keepSlots*primSize
	s.freeFn = func(string) (uint64, error) { return uint64(need), nil }
	ev, err := s.compact(context.Background(), vecPrio{}, evictTo(need))
	if err != nil || ev.Evicted != 6 || len(s.idx) != 6 {
		t.Fatalf("%+v %v idx %d", ev, err, len(s.idx))
	}
}

func TestCompactSpaceCheckThatCouldNotRunIsNotAPass(t *testing.T) {
	s := filledStore(t, 6)
	s.freeFn = func(string) (uint64, error) { return 0, errors.New("statfs: boom") }
	ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes))
	if err != nil || !ev.Compacted {
		t.Fatalf("%+v %v", ev, err)
	}
	if !strings.Contains(ev.SpaceUnchecked, "boom") {
		t.Errorf("the unchecked disk was not said: %+v", ev)
	}
	var rep Report
	rep.addVectorEvent(ev)
	if !strings.Contains(rep.VectorsErr, "without checking free disk space") || rep.Err == "" {
		t.Errorf("a compaction that skipped its space check looks like one that passed: %+v", rep)
	}
}

func TestAppendAtTheBoundReportsNoSpaceNotJustFull(t *testing.T) {
	s := primStore(t, 4, nil)
	primAppend(t, s, 1, 2, 3, 4)
	s.freeFn = func(string) (uint64, error) { return 10, nil }
	n, ev, err := s.append(context.Background(), []vecRec{primRec(5)})
	if n != 0 || ev.Refused != 1 || !errors.Is(err, ErrVectorsNoSpace) || !errors.Is(err, ErrVectorsFull) {
		t.Fatalf("append: %d %+v %v, want refused with ErrVectorsNoSpace and ErrVectorsFull", n, ev, err)
	}
	if len(s.idx) != 4 {
		t.Errorf("the log lost records: %d", len(s.idx))
	}
}

func TestCompactKeepsEveryLiveVector(t *testing.T) {
	s := filledStore(t, 20)
	want := map[[16]byte][][]float32{}
	for i := 0; i < 20; i++ {
		h := primHash(i)
		v, err := s.read(h, primN, primDim)
		if err != nil {
			t.Fatal(err)
		}
		want[h] = v
	}
	// Evict the five oldest.
	ranks := map[[16]byte]float64{}
	for i := 5; i < 20; i++ {
		ranks[primHash(i)] = float64(i)
	}
	ev, err := s.compact(context.Background(), vecPrio{rank: ranks}, evictTo(s.maxBytes))
	if err != nil || !ev.Compacted || ev.Evicted != 5 || len(s.idx) != 15 {
		t.Fatalf("%+v %v idx %d", ev, err, len(s.idx))
	}
	noTmp(t, s)

	// A fresh reader of the file sees exactly the live set, bit for bit.
	fresh := newChunkStore(s.path, s.model, s.maxBytes)
	if _, err := fresh.refresh(); err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	if len(fresh.idx) != 15 || fresh.gen != s.gen {
		t.Fatalf("fresh read: %d records gen %d (store %d)", len(fresh.idx), fresh.gen, s.gen)
	}
	for i := 0; i < 20; i++ {
		h := primHash(i)
		got, err := fresh.read(h, primN, primDim)
		if i < 5 {
			if !errors.Is(err, errVecMiss) {
				t.Errorf("evicted %d: %v", i, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want[h]) {
			t.Errorf("live %d changed by compaction: %v", i, err)
		}
		if again, err := s.read(h, primN, primDim); err != nil || !reflect.DeepEqual(again, want[h]) {
			t.Errorf("live %d unreadable from the compacting store: %v", i, err)
		}
	}
}

func TestCompactVectorsRefusesWithoutRoomAndKeepsTheLog(t *testing.T) {
	docs := vecDocs(6)
	dir := t.TempDir() + "/cache"
	m, _ := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	before, err := os.ReadFile(logPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	m.vs.freeFn = func(string) (uint64, error) { return 1, nil }
	if _, err := m.CompactVectors(context.Background()); !errors.Is(err, ErrVectorsNoSpace) {
		t.Fatalf("CompactVectors: %v, want ErrVectorsNoSpace", err)
	}
	after, _ := os.ReadFile(logPath(dir))
	if string(after) != string(before) {
		t.Error("the log changed")
	}
	if _, err := os.Stat(logPath(dir) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("tmp left: %v", err)
	}
	if st, err := VectorStatus(dir); err != nil || st.Records != 6 {
		t.Errorf("%+v %v", st, err)
	}

	// A cancelled context: the lock or the copy stops, the log is as it was.
	m.vs.freeFn = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.CompactVectors(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled CompactVectors: %v", err)
	}
	after, _ = os.ReadFile(logPath(dir))
	if string(after) != string(before) {
		t.Error("the log changed on cancel")
	}
}

func TestFreeBytesOnThisPlatform(t *testing.T) {
	free, err := freeBytes(t.TempDir())
	if err != nil {
		t.Skipf("free space cannot be checked here (%v): the helper reports that, which the store records as unchecked", err)
	}
	if free == 0 {
		t.Error("a temp dir reports no free bytes")
	}
	if _, err := freeBytes(t.TempDir() + "/missing"); err == nil {
		t.Error("a missing directory was said to have free space")
	}
}
