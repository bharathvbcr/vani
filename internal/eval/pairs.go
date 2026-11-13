package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The silver pair log: a model's answers to "which of these two postings would
// the job seeker rather apply to" (internal/decide/wouldapply), one row per
// asked pair and order. They are silver labels. Nothing in the engine reads
// them (Rule 7), they never enter labels.jsonl, and the only thing that reads
// them is the report that scores them against the owner's later gold labels.
// A row holds no posting text, only hashes of it, and the keys of the two
// postings.

const (
	// PairsFile is the silver log inside the state directory.
	PairsFile = "silver_pairs.jsonl"
	// PairsSchema is the row version.
	PairsSchema = 1
	// MaxPairsPerRun bounds the pairs one run plans and asks.
	MaxPairsPerRun = 500

	pairsLockFile = "silver_pairs.lock"
	maxPairLine   = 16 << 10
)

// ErrPairsBusy means another run holds the silver log.
var ErrPairsBusy = errors.New("eval: another silver pair run is using the pair log")

// PairRow is one stored answer. KeyA and KeyB are the postings in the order the
// model saw them (A first); Swapped is true when that order is the reverse of the
// keys' sort order, so a reader can tell "forward" from "swapped" asks of one
// pair (position bias is real, and a run may ask both orders). Answer is "a",
// "b" or "neither" when Reading is "answered", and empty for an abstention or an
// unusable reply, which are kept so a missing answer is never mistaken for a
// pair nobody asked.
//
// Model, FixtureSHA (wouldapply.Fixture), PromptHash (wouldapply.PromptHash)
// and ExemplarSHA (ExemplarSet.SHA256) say exactly what was asked, of whom.
// RenderSHA256 is the hash of the exact Context bytes sent; TextSHA256A and B
// are HashText of the two descriptions, the hashes a label carries.
type PairRow struct {
	V            int         `json:"v"`
	Task         string      `json:"task"`
	KeyA         posting.Key `json:"key_a"`
	KeyB         posting.Key `json:"key_b"`
	Swapped      bool        `json:"swapped"`
	Stratum      string      `json:"stratum"`
	TextSHA256A  string      `json:"text_sha256_a"`
	TextSHA256B  string      `json:"text_sha256_b"`
	RenderSHA256 string      `json:"render_sha256"`
	Model        string      `json:"model"`
	FixtureSHA   string      `json:"fixture_sha"`
	PromptHash   string      `json:"prompt_hash"`
	ExemplarSHA  string      `json:"exemplar_sha"`
	Reading      string      `json:"reading"`
	Answer       string      `json:"answer,omitempty"`
	Detail       string      `json:"detail,omitempty"`
	At           time.Time   `json:"at"`
}

// PairIdent is what makes a stored answer current: the same model asked the
// same question under the same exemplar set, of the same rendered pair in the
// same order.
type PairIdent struct {
	Model, Fixture, Prompt, Exemplar string
}

func (i PairIdent) valid() bool {
	return i.Model != "" && i.Fixture != "" && i.Prompt != "" && i.Exemplar != ""
}

// PairKeyOf returns the two keys in sort order, the pair's identity whichever
// way round it was shown.
func PairKeyOf(a, b posting.Key) (lo, hi posting.Key) {
	if a > b {
		return b, a
	}
	return a, b
}

func (i PairIdent) key(a, b posting.Key, render string) string {
	lo, hi := PairKeyOf(a, b)
	return strings.Join([]string{string(lo), string(hi), fmt.Sprint(a > b), render, i.Model, i.Fixture, i.Prompt, i.Exemplar}, "\x00")
}

func (r PairRow) ident() PairIdent {
	return PairIdent{Model: r.Model, Fixture: r.FixtureSHA, Prompt: r.PromptHash, Exemplar: r.ExemplarSHA}
}

func (r PairRow) validate() error {
	switch {
	case r.V != PairsSchema:
		return fmt.Errorf("version %d", r.V)
	case r.Task != wouldapply.Task:
		return fmt.Errorf("task %q", r.Task)
	case r.KeyA == "" || r.KeyB == "" || r.KeyA == r.KeyB:
		return errors.New("a pair needs two distinct keys")
	case r.Swapped != (r.KeyA > r.KeyB):
		return errors.New("order does not match the keys")
	case !r.ident().valid() || r.RenderSHA256 == "" || r.TextSHA256A == "" || r.TextSHA256B == "":
		return errors.New("missing identity")
	case r.Reading == "":
		return errors.New("no reading")
	case (r.Reading == string(decide.Answered)) != (r.Answer != ""):
		return errors.New("an answer belongs to an answered reading and no other")
	case r.Answer != "" && !slices.Contains(wouldapply.Options[:], r.Answer):
		return fmt.Errorf("answer %q is not an option", r.Answer)
	}
	return nil
}

// PairStore is the silver log. Rows are appended whole, fsynced; a torn last
// line is repaired with a newline before the next row and skipped on read.
type PairStore struct {
	path string
	mu   sync.Mutex
	seen map[string]struct{}
	rows []PairRow
	// Skipped counts lines that were not valid rows (a torn tail, a hand edit).
	Skipped int
}

// OpenPairStore reads path (a missing file is an empty store).
func OpenPairStore(path string) (*PairStore, error) {
	s := &PairStore{path: path, seen: map[string]struct{}{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("silver: %w", err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, tooLong, err := readBoundedLine(br, maxPairLine)
		if tooLong {
			s.Skipped++
		} else if len(line) > 0 {
			var r PairRow
			if json.Unmarshal(line, &r) != nil || r.validate() != nil {
				s.Skipped++
			} else {
				s.seen[r.ident().key(r.KeyA, r.KeyB, r.RenderSHA256)] = struct{}{}
				s.rows = append(s.rows, r)
			}
		}
		if errors.Is(err, io.EOF) {
			return s, nil
		}
		if err != nil {
			return nil, fmt.Errorf("silver: %w", err)
		}
	}
}

// Len is how many valid rows the log holds.
func (s *PairStore) Len() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.rows) }

// Rows is a copy of every valid row, in file order.
func (s *PairStore) Rows() []PairRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows)
}

// Has reports whether the pair shown as (a, b) with this rendering is already
// answered under id, whatever the answer was.
func (s *PairStore) Has(id PairIdent, a, b posting.Key, render string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id.key(a, b, render)]
	return ok
}

// Append durably adds r. The caller holds the run lock (LockPairs).
func (s *PairStore) Append(r PairRow) error {
	if r.V == 0 {
		r.V = PairsSchema
	}
	if err := r.validate(); err != nil {
		return fmt.Errorf("silver: %w", err)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, created, err := atomicfile.OpenAppend(s.path, os.O_APPEND)
	if err != nil {
		return fmt.Errorf("silver: open pair log: %w", err)
	}
	defer f.Close()
	if created {
		if err := atomicfile.SyncNew(s.path, atomicfile.SyncDir); err != nil {
			return fmt.Errorf("silver: %w", err)
		}
	}
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		return fmt.Errorf("silver: %w", err)
	}
	if torn {
		line = append([]byte{'\n'}, line...)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("silver: write pair row: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("silver: sync pair log: %w", err)
	}
	s.seen[r.ident().key(r.KeyA, r.KeyB, r.RenderSHA256)] = struct{}{}
	s.rows = append(s.rows, r)
	return nil
}

// LockPairs takes the exclusive run lock beside path, so two runs never read the
// same log and ask a pair twice.
func LockPairs(ctx context.Context, path string, wait time.Duration) (unlock func(), err error) {
	return lockBeside(ctx, filepath.Join(filepath.Dir(path), pairsLockFile), "silver", ErrPairsBusy, wait)
}

// PairWork is one question to ask: the pair as shown (A first), its stratum, and
// the question exactly as it will be sent.
type PairWork struct {
	A, B    posting.Posting
	Stratum string
	Q       decide.Question
}

// PairRun is one run's settings.
type PairRun struct {
	// D asks the model; it must be the same Decider whatever the pair.
	D decide.Decider
	// Ident names what is asked (model, question, exemplar set).
	Ident PairIdent
	Store *PairStore
	// Concurrency is the questions in flight (1..MaxTeacherConcurrency).
	Concurrency int
	// Timeout bounds one question; zero is no extra bound beyond D's own.
	Timeout time.Duration
	// LimitNano is the most this run may add to the metered total Spent reads;
	// Worst bounds what one question can cost. A question is sent only when what
	// was spent, plus every question in flight at its worst, plus this one at its
	// worst, fits the limit (the teacher's gate).
	LimitNano int64
	Spent     func(context.Context) (int64, error)
	Worst     func(decide.Question) int64
	// Meter is called after every question that was sent, whatever came back, to
	// record what it cost (the daily cap's spend file). It is required: a run
	// that cannot record its spend does not start, and one whose record cannot
	// be written stops.
	Meter func(context.Context) error
	// Now stamps rows; nil is time.Now.
	Now func() time.Time
}

// PairResult says what a run did.
type PairResult struct {
	Considered, Skipped int // Skipped: already stored under the same identity
	Asked, Stored       int
	Unread              int // asked but no reply came back (provider down); not stored
	Abstained           int // stored: the model said it could not tell
	Unusable            int // stored: the reply was not one of the options
	// Stopped is why the run ended early; empty when every pair was handled.
	Stopped string
	// SpentNano is what the metered total grew by, when it could be read.
	SpentNano int64
}

// Run asks every pair not already stored, keeping each answer as it arrives. It
// stops when ctx ends, when the next question could pass LimitNano, after
// TeacherStopAfter questions in a row got no usable reply, or when the log or the
// spend record cannot be written. What was stored stays stored and a later run
// resumes from it. A stop other than ctx ending issues no further question but
// lets those in flight finish and stores their answers, since they are paid for.
func (r PairRun) Run(ctx context.Context, work []PairWork) (PairResult, error) {
	var res PairResult
	switch {
	case r.D == nil || r.Store == nil || r.Spent == nil || r.Worst == nil || r.Meter == nil:
		return res, errors.New("eval: pair run needs a decider, store, spend meter and spend record")
	case !r.Ident.valid():
		return res, errors.New("eval: pair run needs a model, fixture, prompt and exemplar identity")
	case r.Concurrency < 1 || r.Concurrency > MaxTeacherConcurrency:
		return res, fmt.Errorf("eval: pair concurrency %d outside 1..%d", r.Concurrency, MaxTeacherConcurrency)
	case r.LimitNano <= 0:
		return res, errors.New("eval: pair run needs a positive spend limit")
	case len(work) > MaxPairsPerRun*2:
		return res, fmt.Errorf("eval: %d questions in one run, max %d", len(work), MaxPairsPerRun*2)
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	base, err := r.Spent(ctx)
	if err != nil {
		return res, fmt.Errorf("eval: reading the spend: %w", err)
	}
	st := &teacherState{base: base}

	var todo []PairWork
	seen := map[string]bool{}
	for _, w := range work {
		render := wouldapply.RenderSHA256(w.Q)
		k := r.Ident.key(w.A.Key, w.B.Key, render)
		if w.A.Key == "" || w.A.Key == w.B.Key || seen[k] {
			continue // a pair of one posting is not a pair; a repeat is asked once
		}
		seen[k] = true
		res.Considered++
		if r.Store.Has(r.Ident, w.A.Key, w.B.Key, render) {
			res.Skipped++
			continue
		}
		todo = append(todo, w)
	}

	var (
		mu      sync.Mutex // guards res, streak, fatal and stopped
		streak  int
		fatal   error
		stopped string
		wg      sync.WaitGroup
	)
	halt := make(chan struct{})
	stop := func(why string) { // mu held
		if stopped == "" {
			stopped = why
			close(halt)
		}
	}
	halted := func() bool {
		select {
		case <-halt:
			return true
		default:
			return false
		}
	}
	jobs := make(chan PairWork)
	for range r.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range jobs {
				if ctx.Err() != nil || halted() {
					continue
				}
				worst := r.Worst(w.Q)
				if err := st.admitWithin(ctx, r.Spent, r.LimitNano, worst); err != nil {
					mu.Lock()
					if errors.Is(err, errTeacherLimit) {
						stop(TeacherStoppedAtLimit)
					} else {
						fatal = errors.Join(fatal, err)
						stop("the spend could not be read")
					}
					mu.Unlock()
					continue
				}
				qctx, cancel := ctx, context.CancelFunc(func() {})
				if r.Timeout > 0 {
					qctx, cancel = context.WithTimeout(ctx, r.Timeout)
				}
				ans, err := r.D.Decide(qctx, w.Q)
				cancel()
				st.release(worst)
				mu.Lock()
				merr := r.Meter(context.WithoutCancel(ctx))
				if merr != nil {
					fatal = errors.Join(fatal, fmt.Errorf("recording the spend: %w", merr))
					stop("the spend record could not be written")
				}
				switch {
				case err != nil && ctx.Err() != nil:
					stop("interrupted")
				case err != nil:
					res.Asked++
					res.Unread++
					if streak++; streak >= TeacherStopAfter {
						stop(fmt.Sprintf("the last %d questions got no usable reply (%s)", streak, firstLine(err)))
					}
				default:
					res.Asked++
					row := pairRow(r.Ident, w, wouldapply.Read(ans), now())
					if aerr := r.Store.Append(row); aerr != nil {
						fatal = errors.Join(fatal, aerr)
						stop("the pair log could not be written")
						break
					}
					res.Stored++
					switch decide.Reading(row.Reading) {
					case decide.Answered:
						streak = 0
					case decide.Abstained:
						res.Abstained++
						streak = 0
					default:
						res.Unusable++
						if streak++; streak >= TeacherStopAfter {
							stop(fmt.Sprintf("the last %d questions got no usable reply (%s)", streak, row.Detail))
						}
					}
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, w := range todo {
		select {
		case jobs <- w:
		case <-halt:
			break feed
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if spent, serr := r.Spent(context.WithoutCancel(ctx)); serr == nil {
		res.SpentNano = spent - base
	}
	if fatal != nil {
		res.Stopped = stopped
		return res, fatal
	}
	if stopped == "" && ctx.Err() != nil {
		stopped = "interrupted"
	}
	res.Stopped = stopped
	return res, nil
}

// pairRow records c, the model's reading of w.Q.
func pairRow(id PairIdent, w PairWork, c wouldapply.Choice, at time.Time) PairRow {
	return PairRow{
		V: PairsSchema, Task: wouldapply.Task, KeyA: w.A.Key, KeyB: w.B.Key, Swapped: w.A.Key > w.B.Key, Stratum: w.Stratum,
		TextSHA256A: HashText(w.A.Description), TextSHA256B: HashText(w.B.Description), RenderSHA256: wouldapply.RenderSHA256(w.Q),
		Model: id.Model, FixtureSHA: id.Fixture, PromptHash: id.Prompt, ExemplarSHA: id.Exemplar,
		Reading: string(c.Reading), Answer: c.Value, Detail: c.Detail, At: at.UTC(),
	}
}
