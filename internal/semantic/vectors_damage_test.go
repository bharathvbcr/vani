package semantic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A structurally bad record in the chunk-vector log cuts the log back to the
// records before it. It never removes the good records: re-embedding is rationed,
// so the loss would cost days.

func recStart(t *testing.T, dir string, i int) int64 {
	t.Helper()
	hdr, recs := readLog(t, dir)
	off := int64(len(hdr))
	for _, r := range recs[:i] {
		off += int64(len(r))
	}
	return off
}

func TestVectorsDamageKeepsTheRecordsBeforeIt(t *testing.T) {
	docs := vecDocs(6)
	p2 := profile2(t)
	cases := map[string]struct {
		damage func(t *testing.T, dir string) (kept int, badOff int64)
	}{
		"zero-filled tail": {func(t *testing.T, dir string) (int, int64) {
			st, _ := os.Stat(logPath(dir))
			size0 := st.Size()
			f, err := os.OpenFile(logPath(dir), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Write(make([]byte, 4096)); err != nil {
				t.Fatal(err)
			}
			return 6, size0
		}},
		"corrupt record in the middle": {func(t *testing.T, dir string) (int, int64) {
			f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			off := recStart(t, dir, 3)
			if _, err := f.WriteAt(binary.LittleEndian.AppendUint32(nil, maxRecLen+1), off); err != nil {
				t.Fatal(err)
			}
			return 3, off
		}},
		"first record bad": {func(t *testing.T, dir string) (int, int64) {
			f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			off := recStart(t, dir, 0)
			if _, err := f.WriteAt([]byte{0, 0, 0, 0}, off); err != nil {
				t.Fatal(err)
			}
			return 0, off
		}},
		"valid header, garbage after it": {func(t *testing.T, dir string) (int, int64) {
			hdr, _ := readLog(t, dir)
			writeLog(t, dir, hdr, bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 300))
			return 0, int64(len(hdr))
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			warmIn(t, newSpy(), synthProfile(t), dir, docs)
			kept, badOff := c.damage(t, dir)
			fst, err := os.Stat(logPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			size := fst.Size()
			// A read-only status names what is wrong and what survives.
			st, serr := VectorStatus(dir)
			if !errors.Is(serr, errVecCorrupt) || st.Records != kept {
				t.Fatalf("status before Warm: %+v %v, want %d records and a corrupt error", st, serr, kept)
			}

			m, rep := warmIn(t, newSpy(), p2, dir, docs)
			if rep.VectorsRebuilt {
				t.Errorf("the whole log was removed and rebuilt: %+v", rep)
			}
			if rep.Rescored != kept || rep.Scored != len(docs)-kept {
				t.Errorf("rescored %d scored %d, want %d and %d: %+v", rep.Rescored, rep.Scored, kept, len(docs)-kept, rep)
			}
			wantOff := fmt.Sprintf("offset %d", badOff)
			wantDropped := fmt.Sprintf("dropped %d bytes", size-badOff)
			wantKept := fmt.Sprintf("kept the %d records", kept)
			for _, w := range []string{wantOff, wantDropped, wantKept} {
				if !strings.Contains(rep.VectorsErr, w) || !strings.Contains(rep.Err, w) {
					t.Errorf("report does not say %q: VectorsErr=%q Err=%q", w, rep.VectorsErr, rep.Err)
				}
			}
			covered(t, rep, len(docs))
			assertSame(t, matchesOf(t, m, docs), oracle(t, p2, docs))

			// The damage is reported once: the log is whole again, and appends.
			st, err = VectorStatus(dir)
			if err != nil || st.Records != len(docs) || st.TornBytes != 0 {
				t.Errorf("log not whole afterwards: %+v %v", st, err)
			}
			_, rep = warmIn(t, newSpy(), profile3(t), dir, docs)
			if rep.VectorsErr != "" || rep.Rescored != len(docs) || rep.VectorsRebuilt {
				t.Errorf("second Warm: %+v", rep)
			}
		})
	}
}

func TestVectorsGarbageLogIsRebuiltAndSaid(t *testing.T) {
	docs := vecDocs(4)
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath(dir), bytes.Repeat([]byte{0xA5, 0x5A, 0x01}, 700), 0o600); err != nil {
		t.Fatal(err)
	}
	m, rep := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	// Nothing in it was a record: the file is replaced, and that is reported.
	if !rep.VectorsRebuilt || rep.VectorsErr == "" || rep.Scored != len(docs) || rep.VectorsAppended != len(docs) {
		t.Errorf("%+v", rep)
	}
	assertSame(t, matchesOf(t, m, docs), oracle(t, synthProfile(t), docs))
	if st, err := VectorStatus(dir); err != nil || st.Records != len(docs) {
		t.Errorf("%+v %v", st, err)
	}
}

func TestVectorsEmptyLogFile(t *testing.T) {
	docs := vecDocs(4)
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m, rep := warmIn(t, newSpy(), synthProfile(t), dir, docs)
	if rep.Scored != len(docs) || rep.VectorsAppended != len(docs) || rep.VectorsRebuilt {
		t.Errorf("%+v", rep)
	}
	assertSame(t, matchesOf(t, m, docs), oracle(t, synthProfile(t), docs))
	if st, err := VectorStatus(dir); err != nil || st.Records != len(docs) {
		t.Errorf("%+v %v", st, err)
	}
}

// errReaderAt fails every read after the header: a disk fault, not a bad record.
type errReaderAt struct {
	r    *os.File
	from int64
}

func (e errReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= e.from {
		return 0, errors.New("input/output error")
	}
	return e.r.ReadAt(p, off)
}

// A log that could not be read must not read as an empty log, and must not be
// treated as damage to cut back.
func TestVectorsUnreadableLogIsAnErrorNotAnEmptyOne(t *testing.T) {
	docs := vecDocs(3)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	f, err := os.Open(logPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	hdr, _ := readLog(t, dir)
	s := newChunkStore("", "m", MaxVectorBytes)
	_, _, serr := s.scan(errReaderAt{f, int64(len(hdr))}, st.Size(), int64(len(hdr)))
	if serr == nil || errors.Is(serr, errVecCorrupt) || !errors.Is(serr, ErrCache) {
		t.Fatalf("scan over a failing read: %v, want an ErrCache error that is not corruption", serr)
	}

	// Through Warm: a log path that cannot be opened as a file is reported, the
	// scores still come, and nothing is said to be rebuilt.
	bad := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(logPath(bad), 0o700); err != nil {
		t.Fatal(err)
	}
	_, rep := warmIn(t, newSpy(), synthProfile(t), bad, docs)
	if rep.VectorsErr == "" || rep.Err == "" || rep.VectorsRebuilt || rep.Scored != len(docs) {
		t.Errorf("an unusable log looked healthy: %+v", rep)
	}
}
