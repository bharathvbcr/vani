package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// The run history keeps every finished run, not only the last. When a run's
// record is finalised (outcome other than "running") saveRunRecord also writes
// it, byte for byte what last.json holds, to
// <state dir>/runs/history/<id>.json, then prunes the directory to the newest
// maxRunHistory records and maxRunHistoryBytes. last.json stays the one record
// `vani status --json` and the Mac app read; the history is only for looking
// back (`vani runs`). A record that never ended (the process died) is never
// written to the history: it is last.json until the next run replaces it.
//
// The directory also holds <ts>-manual.log files, the app's rotated
// runs/manual.log (macos ManualRunLauncher). Those are the app's: this file
// reads, prunes and lists *.json records named by runID and nothing else, so
// the two pruners never touch each other's files.

const (
	runHistoryDir = "history"
	// maxRunHistory is how many finished runs are kept: a month of daily runs.
	maxRunHistory = 30
	// maxRunHistoryBytes bounds the records together. The worst-case record is
	// under maxRunRecord/2 (TestWorstCaseRunRecordIsReadable) and a usual one
	// is some tens of KB, so the count is the bound that usually binds; this
	// one keeps a pathological run from filling the disk.
	maxRunHistoryBytes = 4 << 20
	// maxRunHistoryScan bounds the records one `vani runs list` reads. Pruning
	// keeps the directory far under it; a directory past it (records copied in
	// by hand) is listed to this many, newest first, and says how many it left.
	maxRunHistoryScan = 200
)

// runIDLayout is a run's start in UTC to the millisecond, fixed width so ids
// sort in time order, with no colon so an id is a safe file name and CLI
// argument.
const runIDLayout = "20060102T150405.000Z"

// runIDPattern is the only shape of id the history reads or writes, so an id
// from the command line can never name a path outside the directory.
var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{3}Z-[0-9]{1,10}$`)

// runID names a run: its start and the pid that made it.
func runID(r runRecord) string {
	return r.StartedAt.UTC().Format(runIDLayout) + "-" + strconv.Itoa(r.PID)
}

func validRunID(id string) bool { return runIDPattern.MatchString(id) }

func runHistoryPath(stateDir, id string) string {
	return filepath.Join(stateDir, runRecordDir, runHistoryDir, id+".json")
}

// writeRunHistory writes a finished run's record to the history, atomically,
// then prunes. Writing the same run twice replaces its file, so a retry is safe.
func writeRunHistory(stateDir string, r runRecord) error {
	if r.Outcome == outcomeRunning {
		return errors.New("a run that has not ended is not history")
	}
	raw, err := marshalRunRecord(r)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(runHistoryPath(stateDir, runID(r)), raw); err != nil {
		return err
	}
	_, err = pruneRunHistory(stateDir, maxRunHistory, maxRunHistoryBytes)
	return err
}

// historyFile is one record file of the history, as the directory shows it.
type historyFile struct {
	ID   string
	Size int64
	// Regular is false for a link, directory or device wearing a record's name;
	// it is never read, only listed (as a problem) or pruned.
	Regular bool
}

// listRunHistory returns the history's record files newest first. limit > 0
// keeps the newest limit of them and says how many further names it left out;
// 0 returns every one (pruning needs them all). A missing directory is no
// history, not an error.
func listRunHistory(stateDir string, limit int) (files []historyFile, more int, err error) {
	entries, err := os.ReadDir(filepath.Join(stateDir, runRecordDir, runHistoryDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, en := range entries {
		id, ok := strings.CutSuffix(en.Name(), ".json")
		if !ok || !validRunID(id) {
			continue // not ours: the app's logs, a temp file, a note
		}
		f := historyFile{ID: id}
		if info, ierr := en.Info(); ierr == nil && info.Mode().IsRegular() {
			f.Regular, f.Size = true, info.Size()
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return idNewer(files[i].ID, files[j].ID) })
	if limit > 0 && len(files) > limit {
		more = len(files) - limit
		files = files[:limit]
	}
	return files, more, nil
}

// idNewer orders ids by start time, then pid, newest first.
func idNewer(a, b string) bool {
	ta, pa, _ := strings.Cut(a, "-")
	tb, pb, _ := strings.Cut(b, "-")
	if ta != tb {
		return ta > tb
	}
	na, _ := strconv.Atoi(pa)
	nb, _ := strconv.Atoi(pb)
	return na > nb
}

// pruneRunHistory keeps the newest keep record files whose sizes together stay
// within maxBytes, and removes the rest. The newest is always kept, however
// large: the run that just finished is the one a reader came for. A file
// already gone (another process pruned it) is not an error. It returns how
// many it removed and the first real failure, having tried the rest.
func pruneRunHistory(stateDir string, keep int, maxBytes int64) (removed int, err error) {
	files, _, err := listRunHistory(stateDir, 0)
	if err != nil {
		return 0, err
	}
	var total int64
	for i, f := range files {
		total += f.Size
		if i < keep && (i == 0 || total <= maxBytes) {
			continue
		}
		total -= f.Size // it goes: it does not count against the ones after it
		switch rerr := os.Remove(runHistoryPath(stateDir, f.ID)); {
		case rerr == nil:
			removed++
		case errors.Is(rerr, fs.ErrNotExist):
		case err == nil:
			err = fmt.Errorf("prune run history: %w", rerr)
		}
	}
	return removed, err
}
