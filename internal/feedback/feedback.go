// Package feedback keeps the impression log: which postings a surface showed
// the owner, where, and with which scores. It is append-only JSONL beside the
// ledger, in <state dir>/feedback, and holds posting keys, section names and
// numbers only: never a title, description, note or anything personal, so it
// is safe to read into a fitter or a report. The owner's verdicts are not
// here: they are the would_apply labels of internal/eval.
//
// Unmarked is not negative: a posting counts as seen-and-passed only when an
// impression says it was shown.
//
// The log is bounded. When the live file reaches MaxLogBytes it becomes the
// one rotated file (the previous rotated file is replaced), so the log holds
// at most about twice MaxLogBytes, newest rows last.
package feedback

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// Version is the only line version this package reads or writes.
	Version = 1
	// DirName is the feedback directory inside the state dir.
	DirName = "feedback"
	// ImpressionsFile is the live log; RotatedFile is the one before it.
	ImpressionsFile = "impressions.jsonl"
	RotatedFile     = "impressions.1.jsonl"

	// MaxLine bounds one line, written or read.
	MaxLine = 8 << 10
	// MaxAppend bounds how many rows one AppendImpressions writes.
	MaxAppend = 1000
	// MaxRead bounds how many lines a read returns; a longer log reads its
	// newest MaxRead lines and says it was capped.
	MaxRead = 500_000
	// MaxFactors bounds the factor map on one line.
	MaxFactors = 32
	// MaxLogBytes is the size at which the live log is rotated.
	MaxLogBytes = 16 << 20

	maxKeyBytes   = 512
	maxNameBytes  = 64
	maxReasonByte = 300
	lockFile      = "feedback.lock"
	lockWait      = 5 * time.Second
	lockPoll      = 25 * time.Millisecond
)

// Surfaces an impression can come from.
const (
	SurfaceDigest = "digest"
	SurfaceJobs   = "jobs"
	SurfaceAudit  = "audit"
)

// Digest sections an impression can name.
const (
	SectionRanked          = "ranked"
	SectionUnranked        = "not_ranked"
	SectionCoverageUnknown = "coverage_unknown"
	SectionUnsure          = "unsure"
	SectionSaved           = "saved"
	SectionTopOpen         = "top_open"
	SectionExplore         = "explore"
)

// ErrBusy means another process held the feedback lock for the whole wait.
var ErrBusy = errors.New("feedback: the log is locked by another process")

// Scores is a posting's ranking as a surface computed it. Priority and
// Relevance are Score's 0-100 numbers; Factors are its parts by name (see
// ScoresOf), only the known ones.
type Scores struct {
	Priority  float64            `json:"priority"`
	Relevance float64            `json:"relevance"`
	Factors   map[string]float64 `json:"factors,omitempty"`
}

// Impression is one posting shown on a surface.
type Impression struct {
	V  int       `json:"v"`
	At time.Time `json:"at"`
	// SlateID names the slate this row belongs to (the digest's date, an audit
	// draw's seed, a Jobs view's refresh). A key is logged once per slate, so
	// a surface that redraws the same slate does not count as showing it twice.
	// Empty means the row is not deduplicated.
	SlateID  string      `json:"slate_id,omitempty"`
	Surface  string      `json:"surface"`
	Section  string      `json:"section,omitempty"`
	Key      posting.Key `json:"key"`
	Position int         `json:"position"` // 1-based within the surface
	Status   string      `json:"status,omitempty"`
	// DropReason is the ledger reason of a dropped posting shown for
	// exploration, so precision can be measured per drop rule.
	DropReason string `json:"drop_reason,omitempty"`
	// Source and PSelect belong to a row drawn by a random sample (an Explore
	// row, an audit card): the stratum and its inclusion probability.
	Source  string  `json:"source,omitempty"`
	PSelect float64 `json:"p_select,omitempty"`
	Scores
}

// maxLogBytes is MaxLogBytes; a test lowers it.
var maxLogBytes int64 = MaxLogBytes

// Dir is the feedback directory of a state dir.
func Dir(stateDir string) string { return filepath.Join(stateDir, DirName) }

func (i Impression) check() error {
	if err := checkKey(i.Key); err != nil {
		return err
	}
	if !name(i.Surface) || (i.Section != "" && !name(i.Section)) || (i.SlateID != "" && !name(i.SlateID)) ||
		(i.Source != "" && !name(i.Source)) || len(i.Status) > maxNameBytes {
		return fmt.Errorf("feedback: impression for %s: bad surface, section, slate or source", i.Key)
	}
	if i.Position < 1 {
		return fmt.Errorf("feedback: impression for %s: position %d must be at least 1", i.Key, i.Position)
	}
	if len(i.DropReason) > maxReasonByte {
		return fmt.Errorf("feedback: impression for %s: drop reason longer than %d bytes", i.Key, maxReasonByte)
	}
	if math.IsNaN(i.PSelect) || i.PSelect < 0 || i.PSelect > 1 {
		return fmt.Errorf("feedback: impression for %s: p_select %v must be within 0..1", i.Key, i.PSelect)
	}
	return i.Scores.check()
}

func (s Scores) check() error {
	if !score100(s.Priority) || !score100(s.Relevance) {
		return fmt.Errorf("feedback: priority %v and relevance %v must be within 0..100", s.Priority, s.Relevance)
	}
	if len(s.Factors) > MaxFactors {
		return fmt.Errorf("feedback: %d factors, at most %d", len(s.Factors), MaxFactors)
	}
	for k, v := range s.Factors {
		if !name(k) || math.IsNaN(v) || v < 0 || v > maxFactor {
			return fmt.Errorf("feedback: factor %q = %v must be named and within 0..%d", k, v, maxFactor)
		}
	}
	return nil
}

// ConfSuffix names a known part's confidence in Scores.Factors ("fit_conf").
// A line written before it was recorded has none; read it as 1.
const ConfSuffix = "_conf"

// maxFactor bounds a factor value: Score's parts are 0-1 except the company
// multiplier, which reaches jobinsight.CompanyMax (1.1).
const maxFactor = 2

// ScoresOf is a Score result as feedback keeps it: priority and relevance, the
// four multiplicative parts, and each additive part that was known. Unknown
// parts are left out: a fitter must not learn from a prior as if it were data.
func ScoresOf(r jobinsight.Result) Scores {
	f := map[string]float64{"match": r.Match, "gate": r.Gate, "fresh": r.Fresh, "company": r.Company}
	for _, x := range r.Factors {
		if _, multiplier := f[x.Name]; x.Known && !multiplier {
			// The blend weighs a known part by Weight x Conf, so the
			// confidence is kept beside the value (rankfit.ItemFromScores).
			f[x.Name], f[x.Name+ConfSuffix] = x.Value, x.Conf
		}
	}
	for k, v := range f {
		if math.IsNaN(v) || v < 0 {
			f[k] = 0
		} else if v > maxFactor {
			f[k] = maxFactor
		}
	}
	return Scores{Priority: clamp100(r.Score), Relevance: clamp100(r.Relevance), Factors: f}
}

func clamp100(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return min(v, 100)
}

func score100(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 100 }

func checkKey(k posting.Key) error {
	if k == "" || len(k) > maxKeyBytes || bytes.ContainsAny([]byte(k), "\n\r") {
		return fmt.Errorf("feedback: bad posting key %q", k)
	}
	return nil
}

// name is a short lower-case token: letters, digits and _:-.
func name(s string) bool {
	if s == "" || len(s) > maxNameBytes {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ':' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// AppendImpressions writes rows to the impression log, stamping V, and returns
// how many it wrote. Every row is checked first; one bad row writes nothing.
// A row whose (SlateID, Key) is already in the log, or earlier in rows, is
// skipped: that is the dedupe, so a surface may report the same slate again.
func AppendImpressions(ctx context.Context, stateDir string, rows []Impression) (int, error) {
	if stateDir == "" {
		return 0, errors.New("feedback: empty state dir")
	}
	if len(rows) > MaxAppend {
		return 0, fmt.Errorf("feedback: %d rows in one append, at most %d", len(rows), MaxAppend)
	}
	for i := range rows {
		rows[i].V = Version
		if err := rows[i].check(); err != nil {
			return 0, err
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	dir := Dir(stateDir)
	unlock, err := lock(ctx, dir)
	if err != nil {
		return 0, err
	}
	defer unlock()

	seen, err := slatesIn(dir, rows)
	if err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	n := 0
	for _, r := range rows {
		if r.SlateID != "" {
			k := slateKey{r.SlateID, r.Surface, r.Key}
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		b, err := json.Marshal(r)
		if err != nil {
			return 0, fmt.Errorf("feedback: %w", err)
		}
		if len(b)+1 > MaxLine {
			return 0, fmt.Errorf("feedback: a row of %d bytes is over %d", len(b), MaxLine)
		}
		buf.Write(b)
		buf.WriteByte('\n')
		n++
	}
	if n == 0 {
		return 0, nil
	}
	live := filepath.Join(dir, ImpressionsFile)
	// Open before any rename. Stat would follow a symlink and Rename would move
	// the link; OpenAppend refuses a symlink or a non-regular leaf (a FIFO)
	// without creating, removing, or blocking on it.
	f, created, err := openImpression(live)
	if err != nil {
		return 0, err
	}
	if !created {
		st, serr := f.Stat()
		if serr != nil {
			_ = f.Close()
			return 0, fmt.Errorf("feedback: stat %s: %w", ImpressionsFile, serr)
		}
		if st.Size()+int64(buf.Len()) > maxLogBytes {
			if rerr := refuseLeafSwap(live, st); rerr != nil {
				_ = f.Close()
				return 0, fmt.Errorf("feedback: rotate %s: %w", ImpressionsFile, rerr)
			}
			if cerr := f.Close(); cerr != nil {
				return 0, fmt.Errorf("feedback: rotate %s: %w", ImpressionsFile, cerr)
			}
			if rerr := os.Rename(live, filepath.Join(dir, RotatedFile)); rerr != nil {
				return 0, fmt.Errorf("feedback: rotate %s: %w", ImpressionsFile, rerr)
			}
			f, _, err = openImpression(live)
			if err != nil {
				return 0, err
			}
		}
	}
	// Read before the write: a log that ends mid-row (a crash mid-append) would
	// glue the first new row onto that fragment and lose both.
	data := buf.Bytes()
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("feedback: %s: %w", ImpressionsFile, err)
	}
	if torn {
		data = append([]byte{'\n'}, data...)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("feedback: stat %s: %w", ImpressionsFile, err)
	}
	if wrote, err := writeAll(f, data); err != nil {
		// A short write leaves a partial row; cut it back so the log still ends
		// on a row boundary and no reader counts it as junk forever.
		err = fmt.Errorf("feedback: write %s: %w", ImpressionsFile, err)
		// Truncate only if nothing else was appended since: another appender's
		// row after our fragment would be cut off with it.
		if now, serr := f.Stat(); serr != nil {
			err = errors.Join(err, fmt.Errorf("feedback: the partial write could not be cut back safely: %w", serr))
		} else if now.Size() != st.Size()+int64(wrote) {
			err = errors.Join(err, fmt.Errorf("feedback: the partial write could not be cut back safely: %s changed by %d bytes besides ours", ImpressionsFile, now.Size()-st.Size()-int64(wrote)))
		} else if terr := f.Truncate(st.Size()); terr != nil {
			err = errors.Join(err, fmt.Errorf("feedback: the partial write could not be cut back: %w", terr))
		}
		_ = f.Close()
		return 0, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("feedback: sync %s: %w", ImpressionsFile, err)
	}
	return n, f.Close()
}

// writeAll is f.Write; a variable only so a test can inject a short write.
var writeAll = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

// openImpression opens the live log for append. A new file's directory entry
// is synced before the caller trusts it. A symlink leaf is ErrLinkedLeaf and
// a non-regular leaf is ErrNotRegular; neither is created or removed.
func openImpression(live string) (*os.File, bool, error) {
	f, created, err := atomicfile.OpenAppend(live, os.O_APPEND)
	if err != nil {
		return nil, false, fmt.Errorf("feedback: %w", err)
	}
	if created {
		if err := atomicfile.SyncNew(live, atomicfile.SyncDir); err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("feedback: %w", err)
		}
	}
	return f, created, nil
}

// refuseLeafSwap reports whether live is still the regular file opened holds.
// Rotation must not rename a symlink or a non-regular leaf planted in its place.
func refuseLeafSwap(live string, opened os.FileInfo) error {
	lst, err := os.Lstat(live)
	if err != nil {
		return err
	}
	switch {
	case lst.Mode()&os.ModeSymlink != 0:
		return atomicfile.ErrLinkedLeaf
	case !lst.Mode().IsRegular():
		return atomicfile.ErrNotRegular
	case !os.SameFile(opened, lst):
		return errors.New("replaced before rotation")
	default:
		return nil
	}
}

type slateKey struct {
	slate, surface string
	key            posting.Key
}

// slatesIn returns the (slate, surface, key) triples already logged for the
// slates rows name, reading the rotated and the live file. A line that is not
// valid is ignored here (ReadImpressions counts it).
func slatesIn(dir string, rows []Impression) (map[slateKey]bool, error) {
	want := map[string]bool{}
	for _, r := range rows {
		if r.SlateID != "" {
			want[r.SlateID] = true
		}
	}
	seen := map[slateKey]bool{}
	if len(want) == 0 {
		return seen, nil
	}
	for _, file := range []string{RotatedFile, ImpressionsFile} {
		err := scanLog(filepath.Join(dir, file), func(line []byte) {
			var v Impression
			if json.Unmarshal(line, &v) == nil && v.V == Version && want[v.SlateID] {
				seen[slateKey{v.SlateID, v.Surface, v.Key}] = true
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// lock takes the exclusive feedback lock in dir, waiting at most lockWait.
func lock(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("feedback: lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrBusy
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// Read is what a log held: its valid lines, oldest first, and what could not be
// used. A reader must show Skipped and Capped: a log that was partly unreadable
// is not a complete one.
type Read struct {
	Rows []Impression
	// Skipped counts lines that were malformed, oversized, of another version
	// or failed validation. A torn last line (a crash mid-append) counts here.
	Skipped int
	// Capped is how many older lines were left out past MaxRead.
	Capped int
}

// ReadImpressions reads the rotated and the live impression log; a missing log
// is empty.
func ReadImpressions(stateDir string) (Read, error) {
	var out Read
	if stateDir == "" {
		return out, errors.New("feedback: empty state dir")
	}
	for _, file := range []string{RotatedFile, ImpressionsFile} {
		err := scanLog(filepath.Join(Dir(stateDir), file), func(line []byte) {
			var v Impression
			if len(line) > MaxLine || json.Unmarshal(line, &v) != nil || v.V != Version || v.check() != nil {
				out.Skipped++
				return
			}
			out.Rows = append(out.Rows, v)
			if len(out.Rows) > MaxRead {
				out.Rows = out.Rows[1:]
				out.Capped++
			}
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// scanLog calls each for every non-empty line of the file (an oversized line
// arrives truncated to MaxLine+1 bytes); a missing file has no lines.
func scanLog(path string, each func(line []byte)) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("feedback: %w", err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			each(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("feedback: read %s: %w", filepath.Base(path), err)
		}
	}
}

// readLine reads one line without its newline. A line longer than MaxLine is
// consumed whole and returned truncated to MaxLine+1 bytes, so the caller sees
// it as oversized without holding it in memory.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line) <= MaxLine {
			line = append(line, chunk[:min(len(chunk), MaxLine+1-len(line))]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(line, "\r\n"), err
	}
}
