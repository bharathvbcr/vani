package semantic

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// The chunk-vector log keeps the unit vectors Warm embedded for each posting,
// so a profile change (which invalidates every score) rescores from disk
// instead of embedding every posting again. It is append-only, little-endian:
//
//	header: "VANIVEC\0" | u32 format | u32 semantic.ChunkVersion | u64 generation | u32 len | model id
//	record: u32 len | [16]B TextHash | u16 nChunks | u16 dim | f32 x nChunks*dim | u32 crc32
//
// len counts the bytes after itself, so a record is 4+len bytes; the crc covers
// the bytes between len and itself. The vectors are exactly what validRow
// produced and are loaded without renormalising, so a rescore equals a fresh
// embed bit for bit. No chunk text is stored: the chunks are re-cut by the
// deterministic ChunkPosting and a record whose chunk count differs is stale.
// The file is keyed by (model, ChunkVersion) in its header and by TextHash per
// record, so one posting text under several keys embeds once.
//
// All access happens inside Warm (or CompactVectors) under the cache lock. A
// compaction renames a new file over the log and bumps the generation, so every
// Warm reopens by path and compares the generation before trusting its index.

const (
	vectorLogFileName = "vectors.log"

	// MaxVectorBytes caps the chunk-vector log (its own bound, not MaxCacheBytes).
	// A posting stores ~69 KB (about 22 chunks of 768 float32), so 512 MiB holds
	// ~7,600 postings: the owner's ~6,150 browsable ones (~430 MB). The cost of the
	// size is open time and memory, both per record: a Warm scans two small reads
	// per record (never the vectors) and the index is ~50 bytes a record.
	// Appends stop at the bound and the log is compacted, dropping postings
	// outside the live set, then the lowest priority (SetVectorPriority), then the
	// oldest written, before anything is refused.
	MaxVectorBytes = 512 << 20

	vecMagic      = "VANIVEC\x00"
	vecFormat     = 1
	vecHdrFixed   = len(vecMagic) + 4 + 4 + 8 + 4
	maxModelIDLen = 1024
	recFixed      = 16 + 2 + 2 // hash, nChunks, dim
	minRecLen     = recFixed + 4 + 4
	maxRecLen     = recFixed + 4*MaxPostingChunks*maxVectorDim + 4
	// vecCompactMin is the dead bytes (superseded duplicates, dropped records) a
	// log must hold before Warm compacts it for them alone.
	vecCompactMin = 4 << 20
	// rescoreSaveEvery is how many rescored postings pass between score saves.
	rescoreSaveEvery = 500
)

var (
	// ErrVectorsFull means the chunk-vector log is at its size bound and
	// compaction could not make room, so vectors were not stored. The scores
	// are unaffected.
	ErrVectorsFull = fmt.Errorf("%w: chunk-vector log is at its size bound", ErrCache)

	errVecCorrupt    = fmt.Errorf("%w: chunk-vector log is corrupt", ErrCache)
	errVecStale      = errors.New("semantic: stored vectors are for a different chunking")
	errVecMiss       = errors.New("semantic: no stored vectors")
	errVecTornHeader = errors.New("semantic: chunk-vector log header is incomplete")
)

func vecCorrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errVecCorrupt, fmt.Sprintf(format, a...))
}

// vecLoc is where one posting's record sits.
type vecLoc struct {
	off int64 // offset of the record's length prefix
	n   uint16
	dim uint16
}

func (l vecLoc) size() int64 { return 4 + recFixed + 4*int64(l.n)*int64(l.dim) + 4 }

// vecRec is one posting's vectors to store.
type vecRec struct {
	h    [16]byte
	vecs [][]float32
}

type vecHeader struct {
	version uint32
	gen     uint64
	model   string
	hdrLen  int64
}

type vecKeep struct {
	h   [16]byte
	loc vecLoc
}

// prio is what the next compaction keeps: the matcher's priorities, or no live
// set (age alone decides) when none were supplied.
func (s *chunkStore) prio() vecPrio {
	if s.prioFn == nil {
		return vecPrio{}
	}
	return s.prioFn()
}

// vecEnc is one posting's record, encoded and waiting to be appended.
type vecEnc struct {
	h      [16]byte
	b      []byte
	n, dim int
}

// vecPrio is what a compaction keeps. rank holds each live posting's priority,
// higher kept longer; nil names no live set, so every posting is live and ties
// (age alone then decides, oldest written first).
type vecPrio struct{ rank map[[16]byte]float64 }

// outside says h is not in the live set: it goes before any live posting.
func (p vecPrio) outside(h [16]byte) bool {
	if p.rank == nil {
		return false
	}
	_, ok := p.rank[h]
	return !ok
}

// of is the stored posting h's priority (0 when no live set was named).
func (p vecPrio) of(h [16]byte) float64 { return p.rank[h] }

// incoming is the priority of a posting about to be stored. One with no entry is
// a posting the caller did not rank: nothing shows it is lower than anything
// stored, so it counts as the highest.
func (p vecPrio) incoming(h [16]byte) float64 {
	if p.rank == nil {
		return 0
	}
	if r, ok := p.rank[h]; ok {
		return r
	}
	return math.Inf(1)
}

// vecPlan is how far a compaction evicts live postings, lowest priority first
// (oldest written among equals): it stops at soft bytes, but only goes past hard
// bytes (the room the incoming records need) or evicts a posting ranked above
// floor when it must. A posting ranked above floor outranks every incoming
// record, so it is never evicted for headroom.
type vecPlan struct {
	soft, hard int64
	floor      float64
}

// evictTo is a plan with no incoming records: evict down to budget, whatever
// the rank.
func evictTo(budget int64) vecPlan { return vecPlan{soft: budget, hard: budget, floor: math.Inf(1)} }

// vecEvent is what a refresh or an append did besides the ordinary work.
type vecEvent struct {
	TornBytes int64 // incomplete trailing bytes cut off
	Replaced  bool  // another model or ChunkVersion (or a torn header): the log was started anew
	Compacted bool
	Evicted   int // postings dropped by a compaction (outside the live set, then lowest priority, then oldest)
	Dropped   int // records a compaction shed because they failed their checksum
	Refused   int // postings an append did not store: no room (or none for one ranked that low), or a bad record
}

// parseTextHash turns a TextHash into the 16 bytes a record stores.
func parseTextHash(th string) ([16]byte, bool) {
	var h [16]byte
	if len(th) != 2*len(h) {
		return h, false
	}
	if _, err := hex.Decode(h[:], []byte(th)); err != nil {
		return [16]byte{}, false
	}
	return h, true
}

func encodeVecHeader(model string, gen uint64) []byte {
	b := make([]byte, 0, vecHdrFixed+len(model))
	b = append(b, vecMagic...)
	b = binary.LittleEndian.AppendUint32(b, vecFormat)
	b = binary.LittleEndian.AppendUint32(b, ChunkVersion)
	b = binary.LittleEndian.AppendUint64(b, gen)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(model)))
	return append(b, model...)
}

// parseVecHeader reads the header of a file of the given size. A file that ends
// inside the header (and agrees with it so far) is errVecTornHeader; anything
// that is not this format is an ErrCache error. Reads are bounded by the fixed
// header and maxModelIDLen.
func parseVecHeader(r io.ReaderAt, size int64) (vecHeader, error) {
	var h vecHeader
	var fixed [vecHdrFixed]byte
	n, err := r.ReadAt(fixed[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return h, fmt.Errorf("%w: read header: %v", ErrCache, err)
	}
	if int64(n) > size {
		n = int(size)
	}
	if k := min(n, len(vecMagic)); string(fixed[:k]) != vecMagic[:k] {
		return h, vecCorrupt("not a chunk-vector log")
	}
	if n < vecHdrFixed {
		return h, errVecTornHeader
	}
	o := len(vecMagic)
	if f := binary.LittleEndian.Uint32(fixed[o:]); f != vecFormat {
		return h, fmt.Errorf("%w: chunk-vector log has format %d, want %d", ErrCache, f, vecFormat)
	}
	h.version = binary.LittleEndian.Uint32(fixed[o+4:])
	h.gen = binary.LittleEndian.Uint64(fixed[o+8:])
	mlen := int64(binary.LittleEndian.Uint32(fixed[o+16:]))
	if mlen > maxModelIDLen {
		return h, vecCorrupt("model id of %d bytes", mlen)
	}
	h.hdrLen = int64(vecHdrFixed) + mlen
	if size < h.hdrLen {
		return h, errVecTornHeader
	}
	mb := make([]byte, mlen)
	if _, err := r.ReadAt(mb, int64(vecHdrFixed)); err != nil && !(errors.Is(err, io.EOF) && mlen == 0) {
		return h, fmt.Errorf("%w: read header: %v", ErrCache, err)
	}
	h.model = string(mb)
	return h, nil
}

// encodeVecRecord frames one posting's vectors.
func encodeVecRecord(h [16]byte, vecs [][]float32) ([]byte, error) {
	n := len(vecs)
	if n == 0 || n > MaxPostingChunks {
		return nil, fmt.Errorf("%w: %d chunk vectors", ErrCache, n)
	}
	dim := len(vecs[0])
	if dim == 0 || dim > maxVectorDim {
		return nil, fmt.Errorf("%w: vectors of width %d", ErrCache, dim)
	}
	buf := make([]byte, 0, 4+recFixed+4*n*dim+4)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(recFixed+4*n*dim+4))
	buf = append(buf, h[:]...)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(n))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(dim))
	for _, v := range vecs {
		if len(v) != dim {
			return nil, fmt.Errorf("%w: chunk vectors of different widths", ErrCache)
		}
		for _, x := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
		}
	}
	return binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf[4:])), nil
}

// checkVecRecord verifies a whole record's length, checksum, hash and shape
// against the index entry, without decoding the vectors.
func checkVecRecord(b []byte, h [16]byte, loc vecLoc) error {
	if int64(len(b)) != loc.size() || int64(binary.LittleEndian.Uint32(b))+4 != loc.size() {
		return vecCorrupt("record length")
	}
	body := b[4 : len(b)-4]
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
		return vecCorrupt("record checksum")
	}
	if string(body[:16]) != string(h[:]) ||
		binary.LittleEndian.Uint16(body[16:]) != loc.n || binary.LittleEndian.Uint16(body[18:]) != loc.dim {
		return vecCorrupt("record header")
	}
	return nil
}

// decodeVecRecord checks a whole record (4+len bytes) against what the index
// expects and returns its vectors, unnormalised. It checks the length, hash,
// shape and checksum, then that every value is finite and each vector has a
// length; it does not renormalise.
func decodeVecRecord(b []byte, h [16]byte, loc vecLoc) ([][]float32, error) {
	if err := checkVecRecord(b, h, loc); err != nil {
		return nil, err
	}
	data := b[4+recFixed:]
	out := make([][]float32, loc.n)
	for i := range out {
		v := make([]float32, loc.dim)
		var sq float64
		for k := range v {
			x := math.Float32frombits(binary.LittleEndian.Uint32(data[4*(i*int(loc.dim)+k):]))
			f := float64(x)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, vecCorrupt("non-finite value")
			}
			sq += f * f
			v[k] = x
		}
		if sq == 0 {
			return nil, vecCorrupt("zero vector")
		}
		out[i] = v
	}
	return out, nil
}

// nextGeneration is a generation greater than old and different from any an
// earlier incarnation of the file used (time-based), so a deleted and recreated
// log is never mistaken for the one an index was built from.
func nextGeneration(old uint64) uint64 {
	g := uint64(time.Now().UnixNano())
	if g <= old {
		g = old + 1
	}
	return g
}

// chunkStore is the index over one chunk-vector log. It is not safe for
// concurrent use: the Matcher guards it with warmMu and the cache lock. The file
// is open only between refresh and close (one Warm).
type chunkStore struct {
	path       string
	model      string
	maxBytes   int64
	compactMin int64
	prioFn     func() vecPrio // what to keep when compacting; nil names no live set, so only age decides

	f      *os.File
	gen    uint64
	hdrLen int64
	end    int64 // size the index covers (== file size when healthy)
	dim    int   // width of the stored vectors; 0 while empty
	idx    map[[16]byte]vecLoc
	live   int64 // bytes of the records idx points at
	valid  bool  // idx covers the file up to end for generation gen
	broken bool  // an append failed and could not be rolled back: no more appends
	full   bool  // no room was left this session (not a refusal by rank): stop trying
}

func newChunkStore(path, model string, maxBytes int64) *chunkStore {
	s := &chunkStore{path: path, model: model, maxBytes: maxBytes, compactMin: vecCompactMin}
	s.resetIndex()
	return s
}

func (s *chunkStore) resetIndex() {
	s.idx = map[[16]byte]vecLoc{}
	s.live, s.dim, s.end, s.valid = 0, 0, 0, false
}

func (s *chunkStore) close() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
}

// set records the newest location of h.
func (s *chunkStore) set(h [16]byte, loc vecLoc) {
	if old, ok := s.idx[h]; ok {
		s.live -= old.size()
	}
	s.idx[h] = loc
	s.live += loc.size()
}

// drop forgets h in memory (a record that failed its checksum); the next
// compaction sheds its bytes, and a newer record for h shadows it before that.
func (s *chunkStore) drop(h [16]byte) {
	if old, ok := s.idx[h]; ok {
		s.live -= old.size()
		delete(s.idx, h)
	}
}

// scan indexes the records from start to size, updating idx and dim. It returns
// where the last whole record ends and how many bytes after it are an
// incomplete tail (a length the file ends before, or a last record whose
// checksum fails: a crash mid-append). A structurally impossible record anywhere
// else (length, chunk count, width, a width unlike the rest) is errVecCorrupt;
// payload checksums are verified when a record is read, and the last record's
// here. scan never writes.
func (s *chunkStore) scan(r io.ReaderAt, size, start int64) (end, torn int64, err error) {
	off := start
	var hdr [4 + recFixed]byte
	for off < size {
		if rem := size - off; rem < 4 {
			return off, rem, nil
		}
		if _, err := r.ReadAt(hdr[:4], off); err != nil {
			return off, 0, fmt.Errorf("%w: read: %v", ErrCache, err)
		}
		ln := int64(binary.LittleEndian.Uint32(hdr[:4]))
		if ln < minRecLen || ln > maxRecLen {
			return off, 0, vecCorrupt("record at %d has length %d", off, ln)
		}
		if off+4+ln > size {
			return off, size - off, nil
		}
		if _, err := r.ReadAt(hdr[4:], off+4); err != nil {
			return off, 0, fmt.Errorf("%w: read: %v", ErrCache, err)
		}
		n := int64(binary.LittleEndian.Uint16(hdr[20:]))
		dim := int64(binary.LittleEndian.Uint16(hdr[22:]))
		if n == 0 || n > MaxPostingChunks || dim == 0 || dim > maxVectorDim {
			return off, 0, vecCorrupt("record at %d has %d chunks of width %d", off, n, dim)
		}
		if ln != recFixed+4*n*dim+4 {
			return off, 0, vecCorrupt("record at %d: length %d does not fit %d chunks of width %d", off, ln, n, dim)
		}
		if s.dim != 0 && int(dim) != s.dim {
			return off, 0, vecCorrupt("record at %d has width %d, the log holds %d", off, dim, s.dim)
		}
		if off+4+ln == size {
			b := make([]byte, 4+ln)
			if _, err := r.ReadAt(b, off); err != nil {
				return off, 0, fmt.Errorf("%w: read: %v", ErrCache, err)
			}
			if crc32.ChecksumIEEE(b[4:len(b)-4]) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
				return off, size - off, nil
			}
		}
		var h [16]byte
		copy(h[:], hdr[4:20])
		s.dim = int(dim)
		s.set(h, vecLoc{off: off, n: uint16(n), dim: uint16(dim)})
		off += 4 + ln
	}
	return off, 0, nil
}

// refresh opens the log by path (creating it) and brings the index up to date:
// a header for another model or ChunkVersion replaces the file, an unchanged
// generation is scanned from where the last scan stopped, and a changed
// generation (another process compacted) is scanned afresh. An incomplete tail
// is truncated. Corruption is an errVecCorrupt error and leaves the file alone;
// the caller decides to remove it. The caller holds the cache lock.
func (s *chunkStore) refresh() (ev vecEvent, err error) {
	s.close()
	s.full = false
	if len(s.model) == 0 || len(s.model) > maxModelIDLen {
		return ev, fmt.Errorf("%w: model id of %d bytes cannot key a chunk-vector log", ErrCache, len(s.model))
	}
	f, created, err := atomicfile.OpenAppend(s.path, os.O_APPEND)
	if err != nil {
		return ev, fmt.Errorf("%w: %w", ErrCache, err)
	}
	s.f = f
	defer func() {
		if err != nil {
			s.close()
			s.valid = false
		}
	}()
	if created {
		if _, err = f.Write(encodeVecHeader(s.model, nextGeneration(s.gen))); err != nil {
			return ev, fmt.Errorf("%w: write header: %v", ErrCache, err)
		}
		if err = f.Sync(); err != nil {
			return ev, fmt.Errorf("%w: sync header: %v", ErrCache, err)
		}
		if err = atomicfile.SyncNew(s.path, atomicfile.SyncDir); err != nil {
			return ev, fmt.Errorf("%w: %v", ErrCache, err)
		}
	}
	st, err := f.Stat()
	if err != nil {
		return ev, fmt.Errorf("%w: stat: %v", ErrCache, err)
	}
	size := st.Size()
	h, perr := parseVecHeader(f, size)
	switch {
	case errors.Is(perr, errVecTornHeader), perr == nil && (h.version != ChunkVersion || h.model != s.model):
		ev.Replaced = true
		s.resetIndex()
		if _, err = s.rewrite(nil); err != nil {
			return ev, err
		}
		return ev, nil
	case perr != nil:
		s.resetIndex()
		return ev, perr
	}
	start := h.hdrLen
	if s.valid && s.gen == h.gen && s.hdrLen == h.hdrLen && size >= s.end {
		start = s.end
	} else {
		s.resetIndex()
	}
	s.gen, s.hdrLen = h.gen, h.hdrLen
	end, torn, serr := s.scan(f, size, start)
	if serr != nil {
		s.resetIndex()
		return ev, serr
	}
	if torn > 0 {
		if err = f.Truncate(end); err != nil {
			return ev, fmt.Errorf("%w: truncate torn tail: %v", ErrCache, err)
		}
		if err = f.Sync(); err != nil {
			return ev, fmt.Errorf("%w: sync after truncate: %v", ErrCache, err)
		}
		ev.TornBytes = torn
	}
	s.end, s.valid = end, true
	return ev, nil
}

// lookup says whether h has stored vectors for a posting of n chunks.
func (s *chunkStore) lookup(h [16]byte, n int) error {
	loc, ok := s.idx[h]
	switch {
	case !ok:
		return errVecMiss
	case int(loc.n) != n:
		return errVecStale
	}
	return nil
}

// read returns h's vectors, checking the record's checksum, hash and shape
// against the index and the posting's chunk count and the profile's width. Any
// fault is errVecCorrupt (or errVecStale for a chunk-count difference): the
// caller embeds the posting instead.
func (s *chunkStore) read(h [16]byte, n, dim int) ([][]float32, error) {
	loc, ok := s.idx[h]
	if !ok {
		return nil, errVecMiss
	}
	if int(loc.n) != n {
		return nil, errVecStale
	}
	if int(loc.dim) != dim {
		return nil, vecCorrupt("vectors are %d wide, the profile %d", loc.dim, dim)
	}
	if s.f == nil {
		return nil, fmt.Errorf("%w: chunk-vector log is not open", ErrCache)
	}
	b := make([]byte, loc.size())
	if _, err := s.f.ReadAt(b, loc.off); err != nil {
		return nil, vecCorrupt("read at %d: %v", loc.off, err)
	}
	return decodeVecRecord(b, h, loc)
}

// rewrite streams the records in keep (in order) into a new file beside the
// log under a new generation, fsyncs it, renames it over the log and syncs the
// directory, then reopens it. A record that fails its checksum is shed, not
// copied. Nothing is held in memory but one record and the write buffer. On a
// failure before the rename the old log is untouched. keep nil starts an empty
// log. It returns how many records were shed.
func (s *chunkStore) rewrite(keep []vecKeep) (dropped int, err error) {
	gen := nextGeneration(s.gen)
	tmpPath := s.path + ".tmp"
	_ = os.Remove(tmpPath)
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("%w: compaction file: %v", ErrCache, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	bw := bufio.NewWriterSize(tmp, 1<<20)
	hdr := encodeVecHeader(s.model, gen)
	if _, err = bw.Write(hdr); err != nil {
		return 0, fmt.Errorf("%w: compaction write: %v", ErrCache, err)
	}
	off := int64(len(hdr))
	newIdx := make(map[[16]byte]vecLoc, len(keep))
	var live int64
	var buf []byte
	dim := 0
	for _, k := range keep {
		size := k.loc.size()
		if int64(cap(buf)) < size {
			buf = make([]byte, size)
		}
		b := buf[:size]
		if s.f == nil {
			err = fmt.Errorf("%w: chunk-vector log is not open", ErrCache)
			return dropped, err
		}
		if _, rerr := s.f.ReadAt(b, k.loc.off); rerr != nil {
			err = fmt.Errorf("%w: compaction read at %d: %v", ErrCache, k.loc.off, rerr)
			return dropped, err
		}
		if derr := checkVecRecord(b, k.h, k.loc); derr != nil {
			dropped++
			continue
		}
		if _, err = bw.Write(b); err != nil {
			err = fmt.Errorf("%w: compaction write: %v", ErrCache, err)
			return dropped, err
		}
		newIdx[k.h] = vecLoc{off: off, n: k.loc.n, dim: k.loc.dim}
		off += size
		live += size
		dim = int(k.loc.dim)
	}
	if err = bw.Flush(); err != nil {
		err = fmt.Errorf("%w: compaction write: %v", ErrCache, err)
		return dropped, err
	}
	if err = tmp.Sync(); err != nil {
		err = fmt.Errorf("%w: compaction sync: %v", ErrCache, err)
		return dropped, err
	}
	if err = tmp.Close(); err != nil {
		err = fmt.Errorf("%w: compaction close: %v", ErrCache, err)
		return dropped, err
	}
	s.close()
	if err = os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		err = fmt.Errorf("%w: replace log: %v", ErrCache, err)
		if f, _, oerr := atomicfile.OpenAppend(s.path, os.O_APPEND); oerr == nil {
			s.f = f
		} else {
			s.valid = false
		}
		return dropped, err
	}
	serr := atomicfile.SyncDir(filepath.Dir(s.path))
	f, _, oerr := atomicfile.OpenAppend(s.path, os.O_APPEND)
	if oerr != nil {
		s.valid = false
		err = fmt.Errorf("%w: reopen after compaction: %v", ErrCache, oerr)
		return dropped, err
	}
	s.f = f
	s.gen, s.hdrLen, s.end, s.idx, s.live, s.dim, s.valid = gen, int64(len(hdr)), off, newIdx, live, dim, true
	if serr != nil {
		// The new log is in place; only its durability is unknown.
		err = fmt.Errorf("%w: replaced the log, but syncing the directory failed: %v", ErrCache, serr)
	}
	return dropped, err
}

// compact rewrites the log keeping only live postings (every posting when p
// names no live set) and, when they still exceed plan.soft bytes (a total file
// size, header included), evicting them lowest priority first, the oldest
// written among equals, as plan allows. The records kept stay in written order.
func (s *chunkStore) compact(p vecPrio, plan vecPlan) (ev vecEvent, err error) {
	type cand struct {
		vecKeep
		rank float64
	}
	cands := make([]cand, 0, len(s.idx))
	total := s.hdrLen
	for h, loc := range s.idx {
		if p.outside(h) {
			ev.Evicted++
			continue
		}
		cands = append(cands, cand{vecKeep{h: h, loc: loc}, p.of(h)})
		total += loc.size()
	}
	slices.SortFunc(cands, func(a, b cand) int {
		if c := cmp.Compare(a.rank, b.rank); c != 0 {
			return c
		}
		return cmp.Compare(a.loc.off, b.loc.off)
	})
	cut := 0
	for cut < len(cands) && total > plan.soft && (total > plan.hard || cands[cut].rank <= plan.floor) {
		total -= cands[cut].loc.size()
		cut++
		ev.Evicted++
	}
	keep := make([]vecKeep, 0, len(cands)-cut)
	for _, c := range cands[cut:] {
		keep = append(keep, c.vecKeep)
	}
	slices.SortFunc(keep, func(a, b vecKeep) int { return cmp.Compare(a.loc.off, b.loc.off) })
	oldGen := s.gen
	ev.Dropped, err = s.rewrite(keep)
	ev.Compacted = s.gen != oldGen // the new log replaced the old one
	return ev, err
}

// admit says which of encs may be stored when the log is at its bound, and the
// lowest priority among those admitted. Highest priority first, a record is
// admitted when it fits beside the records already admitted and the stored
// postings that outrank it (live ones ranked strictly above it: it may evict
// the rest, outside-live and equal or lower ranked, never those). A record that
// cannot fit that way is refused, never at the cost of a higher-ranked posting.
// The admitted ones are what compact then makes room for.
func (s *chunkStore) admit(encs []vecEnc, p vecPrio) (ok []bool, floor float64) {
	type held struct {
		rank float64
		size int64
	}
	stored := make([]held, 0, len(s.idx))
	for h, loc := range s.idx {
		if !p.outside(h) {
			stored = append(stored, held{p.of(h), loc.size()})
		}
	}
	slices.SortFunc(stored, func(a, b held) int { return cmp.Compare(b.rank, a.rank) })
	order := make([]int, len(encs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int {
		return cmp.Compare(p.incoming(encs[j].h), p.incoming(encs[i].h))
	})
	ok, floor = make([]bool, len(encs)), math.Inf(1)
	var outranking, admitted int64
	next := 0
	for _, i := range order {
		r := p.incoming(encs[i].h)
		for next < len(stored) && stored[next].rank > r {
			outranking += stored[next].size
			next++
		}
		if size := int64(len(encs[i].b)); s.hdrLen+outranking+admitted+size <= s.maxBytes {
			ok[i], floor = true, r
			admitted += size
		}
	}
	return ok, floor
}

// needsCompaction is true when the log passes its bound, or holds more dead
// bytes than live ones (and at least compactMin of them).
func (s *chunkStore) needsCompaction() bool {
	dead := s.end - s.hdrLen - s.live
	return s.end > s.maxBytes || (dead >= s.compactMin && dead > s.live)
}

// reset starts the log empty (a width change: nothing in it can be used).
func (s *chunkStore) reset() error {
	_, err := s.rewrite(nil)
	return err
}

// append stores recs and fsyncs once. Postings that already have a record of the
// same shape are skipped. At the bound it first decides which records may be
// stored (admit: one ranked below everything it would have to evict is
// refused, not stored at a higher-ranked posting's cost), then compacts for
// them (outside the live set, then lowest priority, then oldest, leaving an
// eighth of the bound free so the next batch does not compact again); records
// that still do not fit are refused with ErrVectorsFull after those that fit
// are written, and the store stops accepting vectors for the rest of the
// session. ev.Refused counts every record not stored. A failed write is rolled
// back by truncating to the last good end; if that fails too the store refuses
// further appends.
func (s *chunkStore) append(recs []vecRec) (appended int, ev vecEvent, err error) {
	if s.f == nil || !s.valid || s.broken {
		ev.Refused = len(recs)
		return 0, ev, fmt.Errorf("%w: chunk-vector log is not open for appends", ErrCache)
	}
	if s.full {
		ev.Refused = len(recs)
		return 0, ev, ErrVectorsFull
	}
	var encs []vecEnc
	var total int64
	seen := map[[16]byte]bool{}
	var firstErr error
	refused := 0
	for _, r := range recs {
		if seen[r.h] {
			continue
		}
		seen[r.h] = true
		b, eerr := encodeVecRecord(r.h, r.vecs)
		if eerr != nil {
			if firstErr == nil {
				firstErr = eerr
			}
			refused++
			continue
		}
		n, dim := len(r.vecs), len(r.vecs[0])
		if s.dim != 0 && dim != s.dim {
			if firstErr == nil {
				firstErr = vecCorrupt("vectors are %d wide, the log holds %d", dim, s.dim)
			}
			refused++
			continue
		}
		if old, ok := s.idx[r.h]; ok && int(old.n) == n && int(old.dim) == dim {
			continue
		}
		encs = append(encs, vecEnc{h: r.h, b: b, n: n, dim: dim})
		total += int64(len(b))
	}
	if len(encs) == 0 {
		ev.Refused = refused
		return 0, ev, firstErr
	}
	if s.end+total > s.maxBytes {
		p := s.prio()
		ok, floor := s.admit(encs, p)
		admitted := encs[:0:0]
		total = 0
		for i, e := range encs {
			if ok[i] {
				admitted = append(admitted, e)
				total += int64(len(e.b))
			}
		}
		if len(admitted) < len(encs) {
			// Refused for rank, not for room: a later batch ranked higher may still be
			// admitted, so this does not set full (a refusal here costs a sort, no I/O).
			firstErr = ErrVectorsFull
		}
		if refused += len(encs) - len(admitted); len(admitted) == 0 {
			ev.Refused = refused
			return 0, ev, firstErr
		}
		encs = admitted
		soft := max(s.maxBytes-s.maxBytes/8-total, s.hdrLen)
		hard := max(s.maxBytes-total, s.hdrLen)
		cev, cerr := s.compact(p, vecPlan{soft: soft, hard: hard, floor: floor})
		ev = cev
		if cerr != nil {
			if !s.valid || s.f == nil {
				ev.Refused = refused + len(encs)
				return 0, ev, cerr
			}
			if firstErr == nil {
				firstErr = cerr
			}
		}
	}
	fit, cum := 0, s.end
	for fit < len(encs) && cum+int64(len(encs[fit].b)) <= s.maxBytes {
		cum += int64(len(encs[fit].b))
		fit++
	}
	if fit < len(encs) {
		s.full = true
		firstErr = ErrVectorsFull
		refused += len(encs) - fit
	}
	if fit == 0 {
		ev.Refused = refused
		return 0, ev, firstErr
	}
	buf := make([]byte, 0, cum-s.end)
	offs := make([]int64, fit)
	at := s.end
	for i := 0; i < fit; i++ {
		offs[i] = at
		buf = append(buf, encs[i].b...)
		at += int64(len(encs[i].b))
	}
	var werr error
	if _, werr = s.f.Write(buf); werr == nil {
		werr = s.f.Sync()
	}
	if werr != nil {
		if terr := s.f.Truncate(s.end); terr != nil {
			s.broken = true
		} else {
			_ = s.f.Sync()
		}
		ev.Refused = refused + fit
		return 0, ev, fmt.Errorf("%w: append: %v", ErrCache, werr)
	}
	for i := 0; i < fit; i++ {
		s.set(encs[i].h, vecLoc{off: offs[i], n: uint16(encs[i].n), dim: uint16(encs[i].dim)})
		s.dim = encs[i].dim
	}
	s.end = at
	ev.Refused = refused
	return fit, ev, firstErr
}

// ---- Matcher integration ----

// VectorStats describes a chunk-vector log.
type VectorStats struct {
	Present    bool   // the file exists
	Records    int    // postings with stored vectors
	Bytes      int64  // file size
	LiveBytes  int64  // bytes of the records in use; Bytes minus the header and these is dead
	Generation uint64 // changes whenever the log is compacted or replaced
	Model      string // embedding model the vectors came from
	Version    int    // semantic.ChunkVersion the vectors were chunked under
	Dim        int
	// TornBytes is the incomplete tail after the last whole record. A Warm
	// truncates it; seen here it may also be an append in flight.
	TornBytes int64
	// Evicted and Dropped are CompactVectors' result: postings removed (outside
	// the live set, then lowest priority, then oldest) and records shed for a bad
	// checksum.
	Evicted, Dropped int
}

// VectorStatus reads the chunk-vector log in cacheDir without loading a model,
// taking the lock or changing the file. A missing log is the zero value with
// Present false. Corruption is an ErrCache error (the stats gathered up to it
// are still returned).
func VectorStatus(cacheDir string) (VectorStats, error) {
	var vs VectorStats
	f, err := os.Open(filepath.Join(cacheDir, vectorLogFileName))
	if errors.Is(err, os.ErrNotExist) {
		return vs, nil
	}
	if err != nil {
		return vs, fmt.Errorf("%w: %v", ErrCache, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return vs, fmt.Errorf("%w: %v", ErrCache, err)
	}
	if !st.Mode().IsRegular() {
		return vs, fmt.Errorf("%w: %s is not a regular file", ErrCache, vectorLogFileName)
	}
	vs.Present, vs.Bytes = true, st.Size()
	h, err := parseVecHeader(f, st.Size())
	if errors.Is(err, errVecTornHeader) {
		vs.TornBytes = st.Size()
		return vs, nil
	}
	if err != nil {
		return vs, err
	}
	vs.Generation, vs.Model, vs.Version = h.gen, h.model, int(h.version)
	s := newChunkStore("", h.model, MaxVectorBytes)
	_, torn, err := s.scan(f, st.Size(), h.hdrLen)
	vs.Records, vs.LiveBytes, vs.Dim, vs.TornBytes = len(s.idx), s.live, s.dim, torn
	return vs, err
}

func (s *chunkStore) stats() VectorStats {
	return VectorStats{Present: true, Records: len(s.idx), Bytes: s.end, LiveBytes: s.live,
		Generation: s.gen, Model: s.model, Version: ChunkVersion, Dim: s.dim}
}

// SetVectorPriority tells the matcher which postings still matter and how much,
// keyed by TextHash: the keys are the live set and a higher value is kept
// longer. When the chunk-vector log is compacted (it reached its bound, or holds
// mostly dead bytes) the vectors of postings outside the map go first, then the
// lowest priority, and among equal priorities the oldest written; a posting NaN
// ranks as the lowest. At the bound a posting ranked below everything it would
// have to evict is refused (Report.VectorsRefused) rather than evict a
// higher-ranked one; its score is still saved. The postings of the Warm that
// triggers a compaction are always kept, and one the map does not rank counts as
// the highest. nil (the default) names no live set: nothing is evicted for it,
// and only age decides. The map is copied.
func (m *Matcher) SetVectorPriority(rank map[string]float64) {
	var c map[string]float64
	if rank != nil {
		c = make(map[string]float64, len(rank))
		for k, v := range rank {
			if math.IsNaN(v) {
				v = math.Inf(-1)
			}
			c[k] = v
		}
	}
	m.mu.Lock()
	m.vecPrio = c
	m.mu.Unlock()
}

// vecPrioFn builds the compaction priorities: the caller's map plus the docs of
// this Warm, ranked highest unless the map ranks them. nil when no map was
// supplied.
func (m *Matcher) vecPrioFn(docs []Doc) func() vecPrio {
	m.mu.RLock()
	set := m.vecPrio
	m.mu.RUnlock()
	if set == nil {
		return nil
	}
	return func() vecPrio {
		out := make(map[[16]byte]float64, len(set)+len(docs))
		for th, r := range set {
			if h, ok := parseTextHash(th); ok {
				out[h] = r
			}
		}
		for _, d := range docs {
			if h, ok := parseTextHash(TextHash(d.Title, d.Description)); ok {
				if _, ranked := out[h]; !ranked {
					out[h] = math.Inf(1)
				}
			}
		}
		return vecPrio{rank: out}
	}
}

func (m *Matcher) vecBound() int64 {
	if m.vecMax > 0 {
		return m.vecMax
	}
	return MaxVectorBytes
}

// noteVectors records the first problem with the chunk-vector log and, if
// nothing else has gone wrong yet, makes it the report's Err: a log that could
// not be used must not look like one that was.
func (r *Report) noteVectors(err error) {
	if err == nil {
		return
	}
	if r.VectorsErr == "" {
		r.VectorsErr = err.Error()
	}
	r.note(err)
}

// openVectors opens the log under the cache lock and brings its index up to
// date, repairing what it can: an incomplete tail is truncated (VectorsTornBytes),
// another model or ChunkVersion starts the log anew, and a corrupt log is removed and
// started anew (VectorsRebuilt: that loss costs re-embedding). nil means the log
// cannot be used this Warm (reported in VectorsErr); scoring goes on without it.
func (m *Matcher) openVectors(rep *Report, docs []Doc) *chunkStore {
	if m.vs == nil {
		m.vs = newChunkStore(filepath.Join(m.dir, vectorLogFileName), m.model, m.vecBound())
	}
	s := m.vs
	s.maxBytes = m.vecBound()
	s.prioFn = m.vecPrioFn(docs)
	ev, err := s.refresh()
	if errors.Is(err, errVecCorrupt) {
		rep.noteVectors(err)
		s.close()
		if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			rep.noteVectors(fmt.Errorf("%w: remove corrupt log: %v", ErrCache, rerr))
			return nil
		}
		s.resetIndex()
		rep.VectorsRebuilt = true
		ev, err = s.refresh()
	}
	if err != nil {
		rep.noteVectors(err)
		s.close()
		return nil
	}
	rep.VectorsTornBytes += ev.TornBytes
	if ev.TornBytes > 0 {
		rep.noteVectors(fmt.Errorf("%w: cut %d bytes of an incomplete tail", ErrCache, ev.TornBytes))
	}
	if s.needsCompaction() {
		m.compactInto(rep, s)
	}
	return s
}

// compactInto compacts s and adds the outcome to rep.
func (m *Matcher) compactInto(rep *Report, s *chunkStore) {
	ev, err := s.compact(s.prio(), evictTo(s.maxBytes-s.maxBytes/8))
	rep.addVectorEvent(ev)
	rep.noteVectors(err)
}

func (r *Report) addVectorEvent(ev vecEvent) {
	r.VectorsEvicted += ev.Evicted
	r.VectorsRefused += ev.Refused
	r.VectorsCorrupt += ev.Dropped
	r.VectorsCompacted = r.VectorsCompacted || ev.Compacted
}

// appendVectors stores the vectors of postings just embedded.
func (m *Matcher) appendVectors(s *chunkStore, recs []vecRec, rep *Report) {
	if s == nil || len(recs) == 0 {
		return
	}
	n, ev, err := s.append(recs)
	rep.VectorsAppended += n
	rep.addVectorEvent(ev)
	rep.noteVectors(err)
}

// finishVectors records the log's size in rep and closes it. Called when Warm
// returns, by whichever path.
func (m *Matcher) finishVectors(rep *Report, s *chunkStore) {
	if s == nil {
		return
	}
	rep.VectorsCount, rep.VectorsBytes = len(s.idx), s.end
	s.close()
}

// CompactVectors compacts the chunk-vector log now, under the cache lock:
// superseded duplicates and corrupt records go, then postings outside the live
// set (SetVectorPriority), then the lowest priority (the oldest written among
// equals) until the log is an eighth under its bound. It loads no model. A corrupt log is an ErrCache error; Warm, not this,
// removes and rebuilds it.
func (m *Matcher) CompactVectors(ctx context.Context) (VectorStats, error) {
	m.warmMu.Lock()
	defer m.warmMu.Unlock()
	wait := m.lockWait
	if wait <= 0 {
		wait = DefaultLockWait
	}
	unlock, err := lockCache(ctx, m.dir, wait)
	if err != nil {
		return VectorStats{}, err
	}
	defer unlock()
	var rep Report
	s := m.openVectors(&rep, nil)
	if s == nil {
		return VectorStats{}, fmt.Errorf("%w: %s", ErrCache, rep.VectorsErr)
	}
	defer s.close()
	ev, err := s.compact(s.prio(), evictTo(s.maxBytes-s.maxBytes/8))
	st := s.stats()
	st.Evicted, st.Dropped = ev.Evicted, ev.Dropped
	return st, err
}
