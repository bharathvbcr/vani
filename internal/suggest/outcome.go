package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// The outcome log answers "how often does a draft work, and why not?" without
// recording anything about the question or the answer. One line per run of
// `vani suggest` or `vani draft`, under <state dir>/suggest/outcomes.jsonl.
//
// It holds counts, enumerated codes and durations only. It never holds a field
// id, a label, a question, a draft, a posting, a company, a title or an error
// message: Outcome has no field that could carry one, and every string it does
// have is cut to a short token of [a-z0-9_.:/-] (clean), so even a bug that
// routed text into Code or Backend would not write it.
const (
	OutcomeDir  = "suggest"
	OutcomeFile = "outcomes.jsonl"
	// OutcomeRotated is the one earlier generation kept when the live file is full.
	OutcomeRotated = "outcomes.jsonl.1"
	// MaxOutcomeBytes bounds the live file; the next line past it rotates.
	MaxOutcomeBytes = 1 << 20
	// MaxOutcomeLine bounds one line (60 fields at their longest).
	MaxOutcomeLine = 32 << 10
	// maxTokenBytes bounds every string in a record.
	maxTokenBytes = 48
)

// Operations an Outcome records.
const (
	OpSuggest = "suggest"
	OpDraft   = "draft"
)

// FieldOutcome is one field's result: no id, no label, no text.
type FieldOutcome struct {
	Category string `json:"category,omitempty"`
	Status   string `json:"status"`
	Code     string `json:"code,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// AnswerKind is what the field was drafted as (essay, fact, choice), or sent to
	// the batched turn as when that did not come back drafted.
	AnswerKind string `json:"answer_kind,omitempty"`
	Backend    string `json:"backend,omitempty"` // the decision backend that triaged it
	Model      string `json:"model,omitempty"`   // the model that drafted it
	// Turns is the provider turns the draft took (2 when a repair turn ran).
	Turns    int   `json:"turns,omitempty"`
	Repaired bool  `json:"repaired,omitempty"`
	DraftMS  int64 `json:"draft_ms,omitempty"`
}

// Outcome is one run.
type Outcome struct {
	V      int            `json:"v"`
	Time   string         `json:"time"` // RFC 3339, UTC
	Op     string         `json:"op"`
	Code   string         `json:"code,omitempty"` // the whole run failed with this code
	Fields []FieldOutcome `json:"fields,omitempty"`
	// Attempts is how many drafts were asked for; Drafted how many came back;
	// Cap the page's draft cap (suggest only).
	Attempts int `json:"attempts,omitempty"`
	Drafted  int `json:"drafted,omitempty"`
	Cap      int `json:"cap,omitempty"`
	// The page's one batched turn for fact and choice fields: how many fields
	// were asked, how many came back answered, the provider turns it took (0 when
	// no turn was spent) and how long it ran.
	AnswerAsked    int   `json:"answer_asked,omitempty"`
	AnswerAnswered int   `json:"answer_answered,omitempty"`
	AnswerTurns    int   `json:"answer_turns,omitempty"`
	AnswerMS       int64 `json:"answer_ms,omitempty"`
	TriageMS       int64 `json:"triage_ms,omitempty"`
	DraftMS        int64 `json:"draft_ms,omitempty"`
	TotalMS        int64 `json:"total_ms"`
}

// clean returns s when it is a short token of [a-z0-9_.:/-] and "invalid"
// otherwise, so a record can only ever hold an identifier.
func clean(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > maxTokenBytes {
		return "invalid"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' && r != ':' && r != '/' {
			return "invalid"
		}
	}
	return s
}

func (f FieldOutcome) cleaned() FieldOutcome {
	f.Category, f.Status, f.Code, f.Kind = clean(f.Category), clean(f.Status), clean(f.Code), clean(f.Kind)
	f.Backend, f.Model, f.AnswerKind = clean(f.Backend), clean(f.Model), clean(f.AnswerKind)
	f.Turns, f.DraftMS = max(f.Turns, 0), max(f.DraftMS, 0)
	return f
}

// outcomeOf is the record for one suggest run: what each Suggestion came to.
func outcomeOf(op string, start time.Time, out []Suggestion, attempts, drafted, limit int, triage time.Duration, batch batchCounts) Outcome {
	o := Outcome{V: 1, Time: start.UTC().Format(time.RFC3339), Op: op, Attempts: attempts, Drafted: drafted, Cap: limit,
		AnswerAsked: batch.asked, AnswerAnswered: batch.answered, AnswerTurns: batch.turns, AnswerMS: batch.ms,
		TriageMS: triage.Milliseconds(), TotalMS: time.Since(start).Milliseconds()}
	for _, s := range out {
		kind := s.AnswerKind
		if kind == "" {
			kind = s.Batch
		}
		o.Fields = append(o.Fields, FieldOutcome{Category: s.Category, Status: s.Status, Code: s.Code, Kind: s.Kind, AnswerKind: kind, Backend: s.Backend,
			Model: s.Model, Turns: s.Turns, Repaired: s.Repaired, DraftMS: s.DraftMS})
		o.DraftMS += s.DraftMS
	}
	return o
}

// FailureOutcome is the record for a run that failed as a whole with code.
func FailureOutcome(op string, start time.Time, code string) Outcome {
	return Outcome{V: 1, Time: start.UTC().Format(time.RFC3339), Op: op, Code: code, TotalMS: time.Since(start).Milliseconds()}
}

// DraftOutcome is the record for one `vani draft`: res and err are
// draft.Drafter.Draft's, code the reply code. A question refused as never
// drafted is "manual", as in suggest.
func DraftOutcome(start time.Time, res draft.Result, err error, code string) Outcome {
	o := Outcome{V: 1, Time: start.UTC().Format(time.RFC3339), Op: OpDraft, TotalMS: time.Since(start).Milliseconds()}
	f := FieldOutcome{Status: Drafted, AnswerKind: AnswerEssay, Model: res.Model, Turns: res.Turns, Repaired: res.Repaired, DraftMS: o.TotalMS}
	if err != nil {
		f.Status, f.Code, f.Kind, f.Category, f.Turns = Failed, code, draft.KindOf(err), draft.CategoryOf(err), draft.TurnsOf(err)
		if code == draft.CodeNotDrafted {
			f.Status = Manual
		} else {
			o.Attempts = 1
		}
	} else {
		o.Attempts, o.Drafted = 1, 1
	}
	o.Fields = []FieldOutcome{f}
	o.DraftMS = f.DraftMS
	return o
}

// line is o as one newline-terminated JSON line, every string cleaned.
func (o Outcome) line() ([]byte, error) {
	o.Op, o.Code = clean(o.Op), clean(o.Code)
	if len(o.Fields) > MaxFields {
		return nil, fmt.Errorf("suggest: outcome has %d fields, max %d", len(o.Fields), MaxFields)
	}
	fs := make([]FieldOutcome, len(o.Fields))
	for i, f := range o.Fields {
		fs[i] = f.cleaned()
	}
	o.Fields = fs
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxOutcomeLine {
		return nil, fmt.Errorf("suggest: outcome line is %d bytes, max %d", len(b)+1, MaxOutcomeLine)
	}
	return append(b, '\n'), nil
}

// outcomeLockWait bounds how long an append waits for another process's append
// or rotation: past it the record is skipped, never the command held up.
var outcomeLockWait = 2 * time.Second

// AppendOutcome appends o to <stateDir>/suggest/outcomes.jsonl. The file is
// bounded: a line that would pass MaxOutcomeBytes first moves the live file to
// outcomes.jsonl.1 (replacing the older one), so at most two generations exist.
// The size check, the rotation and the write are one exclusive step under
// outcomes.jsonl.lock (filelock; the lock is per open file, so it holds between
// goroutines as well as between processes): two commands that both see a full
// log cannot both rotate it and lose the generation the first one kept. Waiting
// for the lock is bounded by outcomeLockWait; the error then wraps
// filelock.ErrWouldBlock. A symlinked or non-regular leaf is refused, and a log
// that ends mid-line (a crash during an append) gets a newline before the next
// line so the two are not glued. An error means nothing was recorded; callers
// warn and go on.
func AppendOutcome(stateDir string, o Outcome) error {
	if stateDir == "" {
		return errors.New("suggest: no state directory to record the outcome in")
	}
	line, err := o.line()
	if err != nil {
		return err
	}
	dir := filepath.Join(stateDir, OutcomeDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("suggest: outcome log: %w", err)
	}
	live := filepath.Join(dir, OutcomeFile)

	ctx, cancel := context.WithTimeout(context.Background(), outcomeLockWait)
	defer cancel()
	lock, err := filelock.Acquire(ctx, live+".lock")
	if err != nil {
		return fmt.Errorf("suggest: outcome log lock: %w", err)
	}
	defer func() { _ = lock.Release() }()
	f, created, err := openOutcomeLog(live)
	if err != nil {
		return err
	}
	if !created {
		st, serr := f.Stat()
		if serr != nil {
			_ = f.Close()
			return fmt.Errorf("suggest: outcome log: %w", serr)
		}
		if st.Size()+int64(len(line)) > MaxOutcomeBytes {
			lst, lerr := os.Lstat(live)
			if lerr != nil || !os.SameFile(st, lst) {
				_ = f.Close()
				return errors.New("suggest: outcome log was replaced before rotation")
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("suggest: outcome log: %w", err)
			}
			if err := os.Rename(live, filepath.Join(dir, OutcomeRotated)); err != nil {
				return fmt.Errorf("suggest: rotate outcome log: %w", err)
			}
			if f, _, err = openOutcomeLog(live); err != nil {
				return err
			}
		}
	}
	defer func() { _ = f.Close() }()
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		return fmt.Errorf("suggest: outcome log: %w", err)
	}
	if torn {
		line = append([]byte{'\n'}, line...)
	}
	if n, err := f.Write(line); err != nil || n != len(line) {
		if err == nil {
			err = errors.New("short write")
		}
		return fmt.Errorf("suggest: outcome log: %w", err)
	}
	return nil
}

func openOutcomeLog(live string) (*os.File, bool, error) {
	f, created, err := atomicfile.OpenAppend(live, os.O_APPEND)
	if err != nil {
		return nil, false, fmt.Errorf("suggest: outcome log: %w", err)
	}
	if created {
		if err := atomicfile.SyncNew(live, atomicfile.SyncDir); err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("suggest: outcome log: %w", err)
		}
	}
	return f, created, nil
}
