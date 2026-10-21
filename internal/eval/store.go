// Package eval holds Vani's hand-labelled triage evaluation set and measures
// the decision backends against it. Labels (slot -> option, plus the posting's
// text hash) are small and live in the repository; the job-description text
// never does: it is cached under its sha256 name in a separate state
// directory, so git only ever holds the hash.
package eval

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// SchemaVersion is the labels.jsonl line version.
	SchemaVersion = 1
	// Skip is the label value for "can't tell". It is valid for every slot and
	// is excluded from every precision and coverage denominator.
	Skip = "skip"
	// LabelsFile is the append-only label log inside the label directory.
	LabelsFile = "labels.jsonl"

	// maxLabelLine bounds one labels.jsonl line when reading.
	maxLabelLine = 64 << 10
	// MaxTextBytes bounds a cached description.
	MaxTextBytes = 4 << 20
)

// Entry is one labels.jsonl line: the human's reading of one posting.
type Entry struct {
	V          int               `json:"v"`
	Key        posting.Key       `json:"key"`
	Labels     map[string]string `json:"labels"`
	TextSHA256 string            `json:"text_sha256,omitempty"`
	LabeledAt  time.Time         `json:"labeled_at"`
	// Source and PSelect belong to a would_apply label (see WouldApply): the
	// sampling stratum the posting was drawn from ("decile:10",
	// "dropped:title:role_fit") or SourceManual, and the posting's inclusion
	// probability in that draw. A triage-slot label carries neither.
	Source  string  `json:"source,omitempty"`
	PSelect float64 `json:"p_select,omitempty"`
}

const (
	// WouldApply is the label slot for the owner's "would I apply to this" read
	// of a posting. Its options are WouldApplyOptions and Skip. It is not a
	// triage slot: no backend answers it, so the triage report ignores it.
	WouldApply = "would_apply"
	// SourceManual marks a would_apply label the owner gave outside any sample.
	// It has no inclusion probability, so the relevance report keeps it out of
	// every stratum.
	SourceManual = "manual"
	// maxSourceBytes bounds a Source.
	maxSourceBytes = 64
	// MinInclusionProbability is the smallest p_select a label may carry. The
	// audit draws a stratum's places over its unlabelled pool, so a pick's
	// probability is at least 1/(strata x pool): with a pool of a million
	// postings and tens of strata that is about 1e-8, and 1e-9 leaves room for a
	// ledger a hundred times larger. Below it a value is not something the
	// sampler produced. A pair is weighted 1/(p_i p_j), so the floor also keeps
	// every weight (at most 1e18) and every sum of them finite; without it a
	// p_select under 1e-154 overflows the weight to +Inf and poisons the fit.
	MinInclusionProbability = 1e-9
)

// WouldApplyOptions are the would_apply values besides Skip.
var WouldApplyOptions = []string{"yes", "maybe", "no"}

// Store keeps labels in LabelDir/labels.jsonl and description text in TextDir.
type Store struct {
	labelDir string
	textDir  string
	// Now stamps new labels; nil means time.Now.
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// NewStore returns a Store, creating both directories if needed.
func NewStore(labelDir, textDir string) (*Store, error) {
	if labelDir == "" || textDir == "" {
		return nil, errors.New("eval: label and text directories are required")
	}
	if err := os.MkdirAll(labelDir, 0o755); err != nil {
		return nil, fmt.Errorf("eval: label dir: %w", err)
	}
	if err := os.MkdirAll(textDir, 0o700); err != nil {
		return nil, fmt.Errorf("eval: text dir: %w", err)
	}
	return &Store{labelDir: labelDir, textDir: textDir}, nil
}

func (s *Store) labelsPath() string { return filepath.Join(s.labelDir, LabelsFile) }

// slotOptions maps each triage slot to its legal option values.
func slotOptions() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(jdtriage.Slots))
	for _, sl := range jdtriage.Slots {
		m := make(map[string]bool, len(sl.Options))
		for _, o := range sl.Options {
			m[o.Value] = true
		}
		out[sl.Name] = m
	}
	wa := make(map[string]bool, len(WouldApplyOptions))
	for _, o := range WouldApplyOptions {
		wa[o] = true
	}
	out[WouldApply] = wa
	return out
}

// validSource reports whether s is a short lower-case token: letters, digits
// and _:-. (what a stratum name is made of).
func validSource(s string) bool {
	if s == "" || len(s) > maxSourceBytes {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == ':' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// checkSampling validates Source and PSelect: a would_apply label names its
// stratum and, unless it is manual, an inclusion probability in (0, 1]; any
// other label carries neither.
func (l Entry) checkSampling() error {
	_, wa := l.Labels[WouldApply]
	if !wa {
		if l.Source != "" || l.PSelect != 0 {
			return errors.New("eval: source and p_select belong to a would_apply label")
		}
		return nil
	}
	if !validSource(l.Source) {
		return fmt.Errorf("eval: would_apply label needs a source of at most %d characters from a-z 0-9 _ : - .", maxSourceBytes)
	}
	if l.Source == SourceManual {
		if l.PSelect != 0 {
			return errors.New("eval: a manual would_apply label has no p_select")
		}
		return nil
	}
	if !(l.PSelect >= MinInclusionProbability && l.PSelect <= 1) { // false for NaN
		return fmt.Errorf("eval: p_select %v must be in [%g, 1]", l.PSelect, MinInclusionProbability)
	}
	return nil
}

// Validate refuses a label with no key, no slots, an unknown slot, or a value
// that is neither one of the slot's options nor Skip.
func (l Entry) Validate() error {
	if l.Key == "" {
		return errors.New("eval: label has no key")
	}
	if len(l.Labels) == 0 {
		return errors.New("eval: label has no slots")
	}
	opts := slotOptions()
	for slot, v := range l.Labels {
		o, ok := opts[slot]
		if !ok {
			return fmt.Errorf("eval: unknown slot %q", slot)
		}
		if v != Skip && !o[v] {
			return fmt.Errorf("eval: %q is not an option of slot %q", v, slot)
		}
	}
	if l.TextSHA256 != "" && !validHash(l.TextSHA256) {
		return fmt.Errorf("eval: malformed text_sha256 %q", l.TextSHA256)
	}
	return l.checkSampling()
}

func validHash(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Put validates l and appends it as one line, then fsyncs. A zero V and
// LabeledAt are filled in. If the file ends in a torn line (a crash mid-write)
// a newline is written first so the new line is never glued onto it.
func (s *Store) Put(l Entry) error {
	if err := l.Validate(); err != nil { // what the caller gave must stand alone
		return err
	}
	return s.Update(l.Key, func(prev Entry, _ bool) (Entry, error) { return Merge(prev, l), nil })
}

// Merge is the rule every label write follows, because the store keeps the
// latest line per key whole. The entry holds two kinds of label and next
// replaces only the kinds it carries: triage slots (a next with any slot
// replaces the whole set, so a re-label does not leave stale slots) and
// would_apply, whose Source and PSelect travel with it. A next with no triage
// slot keeps prev's; a next with no would_apply keeps prev's, with its
// sampling fields. TextSHA256 is next's when it has one. next's V and
// LabeledAt are kept as given, so Update stamps them.
func Merge(prev, next Entry) Entry {
	out := next
	out.Labels = make(map[string]string, len(prev.Labels)+len(next.Labels))
	_, nextWA := next.Labels[WouldApply]
	nextSlots := len(next.Labels) > 1 || (len(next.Labels) == 1 && !nextWA)
	for slot, v := range prev.Labels {
		if (slot == WouldApply && !nextWA) || (slot != WouldApply && !nextSlots) {
			out.Labels[slot] = v
		}
	}
	for slot, v := range next.Labels {
		out.Labels[slot] = v
	}
	if !nextWA {
		if _, ok := prev.Labels[WouldApply]; ok {
			out.Source, out.PSelect = prev.Source, prev.PSelect
		}
	}
	if out.TextSHA256 == "" {
		out.TextSHA256 = prev.TextSHA256
	}
	return out
}

const (
	// labelsLockFile is the flock that serialises label writes across
	// processes; it sits beside labels.jsonl and is never read.
	labelsLockFile = "labels.lock"
	lockRetry      = 5 * time.Millisecond
)

// lockWait bounds how long a write waits for the labels lock; a variable only
// so a test need not wait it out.
var lockWait = 10 * time.Second

// ErrBusy is returned when the labels lock could not be taken in time. Update
// holds it for the length of fn, so an fn that itself writes to the store
// meets this rather than deadlocking.
var ErrBusy = errors.New("eval: labels are locked by another writer")

// lockLabels takes the exclusive labels flock, polling until lockWait passes.
// Each call opens its own file, so writers in one process exclude each other
// as well as writers in other processes.
func (s *Store) lockLabels() (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(s.labelDir, labelsLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eval: open labels lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("eval: lock labels: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrBusy
		}
		time.Sleep(lockRetry)
	}
}

// Update is the store's one read-modify-write: under the labels lock it reads
// the latest entry for key (exists is false when there is none), calls fn, and
// appends what fn returns, validated and stamped as Put does. The read and the
// append share the lock, so no writer can land between them and a stale prev
// cannot overwrite a newer entry. An error from fn is returned as is and
// nothing is written. fn runs with the lock held: keep it pure (no I/O, no
// store calls, nothing slow); one that writes to the store gets ErrBusy after
// lockWait. fn must return an entry for key.
func (s *Store) Update(key posting.Key, fn func(prev Entry, exists bool) (Entry, error)) error {
	if s == nil {
		return errors.New("eval: no label store")
	}
	unlock, err := s.lockLabels()
	if err != nil {
		return err
	}
	defer unlock()
	all, _, err := s.Labels()
	if err != nil {
		return err
	}
	prev, exists := all[key]
	l, err := fn(prev, exists)
	if err != nil {
		return err
	}
	if l.Key != key {
		return fmt.Errorf("eval: update of %q returned an entry for %q", key, l.Key)
	}
	if l.V == 0 {
		l.V = SchemaVersion
	}
	if l.LabeledAt.IsZero() {
		l.LabeledAt = s.now().UTC()
	}
	if err := l.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.labelsPath(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("eval: open labels: %w", err)
	}
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		f.Close()
		return fmt.Errorf("eval: labels: %w", err)
	}
	if torn {
		line = append([]byte{'\n'}, line...)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("eval: write label: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("eval: sync labels: %w", err)
	}
	return f.Close()
}

// Labels reads labels.jsonl; the latest line per key wins. Lines that are not
// valid labels (a torn tail, a hand edit) are skipped and counted, never
// silently merged. A missing file is an empty set; a missing store is an
// error, so a report built without one says its labels could not be read
// rather than showing an empty, valid-looking table.
func (s *Store) Labels() (map[posting.Key]Entry, int, error) {
	if s == nil {
		return nil, 0, errors.New("eval: no label store")
	}
	f, err := os.Open(s.labelsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[posting.Key]Entry{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("eval: open labels: %w", err)
	}
	defer f.Close()
	out := map[posting.Key]Entry{}
	bad := 0
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, tooLong, err := readLine(br, maxLabelLine)
		if len(strings.TrimSpace(line)) > 0 {
			var l Entry
			if tooLong || json.Unmarshal([]byte(line), &l) != nil || l.V != SchemaVersion || l.Validate() != nil {
				bad++
			} else {
				out[l.Key] = l
			}
		}
		if err == io.EOF {
			return out, bad, nil
		}
		if err != nil {
			return nil, bad, fmt.Errorf("eval: read labels: %w", err)
		}
	}
}

// readLine returns one line without its newline, discarding bytes beyond max
// (tooLong then reports the cut). err is io.EOF with the final partial line.
func readLine(br *bufio.Reader, max int) (line string, tooLong bool, err error) {
	var b []byte
	for {
		chunk, isPrefix, e := br.ReadLine()
		if e != nil {
			return string(b), tooLong, e
		}
		if len(b)+len(chunk) > max {
			tooLong = true
		} else {
			b = append(b, chunk...)
		}
		if !isPrefix {
			return string(b), tooLong, nil
		}
	}
}

// HashText is the cache name for a description.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// PutText caches text under its sha256 (mode 0600) and returns the hash. An
// existing entry is left alone; the write is atomic (temp file, then rename).
func (s *Store) PutText(text string) (string, error) {
	if len(text) > MaxTextBytes {
		return "", fmt.Errorf("eval: text is %d bytes, max %d", len(text), MaxTextBytes)
	}
	h := HashText(text)
	dst := filepath.Join(s.textDir, h)
	if _, err := os.Stat(dst); err == nil {
		return h, nil
	}
	if err := atomicfile.Write(dst, []byte(text)); err != nil {
		return "", fmt.Errorf("eval: cache text: %w", err)
	}
	return h, nil
}

// Text reads a cached description by hash. The hash is checked as 64 lowercase
// hex characters, so it cannot name a path outside the text directory.
func (s *Store) Text(hash string) (string, error) {
	if !validHash(hash) {
		return "", fmt.Errorf("eval: malformed text hash %q", hash)
	}
	b, err := os.ReadFile(filepath.Join(s.textDir, hash))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
