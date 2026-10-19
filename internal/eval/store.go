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
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
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
}

// Store keeps labels in LabelDir/labels.jsonl and description text in TextDir.
type Store struct {
	labelDir string
	textDir  string
	mu       sync.Mutex
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
	return out
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
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.labelsPath(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("eval: open labels: %w", err)
	}
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], st.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
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
