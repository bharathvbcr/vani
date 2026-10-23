package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Which log holds a run's output. The Mac app starts a manual run with its
// output sent to <state>/runs/manual.log and says so in VANI_RUN_LOG; the run
// records that path (runRecord.Log), and the app later keeps the file as
// <state>/runs/history/manual-<run id>.log when the next run replaces it
// (macos RunLogs.swift). `vani runs failures` follows the same two names, so the
// report names the log of that run and no other.
//
// An environment variable and not a flag, so a newer app still starts an older
// vani, which ignores it; the record of a run that did not say has no log, and
// the report then lists the logs that exist, as it always did.

const (
	// runLogEnv names the file a run's output goes to.
	runLogEnv = "VANI_RUN_LOG"
	// maxRunLogPath bounds the recorded path.
	maxRunLogPath = 1024
)

// checkRunLog validates the path a run was told to log to and returns the one
// to record. The path must be absolute, clean of ".." and control characters,
// and name a file inside <state>/runs once symlinks are resolved: the folder
// must exist, and the file may not be a link, so the record can never point a
// reader at a file outside the runs folder. The file itself need not exist yet.
func checkRunLog(stateDir, p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("it is empty")
	case len(p) > maxRunLogPath:
		return "", fmt.Errorf("it is longer than %d bytes", maxRunLogPath)
	case !filepath.IsAbs(p):
		return "", errors.New("it is not an absolute path")
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return "", errors.New("it holds a control character")
	}
	for _, el := range strings.Split(p, string(filepath.Separator)) {
		if el == ".." {
			return "", errors.New("it holds a .. element")
		}
	}
	p = filepath.Clean(p)
	runsDir, err := filepath.EvalSymlinks(filepath.Join(stateDir, runRecordDir))
	if err != nil {
		return "", fmt.Errorf("the runs folder cannot be resolved: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", fmt.Errorf("its folder cannot be resolved: %w", err)
	}
	if rel, err := filepath.Rel(runsDir, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("it is not inside the state folder's runs folder")
	}
	if st, err := os.Lstat(p); err == nil && !st.Mode().IsRegular() {
		return "", errors.New("it is not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return p, nil
}

// useRunLog reads VANI_RUN_LOG for a run-once and keeps the path the record
// will carry. A path that fails the check is said on stderr and not recorded:
// the run itself is not affected, only what the record says about its log.
func (e *env) useRunLog() {
	p, ok := os.LookupEnv(runLogEnv)
	if !ok || p == "" {
		return
	}
	checked, err := checkRunLog(e.stateDir, p)
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: %s ignored, so the run record names no log: %s\n", runLogEnv, oneLine(err.Error(), maxRunErrorRunes))
		return
	}
	e.runLog = checked
}

// runLogReport is the report's line for the log of a run that recorded one,
// and whether the run recorded one. The run's log is, in order:
//   - runs/history/manual-<run id>.log, where the app keeps it once a later run
//     replaces it;
//   - the recorded file itself, unless a later run recorded the same path: the
//     shared runs/manual.log belongs to the newest run that claims it, however
//     many runs that claim nothing (the daemon's) came after.
//
// Anything else is said as not kept; the line never names a log of another run.
// A recorded path that does not pass checkRunLog (a record edited by hand) is
// not followed.
func (e *env) runLogReport(ref runRef) (string, bool) {
	r := ref.Rec
	if r.Log == "" {
		return "", false
	}
	if p := filepath.Join(e.stateDir, runRecordDir, runHistoryDir, "manual-"+ref.ID+".log"); isRegular(p) {
		return logLine(p), true
	}
	checked, err := checkRunLog(e.stateDir, r.Log)
	if err != nil {
		return "Log: not followed, the recorded path is not a log in the runs folder", true
	}
	if e.logClaimedLater(ref, checked) {
		return "Log: not kept (" + checked + " held it until a later run replaced it)", true
	}
	if !isRegular(checked) {
		return "Log: not found (" + checked + ")", true
	}
	return logLine(checked), true
}

// logClaimedLater reports whether a run newer than ref recorded the same log
// path: last.json, or a history entry newer than ref (at most maxRunHistoryScan
// are listed, newest first, and the scan stops at ref). A record that cannot be
// read claims nothing.
func (e *env) logClaimedLater(ref runRef, path string) bool {
	claims := func(r *runRecord, err error) bool {
		return err == nil && r != nil && r.Log != "" && filepath.Clean(r.Log) == path
	}
	if last, err := readRunRecord(e.stateDir); err == nil && last != nil && idNewer(runID(*last), ref.ID) && claims(last, nil) {
		return true
	}
	files, _, err := listRunHistory(e.stateDir, maxRunHistoryScan)
	if err != nil {
		return false
	}
	for _, f := range files {
		if !idNewer(f.ID, ref.ID) {
			break
		}
		if f.Regular && claims(readRunRecordFile(runHistoryPath(e.stateDir, f.ID))) {
			return true
		}
	}
	return false
}

func isRegular(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

func logLine(p string) string {
	st, err := os.Lstat(p)
	if err != nil {
		return "Log: " + p
	}
	return fmt.Sprintf("Log: %s (%d bytes, last written %s)", p, st.Size(), st.ModTime().UTC().Format(time.RFC3339))
}
