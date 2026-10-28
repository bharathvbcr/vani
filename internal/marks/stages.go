package marks

// Stages: what happened to an application after the owner applied.
//
// Where they live. An applied posting stays Applied in marks.jsonl whatever
// its stage, so every reader of the marks (the ranking's labels, the digest,
// the Jobs views, the app, the extension) sees exactly what it saw before
// stages existed, and an installed older binary keeps working: it refuses a
// marks.jsonl line it does not know (compat_test.go), so a new mark value, line
// version or field there would lock it out of the whole store. The stage is
// kept in stages.jsonl beside it, which an older binary never opens, under the
// same lock. A stage change appends one line there and touches nothing else.
//
// What is derived. A stage line names the applied mark it belongs to (its
// applied_at is that mark's time), so a posting that is un-applied and applied
// again starts a new application with no stage and nothing carried over. The
// date first applied is the applied mark's time (Entry.MarkedAt): no stage line
// ever moves it. The furthest stage reached is the highest of screening,
// interview and offer any line of that application named; rejected and withdrawn
// never lower it. The current stage is the last line's. Compaction keeps a line
// for the furthest stage and a line for the current one, which fold to the same
// state, so the history can shrink without the furthest stage being forgotten.
//
// An older binary that writes marks.jsonl cannot see stages. If it applies a
// posting again, the applied time moves, and the stage line no longer names the
// applied mark: the posting reads as Applied with no stage. The line stays in
// stages.jsonl until compaction drops it; nothing in marks.jsonl is ever misread.

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Stage is where an applied posting stands. StageNone is applied and nothing
// since. Only an Applied posting has a stage.
type Stage string

const (
	StageNone      Stage = ""
	StageScreening Stage = "screening"
	StageInterview Stage = "interview"
	StageOffer     Stage = "offer"
	// StageRejected and StageWithdrawn end the application. They are
	// alternatives, and neither lowers the furthest stage reached.
	StageRejected  Stage = "rejected"
	StageWithdrawn Stage = "withdrawn"
)

// Stages are every stage but StageNone, in the order a search moves through
// them (the two that end it last).
var Stages = []Stage{StageScreening, StageInterview, StageOffer, StageRejected, StageWithdrawn}

// Valid reports whether s is a declared stage (StageNone clears).
func (s Stage) Valid() bool { return s == StageNone || slices.Contains(Stages, s) }

// Terminal reports whether s ends the application.
func (s Stage) Terminal() bool { return s == StageRejected || s == StageWithdrawn }

// rank orders the stages that move an application forward; the two that end
// it, and StageNone, have none.
func (s Stage) rank() int {
	switch s {
	case StageScreening:
		return 1
	case StageInterview:
		return 2
	case StageOffer:
		return 3
	}
	return 0
}

// CheckTransition is the one place that says what may follow what. Any stage
// may be set from any other, forward, back (undoing a click) or to StageNone,
// except that an ended application does not end a second way: rejected and
// withdrawn are not swapped, the stage is set back first. Setting the stage a
// posting already has is allowed and changes nothing. Whether the posting is
// Applied is checked by SetStage.
func CheckTransition(cur, next Stage) error {
	switch {
	case !cur.Valid() || !next.Valid():
		return fmt.Errorf("%w: stage %q", ErrInvalid, truncate(string(next), 40))
	case cur.Terminal() && next.Terminal() && cur != next:
		return fmt.Errorf("%w: an application that is %s cannot become %s; set the stage back first", ErrInvalid, cur, next)
	}
	return nil
}

const (
	// stageVersion is the only stages.jsonl line version this package reads or
	// writes.
	stageVersion = 1
	stagesName   = "stages.jsonl"
	// MaxStageFileBytes bounds stages.jsonl. A write that would pass it
	// compacts first, and is refused (ErrFull) only if the live stages alone
	// do not fit.
	MaxStageFileBytes = 1 << 20
	// maxStageLineBytes bounds one line: a full-size key of six-byte escapes
	// plus two timestamps and the rest is under 3.5 KiB (a test pins it).
	maxStageLineBytes = 4 << 10
)

// stageEvent is one stages.jsonl line. Stage is always written, "" being the
// stage set back to applied.
type stageEvent struct {
	V         int         `json:"v"`
	At        time.Time   `json:"at"`
	Key       posting.Key `json:"key"`
	AppliedAt time.Time   `json:"applied_at"`
	Stage     Stage       `json:"stage"`
}

func validateStage(e stageEvent) error {
	switch {
	case e.V != stageVersion:
		return fmt.Errorf("%w: stage line version %d", ErrInvalid, e.V)
	case e.At.IsZero() || e.AppliedAt.IsZero():
		return fmt.Errorf("%w: zero time", ErrInvalid)
	case !e.Stage.Valid():
		return fmt.Errorf("%w: stage %q", ErrInvalid, truncate(string(e.Stage), 40))
	}
	return CheckKey(e.Key)
}

// application is the folded state of one application: one posting applied at
// one time.
type application struct {
	stage      Stage
	at         time.Time
	furthest   Stage
	furthestAt time.Time
}

type appKey struct {
	key     posting.Key
	applied string // the applied mark's time, as RFC 3339 with nanoseconds, UTC
}

func appKeyOf(k posting.Key, applied time.Time) appKey {
	return appKey{k, applied.UTC().Format(time.RFC3339Nano)}
}

// stageLog is what replayStages read.
type stageLog struct {
	apps  map[appKey]*application
	good  int64
	lines int
	torn  bool
}

func (l stageLog) foldEvent(e stageEvent) {
	a := l.apps[appKeyOf(e.Key, e.AppliedAt)]
	if a == nil {
		a = &application{}
		l.apps[appKeyOf(e.Key, e.AppliedAt)] = a
	}
	a.stage, a.at = e.Stage, e.At
	if e.Stage.rank() > a.furthest.rank() {
		a.furthest, a.furthestAt = e.Stage, e.At
	}
}

// join adds the stage fields to every Applied entry of m that has an
// application in the log.
func (l stageLog) join(m map[posting.Key]Entry) {
	for k, e := range m {
		if e.Mark != Applied {
			continue
		}
		if a := l.apps[appKeyOf(k, e.MarkedAt)]; a != nil {
			e.Stage, e.StageAt, e.Furthest = a.stage, a.at, a.furthest
			m[k] = e
		}
	}
}

// stagesOf is e with the stage fields of its application, when it is Applied.
func (l stageLog) stagesOf(e Entry) Entry {
	if e.Mark == Applied {
		if a := l.apps[appKeyOf(e.Key, e.MarkedAt)]; a != nil {
			e.Stage, e.StageAt, e.Furthest = a.stage, a.at, a.furthest
		}
	}
	return e
}

// replayStages folds src with the rules replay has: a torn last line is
// skipped and reported, any other bad line is ErrCorrupt.
func replayStages(src io.Reader) (stageLog, error) {
	out := stageLog{apps: map[appKey]*application{}}
	r := bufio.NewReaderSize(src, 2*maxStageLineBytes)
	for n := 1; ; n++ {
		line, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull) || len(line) > maxStageLineBytes:
			return stageLog{}, fmt.Errorf("%w: %s line %d is over %d bytes", ErrCorrupt, stagesName, n, maxStageLineBytes)
		case errors.Is(err, io.EOF):
			out.torn = len(line) > 0
			return out, nil
		case err != nil:
			return stageLog{}, fmt.Errorf("marks: read %s: %w", stagesName, err)
		}
		var e stageEvent
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		perr := dec.Decode(&e)
		if perr == nil {
			perr = validateStage(e)
		}
		if perr != nil {
			if _, pe := r.Peek(1); errors.Is(pe, io.EOF) && !json.Valid(line) {
				out.torn = true
				return out, nil
			}
			return stageLog{}, fmt.Errorf("%w: %s line %d: %v", ErrCorrupt, stagesName, n, perr)
		}
		out.foldEvent(e)
		out.good += int64(len(line))
		out.lines++
	}
}

// readStageLog reads dir's stages.jsonl; one that was never written is empty.
// The caller holds the store's lock.
func readStageLog(dir string) (stageLog, error) {
	f, err := atomicfile.OpenRead(filepath.Join(dir, stagesName))
	if errors.Is(err, os.ErrNotExist) {
		return stageLog{apps: map[appKey]*application{}}, nil
	}
	if err != nil {
		return stageLog{}, fmt.Errorf("marks: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return stageLog{}, fmt.Errorf("marks: stat: %w", err)
	} else if st.Size() > MaxStageFileBytes {
		return stageLog{}, fmt.Errorf("%w: %s is %d bytes, over %d", ErrCorrupt, stagesName, st.Size(), MaxStageFileBytes)
	}
	return replayStages(f)
}

// SetStage records stage for k at now (StageNone sets it back to applied) and
// returns the entry. k must be Applied. A stage equal to the current one
// changes nothing and writes nothing. Errors are ErrInvalid for a stage that
// does not apply (not Applied, an unknown stage, CheckTransition), and the
// store's own for one that cannot be read or written.
func SetStage(dir string, k posting.Key, stage Stage, now time.Time) (Entry, error) {
	if err := CheckKey(k); err != nil {
		return Entry{}, err
	}
	if now.IsZero() {
		return Entry{}, fmt.Errorf("%w: zero time", ErrInvalid)
	}
	if !stage.Valid() {
		return Entry{}, fmt.Errorf("%w: stage %q", ErrInvalid, truncate(string(stage), 40))
	}
	l, err := lock(dir, true, false)
	if err != nil {
		return Entry{}, err
	}
	if l == nil {
		return Entry{}, notApplied(k, None)
	}
	defer l.Close()
	m, err := loadMarks(dir)
	if err != nil {
		return Entry{}, err
	}
	if m[k].Mark != Applied {
		return Entry{}, notApplied(k, m[k].Mark)
	}
	log, err := readStageLog(dir)
	if err != nil {
		return Entry{}, err
	}
	cur := log.stagesOf(m[k])
	if err := CheckTransition(cur.Stage, stage); err != nil {
		return Entry{}, err
	}
	if stage == cur.Stage {
		return cur, nil
	}
	e := stageEvent{V: stageVersion, At: now.UTC(), Key: k, AppliedAt: cur.MarkedAt, Stage: stage}
	if err := validateStage(e); err != nil {
		return Entry{}, err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("marks: marshal: %w", err)
	}
	line = append(line, '\n')
	if len(line) > maxStageLineBytes {
		return Entry{}, fmt.Errorf("%w: stage line is over %d bytes", ErrInvalid, maxStageLineBytes)
	}
	if err := appendStage(dir, line, m); err != nil {
		return Entry{}, err
	}
	log.foldEvent(e)
	return log.stagesOf(m[k]), nil
}

func notApplied(k posting.Key, mark Mark) error {
	return fmt.Errorf("%w: %s is %s, and only an applied posting has a stage", ErrInvalid, truncate(string(k), 80), orNone(mark))
}

func orNone(m Mark) string {
	if m == None {
		return "not marked"
	}
	return string(m)
}

// appendStage appends line to stages.jsonl under the held lock, with marks'
// rules: a torn last line is cut first, a store that does not read cleanly is
// not written to, and a failed write or fsync is cut back. When the line would
// pass MaxStageFileBytes the file is compacted first, dropping the lines of
// applications m no longer holds.
func appendStage(dir string, line []byte, m map[posting.Key]Entry) error {
	path := filepath.Join(dir, stagesName)
	f, created, err := atomicfile.OpenAppend(path, 0)
	if err != nil {
		return fmt.Errorf("marks: open: %w", err)
	}
	defer func() { f.Close() }()
	if created {
		if err := atomicfile.SyncNew(path, syncDir); err != nil {
			return fmt.Errorf("marks: %w", err)
		}
	}
	if st, err := f.Stat(); err != nil {
		return fmt.Errorf("marks: stat: %w", err)
	} else if st.Size() > MaxStageFileBytes {
		return fmt.Errorf("%w: %s is %d bytes, over %d", ErrCorrupt, stagesName, st.Size(), MaxStageFileBytes)
	}
	log, err := replayStages(f)
	if err != nil {
		return err
	}
	good := log.good
	if good+int64(len(line)) > MaxStageFileBytes {
		if err := f.Close(); err != nil {
			return fmt.Errorf("marks: close: %w", err)
		}
		if err := writeStagesCompacted(path, log, m); err != nil {
			return err
		}
		afterCompact(path)
		if f, err = reopenCompacted(path); err != nil {
			return fmt.Errorf("marks: reopen after compaction: %w", err)
		}
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("marks: stat: %w", err)
		}
		good = st.Size()
	} else if log.torn {
		if err := f.Truncate(good); err != nil {
			return fmt.Errorf("marks: truncate torn tail: %w", err)
		}
	}
	if good+int64(len(line)) > MaxStageFileBytes {
		return ErrFull
	}
	if _, err := f.WriteAt(line, good); err != nil {
		return cutBack(f, good, fmt.Errorf("marks: write: %w", err))
	}
	if err := f.Sync(); err != nil {
		return cutBack(f, good, fmt.Errorf("marks: fsync: %w", err))
	}
	return nil
}

// writeStagesCompacted replaces path with the lines that fold to log's
// applications that m still holds as Applied, at the same time: for each, a
// line for the furthest stage and, when it differs, one for the current stage.
// Applications of postings since cleared, or applied again, are dropped.
func writeStagesCompacted(path string, log stageLog, m map[posting.Key]Entry) error {
	type kept struct {
		key posting.Key
		app *application
		at  time.Time
	}
	var live []kept
	for ak, a := range log.apps {
		e, ok := m[ak.key]
		if !ok || e.Mark != Applied || appKeyOf(ak.key, e.MarkedAt) != ak {
			continue
		}
		live = append(live, kept{ak.key, a, e.MarkedAt})
	}
	slices.SortFunc(live, func(a, b kept) int {
		if a.key != b.key {
			return cmp.Compare(a.key, b.key)
		}
		return a.at.Compare(b.at)
	})
	var buf bytes.Buffer
	put := func(k kept, s Stage, at time.Time) error {
		line, err := json.Marshal(stageEvent{V: stageVersion, At: at, Key: k.key, AppliedAt: k.at, Stage: s})
		if err != nil {
			return fmt.Errorf("marks: compact: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		return nil
	}
	for _, k := range live {
		a := k.app
		if a.furthest != StageNone {
			if err := put(k, a.furthest, a.furthestAt); err != nil {
				return err
			}
		}
		if a.stage != a.furthest || !a.at.Equal(a.furthestAt) {
			if err := put(k, a.stage, a.at); err != nil {
				return err
			}
		}
	}
	if err := atomicfile.Write(path, buf.Bytes()); err != nil {
		return fmt.Errorf("marks: compact: %w", err)
	}
	return nil
}
