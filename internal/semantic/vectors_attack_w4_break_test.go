package semantic

import (
	"context"
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
