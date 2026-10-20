// Package feedback keeps what Vani needs to measure its ranking against the
// owner's judgement: impressions (which postings a surface showed, where, and
// with which scores) and audit judgments (the owner's "would I apply" on a
// sample). Both are append-only JSONL files beside the ledger, in
// <state dir>/feedback. They hold posting keys, section names and numbers only:
// never a title, description, note or anything personal, so they are safe to
// read into a fitter or a report.
//
// Unmarked is not negative: a posting counts as seen-and-passed only when an
// impression says it was shown. Judgments carry the score factors as they were
// when judged, so a later fit learns from what the ranker saw then.
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

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// Version is the only line version this package reads or writes.
	Version = 1
	// DirName is the feedback directory inside the state dir.
	DirName = "feedback"
	// ImpressionsFile and JudgmentsFile are the two logs in it.
	ImpressionsFile = "impressions.jsonl"
	JudgmentsFile   = "judgments.jsonl"

	// MaxLine bounds one line, written or read.
	MaxLine = 8 << 10
	// MaxAppend bounds how many lines one Append writes.
	MaxAppend = 1000
	// MaxRead bounds how many lines a read returns; a longer log reads its
	// newest MaxRead lines and says it was capped.
	MaxRead = 500_000
	// MaxFactors bounds the factor map on one line.
	MaxFactors = 32

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
	SectionRanked   = "ranked"
	SectionUnranked = "not_ranked"
	SectionUnsure   = "unsure"
	SectionSaved    = "saved"
	SectionTopOpen  = "top_open"
	SectionExplore  = "explore"
)

// Verdicts a judgment can carry. Skip is "can't tell" and counts nowhere.
const (
	VerdictYes   = "yes"
	VerdictMaybe = "maybe"
	VerdictNo    = "no"
	VerdictSkip  = "skip"
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
	V        int         `json:"v"`
	At       time.Time   `json:"at"`
	Surface  string      `json:"surface"`
	Section  string      `json:"section,omitempty"`
	Key      posting.Key `json:"key"`
	Position int         `json:"position"` // 1-based within the surface
	Status   string      `json:"status,omitempty"`
	// DropReason is the ledger reason of a dropped posting shown for
	// exploration, so precision can be measured per drop rule.
	DropReason string `json:"drop_reason,omitempty"`
	Scores
}

// Judgment is the owner's verdict on one posting.
type Judgment struct {
	V       int         `json:"v"`
	At      time.Time   `json:"at"`
	Key     posting.Key `json:"key"`
	Verdict string      `json:"verdict"`
	// Source says how the posting reached the owner: an audit sample stratum
	// ("decile:3", "dropped:title:role_fit") or "explore".
	Source     string `json:"source,omitempty"`
	Status     string `json:"status,omitempty"`
	DropReason string `json:"drop_reason,omitempty"`
	Scores
}

// Dir is the feedback directory of a state dir.
func Dir(stateDir string) string { return filepath.Join(stateDir, DirName) }

// ValidVerdict reports whether v is a declared verdict.
func ValidVerdict(v string) bool {
	switch v {
	case VerdictYes, VerdictMaybe, VerdictNo, VerdictSkip:
		return true
	}
	return false
}

func (i Impression) check() error {
	if err := checkKey(i.Key); err != nil {
		return err
	}
	if !name(i.Surface) || (i.Section != "" && !name(i.Section)) || len(i.Status) > maxNameBytes {
		return fmt.Errorf("feedback: impression for %s: bad surface or section", i.Key)
	}
	if i.Position < 1 {
		return fmt.Errorf("feedback: impression for %s: position %d must be at least 1", i.Key, i.Position)
	}
	if len(i.DropReason) > maxReasonByte {
		return fmt.Errorf("feedback: impression for %s: drop reason longer than %d bytes", i.Key, maxReasonByte)
	}
	return i.Scores.check()
}

func (j Judgment) check() error {
	if err := checkKey(j.Key); err != nil {
		return err
	}
	if !ValidVerdict(j.Verdict) {
		return fmt.Errorf("feedback: verdict %q must be yes, maybe, no or skip", j.Verdict)
	}
	if len(j.Source) > maxNameBytes || len(j.Status) > maxNameBytes || len(j.DropReason) > maxReasonByte {
		return fmt.Errorf("feedback: judgment for %s: source, status or reason too long", j.Key)
	}
	return j.Scores.check()
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
			f[x.Name] = x.Value
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
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ':' || r == '-') {
			return false
		}
	}
	return true
}

// AppendImpressions writes rows to the impression log, stamping V. Every row is
// checked first; one bad row writes nothing.
func AppendImpressions(ctx context.Context, stateDir string, rows []Impression) error {
	lines := make([][]byte, 0, len(rows))
	for _, r := range rows {
		r.V = Version
		if err := r.check(); err != nil {
			return err
		}
		b, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("feedback: %w", err)
		}
		lines = append(lines, b)
	}
	return appendLines(ctx, stateDir, ImpressionsFile, lines)
}

// AppendJudgment writes one judgment, stamping V.
func AppendJudgment(ctx context.Context, stateDir string, j Judgment) error {
	j.V = Version
	if err := j.check(); err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("feedback: %w", err)
	}
	return appendLines(ctx, stateDir, JudgmentsFile, [][]byte{b})
}

func appendLines(ctx context.Context, stateDir, file string, lines [][]byte) error {
	if stateDir == "" {
		return errors.New("feedback: empty state dir")
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > MaxAppend {
		return fmt.Errorf("feedback: %d lines in one append, at most %d", len(lines), MaxAppend)
	}
	var buf bytes.Buffer
	for _, l := range lines {
		if len(l)+1 > MaxLine {
			return fmt.Errorf("feedback: a line of %d bytes is over %d", len(l), MaxLine)
		}
		buf.Write(l)
		buf.WriteByte('\n')
	}
	dir := Dir(stateDir)
	unlock, err := lock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("feedback: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("feedback: write %s: %w", file, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("feedback: sync %s: %w", file, err)
	}
	return f.Close()
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
type Read[T any] struct {
	Rows []T
	// Skipped counts lines that were malformed, oversized, of another version
	// or failed validation. A torn last line (a crash mid-append) counts here.
	Skipped int
	// Capped is how many older lines were left out past MaxRead.
	Capped int
}

// ReadImpressions reads the impression log; a missing log is empty.
func ReadImpressions(stateDir string) (Read[Impression], error) {
	return readLog(stateDir, ImpressionsFile, func(i Impression) error { return i.check() }, func(i Impression) int { return i.V })
}

// ReadJudgments reads the judgment log; a missing log is empty.
func ReadJudgments(stateDir string) (Read[Judgment], error) {
	return readLog(stateDir, JudgmentsFile, func(j Judgment) error { return j.check() }, func(j Judgment) int { return j.V })
}

func readLog[T any](stateDir, file string, check func(T) error, version func(T) int) (Read[T], error) {
	var out Read[T]
	if stateDir == "" {
		return out, errors.New("feedback: empty state dir")
	}
	f, err := os.Open(filepath.Join(Dir(stateDir), file))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("feedback: %w", err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			var v T
			if len(line) > MaxLine || json.Unmarshal(line, &v) != nil || version(v) != Version || check(v) != nil {
				out.Skipped++
			} else {
				out.Rows = append(out.Rows, v)
				if len(out.Rows) > MaxRead {
					out.Rows = out.Rows[1:]
					out.Capped++
				}
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("feedback: read %s: %w", file, err)
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
