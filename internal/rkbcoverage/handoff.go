package rkbcoverage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// The gaps hand-off is one JSON line per job in a bounded file the owner's
// career session reads (docs/RUNTIME.md, "Coverage"). Terms are skills the bank
// has no eligible bullet for; company and title are the posting's own words.
// Nothing about the owner is in it.
const (
	// MaxHandoffLines and MaxHandoffBytes bound the file: past either, the oldest
	// lines go first (the newest is always kept).
	MaxHandoffLines = 500
	MaxHandoffBytes = 256 << 10
	// MaxLineBytes bounds one line; terms past it are dropped, never the line.
	MaxLineBytes = 16 << 10
	// handoffLockWait bounds the wait for another writer.
	handoffLockWait = 10 * time.Second
)

// ErrHandoffBusy means another writer held the file's lock for the whole wait.
var ErrHandoffBusy = errors.New("rkbcoverage: the gaps file is being written by another process")

// Line is one job's hand-off.
type Line struct {
	At      string   `json:"at"`
	Key     string   `json:"key"`
	Company string   `json:"company"`
	Title   string   `json:"title"`
	Terms   []string `json:"terms"`
}

// Fit drops terms from the end until the encoded line is at most MaxLineBytes,
// and returns it. It reports whether any term was dropped.
func (l Line) Fit() (Line, []byte, bool, error) {
	dropped := false
	for {
		raw, err := json.Marshal(l)
		if err != nil {
			return l, nil, dropped, err
		}
		if len(raw) <= MaxLineBytes {
			return l, raw, dropped, nil
		}
		if len(l.Terms) == 0 {
			return l, nil, dropped, fmt.Errorf("rkbcoverage: a hand-off line is over %d bytes with no terms", MaxLineBytes)
		}
		l.Terms, dropped = l.Terms[:len(l.Terms)-1], true
	}
}

// sameTerms says two term lists hold the same terms, whatever the order.
func sameTerms(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[strings.ToLower(t)] = true
	}
	for _, t := range b {
		if !set[strings.ToLower(t)] {
			return false
		}
	}
	return true
}

// AppendHandoff adds l to the file at path, atomically and under a lock beside it
// (path + ".lock"), keeping at most MaxHandoffLines lines and MaxHandoffBytes
// bytes (the oldest go first). The file is 0600. It reports unchanged, and
// writes nothing, when the newest line of the same key already holds the same
// terms (a retried call adds nothing). A file that cannot be read whole (over
// its bound, not a regular file, lines that are not JSON objects) is replaced:
// it is a window onto recent jobs, and must not stop the next one from being
// handed over.
func AppendHandoff(ctx context.Context, path string, l Line) (unchanged bool, err error) {
	l, line, _, err := l.Fit()
	if err != nil {
		return false, err
	}
	lctx, cancel := context.WithTimeout(ctx, handoffLockWait)
	defer cancel()
	lock, err := filelock.Acquire(lctx, path+".lock")
	if err != nil {
		if errors.Is(err, filelock.ErrWouldBlock) {
			return false, ErrHandoffBusy
		}
		return false, fmt.Errorf("rkbcoverage: lock the gaps file: %w", err)
	}
	defer func() { _ = lock.Release() }()

	var lines [][]byte
	if old, rerr := readRegular(path, 4*MaxHandoffBytes); rerr == nil {
		for _, ln := range bytes.Split(old, []byte("\n")) {
			var probe Line
			if len(ln) == 0 || json.Unmarshal(ln, &probe) != nil {
				continue
			}
			lines = append(lines, ln)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var prev Line
		if json.Unmarshal(lines[i], &prev) == nil && prev.Key == l.Key {
			if sameTerms(prev.Terms, l.Terms) {
				return true, nil
			}
			break
		}
	}
	lines = append(lines, line)
	if len(lines) > MaxHandoffLines {
		lines = lines[len(lines)-MaxHandoffLines:]
	}
	size := 0
	for _, ln := range lines {
		size += len(ln) + 1
	}
	for len(lines) > 1 && size > MaxHandoffBytes {
		size -= len(lines[0]) + 1
		lines = lines[1:]
	}
	out := append(bytes.Join(lines, []byte("\n")), '\n')
	if err := atomicfile.Write(path, out); err != nil {
		return false, fmt.Errorf("rkbcoverage: write the gaps file: %w", err)
	}
	return false, nil
}

// readRegular reads a plain file of at most max bytes; a link, folder or
// oversized file is an error.
func readRegular(path string, max int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", st.Name())
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is over %d bytes", st.Name(), max)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s grew past %d bytes", st.Name(), max)
	}
	return b, nil
}
