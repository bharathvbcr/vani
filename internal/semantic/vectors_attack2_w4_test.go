package semantic

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Attack round 2 for ft-9946 items 150/151: the torn-versus-damage rule in scan,
// the stale temp removal and the damage report text.

func refreshAfterPatch(t *testing.T, records int, patch func(s *chunkStore) (off int64, b []byte)) (*chunkStore, vecEvent, error, int64) {
	t.Helper()
	s := filledStore(t, records)
	s.close()
	off, b := patch(s)
	patchAt(t, s.path, off, b)
	n := newChunkStore(s.path, s.model, s.maxBytes)
	t.Cleanup(n.close)
	ev, err := n.refresh()
	return n, ev, err, off
}

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// A record boundary at exactly EOF is clean: no torn bytes, no damage.
func TestAttack2FileEndsAtBoundaryIsClean(t *testing.T) {
	s := filledStore(t, 4)
	s.close()
	n := newChunkStore(s.path, s.model, s.maxBytes)
	defer n.close()
	ev, err := n.refresh()
	if err != nil || ev.Damaged != nil || ev.TornBytes != 0 || ev.TornAt != nil || len(n.idx) != 4 {
		t.Fatalf("%+v %v", ev, err)
	}
}

// Every boundary length on the last record's length field (0, 1, header size,
// min-1, min, max, max+1): no panic, the first records survive, the log is
// idempotent after one repair, and the file size equals the cut.
func TestAttack2BoundaryLengthsAtTail(t *testing.T) {
	lens := []uint32{0, 1, 4, recFixed, minRecLen - 1, minRecLen, minRecLen + 1, primSize - 4 + 1, primSize - 4 - 1, maxRecLen - 1, maxRecLen, maxRecLen + 1, 1 << 31, 0xffffffff}
	for _, ln := range lens {
		n, ev, err, off := refreshAfterPatch(t, 4, func(s *chunkStore) (int64, []byte) { return s.hdrLen + 3*primSize, u32(ln) })
		if err != nil {
			t.Fatalf("len %d: %v", ln, err)
		}
		if len(n.idx) != 3 {
			t.Errorf("len %d: kept %d records, want 3 (%+v)", ln, len(n.idx), ev)
		}
		if ev.Damaged == nil && ev.TornBytes == 0 {
			t.Errorf("len %d: cut something and reported nothing: %+v", ln, ev)
		}
		if st, _ := os.Stat(n.path); st.Size() != off {
			t.Errorf("len %d: size %d, want %d", ln, st.Size(), off)
		}
		ev2, err := n.refresh()
		if err != nil || ev2.Damaged != nil || ev2.TornBytes != 0 || len(n.idx) != 3 {
			t.Errorf("len %d: second refresh not clean: %+v %v", ln, ev2, err)
		}
	}
}

// A genuinely torn last record at every cut point inside it is torn, never
// damage, and a later refresh is clean.
func TestAttack2EveryCutInsideLastRecordIsTorn(t *testing.T) {
	s := filledStore(t, 3)
	s.close()
	b := logBytes(t, s)
	full := int64(len(b))
	start := full - primSize
	for cut := start + 1; cut < full; cut++ {
		p := filepath.Join(t.TempDir(), "v.log")
		if err := os.WriteFile(p, b[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		n := newChunkStore(p, s.model, s.maxBytes)
		ev, err := n.refresh()
		n.close()
		if err != nil || ev.Damaged != nil || ev.TornBytes != cut-start || len(n.idx) != 2 {
			t.Fatalf("cut at +%d: %+v %v kept %d", cut-start, ev, err, len(n.idx))
		}
	}
}

// Repeated refreshes of a damaged log report once and truncate once.
func TestAttack2DamageReportedOnce(t *testing.T) {
	n, ev, err, off := refreshAfterPatch(t, 5, func(s *chunkStore) (int64, []byte) { return s.hdrLen + primSize, u32(maxRecLen + 1) })
	if err != nil || ev.Damaged == nil || ev.Damaged.Off != off {
		t.Fatalf("%+v %v", ev, err)
	}
	for i := 0; i < 3; i++ {
		ev, err = n.refresh()
		if err != nil || ev.Damaged != nil || ev.TornBytes != 0 {
			t.Fatalf("refresh %d repeated: %+v %v", i, ev, err)
		}
		if st, _ := os.Stat(n.path); st.Size() != off {
			t.Fatalf("size %d", st.Size())
		}
	}
}

// A stale temp that is a symlink: the link is removed, its target is untouched.
func TestAttack2StaleTmpSymlinkLeavesTarget(t *testing.T) {
	s := filledStore(t, 6)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, s.path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted {
		t.Fatalf("%+v %v", ev, err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me" {
		t.Fatalf("symlink target changed: %q %v", b, err)
	}
	noTmp(t, s)
}

// A dangling symlink as the temp is removed too.
func TestAttack2StaleTmpDanglingSymlink(t *testing.T) {
	s := filledStore(t, 6)
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), s.path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted {
		t.Fatalf("%+v %v", ev, err)
	}
}

// A stale temp that is an empty directory is removed; a non-empty one is a loud
// ErrCache failure that leaves the log intact.
func TestAttack2StaleTmpDirectory(t *testing.T) {
	s := filledStore(t, 6)
	if err := os.Mkdir(s.path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted {
		t.Fatalf("empty dir: %+v %v", ev, err)
	}
	before := logBytes(t, s)
	if err := os.Mkdir(s.path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.path+".tmp", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes))
	if err == nil {
		t.Fatalf("non-empty directory temp: no error, %+v", ev)
	}
	if string(logBytes(t, s)) != string(before) {
		t.Fatal("the log changed on a refused compaction")
	}
}

// The damage report carries offsets and counts only, and is JSON-safe.
func TestAttack2DamageReportText(t *testing.T) {
	docs := vecDocs(4)
	dir := t.TempDir() + "/cache"
	m, _ := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	f, _ := os.OpenFile(logPath(dir), os.O_WRONLY|os.O_APPEND, 0)
	f.Write(u32(maxRecLen + 1))
	f.Write(make([]byte, 100))
	f.Close()
	st, err := m.CompactVectors(context.Background())
	if err != nil || st.Damage == nil {
		t.Fatalf("%+v %v", st, err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	rep := Report{VectorsDamage: st.Damage, VectorsErr: st.Damage.Why}
	rb, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		for _, w := range strings.Fields(d.Title + " " + d.Description + " " + d.Key) {
			if len(w) > 3 && (strings.Contains(string(b), w) || strings.Contains(string(rb), w)) {
				t.Errorf("posting text %q in the damage report", w)
			}
		}
	}
	if strings.ContainsRune(st.Damage.Why, '\n') {
		t.Errorf("newline in %q", st.Damage.Why)
	}
}
