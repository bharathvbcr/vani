package semantic

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// CacheVersion is the cache-file format. Any other version is an error, as in
// kbsync's state file: guessing at an unknown layout would hide a problem.
const CacheVersion = 1

const (
	profileFileName = "semantic-profile.json"
	scoresFileName  = "semantic-scores.json"
)

// profileFile holds the embedded profile and its calibration for one
// (profile hash, model).
type profileFile struct {
	Version     int      `json:"version"`
	ProfileHash string   `json:"profile_hash"`
	Model       string   `json:"model"`
	Dim         int      `json:"dim"`
	Vectors     []string `json:"vectors"` // base64 little-endian float32, one per profile chunk
	Floor       float64  `json:"floor"`
	Ceil        float64  `json:"ceil"`
}

// entry is the cached verdict for one posting key.
type entry struct {
	TextHash string     `json:"text_hash"`
	Raw      float64    `json:"raw"`
	Score    float64    `json:"score"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Thin     bool       `json:"thin,omitempty"` // fewer than minDistinctChunks distinct chunks
	Seq      uint64     `json:"seq"`            // write order; the smallest is evicted first
}

// scoreFile holds scored postings for one (profile hash, model).
type scoreFile struct {
	Version     int               `json:"version"`
	ProfileHash string            `json:"profile_hash"`
	Model       string            `json:"model"`
	NextSeq     uint64            `json:"next_seq"`
	Entries     map[string]*entry `json:"entries"`
}

func encodeVec(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeVec(s string, dim int) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 4*dim {
		return nil, fmt.Errorf("vector has %d bytes, want %d", len(b), 4*dim)
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	if _, ok := validRow(v, dim); !ok {
		return nil, errors.New("vector is non-finite or zero")
	}
	return v, nil
}

func finite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

// readCache reads a cache file into out. missing is true (and err nil) when
// the file does not exist; an unreadable, oversized or malformed file is an
// ErrCache error.
func readCache(path string, out any) (missing bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrCache, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxCacheBytes+1))
	if err != nil {
		return false, fmt.Errorf("%w: read: %v", ErrCache, err)
	}
	if len(raw) > MaxCacheBytes {
		return false, fmt.Errorf("%w: %s is larger than %d bytes", ErrCache, path, MaxCacheBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrCache, path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return false, fmt.Errorf("%w: %s: trailing data", ErrCache, path)
	}
	return false, nil
}

// loadProfile returns the cached profile vectors when the file matches hash
// and model, nil when it is missing or belongs to another profile or model,
// and an error when it is malformed.
func loadProfile(path, hash, model string, nChunks int) (*profileData, error) {
	var pf profileFile
	missing, err := readCache(path, &pf)
	if err != nil || missing {
		return nil, err
	}
	if pf.Version != CacheVersion {
		return nil, fmt.Errorf("%w: %s has version %d, want %d", ErrCache, path, pf.Version, CacheVersion)
	}
	if pf.ProfileHash != hash || pf.Model != model {
		return nil, nil
	}
	if pf.Dim <= 0 || len(pf.Vectors) != nChunks || !finite(pf.Floor, pf.Ceil) || pf.Ceil-pf.Floor < minSpread {
		return nil, fmt.Errorf("%w: %s is inconsistent", ErrCache, path)
	}
	pd := &profileData{dim: pf.Dim, floor: pf.Floor, ceil: pf.Ceil}
	for i, s := range pf.Vectors {
		v, err := decodeVec(s, pf.Dim)
		if err != nil {
			return nil, fmt.Errorf("%w: %s vector %d: %v", ErrCache, path, i, err)
		}
		pd.vecs = append(pd.vecs, v)
	}
	return pd, nil
}

func saveProfile(path, hash, model string, pd *profileData) error {
	pf := profileFile{Version: CacheVersion, ProfileHash: hash, Model: model, Dim: pd.dim, Floor: pd.floor, Ceil: pd.ceil}
	for _, v := range pd.vecs {
		pf.Vectors = append(pf.Vectors, encodeVec(v))
	}
	raw, err := json.Marshal(pf)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCache, err)
	}
	if len(raw) > MaxCacheBytes {
		return fmt.Errorf("%w: profile cache would exceed %d bytes", ErrCache, MaxCacheBytes)
	}
	if err := atomicfile.Write(path, raw); err != nil {
		return fmt.Errorf("%w: %v", ErrCache, err)
	}
	return nil
}

// loadScores returns the cached scores when the file matches hash and model;
// a file for another profile or model is ignored (it is replaced on the next
// save); a malformed one is an error. An empty model accepts whichever model
// the file records for this hash (the cache-only lookup path, which loads no
// model); the result then carries that model, or "" when nothing is cached.
func loadScores(path, hash, model string) (*scoreFile, error) {
	fresh := &scoreFile{Version: CacheVersion, ProfileHash: hash, Model: model, Entries: map[string]*entry{}}
	var sf scoreFile
	missing, err := readCache(path, &sf)
	if err != nil {
		return nil, err
	}
	if missing {
		return fresh, nil
	}
	if sf.Version != CacheVersion {
		return nil, fmt.Errorf("%w: %s has version %d, want %d", ErrCache, path, sf.Version, CacheVersion)
	}
	if sf.ProfileHash != hash || (model != "" && sf.Model != model) {
		return fresh, nil
	}
	for k, e := range sf.Entries {
		if e == nil || k == "" || e.TextHash == "" || !finite(e.Raw, e.Score) || e.Score < 0 || e.Score > 1 || len(e.Evidence) > evidenceN {
			return nil, fmt.Errorf("%w: %s holds a malformed entry", ErrCache, path)
		}
		for _, ev := range e.Evidence {
			if !finite(ev.Cosine) {
				return nil, fmt.Errorf("%w: %s holds a malformed entry", ErrCache, path)
			}
		}
		if e.Seq >= sf.NextSeq {
			sf.NextSeq = e.Seq + 1
		}
	}
	if sf.Entries == nil {
		sf.Entries = map[string]*entry{}
	}
	return &sf, nil
}

// evict drops the oldest entries until at most max remain.
func (sf *scoreFile) evict(max int) {
	if max < 0 {
		max = 0
	}
	if len(sf.Entries) <= max {
		return
	}
	keys := make([]string, 0, len(sf.Entries))
	for k := range sf.Entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := sf.Entries[keys[i]], sf.Entries[keys[j]]
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:len(keys)-max] {
		delete(sf.Entries, k)
	}
}

// saveScores evicts down to max entries and writes the file, evicting more if
// the encoding would pass MaxCacheBytes.
func saveScores(path string, sf *scoreFile, max int) error {
	sf.evict(max)
	for {
		raw, err := json.Marshal(sf)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCache, err)
		}
		if len(raw) <= MaxCacheBytes {
			if err := atomicfile.Write(path, raw); err != nil {
				return fmt.Errorf("%w: %v", ErrCache, err)
			}
			return nil
		}
		if len(sf.Entries) == 0 {
			return fmt.Errorf("%w: scores cache cannot fit in %d bytes", ErrCache, MaxCacheBytes)
		}
		sf.evict(len(sf.Entries) / 2)
	}
}
