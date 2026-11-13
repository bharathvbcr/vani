package semantic

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// VectorReader reads the chunk-vector log (vectors.go) without touching it. The
// Matcher's own reader (chunkStore.refresh) creates the file, appends to it,
// truncates an incomplete tail, starts it anew on another model and deletes a
// corrupt one; none of that is acceptable for a measurement over the owner's
// live state, so this opens the file read-only, takes no lock, writes nothing
// and reports what it found. The record format and every check on a record are
// the log's own (parseVecHeader, chunkStore.scan, decodeVecRecord): nothing of
// the file format is restated here.
//
// A Warm in another process may append or compact the log while this is open.
// An append shows as a torn tail (not indexed); a compaction renames a new file
// over the path, so the open descriptor keeps reading the old one, which is
// still a whole log.
type VectorReader struct {
	f     *os.File
	s     *chunkStore
	model string
	torn  int64
}

// OpenVectorReader opens the chunk-vector log in cacheDir for reading. A log
// that is missing, not a regular file, corrupt, torn in its header, or chunked
// under another ChunkVersion (its records could not be matched to a posting's
// chunks) is an error; a torn tail is not (TornBytes reports it).
func OpenVectorReader(cacheDir string) (*VectorReader, error) {
	path := filepath.Join(cacheDir, vectorLogFileName)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCache, err)
	}
	r, err := newVectorReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

func newVectorReader(f *os.File) (*VectorReader, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCache, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrCache, vectorLogFileName)
	}
	h, err := parseVecHeader(f, st.Size())
	switch {
	case errors.Is(err, errVecTornHeader):
		return nil, fmt.Errorf("%w: %s has an incomplete header", ErrCache, vectorLogFileName)
	case err != nil:
		return nil, err
	case h.version != ChunkVersion:
		return nil, fmt.Errorf("%w: %s was chunked under version %d, this build reads %d", ErrCache, vectorLogFileName, h.version, ChunkVersion)
	}
	s := newChunkStore("", h.model, MaxVectorBytes)
	s.f = f
	_, torn, err := s.scan(f, st.Size(), h.hdrLen)
	if err != nil {
		return nil, err
	}
	return &VectorReader{f: f, s: s, model: h.model, torn: torn}, nil
}

// Close releases the file.
func (r *VectorReader) Close() error { return r.f.Close() }

// Model is the embedding model the vectors came from.
func (r *VectorReader) Model() string { return r.model }

// Dim is the width of the stored vectors (0 for a log with no records).
func (r *VectorReader) Dim() int { return r.s.dim }

// Records is how many distinct texts have stored vectors.
func (r *VectorReader) Records() int { return len(r.s.idx) }

// TornBytes is the incomplete tail after the last whole record: an append in
// flight, or a crash. It is left in place.
func (r *VectorReader) TornBytes() int64 { return r.torn }

// PoolStatus says what Pooled found for one posting.
type PoolStatus int

const (
	// PoolOK: the pooled vector is valid.
	PoolOK PoolStatus = iota
	// PoolNoText: the posting has no text to chunk, so it has no vectors.
	PoolNoText
	// PoolNoRecord: no vectors are stored for this text.
	PoolNoRecord
	// PoolStale: vectors are stored but for a different chunk count than the
	// posting cuts into now.
	PoolStale
	// PoolCorrupt: the record failed its checksum, shape or value checks.
	PoolCorrupt
	// PoolDegenerate: the chunk vectors cancel to (nearly) zero, so the mean has
	// no direction to normalise.
	PoolDegenerate
)

// minPooledNorm is the mean's length below which it is not normalised: the
// chunks point in opposite directions and what is left is rounding. The chunk
// vectors are unit length, so a real posting's mean is far above this.
const minPooledNorm = 1e-6

// Pooled is the posting's one vector: the mean of its chunk vectors (the unit
// vectors the log stores, exactly as the Matcher reads them) normalised to unit
// length. The posting is looked up by TextHash(title, description), as Warm
// stores it, and its chunk count must equal what ChunkPosting cuts it into now.
// The returned vector is nil unless the status is PoolOK.
func (r *VectorReader) Pooled(title, description string) ([]float32, PoolStatus) {
	chunks := ChunkPosting(title, description)
	if len(chunks) == 0 {
		return nil, PoolNoText
	}
	h, ok := parseTextHash(TextHash(title, description))
	if !ok {
		return nil, PoolNoRecord
	}
	switch err := r.s.lookup(h, len(chunks)); {
	case errors.Is(err, errVecMiss):
		return nil, PoolNoRecord
	case errors.Is(err, errVecStale):
		return nil, PoolStale
	case err != nil:
		return nil, PoolCorrupt
	}
	vecs, err := r.s.read(h, len(chunks), int(r.s.idx[h].dim))
	if err != nil {
		return nil, PoolCorrupt
	}
	out, ok := poolUnit(vecs)
	if !ok {
		return nil, PoolDegenerate
	}
	return out, PoolOK
}

// poolUnit is the mean of vecs, normalised. ok is false for no vectors, ragged
// widths, a non-finite value, or a mean shorter than minPooledNorm.
func poolUnit(vecs [][]float32) ([]float32, bool) {
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, false
	}
	dim := len(vecs[0])
	sum := make([]float64, dim)
	for _, v := range vecs {
		if len(v) != dim {
			return nil, false
		}
		for i, x := range v {
			sum[i] += float64(x)
		}
	}
	var sq float64
	for i := range sum {
		sum[i] /= float64(len(vecs))
		sq += sum[i] * sum[i]
	}
	n := math.Sqrt(sq)
	if math.IsNaN(n) || math.IsInf(n, 0) || n < minPooledNorm {
		return nil, false
	}
	out := make([]float32, dim)
	for i, x := range sum {
		out[i] = float32(x / n)
	}
	return out, true
}
