package semantic

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeVecLog stores recs in a fresh chunk-vector log under dir through the
// log's own writer, so the reader is tested against what Warm produces.
func writeVecLog(t *testing.T, dir string, recs []vecRec) string {
	t.Helper()
	path := filepath.Join(dir, vectorLogFileName)
	s := newChunkStore(path, "test-model", MaxVectorBytes)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	if len(recs) > 0 {
		if _, _, err := s.append(recs); err != nil {
			t.Fatal(err)
		}
	}
	s.close()
	return path
}

// unitAxis is the e_i unit vector of width dim.
func unitAxis(dim, i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

// chunkVecs gives each of d's chunks the vector f(i).
func chunkVecs(t *testing.T, d Doc, f func(i int) []float32) vecRec {
	t.Helper()
	n := len(ChunkPosting(d.Title, d.Description))
	if n == 0 {
		t.Fatal("test doc has no chunks")
	}
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = f(i)
	}
	return vecRec{h: hashOf(d), vecs: vecs}
}

func TestPooledIsTheNormalisedMeanOfTheChunkVectors(t *testing.T) {
	d := vecDocs(1)[0]
	n := len(ChunkPosting(d.Title, d.Description))
	if n < 2 {
		t.Fatalf("want a posting of several chunks, got %d", n)
	}
	rec := chunkVecs(t, d, func(i int) []float32 {
		if i == 0 {
			return unitAxis(3, 0)
		}
		return unitAxis(3, 1)
	})
	dir := t.TempDir()
	writeVecLog(t, dir, []vecRec{rec})
	r, err := OpenVectorReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, st := r.Pooled(d.Title, d.Description)
	if st != PoolOK {
		t.Fatalf("status %v", st)
	}
	// mean = (1, n-1, 0)/n; normalised.
	norm := math.Sqrt(1 + float64((n-1)*(n-1)))
	want := []float64{1 / norm, float64(n-1) / norm, 0}
	var sq float64
	for i, w := range want {
		if math.Abs(float64(got[i])-w) > 1e-6 {
			t.Errorf("component %d = %v, want %v", i, got[i], w)
		}
		sq += float64(got[i]) * float64(got[i])
	}
	if math.Abs(sq-1) > 1e-6 {
		t.Errorf("pooled vector has squared length %v, want 1", sq)
	}
	if r.Dim() != 3 || r.Records() != 1 || r.Model() != "test-model" || r.TornBytes() != 0 {
		t.Errorf("dim %d records %d model %q torn %d", r.Dim(), r.Records(), r.Model(), r.TornBytes())
	}
}

func TestPooledSaysWhyAPostingHasNoVector(t *testing.T) {
	docs := vecDocs(4)
	stored, stale, corrupt := docs[0], docs[1], docs[2]
	missing := docs[3]
	dir := t.TempDir()
	staleRec := chunkVecs(t, stale, func(int) []float32 { return unitAxis(3, 0) })
	staleRec.vecs = staleRec.vecs[:len(staleRec.vecs)-1] // stored under another chunk count
	path := writeVecLog(t, dir, []vecRec{
		chunkVecs(t, corrupt, func(int) []float32 { return unitAxis(3, 1) }),
		chunkVecs(t, stored, func(int) []float32 { return unitAxis(3, 0) }),
		staleRec,
	})
	// Flip a byte inside the first record's vectors: its checksum fails. (A bad
	// last record reads as a torn tail instead, which scan does not index.)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[vecHdrFixed+len("test-model")+4+recFixed+1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenVectorReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, tc := range []struct {
		name string
		d    Doc
		want PoolStatus
	}{
		{"stored", stored, PoolOK},
		{"missing", missing, PoolNoRecord},
		{"stale chunk count", stale, PoolStale},
		{"corrupt record", corrupt, PoolCorrupt},
		{"no text", Doc{}, PoolNoText},
	} {
		v, st := r.Pooled(tc.d.Title, tc.d.Description)
		if st != tc.want {
			t.Errorf("%s: status %v, want %v", tc.name, st, tc.want)
		}
		if (st == PoolOK) != (v != nil) {
			t.Errorf("%s: vector %v with status %v", tc.name, v, st)
		}
	}
}

func TestPooledRefusesChunksThatCancel(t *testing.T) {
	// A posting with an even chunk count, half of them +e0 and half -e0.
	var d Doc
	for _, c := range vecDocs(12) {
		if len(ChunkPosting(c.Title, c.Description))%2 == 0 {
			d = c
			break
		}
	}
	if d.Key == "" {
		t.Fatal("no test posting cuts into an even number of chunks")
	}
	rec := chunkVecs(t, d, func(i int) []float32 {
		if i%2 == 0 {
			return []float32{1, 0}
		}
		return []float32{-1, 0}
	})
	dir := t.TempDir()
	writeVecLog(t, dir, []vecRec{rec})
	r, err := OpenVectorReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if v, st := r.Pooled(d.Title, d.Description); st != PoolDegenerate || v != nil {
		t.Errorf("cancelling chunks gave %v, %v", v, st)
	}
}

func TestPoolUnit(t *testing.T) {
	if v, ok := poolUnit([][]float32{{1, 0}, {-1, 0}}); ok || v != nil {
		t.Errorf("opposite vectors pooled to %v", v)
	}
	if _, ok := poolUnit(nil); ok {
		t.Error("no vectors pooled")
	}
	if _, ok := poolUnit([][]float32{{}}); ok {
		t.Error("empty vector pooled")
	}
	if _, ok := poolUnit([][]float32{{1, 0}, {1}}); ok {
		t.Error("ragged vectors pooled")
	}
	if _, ok := poolUnit([][]float32{{float32(math.NaN()), 0}}); ok {
		t.Error("NaN pooled")
	}
	if _, ok := poolUnit([][]float32{{float32(math.Inf(1)), 0}}); ok {
		t.Error("Inf pooled")
	}
	if _, ok := poolUnit([][]float32{{0, 0}}); ok {
		t.Error("zero vector pooled")
	}
	v, ok := poolUnit([][]float32{{1, 0}, {0, 1}})
	if !ok || math.Abs(float64(v[0])-math.Sqrt2/2) > 1e-6 || math.Abs(float64(v[1])-math.Sqrt2/2) > 1e-6 {
		t.Errorf("pooled %v, ok %v", v, ok)
	}
}

// A reader never writes: not a missing file, not a torn tail, not a log of
// another chunking, not a corrupt log.
func TestVectorReaderWritesNothing(t *testing.T) {
	d := vecDocs(1)[0]
	t.Run("missing log is an error and is not created", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := OpenVectorReader(dir); !errors.Is(err, ErrCache) || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("a missing log left %d files", len(ents))
		}
	})
	t.Run("torn tail is reported and left", func(t *testing.T) {
		dir := t.TempDir()
		path := writeVecLog(t, dir, []vecRec{chunkVecs(t, d, func(int) []float32 { return unitAxis(3, 0) })})
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		torn := append(append([]byte(nil), before...), 0x40, 0x00, 0x00, 0x00, 0x01, 0x02) // a length, then too little
		if err := os.WriteFile(path, torn, 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := OpenVectorReader(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if r.TornBytes() != 6 || r.Records() != 1 {
			t.Errorf("torn %d records %d, want 6 and 1", r.TornBytes(), r.Records())
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(torn) {
			t.Error("the reader changed the log")
		}
	})
	t.Run("another ChunkVersion is refused and left", func(t *testing.T) {
		dir := t.TempDir()
		path := writeVecLog(t, dir, nil)
		raw, _ := os.ReadFile(path)
		raw[len(vecMagic)+4]++ // the ChunkVersion field
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVectorReader(dir); !errors.Is(err, ErrCache) {
			t.Fatalf("err = %v", err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(raw) {
			t.Error("the reader changed the log")
		}
	})
	t.Run("corrupt log is refused and left", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, vectorLogFileName)
		junk := []byte("this is not a chunk-vector log at all")
		if err := os.WriteFile(path, junk, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVectorReader(dir); !errors.Is(err, ErrCache) {
			t.Fatalf("err = %v", err)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(junk) {
			t.Error("the reader removed or rewrote the log")
		}
	})
	t.Run("header cut short is refused", func(t *testing.T) {
		dir := t.TempDir()
		path := writeVecLog(t, dir, nil)
		raw, _ := os.ReadFile(path)
		if err := os.WriteFile(path, raw[:len(vecMagic)+6], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVectorReader(dir); !errors.Is(err, ErrCache) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a directory is not a log", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, vectorLogFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVectorReader(dir); err == nil {
			t.Fatal("opened a directory")
		}
	})
}

func TestVectorReaderOfAnEmptyLog(t *testing.T) {
	dir := t.TempDir()
	writeVecLog(t, dir, nil)
	r, err := OpenVectorReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Records() != 0 || r.Dim() != 0 {
		t.Errorf("records %d dim %d", r.Records(), r.Dim())
	}
	d := vecDocs(1)[0]
	if _, st := r.Pooled(d.Title, d.Description); st != PoolNoRecord {
		t.Errorf("status %v, want PoolNoRecord", st)
	}
}
