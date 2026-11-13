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
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
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
//
// Task and DigestSHA are empty on a jd_triage row (every row written before
// the teacher asked anything else is one, and jd_triage rows are still written
// without them, byte for byte as before). A kb_match row carries Task
// "career.kb_match" and DigestSHA, the knowledge-base digest's sha256
// (kbmatch.Digest.SHA256) the question carried; its FixtureSHA is the hash of
// the question's definition (kbmatch.Prompt and its ordered options) and its
// PromptHash the hash of the exact messages with the posting left out, the
// target summary included (TeacherTask).
//
// TitleSHA256 and LocationSHA256 are HashText of the raw title and location
// the question rendered beside the description, so a reader can tell whether
// two readings saw the same rendered posting. Rows written before 2026-11-11
// have neither. They are provenance only: not part of TeacherIdent, so a stored
// reading stays current when they are absent or differ.
//
// RenderSHA256 is posting.RawSHA256 of the exact Context bytes of the question
// the model was sent, as the gate captured them (TeacherRun, teacherGate). For
// jd_triage that is jdtriage.Render's output: the fixture layout with the
// target block (from the normalized target summary), the title, the location
// and the description substituted, then cut to jdtriage.MaxContextBytes at a
// rune boundary. The hash is taken after that cut, over the bytes as sent, so
// two descriptions differing only past the cap hash alike, and a title or
// location change (which TextSHA256 cannot see) moves it. Because the target
// block is part of the rendered bytes, a changed target summary moves it too;
// PromptHash already records that. For kb_match it is the same: the Context
// bytes that task's question carried. Rows written before 2026-11-12 have
// none. Like TitleSHA256 it is provenance only, not part of TeacherIdent: a
// stored reading's currency is unchanged by it, so adding it re-asks nothing.
type TeacherRow struct {
	V              int                    `json:"v"`
	Key            posting.Key            `json:"key"`
	Task           string                 `json:"task,omitempty"`
	TextSHA256     string                 `json:"text_sha256"`
	TitleSHA256    string                 `json:"title_sha256,omitempty"`
	LocationSHA256 string                 `json:"location_sha256,omitempty"`
	RenderSHA256   string                 `json:"render_sha256,omitempty"`
	Model          string                 `json:"model"`
	FixtureSHA     string                 `json:"fixture_sha"`
	PromptHash     string                 `json:"prompt_hash"`
	DigestSHA      string                 `json:"kb_digest_sha,omitempty"`
	Slots          map[string]TeacherSlot `json:"slots"`
	At             time.Time              `json:"at"`
}

// TeacherIdent is what makes a stored reading current: the same text asked the
// same way, of the same task (and, for kb_match, the same knowledge-base
// digest), of the same model. Task and Digest are empty for jd_triage.
type TeacherIdent struct {
	Task, Text, Model, Fixture, Prompt, Digest string
}

func (r TeacherRow) ident() TeacherIdent {
	return TeacherIdent{Task: r.Task, Text: r.TextSHA256, Model: r.Model, Fixture: r.FixtureSHA, Prompt: r.PromptHash, Digest: r.DigestSHA}
}

// TeacherTask is the question a run asks: its identity (what a stored reading
// must match to count as current) and how it is asked. The zero value is not a
// task; JDTriageTask is the default.
type TeacherTask struct {
	// Name is the stored row's task: empty for jd_triage, else the decide task
	// (kbmatch.Task).
	Name string
	// Fixture, Prompt and Digest are the identity hashes a row carries
	// (TeacherRow.FixtureSHA, PromptHash, DigestSHA). Digest is empty for a task
	// that reads no knowledge base.
	Fixture, Prompt, Digest string
	// Ask asks d about p with the very question a real run sends, and returns
	// d's answer as it is: an abstention is an answer to keep, not an error.
	Ask func(ctx context.Context, d decide.Decider, p posting.Posting) (decide.Answer, error)
}

// JDTriageTask is the triage question (jdtriage.Triager) for the normalized
// target summary: the teacher's original task, with the identity rows written
// before tasks existed already carry.
func JDTriageTask(summary string) TeacherTask {
	return TeacherTask{
		Fixture: FixtureSHA(), Prompt: PromptHash(summary),
		Ask: func(ctx context.Context, d decide.Decider, p posting.Posting) (decide.Answer, error) {
			return jdtriage.Triager{D: d, TargetSummary: summary}.Triage(ctx, p)
		},
	}
}

// Ident is the identity a reading of p by model under t carries.
func (t TeacherTask) Ident(p posting.Posting, model string) TeacherIdent {
	return TeacherIdent{Task: t.Name, Text: HashText(p.Description), Model: model, Fixture: t.Fixture, Prompt: t.Prompt, Digest: t.Digest}
}

func (t TeacherTask) validate() error {
	if t.Ask == nil || t.Fixture == "" || t.Prompt == "" {
		return errors.New("eval: teacher task needs a question, a fixture hash and a prompt hash")
	}
	return nil
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
	return strings.Join([]string{string(k), i.Task, i.Text, i.Model, i.Fixture, i.Prompt, i.Digest}, "\x00")
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
				s.seen[r.ident().key(r.Key)] = struct{}{}
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

// LatestByModel returns k's newest row of task per model ("" is jd_triage),
// whatever fixture or prompt it was made under: the reader agreement wants
// every model's last word on a posting, and says how many prompts it spans.
func (s *TeacherStore) LatestByModel(task string, k posting.Key) map[string]TeacherRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]TeacherRow{}
	for _, r := range s.byKey[k] {
		if r.Task != task {
			continue
		}
		if prev, ok := out[r.Model]; ok && r.At.Before(prev.At) {
			continue // file order breaks ties: a later line with the same time wins
		}
		out[r.Model] = r
	}
	return out
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
	s.seen[r.ident().key(r.Key)] = struct{}{}
	s.byKey[r.Key] = append(s.byKey[r.Key], r)
	s.rows++
	return nil
}

// CurrentReadings is the store seen through one identity: the model, the
// shared fixture and the prompt (target summary included) as they are now.
// It implements TeacherReadings.
type CurrentReadings struct {
	s                                    *TeacherStore
	task, model, fixture, prompt, digest string
}

func (c CurrentReadings) matches(r TeacherRow) bool {
	return r.Task == c.task && r.Model == c.model && r.FixtureSHA == c.fixture && r.PromptHash == c.prompt && r.DigestSHA == c.digest
}

// CurrentOf is the store seen through model and the identity of t: for
// kb_match, the question's fixture and prompt hashes and the knowledge-base
// digest as they are now, so a reading made under another digest is not
// current. Current(model, summary) is CurrentOf(model, JDTriageTask(summary)).
func (s *TeacherStore) CurrentOf(model string, t TeacherTask) CurrentReadings {
	return CurrentReadings{s: s, task: t.Name, model: model, fixture: t.Fixture, prompt: t.Prompt, digest: t.Digest}
}

// Current returns the store's readings as the label queue reads them. Only
// rows whose model, fixture_sha and prompt_hash equal the current ones count:
// a reading made under an older prompt answered a different question and must
// not decide what a person labels first. The posting text hash is not part of
// the match (the queue holds the key, and bodies may be evicted), so an edited
// description keeps its reading; it only reorders the queue, never a label.
func (s *TeacherStore) Current(model, summary string) CurrentReadings {
	return s.CurrentOf(model, TeacherTask{Fixture: FixtureSHA(), Prompt: PromptHash(summary)})
}

// Len is how many rows of the store have the current identity (model, task,
// fixture, prompt and digest), whatever their key. Zero on a store with rows
// means every reading was made under another question or model.
func (c CurrentReadings) Len() int {
	if c.s == nil {
		return 0
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	n := 0
	for _, rows := range c.s.byKey {
		for _, r := range rows {
			if c.matches(r) {
				n++
			}
		}
	}
	return n
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
		if !c.matches(r) {
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

// Has reports whether key has a stored reading under the current identity (the
// model, the shared fixture and the prompt), whatever its slots hold. It is the
// restriction `vani teacher --with-readings-of` applies, so a sample can be
// compared against another model's readings of the same question.
func (c CurrentReadings) Has(key string) bool {
	if c.s == nil {
		return false
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for _, r := range c.s.byKey[posting.Key(key)] {
		if c.matches(r) {
			return true
		}
	}
	return false
}

// WithCurrentReading keeps, in order, the candidates c has a current reading of.
func WithCurrentReading(cands []TeacherCandidate, c CurrentReadings) []TeacherCandidate {
	var out []TeacherCandidate
	for _, cand := range cands {
		if c.Has(string(cand.Posting.Key)) {
			out = append(out, cand)
		}
	}
	return out
}

// sampleStream is the fixed second word of the PCG state; the seed is the first.
const sampleStream = 0x76616e69_7465616d // "vani" "team"

// SampleCandidates draws n of cands uniformly at random without replacement,
// deterministically for seed (a partial Fisher-Yates shuffle over a PCG
// generator, whose stream is part of math/rand/v2's compatibility promise), and
// returns them in the order they were drawn. That order is the point: every
// prefix of a draw is itself a uniform sample, and for the same candidates and
// seed a draw of m is the first m of a draw of n >= m, so `--sample 1000
// --max 300` asks about a uniform 300-subset of the 1000. n above len(cands)
// returns every candidate (shuffled); n < 1 is an error, never "all". cands is
// not modified; its order must itself be deterministic (TeacherCandidates sorts
// by key).
func SampleCandidates(cands []TeacherCandidate, n int, seed uint64) ([]TeacherCandidate, error) {
	if n < 1 {
		return nil, fmt.Errorf("eval: sample size %d, want at least 1", n)
	}
	out := append([]TeacherCandidate(nil), cands...)
	rng := rand.New(rand.NewPCG(seed, sampleStream))
	m := min(n, len(out))
	for i := range m {
		j := i + rng.IntN(len(out)-i)
		out[i], out[j] = out[j], out[i]
	}
	return out[:m], nil
}

// TeacherRun is one run's settings.
type TeacherRun struct {
	// D asks the model; it must be the same Decider whatever the posting.
	D decide.Decider
	// Model, Summary and Store identify what is asked and where it is kept.
	Model   string
	Summary string // normalized target summary (jdtriage.NormalizeSummary)
	Store   *TeacherStore
	// Task is the question asked; the zero value is JDTriageTask(Summary).
	Task TeacherTask
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
	task := r.Task
	if task.Ask == nil && task.Name == "" && task.Fixture == "" && task.Prompt == "" && task.Digest == "" {
		task = JDTriageTask(r.Summary)
	}
	if err := task.validate(); err != nil {
		return res, err
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

	var todo []TeacherCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if seen[c.Posting.Key] {
			continue
		}
		seen[c.Posting.Key] = true
		res.Considered++
		if r.Store.Has(c.Posting.Key, task.Ident(c.Posting, r.Model)) {
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
				ans, err := task.Ask(ctx, teacherGate{run: &r, st: st, q: &q}, c.Posting)
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
					row := teacherRow(c.Posting, r.Model, task, q, ans, now())
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

// teacherRow records a's reading of every slot q asked under task t. A slot the
// answer does not mention is recorded as not read, never left out. An answer
// whose value is not one of the slot's options is recorded as failed
// ("invalid_value"), so an answered slot always holds a listed option: a
// model's "none" (no option fits, or it cannot tell) is the backend's
// abstention, kept as reading "abstained" with no value, never as a value.
func teacherRow(p posting.Posting, model string, t TeacherTask, q decide.Question, a decide.Answer, at time.Time) TeacherRow {
	slots := make(map[string]TeacherSlot, len(q.Slots))
	for _, s := range q.Slots {
		rd, ok := a.Slots[s.Name]
		switch {
		case !ok:
			slots[s.Name] = TeacherSlot{Reading: string(decide.Failed), Detail: "missing_slot"}
		case rd.Reading == decide.Answered && !slices.Contains(s.Options, rd.Value):
			slots[s.Name] = TeacherSlot{Reading: string(decide.Failed), Detail: "invalid_value"}
		default:
			slots[s.Name] = TeacherSlot{Reading: string(rd.Reading), Value: rd.Value, Detail: rd.Detail}
		}
	}
	render := ""
	if len(q.Context) > 0 {
		render = posting.RawSHA256(string(q.Context))
	}
	return TeacherRow{
		V: TeacherSchema, Key: p.Key, Task: t.Name, TextSHA256: HashText(p.Description),
		TitleSHA256: HashText(p.Title), LocationSHA256: HashText(p.Location), RenderSHA256: render, Model: model,
		FixtureSHA: t.Fixture, PromptHash: t.Prompt, DigestSHA: t.Digest, Slots: slots, At: at.UTC(),
	}
}
