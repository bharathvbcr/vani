package resumestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Summaries is every job's Summary, read in one walk of the store: a list of
// many jobs asks it per job instead of probing the filesystem once for each
// (most jobs have no resume, and a probe for each of them is a failed open).
//
// The walk reads at most MaxJobs folders, each for its key file, its pointers
// and its current and used records (never the choice or a PDF). It is keyed by
// the job folder, which is the hash of the key, so a folder whose key file is
// missing or unreadable is still attributed to the job it belongs to and is
// reported for it as an error, never dropped and never read as "no resume".
type Summaries struct {
	byFolder map[string]folderSummary
	// err is the walk's own failure: the store could not be listed. Every Of
	// then answers with it.
	err error
}

type folderSummary struct {
	sum Summary
	err error
}

// Summaries walks the store once. A store never written is an empty Summaries
// and no error. When the store cannot be listed, the returned Summaries is
// still usable and answers every Of with that error, which is also returned.
// When the store holds more than MaxJobs folders, the first MaxJobs (by name)
// are read, the rest answer ErrDamaged for their own jobs, and the error says
// how many were not read, so a capped walk never passes for a complete one.
func (s *Store) Summaries() (*Summaries, error) {
	ents, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return &Summaries{}, nil
	}
	if err != nil {
		x := &Summaries{err: fmt.Errorf("resumestore: %w", err)}
		return x, x.err
	}
	x := &Summaries{byFolder: map[string]folderSummary{}}
	unread := 0
	for _, e := range ents {
		if !folderRe.MatchString(e.Name()) {
			continue // `.tmp`, `.lock` and strays are not the store's
		}
		if !e.IsDir() {
			// Named like a job folder but not one: the job it belongs to is damaged.
			x.byFolder[e.Name()] = folderSummary{err: fmt.Errorf("%w: %s is not a folder", ErrDamaged, e.Name()[:12])}
			continue
		}
		if len(x.byFolder) >= MaxJobs {
			x.byFolder[e.Name()] = folderSummary{err: fmt.Errorf("%w: %s is beyond the first %d job folders and was not read", ErrDamaged, e.Name()[:12], MaxJobs)}
			unread++
			continue
		}
		sum, err := summarizeFolder(filepath.Join(s.root, e.Name()), e.Name())
		x.byFolder[e.Name()] = folderSummary{sum: sum, err: err}
	}
	if unread > 0 {
		return x, fmt.Errorf("%w: %d job folders beyond the first %d were not read", ErrDamaged, unread, MaxJobs)
	}
	return x, nil
}

// summarizeFolder is Summary for the folder named name, found by walking rather
// than by key: the key is read from the folder, and must hash to its name.
func summarizeFolder(dir, name string) (Summary, error) {
	l, err := scan(dir, time.Time{})
	if err != nil {
		return Summary{}, fmt.Errorf("resumestore: %w", err)
	}
	got, err := readRegular(filepath.Join(dir, keyFile), MaxKeyBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if len(l.jsons) == 0 {
			return Summary{}, nil // an unfinished create reads as not found
		}
		return Summary{}, fmt.Errorf("%w: %s has versions but no key file", ErrDamaged, name[:12])
	case err != nil:
		return Summary{}, fmt.Errorf("%w: key file: %w", ErrDamaged, err)
	case CheckKey(string(got)) != nil || jobFolder(string(got)) != name:
		return Summary{}, fmt.Errorf("%w: %s names a different posting", ErrDamaged, name[:12])
	}
	return summarize(dir, string(got), l)
}

// Of is key's Summary, with the meaning Summary(key) has: the zero Summary and
// no error for a job with no folder or no versions, an error for anything
// unreadable. A nil Summaries (no store) has no resumes.
func (x *Summaries) Of(key string) (Summary, error) {
	if err := CheckKey(key); err != nil {
		return Summary{}, err
	}
	if x == nil {
		return Summary{}, nil
	}
	if x.err != nil {
		return Summary{}, x.err
	}
	r := x.byFolder[jobFolder(key)]
	return r.sum, r.err
}
