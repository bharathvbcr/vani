package eval

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The second teacher: a larger model reads, with the same triage question, the
// postings the first teacher (the OpenRouter decision model, d1-ultra) read, so
// Lappi's agreement filter can keep the training rows two teachers agree on.
// Its readings are stored beside the ledger as JSONL, one row per posting and
// reading, with enough provenance (text hash, model, fixture hash, prompt hash)
// to say exactly what was asked of which text. They are evidence for training;
// nothing in the engine reads them (Rule 7).

const (
	// TeacherFile is the readings log inside the state directory.
	TeacherFile = "teacher_readings.jsonl"
	// TeacherSchema is the readings line version.
	TeacherSchema = 1

	teacherLockFile = "teacher_readings.lock"
	maxTeacherLine  = 64 << 10
	// MaxTeacherConcurrency bounds the questions in flight at once.
	MaxTeacherConcurrency = 16
	// TeacherStopAfter is how many questions in a row may go unread (the provider
	// unreachable, a cap refusal, a revoked key) before a run stops: the rest
	// would fail the same way.
	TeacherStopAfter = 3
)

// TeacherSlot is one slot's reading, whatever it was: an answer, an abstention
// or an unusable reply are all kept, so a missing value is never mistaken for
// a question nobody asked.
type TeacherSlot struct {
	Reading string `json:"reading"`
	Value   string `json:"value,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// TeacherRow is one stored reading. It holds no posting text: TextSHA256 is
// HashText of the description, the hash a label carries.
type TeacherRow struct {
	V          int                    `json:"v"`
	Key        posting.Key            `json:"key"`
	TextSHA256 string                 `json:"text_sha256"`
	Model      string                 `json:"model"`
	FixtureSHA string                 `json:"fixture_sha"`
	PromptHash string                 `json:"prompt_hash"`
	Slots      map[string]TeacherSlot `json:"slots"`
	At         time.Time              `json:"at"`
}

// TeacherIdent is what makes a stored reading current: the same text asked the
// same way of the same model.
type TeacherIdent struct {
	Text, Model, Fixture, Prompt string
}

// FixtureSHA is the sha256 of the shared triage fixture the question comes from.
func FixtureSHA() string {
	sum := sha256.Sum256(jdtriage.FixtureJSON)
	return hex.EncodeToString(sum[:])
}

// PromptHash fingerprints everything the model is asked apart from the
// posting: the system prompt, the question and its slots with the context
// empty, and the target summary the context will carry.
func PromptHash(summary string) string {
	q := jdtriage.Question(posting.Posting{})
	q.Context = nil
	sys, user := llmbackend.Messages(q)
	h := sha256.New()
	for _, s := range []string{sys, user, summary} {
		fmt.Fprintf(h, "%d:", len(s))
		io.WriteString(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (i TeacherIdent) key(k posting.Key) string {
	return strings.Join([]string{string(k), i.Text, i.Model, i.Fixture, i.Prompt}, "\x00")
}

// TeacherStore is the readings log. Rows are appended whole; a torn last line
// is repaired with a newline before the next row and skipped on read.
type TeacherStore struct {
	path string
	mu   sync.Mutex
	seen map[string]struct{}
	rows int
	// byKey keeps every valid row per posting, in file order, for Current.
	byKey map[posting.Key][]TeacherRow
	// Skipped counts lines that were not valid rows (a torn tail, a hand edit).
	Skipped int
}

// OpenTeacherStore reads path (a missing file is an empty store).
func OpenTeacherStore(path string) (*TeacherStore, error) {
	s := &TeacherStore{path: path, seen: map[string]struct{}{}, byKey: map[posting.Key][]TeacherRow{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("teacher: %w", err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, tooLong, err := readBoundedLine(br, maxTeacherLine)
		if tooLong {
			s.Skipped++
		} else if len(line) > 0 {
			var r TeacherRow
			if json.Unmarshal(line, &r) != nil || r.validate() != nil {
				s.Skipped++
			} else {
				s.seen[TeacherIdent{r.TextSHA256, r.Model, r.FixtureSHA, r.PromptHash}.key(r.Key)] = struct{}{}
				s.byKey[r.Key] = append(s.byKey[r.Key], r)
				s.rows++
			}
		}
		if errors.Is(err, io.EOF) {
			return s, nil
		}
		if err != nil {
			return nil, fmt.Errorf("teacher: %w", err)
		}
	}
}

// readBoundedLine returns the next line; tooLong is true (and the line empty)
// when it exceeds max, the rest of it discarded, so one bad line cannot
// exhaust memory.
func readBoundedLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, false, err
		}
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

func (r TeacherRow) validate() error {
	switch {
	case r.V != TeacherSchema:
		return fmt.Errorf("version %d", r.V)
	case r.Key == "" || r.TextSHA256 == "" || r.Model == "" || r.FixtureSHA == "" || r.PromptHash == "":
		return errors.New("missing identity")
	case len(r.Slots) == 0:
		return errors.New("no slots")
	}
	return nil
}

// Len is how many valid rows the log holds.
func (s *TeacherStore) Len() int { s.mu.Lock(); defer s.mu.Unlock(); return s.rows }

// Has reports whether a reading of k under id is already stored.
func (s *TeacherStore) Has(k posting.Key, id TeacherIdent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id.key(k)]
	return ok
}

// Append durably adds r. The caller holds the run lock (LockTeacher).
func (s *TeacherStore) Append(r TeacherRow) error {
	if r.V == 0 {
		r.V = TeacherSchema
	}
	if err := r.validate(); err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, created, err := atomicfile.OpenAppend(s.path, os.O_APPEND)
	if err != nil {
		return fmt.Errorf("teacher: open readings: %w", err)
	}
	defer f.Close()
	if created {
		if err := atomicfile.SyncNew(s.path, atomicfile.SyncDir); err != nil {
			return fmt.Errorf("teacher: %w", err)
		}
	}
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		return fmt.Errorf("teacher: %w", err)
	}
	if torn {
		line = append([]byte{'\n'}, line...)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("teacher: write reading: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("teacher: sync readings: %w", err)
	}
	s.seen[TeacherIdent{r.TextSHA256, r.Model, r.FixtureSHA, r.PromptHash}.key(r.Key)] = struct{}{}
	s.byKey[r.Key] = append(s.byKey[r.Key], r)
	s.rows++
	return nil
}

// CurrentReadings is the store seen through one identity: the model, the
// shared fixture and the prompt (target summary included) as they are now.
// It implements TeacherReadings.
type CurrentReadings struct {
	s                      *TeacherStore
	model, fixture, prompt string
}

// Current returns the store's readings as the label queue reads them. Only
// rows whose model, fixture_sha and prompt_hash equal the current ones count:
// a reading made under an older prompt answered a different question and must
// not decide what a person labels first. The posting text hash is not part of
// the match (the queue holds the key, and bodies may be evicted), so an edited
// description keeps its reading; it only reorders the queue, never a label.
func (s *TeacherStore) Current(model, summary string) CurrentReadings {
	return CurrentReadings{s: s, model: model, fixture: FixtureSHA(), prompt: PromptHash(summary)}
}

// Reading returns the newest current-identity reading of key: each slot the
// teacher answered, with its value. Abstained, failed and valueless slots are
// left out, so they are never a disagreement. ok is false when none exists.
func (c CurrentReadings) Reading(key string) (map[string]string, bool) {
	if c.s == nil {
		return nil, false
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	rows := c.s.byKey[posting.Key(key)]
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.Model != c.model || r.FixtureSHA != c.fixture || r.PromptHash != c.prompt {
			continue
		}
		out := map[string]string{}
		for name, sl := range r.Slots {
			if sl.Reading == string(decide.Answered) && sl.Value != "" {
				out[name] = sl.Value
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

// ErrTeacherBusy means another run holds the readings log.
var ErrTeacherBusy = errors.New("eval: another teacher run is using the readings log")

// LockTeacher takes the exclusive run lock beside path, waiting at most wait
// (and until ctx ends), so two runs never read the same store and ask twice.
func LockTeacher(ctx context.Context, path string, wait time.Duration) (unlock func(), err error) {
	f, _, err := atomicfile.OpenAppend(filepath.Join(filepath.Dir(path), teacherLockFile), 0)
	if err != nil {
		return nil, fmt.Errorf("teacher: open lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("teacher: lock: %w", err)
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			_ = f.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrTeacherBusy
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TeacherCandidate is a posting the first teacher read.
type TeacherCandidate struct {
	Posting posting.Posting
}

// TeacherSelection says who was chosen and who was left out and why.
type TeacherSelection struct {
	Candidates []TeacherCandidate
	// NoText counts postings the first teacher read whose description is empty
	// (so there is nothing to ask about); Inherited counts copies of another
	// posting's reading, which are not a second observation.
	NoText, Inherited int
}

// TeacherCandidates picks, from recs, the postings whose triage a backend
// named first read in any slot: every status, so a posting the engine dropped
// on that reading is still asked about (its text is in the record, or read
// back through full when the body was evicted). A copied reading is left out,
// and a key that appears twice is asked once. Order is by key.
func TeacherCandidates(recs []ledger.Record, n Names, first string, full func(ledger.Record) ledger.Record) TeacherSelection {
	var sel TeacherSelection
	seen := map[posting.Key]bool{}
	for _, r := range recs {
		if seen[r.Key] || !readBy(r, n, first) {
			continue
		}
		seen[r.Key] = true
		if r.Annotations[ledger.AnnTriageInheritedFrom] != "" {
			sel.Inherited++
			continue
		}
		if full != nil {
			r = full(r)
		}
		if strings.TrimSpace(r.Posting.Description) == "" {
			sel.NoText++
			continue
		}
		p := r.Posting
		p.Key = r.Key
		sel.Candidates = append(sel.Candidates, TeacherCandidate{Posting: p})
	}
	sort.Slice(sel.Candidates, func(i, j int) bool { return sel.Candidates[i].Posting.Key < sel.Candidates[j].Posting.Key })
	return sel
}

// readBy reports whether any triage slot of r records first as its primary
// backend (a legacy name mapped as Names says).
func readBy(r ledger.Record, n Names, first string) bool {
	for k, v := range r.Annotations {
		if !strings.HasPrefix(k, ledger.AnnTriagePrefix) || !strings.HasSuffix(k, ledger.AnnTriageSuffixBackend) ||
			strings.HasSuffix(k, ledger.AnnTriageSuffixShadowBackend) {
			continue
		}
		if name, _ := n.primary(strings.TrimSpace(v)); name == first {
			return true
		}
	}
	return false
}

// TeacherRun is one run's settings.
type TeacherRun struct {
	// D asks the model; it must be the same Decider whatever the posting.
	D decide.Decider
	// Model, Summary and Store identify what is asked and where it is kept.
	Model   string
	Summary string // normalized target summary (jdtriage.NormalizeSummary)
	Store   *TeacherStore
	// Concurrency is the questions in flight (1..MaxTeacherConcurrency).
	Concurrency int
	// Timeout bounds one question; zero is no extra bound beyond D's own.
	Timeout time.Duration
	// LimitNano is the most this run may add to Spent, in nano-USD; Spent reads
	// the metered total now (the daily budget ledger's committed amount), and
	// Worst bounds what one question for q can cost. A question is sent only
	// when what was spent, plus every question in flight at its worst, plus
	// this one at its worst, fits the limit.
	LimitNano int64
	Spent     func(context.Context) (int64, error)
	Worst     func(decide.Question) int64
	// Now stamps rows; nil is time.Now.
	Now func() time.Time
}

// TeacherResult says what a run did.
type TeacherResult struct {
	Considered, Skipped int // Skipped: already stored under the same identity
	Asked, Stored       int
	Unread              int // asked but no reading came back (not stored)
	Unusable            int // stored with at least one slot that was not an answer or an abstention
	// Stopped is why the run ended early; empty when every posting was handled.
	Stopped string
	// SpentNano is what the metered total grew by, when it could be read.
	SpentNano int64
}

// TeacherStoppedAtLimit is TeacherResult.Stopped when the spend limit, not a
// failure, ended the run; the next run resumes from what was stored.
const TeacherStoppedAtLimit = "the next question could pass the spend limit"

var errTeacherLimit = errors.New("eval: the run's spend limit would be passed")

// gate wraps the Decider so the spend check and the capture of the question
// happen on the exact question the Triager built.
type teacherGate struct {
	run *TeacherRun
	st  *teacherState
	q   *decide.Question
}

func (g teacherGate) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	*g.q = q
	worst := g.run.Worst(q)
	if err := g.st.admit(ctx, g.run, worst); err != nil {
		return decide.Answer{}, err
	}
	defer g.st.release(worst)
	if g.run.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.run.Timeout)
		defer cancel()
	}
	return g.run.D.Decide(ctx, q)
}

type teacherState struct {
	mu       sync.Mutex
	base     int64
	inflight int64 // sum of the worst cases of questions in flight
}

func (s *teacherState) admit(ctx context.Context, r *TeacherRun, worst int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	spent, err := r.Spent(ctx)
	if err != nil {
		return fmt.Errorf("eval: reading the spend: %w", err)
	}
	if spent-s.base+s.inflight+worst > r.LimitNano {
		return errTeacherLimit
	}
	s.inflight += worst
	return nil
}

func (s *teacherState) release(worst int64) {
	s.mu.Lock()
	s.inflight -= worst
	s.mu.Unlock()
}

// Run asks about each candidate whose reading is not already stored, keeping
// each reading as it arrives. It stops when ctx ends, when the next question
// could pass LimitNano, or after TeacherStopAfter questions in a row went
// unread; what was stored stays stored, and a later run resumes from it. A stop
// other than ctx ending issues no further question but lets those in flight
// finish and stores their answers, since they are already paid for.
func (r TeacherRun) Run(ctx context.Context, cands []TeacherCandidate) (TeacherResult, error) {
	var res TeacherResult
	if r.D == nil || r.Store == nil || r.Spent == nil || r.Worst == nil || r.Model == "" {
		return res, errors.New("eval: teacher run needs a decider, store, model and spend meter")
	}
	if r.Concurrency < 1 || r.Concurrency > MaxTeacherConcurrency {
		return res, fmt.Errorf("eval: teacher concurrency %d outside 1..%d", r.Concurrency, MaxTeacherConcurrency)
	}
	if r.LimitNano <= 0 {
		return res, errors.New("eval: teacher run needs a positive spend limit")
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
	fixture, prompt := FixtureSHA(), PromptHash(r.Summary)

	var todo []TeacherCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if seen[c.Posting.Key] {
			continue
		}
		seen[c.Posting.Key] = true
		res.Considered++
		id := TeacherIdent{Text: HashText(c.Posting.Description), Model: r.Model, Fixture: fixture, Prompt: prompt}
		if r.Store.Has(c.Posting.Key, id) {
			res.Skipped++
			continue
		}
		todo = append(todo, c)
	}

	// A stop (the limit, a run of unread questions, an unwritable log) ends the
	// ISSUING of questions, never the questions already in flight: those are paid
	// for whether or not their answer is kept, so each is allowed to finish
	// (bounded by Timeout, D's own bounds and ctx) and is stored. Only ctx, the
	// caller's interrupt, cancels a question in flight.
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
	jobs := make(chan TeacherCandidate)
	for range r.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil || halted() {
					continue
				}
				var q decide.Question
				tr := jdtriage.Triager{D: teacherGate{run: &r, st: st, q: &q}, TargetSummary: r.Summary}
				ans, err := tr.Triage(ctx, c.Posting)
				mu.Lock()
				switch {
				case errors.Is(err, errTeacherLimit):
					stop(TeacherStoppedAtLimit)
				case err != nil && ctx.Err() != nil:
					stop("interrupted")
				case err != nil:
					res.Asked++
					res.Unread++
					if streak++; streak >= TeacherStopAfter {
						stop(fmt.Sprintf("the last %d questions got no reading (%s)", streak, firstLine(err)))
					}
				default:
					res.Asked++
					streak = 0
					row := teacherRow(c.Posting, r.Model, fixture, prompt, q, ans, now())
					if aerr := r.Store.Append(row); aerr != nil {
						fatal = errors.Join(fatal, aerr)
						stop("the readings log could not be written")
					} else {
						res.Stored++
						for _, s := range row.Slots {
							if s.Reading != string(decide.Answered) && s.Reading != string(decide.Abstained) {
								res.Unusable++
								break
							}
						}
					}
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, c := range todo {
		select {
		case jobs <- c:
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

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// teacherRow records a's reading of every slot q asked. A slot the answer does
// not mention is recorded as not read, never left out.
func teacherRow(p posting.Posting, model, fixture, prompt string, q decide.Question, a decide.Answer, at time.Time) TeacherRow {
	slots := make(map[string]TeacherSlot, len(q.Slots))
	for _, s := range q.Slots {
		rd, ok := a.Slots[s.Name]
		if !ok {
			slots[s.Name] = TeacherSlot{Reading: string(decide.Failed), Detail: "missing_slot"}
			continue
		}
		slots[s.Name] = TeacherSlot{Reading: string(rd.Reading), Value: rd.Value, Detail: rd.Detail}
	}
	return TeacherRow{
		V: TeacherSchema, Key: p.Key, TextSHA256: HashText(p.Description), Model: model,
		FixtureSHA: fixture, PromptHash: prompt, Slots: slots, At: at.UTC(),
	}
}
