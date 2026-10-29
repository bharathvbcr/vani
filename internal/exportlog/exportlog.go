// Package exportlog remembers which postings `vani jobs export` has written to
// a workbook, so a later export can say a job was exported before and ask
// before writing it again. It holds posting keys, times and the workbook
// paths the owner chose; never a title, a description or anything personal.
//
// The record is one JSON file under the state dir, folded per key (first and
// last export, how many), not a log: one export can name 5,000 keys, which an
// append-per-event store would grow by every time. A write takes the file's
// flock, reads, folds and replaces the file atomically, so two exports at once
// never lose each other's keys; a reader needs no lock, since it only ever
// sees a whole file.
package exportlog

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// Version is the only file version this package reads or writes.
	Version = 1
	// MaxJobs bounds the keys remembered. A write past it forgets the keys
	// exported longest ago and says how many (Record's forgotten).
	MaxJobs = 50_000
	// MaxRecent bounds the exports remembered by file.
	MaxRecent = 50
	// MaxFileBytes bounds the file: MaxJobs keys of the longest a key may be,
	// with their times, fit under it.
	MaxFileBytes = 40 << 20
	// MaxFileNameBytes bounds a remembered workbook path.
	MaxFileNameBytes = 4096

	fileName = "exported.json"
	lockWait = 3 * time.Second
)

var (
	// ErrCorrupt means the file does not read as a record this package wrote.
	ErrCorrupt = errors.New("exportlog: corrupt record")
	// ErrBusy means another export held the record for longer than lockWait.
	ErrBusy = errors.New("exportlog: record is busy")
)

// Job is what the record holds for one posting.
type Job struct {
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	Count int       `json:"count"`
}

// Export is one export: when, which workbook, how many rows.
type Export struct {
	At   time.Time `json:"at"`
	File string    `json:"file"`
	Rows int       `json:"rows"`
}

// Log is the whole record.
type Log struct {
	V    int                 `json:"v"`
	Jobs map[posting.Key]Job `json:"jobs"`
	// Recent is the last MaxRecent exports, oldest first.
	Recent []Export `json:"recent"`
}

// Path is where the record lives for a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, "export", fileName) }

func empty() Log { return Log{V: Version, Jobs: map[posting.Key]Job{}, Recent: []Export{}} }

// Read returns the record at path; one never written is empty. A file that is
// too large, not this version or not well formed is ErrCorrupt, never read as
// empty, so a damaged record cannot let every job through as new.
func Read(path string) (Log, error) {
	f, err := atomicfile.OpenRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty(), nil
	}
	if err != nil {
		return Log{}, fmt.Errorf("exportlog: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Log{}, fmt.Errorf("exportlog: read: %w", err)
	}
	if len(data) > MaxFileBytes {
		return Log{}, fmt.Errorf("%w: %s is over %d bytes", ErrCorrupt, fileName, MaxFileBytes)
	}
	var l Log
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return Log{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, fileName, err)
	}
	if dec.More() {
		return Log{}, fmt.Errorf("%w: %s holds more than one value", ErrCorrupt, fileName)
	}
	if err := l.check(); err != nil {
		return Log{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, fileName, err)
	}
	if l.Jobs == nil {
		l.Jobs = map[posting.Key]Job{}
	}
	if l.Recent == nil {
		l.Recent = []Export{}
	}
	return l, nil
}

func (l Log) check() error {
	switch {
	case l.V != Version:
		return fmt.Errorf("version %d", l.V)
	case len(l.Jobs) > MaxJobs:
		return fmt.Errorf("%d jobs, over %d", len(l.Jobs), MaxJobs)
	case len(l.Recent) > MaxRecent:
		return fmt.Errorf("%d exports, over %d", len(l.Recent), MaxRecent)
	}
	for k, j := range l.Jobs {
		if err := marks.CheckKey(k); err != nil {
			return err
		}
		if j.First.IsZero() || j.Last.Before(j.First) || j.Count < 1 {
			return fmt.Errorf("job %s: first %v, last %v, count %d", k, j.First, j.Last, j.Count)
		}
	}
	for _, x := range l.Recent {
		if x.At.IsZero() || x.Rows < 0 || len(x.File) > MaxFileNameBytes {
			return fmt.Errorf("export at %v of %d rows", x.At, x.Rows)
		}
	}
	return nil
}

// Prior is what the record says of a set of jobs: how many were exported
// before, and the latest export that wrote any of them.
type Prior struct {
	N int
	// Last is the latest export of those N; its File is "" when that export is
	// older than the MaxRecent the record names by file.
	Last Export
}

// Exported reports whether k was exported before.
func (l Log) Exported(k posting.Key) bool {
	_, ok := l.Jobs[k]
	return ok
}

// Prior counts the keys the record holds, each once.
func (l Log) Prior(keys []posting.Key) Prior {
	var p Prior
	seen := make(map[posting.Key]bool, len(keys))
	for _, k := range keys {
		j, ok := l.Jobs[k]
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		p.N++
		if j.Last.After(p.Last.At) {
			p.Last = Export{At: j.Last}
		}
	}
	if p.N > 0 {
		for _, x := range l.Recent {
			if x.At.Equal(p.Last.At) {
				p.Last = x
			}
		}
	}
	return p
}

// Record adds an export of keys to file at now, under the record's lock, and
// returns how many keys were forgotten to stay under MaxJobs (the ones last
// exported longest ago). A record that does not read is not written over.
func Record(ctx context.Context, path string, keys []posting.Key, file string, now time.Time) (forgotten int, err error) {
	if now.IsZero() {
		return 0, errors.New("exportlog: zero time")
	}
	if len(file) > MaxFileNameBytes {
		return 0, fmt.Errorf("exportlog: file name over %d bytes", MaxFileNameBytes)
	}
	for _, k := range keys {
		if err := marks.CheckKey(k); err != nil {
			return 0, fmt.Errorf("exportlog: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	lk, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		if errors.Is(err, filelock.ErrWouldBlock) {
			return 0, fmt.Errorf("%w: %v", ErrBusy, err)
		}
		return 0, fmt.Errorf("exportlog: %w", err)
	}
	defer func() {
		if rerr := lk.Release(); rerr != nil && err == nil {
			err = fmt.Errorf("exportlog: unlock: %w", rerr)
		}
	}()
	l, err := Read(path)
	if err != nil {
		return 0, err
	}
	now = now.UTC()
	seen := make(map[posting.Key]bool, len(keys))
	rows := 0
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		rows++
		j, ok := l.Jobs[k]
		if !ok {
			j.First = now
		}
		if now.After(j.Last) {
			j.Last = now
		}
		if j.First.After(j.Last) {
			j.First = j.Last
		}
		j.Count++
		l.Jobs[k] = j
	}
	forgotten = l.forget()
	l.Recent = append(l.Recent, Export{At: now, File: file, Rows: rows})
	if n := len(l.Recent) - MaxRecent; n > 0 {
		l.Recent = slices.Delete(l.Recent, 0, n)
	}
	data, err := json.Marshal(l)
	if err != nil {
		return 0, fmt.Errorf("exportlog: marshal: %w", err)
	}
	if len(data) > MaxFileBytes {
		return 0, fmt.Errorf("exportlog: record would be %d bytes, over %d", len(data), MaxFileBytes)
	}
	if err := atomicfile.Write(path, data); err != nil {
		return 0, fmt.Errorf("exportlog: %w", err)
	}
	return forgotten, nil
}

// forget drops the keys last exported longest ago until MaxJobs remain, and
// returns how many it dropped.
func (l Log) forget() int {
	n := len(l.Jobs) - MaxJobs
	if n <= 0 {
		return 0
	}
	type aged struct {
		k    posting.Key
		last time.Time
	}
	all := make([]aged, 0, len(l.Jobs))
	for k, j := range l.Jobs {
		all = append(all, aged{k, j.Last})
	}
	slices.SortFunc(all, func(a, b aged) int {
		if c := a.last.Compare(b.last); c != 0 {
			return c
		}
		return cmp.Compare(a.k, b.k)
	})
	for _, a := range all[:n] {
		delete(l.Jobs, a.k)
	}
	return n
}
