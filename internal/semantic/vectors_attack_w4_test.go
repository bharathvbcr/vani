package semantic

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
)

// Attack tests for ft-9946 items 150/151 (vector log damage cut-back, compaction cancel and space check).

func patchAt(t *testing.T, path string, off int64, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

// A length that is valid in range and lands exactly on a later record boundary
// does not fit the record's own chunk count and width: damage, kept records before it.
func TestAttackLengthOnLaterBoundaryIsDamage(t *testing.T) {
	s := filledStore(t, 6)
	s.close()
	off := s.hdrLen + primSize
	patchAt(t, s.path, off, binary.LittleEndian.AppendUint32(nil, uint32(primSize-4+2*primSize)))
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	ev, err := n.refresh()
	if err != nil || ev.Damaged == nil || ev.Damaged.Off != off || ev.Damaged.Kept != 1 || len(n.idx) != 1 {
		t.Fatalf("%+v %v idx=%d", ev, err, len(n.idx))
	}
	if st, _ := os.Stat(s.path); st.Size() != off {
		t.Errorf("size %d, want %d", st.Size(), off)
	}
}

// The largest legal length at the end of a tiny file must not allocate it.
func TestAttackHugeLengthDoesNotAllocate(t *testing.T) {
	s := filledStore(t, 2)
	s.close()
	off := s.hdrLen + primSize
	patchAt(t, s.path, off, binary.LittleEndian.AppendUint32(nil, uint32(maxRecLen)))
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	if _, err := n.refresh(); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&m1)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 8<<20 {
		t.Errorf("scan allocated %d bytes for a %d byte file", d, off+primSize)
	}
}

// Another store appends (under the lock, in real use) and garbage then follows:
// the cut-back keeps the records the other store appended after this one's scan.
func TestAttackCutKeepsRecordsAnotherStoreAppended(t *testing.T) {
	a := filledStore(t, 3)
	b := newChunkStore(a.path, a.model, a.maxBytes)
	defer b.close()
	if _, err := b.refresh(); err != nil {
		t.Fatal(err)
	}
	b.maxBytes = a.maxBytes
	if n, _, err := b.append(context.Background(), []vecRec{seedRec(100), seedRec(101)}); n != 2 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	f, _ := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write(make([]byte, 64))
	f.Close()
	ev, err := a.refresh()
	if err != nil || ev.Damaged == nil || ev.Damaged.Kept != 5 || len(a.idx) != 5 {
		t.Fatalf("%+v %v idx=%d", ev, err, len(a.idx))
	}
	if _, err := a.read(primHash(101), primN, primDim); err != nil {
		t.Errorf("a record another store appended was lost: %v", err)
	}
	// and the other store, whose end is now stale, sees the same log.
	if ev, err := b.refresh(); err != nil || ev.Damaged != nil || len(b.idx) != 5 {
		t.Errorf("second store: %+v %v idx=%d", ev, err, len(b.idx))
	}
}

// Cancel at every check point of a compaction, including after the last one: the
// result is always either the old log intact or a whole new log, never a mix.
func TestAttackCancelAtEveryCheckPoint(t *testing.T) {
	probe := filledStore(t, 8)
	c := &cancelAfter{Context: context.Background(), n: 1 << 30}
	if _, err := probe.compact(c, vecPrio{}, evictTo(probe.maxBytes)); err != nil {
		t.Fatal(err)
	}
	total := c.calls.Load()
	for after := int32(0); after <= total; after++ {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			s := filledStore(t, 8)
			before, gen, idx := logBytes(t, s), s.gen, snapshotIdx(s)
			ev, err := s.compact(&cancelAfter{Context: context.Background(), n: after}, vecPrio{}, evictTo(s.maxBytes))
			if after < total {
				if !errors.Is(err, context.Canceled) || ev.Compacted {
					t.Fatalf("after %d of %d: %+v %v", after, total, ev, err)
				}
				stillTheOldLog(t, s, before, gen, idx)
				return
			}
			if err != nil || !ev.Compacted || len(s.idx) != 8 || !s.valid || s.f == nil {
				t.Fatalf("uncancelled: %+v %v", ev, err)
			}
			noTmp(t, s)
			for i := 0; i < 8; i++ {
				if _, err := s.read(primHash(i), primN, primDim); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// A free-space function that answers zero with no error is a refusal, not a pass.
func TestAttackZeroFreeSpaceRefuses(t *testing.T) {
	s := filledStore(t, 4)
	before, gen, idx := logBytes(t, s), s.gen, snapshotIdx(s)
	s.freeFn = func(string) (uint64, error) { return 0, nil }
	if _, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); !errors.Is(err, ErrVectorsNoSpace) {
		t.Fatalf("%v", err)
	}
	stillTheOldLog(t, s, before, gen, idx)
}

// A store at its bound with no room to compact asks the disk once, then answers
// from s.full: a refused compaction does not repeat on every append.
func TestAttackRefusedCompactionDoesNotRepeatPerAppend(t *testing.T) {
	s := primStore(t, 4, nil)
	primAppend(t, s, 1, 2, 3, 4)
	var calls atomic.Int32
	s.freeFn = func(string) (uint64, error) { calls.Add(1); return 10, nil }
	for i := 5; i < 30; i++ {
		s.append(context.Background(), []vecRec{primRec(i)})
	}
	if c := calls.Load(); c != 1 {
		t.Errorf("free space asked %d times for 25 appends", c)
	}
	if len(s.idx) != 4 {
		t.Errorf("records %d", len(s.idx))
	}
}
