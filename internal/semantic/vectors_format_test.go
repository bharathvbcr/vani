package semantic

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// setLogFormat overwrites the format field of the log's header in place.
func setLogFormat(t *testing.T, dir string, format uint32) {
	t.Helper()
	f, err := os.OpenFile(logPath(dir), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(binary.LittleEndian.AppendUint32(nil, format), int64(len(vecMagic))); err != nil {
		t.Fatal(err)
	}
}

// A log whose format this build does not know (a newer binary's) is refused and
// left byte for byte alone: it is not rebuilt, not truncated, not deleted. That
// is what an older binary does to a newer log, so the two can share a state
// directory without either destroying the other's vectors. Scoring goes on,
// embedding, and the report names the problem.
func TestVectorsUnknownFormatRefusedAndLeftAlone(t *testing.T) {
	docs := vecDocs(5)
	dir := filepath.Join(t.TempDir(), "cache")
	warmIn(t, newSpy(), synthProfile(t), dir, docs)
	const future = 99
	setLogFormat(t, dir, future)
	before, err := os.ReadFile(logPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	spy := newSpy()
	m, err := NewMatcher(spy, profile2(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Warm(context.Background(), docs, 1000)
	if err != nil {
		t.Fatalf("a log of an unknown format stopped scoring: %v (%+v)", err, rep)
	}
	if rep.Scored != len(docs) || rep.Rescored != 0 {
		t.Errorf("the postings were not embedded afresh: %+v", rep)
	}
	if rep.VectorsRebuilt || rep.VectorsErr == "" || rep.VectorsAppended != 0 {
		t.Errorf("want the log refused and reported, not rebuilt or appended to: %+v", rep)
	}
	after, err := os.ReadFile(logPath(dir))
	if err != nil {
		t.Fatalf("the log of an unknown format is gone: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the log of an unknown format was modified: %d bytes before, %d after", len(before), len(after))
	}
	if _, err := VectorStatus(dir); err == nil {
		t.Errorf("VectorStatus read a log of an unknown format")
	}
	if _, err := OpenVectorReader(dir); err == nil {
		t.Errorf("OpenVectorReader read a log of an unknown format")
	}
}
