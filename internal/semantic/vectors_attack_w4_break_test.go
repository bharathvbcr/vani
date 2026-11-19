package semantic

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
)

// A stale .tmp left by a crashed compaction (the disk-full case this check exists
// for) holds the space the next compaction needs; checkSpace runs before it is removed.
func TestAttackStaleTmpBlocksCompactionForever(t *testing.T) {
	s := filledStore(t, 6)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, make([]byte, 1<<16), 0o600); err != nil {
		t.Fatal(err)
	}
	needed := s.hdrLen + 6*primSize
	s.freeFn = func(string) (uint64, error) {
		if _, err := os.Stat(tmp); err == nil {
			return uint64(needed - 1), nil // the stale file holds the space
		}
		return uint64(needed), nil
	}
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted {
		t.Fatalf("compaction refused because of its own stale temp file: %+v %v", ev, err)
	}
}

// CompactVectors cuts a damaged log back and returns neither an error nor any
// field saying so: the report openVectors fills is local and dropped.
func TestAttackCompactVectorsHidesDamage(t *testing.T) {
	docs := vecDocs(5)
	dir := t.TempDir() + "/cache"
	m, _ := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	fi, _ := os.Stat(logPath(dir))
	size0 := fi.Size()
	f, _ := os.OpenFile(logPath(dir), os.O_WRONLY|os.O_APPEND, 0)
	f.Write(make([]byte, 512))
	f.Close()
	st, err := m.CompactVectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d := st.Damage
	if d == nil || d.Bytes != 512 || d.Kept != len(docs) || d.Off != size0 || d.Torn {
		t.Errorf("512 damaged bytes were cut and CompactVectors said %+v (want offset %d, 512 bytes, %d kept)", d, size0, len(docs))
	}
	if st2, err := m.CompactVectors(context.Background()); err != nil || st2.Damage != nil {
		t.Errorf("a repaired log must not report damage again: %+v %v", st2.Damage, err)
	}
}

// A middle record's length flipped to a legal-looking value past EOF is read as an
// incomplete tail, so every record after it is dropped and reported as a crash tail
// (TornBytes), not as damage.
func TestAttackMidLogLengthPastEOFIsCalledTorn(t *testing.T) {
	s := filledStore(t, 6)
	s.close()
	off := s.hdrLen + 2*primSize
	patchAt(t, s.path, off, binary.LittleEndian.AppendUint32(nil, uint32(maxRecLen)))
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	ev, err := n.refresh()
	if err != nil || ev.Damaged == nil {
		t.Fatalf("3 of 6 records dropped from a middle record, reported as: %+v %v", ev, err)
	}
}

// A genuinely torn last record (honest length, file ends inside it) stays a torn
// tail, and is now located: offset, bytes cut, records kept.
func TestMidLogPastEOFTornLastRecordIsLocated(t *testing.T) {
	s := filledStore(t, 6)
	s.close()
	b := logBytes(t, s)
	cut := int64(len(b)) - primSize/2
	if err := os.Truncate(s.path, cut); err != nil {
		t.Fatal(err)
	}
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	ev, err := n.refresh()
	want := int64(len(b)) - primSize
	if err != nil || ev.Damaged != nil || ev.TornBytes != cut-want || ev.TornAt == nil || ev.TornAt.Off != want || ev.TornAt.Kept != 5 {
		t.Fatalf("torn last record: %+v at %+v, %v", ev, ev.TornAt, err)
	}
}

// A flipped middle length reports where it cut, how much and what survives, and
// the records before it are kept.
func TestMidLogPastEOFDamageIsLocated(t *testing.T) {
	s := filledStore(t, 6)
	s.close()
	size := int64(len(logBytes(t, s)))
	off := s.hdrLen + 2*primSize
	patchAt(t, s.path, off, binary.LittleEndian.AppendUint32(nil, uint32(maxRecLen)))
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	ev, err := n.refresh()
	if err != nil || ev.Damaged == nil || ev.TornBytes != 0 {
		t.Fatalf("%+v %v", ev, err)
	}
	if d := ev.Damaged; d.Off != off || d.Bytes != size-off || d.Kept != 2 {
		t.Fatalf("damage %+v, want off %d bytes %d kept 2", d, off, size-off)
	}
}

// A file that ends inside the fixed header of a record start is a torn tail
// (too short to be judged), never damage.
func TestMidLogTinyTailIsTorn(t *testing.T) {
	s := filledStore(t, 3)
	s.close()
	f, _ := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write(binary.LittleEndian.AppendUint32(nil, 100))
	f.Close()
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	if ev, err := n.refresh(); err != nil || ev.Damaged != nil || ev.TornBytes != 4 {
		t.Fatalf("%+v %v", ev, err)
	}
}
