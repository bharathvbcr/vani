package eval

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A key list is the posting keys a teacher run is restricted to (`vani teacher
// --keys`), one per line, and the drawn sample a run saves (--draw-out) so a
// capped or interrupted run can be resumed as the same sample: a seeded draw
// over a candidate list that has since grown is a different draw, but the
// saved keys are the same keys in the same order.

const (
	// MaxKeyListBytes bounds a key file (a million keys of ordinary length fit).
	MaxKeyListBytes = 64 << 20
	// MaxKeyListKeys bounds how many keys one file may name.
	MaxKeyListKeys = 1_000_000
	// maxKeyBytes bounds one key; a longer line is not a posting key.
	maxKeyBytes = 1024
	// maxNamed bounds how many offending keys an error names.
	maxNamed = 20
)

// ReadKeyList reads one posting key per line, in order. Surrounding spaces and
// a CR are trimmed and blank lines skipped. It refuses, naming the offenders,
// a file with no key, a duplicate key, a key with spaces inside it and an
// oversized file, line or count: a key list that is not what its author meant
// must not quietly become a different sample.
func ReadKeyList(r io.Reader) ([]posting.Key, error) {
	lr := &io.LimitedReader{R: r, N: MaxKeyListBytes + 1}
	br := bufio.NewReaderSize(lr, 64<<10)
	var (
		keys  []posting.Key
		dups  []string
		bad   []string
		first = map[posting.Key]int{}
		line  int
	)
	for {
		raw, tooLong, err := readBoundedLine(br, maxKeyBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("key list: %w", err)
		}
		if lr.N == 0 {
			return nil, fmt.Errorf("key list: larger than %d bytes", MaxKeyListBytes)
		}
		line++
		if tooLong {
			return nil, fmt.Errorf("key list: line %d is longer than %d bytes, not a posting key", line, maxKeyBytes)
		}
		k := strings.TrimSpace(string(raw))
		switch {
		case k == "":
		case strings.ContainsAny(k, " \t\v\f\x00"):
			bad = append(bad, fmt.Sprintf("line %d", line))
		default:
			if at, ok := first[posting.Key(k)]; ok {
				dups = append(dups, fmt.Sprintf("%s (lines %d and %d)", k, at, line))
				break
			}
			first[posting.Key(k)] = line
			keys = append(keys, posting.Key(k))
			if len(keys) > MaxKeyListKeys {
				return nil, fmt.Errorf("key list: more than %d keys", MaxKeyListKeys)
			}
		}
	}
	if lr.N == 0 {
		return nil, fmt.Errorf("key list: larger than %d bytes", MaxKeyListBytes)
	}
	var errs []error
	if len(bad) > 0 {
		errs = append(errs, fmt.Errorf("key list: %d line(s) hold more than one word, not a posting key: %s", len(bad), named(bad)))
	}
	if len(dups) > 0 {
		errs = append(errs, fmt.Errorf("key list: %d duplicate key(s): %s", len(dups), named(dups)))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("key list: no keys")
	}
	return keys, nil
}

// named lists at most maxNamed items and says how many more there were.
func named(items []string) string {
	if len(items) <= maxNamed {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxNamed], ", ") + fmt.Sprintf(", and %d more", len(items)-maxNamed)
}

// NamedKeys is named over keys, for an error that must name what it refused.
func NamedKeys(keys []posting.Key) string {
	s := make([]string, len(keys))
	for i, k := range keys {
		s[i] = string(k)
	}
	return named(s)
}

// RestrictToKeys keeps the candidates keys names, in the order keys lists
// them (so a saved draw resumes in its drawn order), and returns the keys that
// name no candidate. keys must hold no duplicate (ReadKeyList refuses one).
func RestrictToKeys(cands []TeacherCandidate, keys []posting.Key) (kept []TeacherCandidate, missing []posting.Key) {
	by := make(map[posting.Key]TeacherCandidate, len(cands))
	for _, c := range cands {
		by[c.Posting.Key] = c
	}
	for _, k := range keys {
		if c, ok := by[k]; ok {
			kept = append(kept, c)
		} else {
			missing = append(missing, k)
		}
	}
	return kept, missing
}

// KeyListBytes is cands' keys, one per line, in order: what WriteKeyList
// writes and ReadKeyList reads back.
func KeyListBytes(cands []TeacherCandidate) []byte {
	var b bytes.Buffer
	for _, c := range cands {
		b.WriteString(string(c.Posting.Key))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// WriteKeyList atomically replaces path with cands' keys (mode 0600, the
// directory made if missing).
func WriteKeyList(path string, cands []TeacherCandidate) error {
	if err := atomicfile.Write(path, KeyListBytes(cands)); err != nil {
		return fmt.Errorf("key list: %w", err)
	}
	return nil
}
