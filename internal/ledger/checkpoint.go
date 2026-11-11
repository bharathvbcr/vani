package ledger

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// Checkpoints.
//
// A Reader's fold of the first N bytes of the events file is the same every
// time, so it is saved (records with their descriptions cut out, where each
// description's line is, and each description's fingerprint) and the next
// Reader folds only what was appended after N. It is a cache: losing it, or
// finding it does not fit the file, costs one full fold and nothing else.
//
// A checkpoint is used only when all of these hold, and is otherwise ignored
// with the reason recorded in ReaderInfo.Checkpoint:
//
//   - its format and the ledger SchemaVersion are this build's;
//   - the events file is the one it was taken of (device and inode, where the
//     platform has them) and is at least as long as the bytes it covers;
//   - the last line it covers, and up to checkpointSamples posting lines spread
//     through the file, are byte for byte what they were (SHA-256), so a
//     rolled-back or rewritten file is refolded rather than trusted.
//
// It is written by whoever folds (OpenReader and Advance past
// ReaderOptions.SaveAfter, or SaveCheckpoint), never under the ledger's lock:
// it lives outside the ledger directory, is replaced atomically, and a lock of
// its own (path + ".lock", taken without waiting) keeps two writers from
// interleaving. A writer that finds the lock held skips the save.

const (
	// checkpointFormat changes whenever the encoded layout or its meaning does.
	checkpointFormat = 1
	// checkpointSamples bounds the lines checked on top of the last one.
	checkpointSamples = 32
	// maxCheckpointBytes bounds a checkpoint read: a file larger than this is
	// not one this package wrote for any ledger it can fold.
	maxCheckpointBytes = 2 << 30
)

type checkpointFile struct {
	Format, Schema int
	ID             FileID
	Good, Last     int64
	LastSum        [32]byte
	Samples        []lineSample
	Entries        []checkpointEntry
}

type lineSample struct {
	Off int64
	N   int32
	Sum [32]byte
}

type checkpointEntry struct {
	Record  Record
	HasFP   bool
	FP      [16]byte
	HasBody bool // the description was cut out and is at BodyOff, BodyN bytes
	BodyOff int64
	BodyN   int32
}

// errCheckpointBusy means another process is writing the checkpoint.
var errCheckpointBusy = errors.New("another process is writing it")

// SaveCheckpoint writes the Reader's fold to ReaderOptions.Checkpoint now.
func (r *Reader) SaveCheckpoint() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ev == nil {
		return ErrClosed
	}
	if r.opts.Checkpoint == "" {
		return errors.New("ledger: no checkpoint file configured")
	}
	err := r.saveCheckpoint()
	r.noteSave(err)
	return err
}

// maybeSave saves after a fold of at least SaveAfter bytes. Caller holds r.mu
// (or owns r alone, in OpenReader).
func (r *Reader) maybeSave() {
	if r.opts.Checkpoint == "" || r.opts.SaveAfter <= 0 || r.info.Folded < r.opts.SaveAfter || r.stale {
		return
	}
	r.noteSave(r.saveCheckpoint())
}

func (r *Reader) noteSave(err error) {
	switch {
	case err == nil:
		r.info.Saved = "saved"
	case errors.Is(err, errCheckpointBusy):
		r.info.Saved = "skipped: " + err.Error()
	default:
		r.info.Saved = "failed: " + err.Error()
	}
}

func (r *Reader) saveCheckpoint() error {
	lock, err := filelock.TryAcquire(r.opts.Checkpoint + ".lock")
	if errors.Is(err, filelock.ErrWouldBlock) {
		return errCheckpointBusy
	}
	if err != nil {
		return fmt.Errorf("ledger: checkpoint lock: %w", err)
	}
	defer lock.Release()
	ck := checkpointFile{Format: checkpointFormat, Schema: SchemaVersion, ID: r.id,
		Good: r.good, Last: r.last, LastSum: r.lastSum}
	recs := sorted(r.recs)
	ck.Entries = make([]checkpointEntry, len(recs))
	var withBody []int
	for i, rec := range recs {
		e := checkpointEntry{Record: rec}
		e.FP, e.HasFP = r.lean.fps[rec.Key]
		if b, ok := r.lean.bodies[rec.Key]; ok && rec.BodyEvicted {
			e.HasBody, e.BodyOff, e.BodyN = true, b.off, b.n
			withBody = append(withBody, i)
		}
		ck.Entries[i] = e
	}
	for s := range min(checkpointSamples, len(withBody)) {
		e := ck.Entries[withBody[s*len(withBody)/min(checkpointSamples, len(withBody))]]
		sum, err := lineSum(r.ev, e.BodyOff, e.BodyOff+int64(e.BodyN))
		if err != nil {
			return fmt.Errorf("ledger: checkpoint sample: %w", err)
		}
		ck.Samples = append(ck.Samples, lineSample{Off: e.BodyOff, N: e.BodyN, Sum: sum})
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&ck); err != nil {
		return fmt.Errorf("ledger: encode checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.opts.Checkpoint), 0o700); err != nil {
		return fmt.Errorf("ledger: checkpoint dir: %w", err)
	}
	return atomicfile.Write(r.opts.Checkpoint, buf.Bytes())
}

// loadCheckpoint installs the checkpoint into r when it describes the events
// file r.ev (size bytes long), and returns why not otherwise. On a non-empty
// answer r's fold state is unspecified; the caller resets it.
func (r *Reader) loadCheckpoint(size int64) string {
	f, err := os.Open(r.opts.Checkpoint)
	if errors.Is(err, os.ErrNotExist) {
		return "no checkpoint yet"
	}
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err.Error()
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxCheckpointBytes {
		return "the checkpoint is not a regular file of a plausible size"
	}
	var ck checkpointFile
	if err := gob.NewDecoder(bufio.NewReaderSize(f, 1<<20)).Decode(&ck); err != nil {
		return "unreadable: " + err.Error()
	}
	switch {
	case ck.Format != checkpointFormat || ck.Schema != SchemaVersion:
		return fmt.Sprintf("format %d schema %d, this build reads %d and %d", ck.Format, ck.Schema, checkpointFormat, SchemaVersion)
	case ck.ID != r.id:
		return "it was taken of another events file"
	case ck.Good < 0 || ck.Good > size:
		return "the events file is shorter than the checkpoint"
	}
	if ck.Good > 0 {
		if sum, err := lineSum(r.ev, ck.Last, ck.Good); err != nil || sum != ck.LastSum {
			return "the last line it covers changed"
		}
	}
	for _, s := range ck.Samples {
		if s.Off < 0 || s.Off+int64(s.N) > ck.Good {
			return "a sampled line is outside the bytes it covers"
		}
		if sum, err := lineSum(r.ev, s.Off, s.Off+int64(s.N)); err != nil || sum != s.Sum {
			return "a sampled line changed"
		}
	}
	in := interner{}
	for i := range ck.Entries {
		e := &ck.Entries[i]
		k := e.Record.Key
		if _, dup := r.recs[k]; dup || k == "" {
			return fmt.Sprintf("a duplicate or empty key %q", k)
		}
		s := &slot{Record: e.Record}
		internRecord(&s.Record, in)
		r.recs[k] = s
		if e.HasFP {
			r.lean.fps[k] = e.FP
		}
		if e.HasBody {
			if e.BodyOff < 0 || e.BodyN <= 1 || e.BodyOff+int64(e.BodyN) > ck.Good || !s.BodyEvicted {
				return fmt.Sprintf("the body of %s is outside the bytes it covers", k)
			}
			r.lean.bodies[k] = leanBody{off: e.BodyOff, n: e.BodyN, fp: e.FP}
		}
	}
	r.good, r.last, r.lastSum = ck.Good, ck.Last, ck.LastSum
	return ""
}

// internRecord gives a decoded record what fold gives a replayed one: a
// non-nil Annotations map (fold writes into it), and one copy of each short
// repeated string (gob decodes every string into its own allocation).
func internRecord(rec *Record, in interner) {
	p := &rec.Posting
	p.Company, p.Location, p.Department = in.of(p.Company), in.of(p.Location), in.of(p.Department)
	ann := make(map[string]string, len(rec.Annotations))
	for k, v := range rec.Annotations {
		ann[in.of(k)] = in.of(v)
	}
	rec.Annotations = ann
}
