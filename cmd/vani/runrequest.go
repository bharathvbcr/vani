package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// The daemon holds the ledger's lock for its whole life, so while it runs a
// `vani run-once` is refused at once. A manual run then goes through the daemon
// instead: `vani run-request` writes the run's options to <state dir>/runs/
// request.json, and the daemon, which polls for it, runs it itself between its
// other ledger work (the scheduled run, the inbox poll), never beside it.
//
// The daemon announces itself in <state dir>/runs/daemon.json (pid and start
// time) once it holds the ledger, and removes it when it exits. run-request
// refuses when that file does not name this vani process. A pid can be reused
// after a crash; a request no daemon picks up is then
// stale after runRequestMaxAge, and the next daemon drops it.
//
// The daemon claims a request by renaming it to request.claimed.json under the
// ledger mutex, and removes that file once the run is over. While it exists,
// `vani status --json` reports the request as claimed, so a reader never sees
// a request vanish before its run has been recorded. A claimed request left by
// a daemon that died is removed at the next daemon's start and never re-run.

const (
	runRequestVersion = 1
	runRequestFile    = "request.json"
	runClaimedFile    = "request.claimed.json"
	daemonRecordFile  = "daemon.json"
	// maxRunRequest bounds a request file: the options are at most
	// maxRunBoards board refs.
	maxRunRequest = 64 << 10
	maxDaemonFile = 4 << 10
	// runRequestMaxAge is how long a request waits for a daemon to pick it up.
	runRequestMaxAge = time.Hour
	// runRequestFutureSlack is how far ahead of the clock a request's time may
	// be (clock adjustments); further ahead it would never go stale.
	runRequestFutureSlack = 5 * time.Minute
)

// requestPollInterval is how often the daemon looks for a run request. A
// variable only so tests need not wait.
var requestPollInterval = 3 * time.Second

// requestWriteGate runs after a read has decided the request file will be
// replaced (missing, stale, or corrupt) and before that write is locked.
// Nil does nothing. Tests hold callers here so both pass the unlocked check.
var requestWriteGate func()

// runRequest is request.json: a manual run the daemon is asked to make.
type runRequest struct {
	V           int        `json:"v"`
	RequestedAt time.Time  `json:"requested_at"`
	Options     runOptions `json:"options"`
}

// daemonRecord is daemon.json: which process is the daemon.
type daemonRecord struct {
	V         int       `json:"v"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Model is the decision model's state in this daemon (daemonmodel.go);
	// absent when the config names none or the daemon predates the field.
	Model *daemonModel `json:"model,omitempty"`
}

var (
	errRequestStale = errors.New("run request is stale")
	errNoDaemon     = errors.New("no running vani daemon")
)

func runsPath(stateDir, name string) string { return filepath.Join(stateDir, runRecordDir, name) }

// errChangedWhileOpen is a file replaced between the inspection and the read.
var errChangedWhileOpen = errors.New("changed while it was opened")

// readBoundedFile reads a regular file of at most max bytes. A symlink, a
// directory or another special file is refused without being opened, and the
// file opened must be the one inspected. A missing file is os.ErrNotExist.
func readBoundedFile(path string, max int) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		// The leaf was replaced between the inspection and this read. Callers
		// that expect a quiet file treat it as a failure; the live snapshot
		// retries, because its writer renames a new file into place often.
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), errChangedWhileOpen)
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > max {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), max)
	}
	return raw, nil
}

// decodeStrict decodes exactly one JSON object with no unknown keys: a request
// a newer vani wrote must not run as a different, bigger run here.
func decodeStrict(raw []byte, v any) error { return decodeObject(raw, v, true) }

// decodeObject decodes exactly one JSON object; strict also refuses unknown keys.
func decodeObject(raw []byte, v any, strict bool) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// readRunRequest reads a request file and checks it as of at. A request older
// than runRequestMaxAge is returned with errRequestStale; anything unreadable
// or invalid is another error. A missing file is os.ErrNotExist. The daemon and
// status both read through here, so they never disagree on what is pending.
func readRunRequest(path string, at time.Time) (runRequest, error) {
	raw, err := readBoundedFile(path, maxRunRequest)
	if err != nil {
		return runRequest{}, err
	}
	var r runRequest
	if err := decodeStrict(raw, &r); err != nil {
		return runRequest{}, fmt.Errorf("run request is corrupt: %w", err)
	}
	switch {
	case r.V != runRequestVersion:
		return runRequest{}, fmt.Errorf("run request version %d, want %d", r.V, runRequestVersion)
	case r.RequestedAt.IsZero():
		return runRequest{}, errors.New("run request has no time")
	case r.RequestedAt.After(at.Add(runRequestFutureSlack)):
		return runRequest{}, fmt.Errorf("run request is dated %s, in the future", r.RequestedAt.UTC().Format(time.RFC3339))
	case len(r.Options.Boards) > maxRunBoards:
		return runRequest{}, fmt.Errorf("run request names more than %d boards", maxRunBoards)
	case at.Sub(r.RequestedAt) > runRequestMaxAge:
		return r, fmt.Errorf("%w: asked for at %s, more than %s ago", errRequestStale, r.RequestedAt.UTC().Format(time.RFC3339), runRequestMaxAge)
	}
	return r, nil
}

// writeDaemonRecord announces this process as the daemon.
func writeDaemonRecord(stateDir string, pid int, at time.Time) error {
	return writeDaemonAnnouncement(stateDir, daemonRecord{V: runRequestVersion, PID: pid, StartedAt: at})
}

// writeDaemonAnnouncement writes rec as daemon.json.
func writeDaemonAnnouncement(stateDir string, rec daemonRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(runsPath(stateDir, daemonRecordFile), raw)
}

// removeDaemonRecord removes daemon.json if it still names pid: a daemon
// started since (after this one lost the ledger) keeps its own.
func removeDaemonRecord(stateDir string, pid int) error {
	path := runsPath(stateDir, daemonRecordFile)
	d, err := readDaemonRecord(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && d.PID != pid {
		return nil
	}
	return os.Remove(path)
}

// readDaemonRecord reads daemon.json. A missing file is os.ErrNotExist.
func readDaemonRecord(stateDir string) (daemonRecord, error) {
	raw, err := readBoundedFile(runsPath(stateDir, daemonRecordFile), maxDaemonFile)
	if err != nil {
		return daemonRecord{}, err
	}
	var d daemonRecord
	// Unlike a request, daemon.json runs nothing: a field a newer vani added
	// must not make its daemon read as corrupt (and so as not running).
	if err := decodeObject(raw, &d, false); err != nil {
		return daemonRecord{}, fmt.Errorf("daemon record is corrupt: %w", err)
	}
	if d.V != runRequestVersion || d.PID <= 0 || d.StartedAt.IsZero() {
		return daemonRecord{}, errors.New("daemon record is incomplete")
	}
	return d, nil
}

// liveDaemon returns the daemon record when its pid is this vani process, else
// an error wrapping errNoDaemon that says why none is known. Liveness uses the
// record's own started_at, not the file's mtime.
func liveDaemon(stateDir string) (daemonRecord, error) {
	d, err := readDaemonRecord(stateDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return daemonRecord{}, fmt.Errorf("%w: no daemon has announced itself in %s", errNoDaemon, runsPath(stateDir, daemonRecordFile))
	case err != nil:
		return daemonRecord{}, fmt.Errorf("%w: %w", errNoDaemon, err)
	}
	if !daemonIsLive(d) {
		return daemonRecord{}, fmt.Errorf("%w: the daemon recorded as pid %d is gone (it did not exit cleanly)", errNoDaemon, d.PID)
	}
	return d, nil
}

// clearClaimedRequest removes a claimed request an earlier daemon left: its
// run never finished, and a run is never repeated without being asked again.
func (e *env) clearClaimedRequest() {
	path := runsPath(e.stateDir, runClaimedFile)
	if _, err := os.Lstat(path); err != nil {
		return
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(e.stderr, "vani: run request: removing the one an earlier daemon claimed: %v\n", err)
		return
	}
	fmt.Fprintln(e.stderr, "vani: run request: an earlier daemon claimed a manual run and did not finish it; it is not run again")
}

// pollRequestLoop serves run requests every requestPollInterval until ctx
// ends. It returns only a run error wrapping daemon.ErrFatal (the ledger is
// unusable, so the daemon must exit); every other failure is reported and the
// loop goes on.
func (e *env) pollRequestLoop(ctx context.Context, run func(context.Context, runOptions) error, mu *sync.Mutex) error {
	t := time.NewTicker(requestPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := e.serveRunRequest(ctx, run, mu); err != nil {
			if errors.Is(err, daemon.ErrFatal) {
				return err
			}
			if ctx.Err() == nil {
				fmt.Fprintf(e.stderr, "vani: run request: %v\n", err)
			}
		}
	}
}

// serveRunRequest runs the pending request, if any, holding mu for the claim
// and the run. A request that cannot be honoured (unreadable, stale, invalid)
// is removed and reported, and no run is made. One the daemon claimed but must
// restart before running is put back (requeueRequest). The error is the run's,
// or why the request was dropped; nil when there was none.
func (e *env) serveRunRequest(ctx context.Context, run func(context.Context, runOptions) error, mu *sync.Mutex) (err error) {
	path, claimed := runsPath(e.stateDir, runRequestFile), runsPath(e.stateDir, runClaimedFile)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil {
		return nil // shutting down: the request waits for the next daemon
	}
	// A note from an earlier job on this env is not this request's restart.
	takeConfigRestart(e)
	// A newer vani may have written options this binary does not know (a
	// scope), which its strict decode would drop as corrupt. A replaced binary
	// restarts first, as a run's reload does, and the request stays unclaimed.
	// A missing binary is not a replacement: replaced returns false for a stat
	// error. It is the same restart, and the request stays unclaimed.
	if e.binary.replaced(e.stderr) {
		e.requestConfigRestart()
		fmt.Fprintln(e.stderr, "vani: run request: not run yet, the daemon is restarting onto the replaced binary; the request waits for the next daemon")
		return nil
	}
	if e.binaryMissing() {
		e.requestConfigRestart()
		fmt.Fprintln(e.stderr, "vani: run request: not run yet, the daemon's binary is missing; the request waits for the next daemon")
		return nil
	}
	// Claimed under the mutex: a request waiting behind the scheduled run
	// stays pending, as status shows it, until its own run starts.
	if err := os.Rename(path, claimed); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("claiming it: %w", err)
	}
	defer func() {
		// Link and rename both failed. The claimed file is the only copy.
		if errors.Is(err, errClaimKept) {
			return
		}
		if rerr := os.Remove(claimed); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing the claimed request: %w", rerr))
		}
	}()
	at := now()
	// The binary can disappear between the pre-claim check and the claim.
	// Missing is still not "replaced", so look again before the run.
	if e.binaryMissing() {
		e.requestConfigRestart()
		fmt.Fprintln(e.stderr, "vani: run request: not run yet, the daemon's binary disappeared after the claim; the request waits for the next daemon")
		return e.requeueRequest(claimed, path)
	}
	req, err := readRunRequest(claimed, at)
	if err != nil {
		if requestStillValidDespiteUnknownFields(claimed, at) {
			e.requestConfigRestart()
			fmt.Fprintln(e.stderr, "vani: run request: not run yet, this binary does not know a field in the request; the request waits for the next daemon")
			return e.requeueRequest(claimed, path)
		}
		return fmt.Errorf("dropped, no run made: %w", err)
	}
	// The boards are checked against the watchlist the run itself reloads
	// (dailySteps), so a board removed since the request is a recorded,
	// failed run rather than a request that silently vanished.
	bounds := req.Options
	bounds.Boards = nil
	if err := checkRunOptions(bounds, nil); err != nil {
		return fmt.Errorf("dropped, no run made: %w", err)
	}
	fmt.Fprintf(e.stderr, "vani: run request: running the manual run asked for at %s\n", req.RequestedAt.UTC().Format(time.RFC3339))
	defer func() {
		if p := recover(); p != nil {
			writePanic(e.stderr, "vani: manual run panicked: ", p)
			err = fmt.Errorf("manual run panicked: %s", panicText(p))
		}
	}()
	err = run(ctx, req.Options)
	if errors.Is(err, daemon.ErrFatal) {
		return err
	}
	// A restart is not a run: dailyJob's reload found a replaced binary or a
	// pinned config change and stopped before the pipeline (returning nil when
	// it cancelled the daemon). The cause can only be this run's own reload,
	// because every other reloadConfig caller holds mu. A signal that cancelled
	// first leaves the cause as the signal, and dailyJob returns nil; the note
	// from noteConfigRestart is that restart. The request goes back for the
	// daemon that starts next, instead of vanishing with the claim.
	sawRestart := takeConfigRestart(e)
	if errors.Is(err, errConfigRestart) || errors.Is(context.Cause(ctx), errConfigRestart) || sawRestart {
		return e.requeueRequest(claimed, path)
	}
	return err
}

// linkRequest and renameRequest put a claimed request back. Tests replace
// them. Link keeps a newer pending file (os.ErrExist). Rename is the fallback
// when the volume cannot link.
var (
	linkRequest   = os.Link
	renameRequest = os.Rename
)

// errClaimKept means the only copy of a claim could not be put back, so the
// caller's deferred remove must leave the claimed file where it is.
var errClaimKept = errors.New("claimed request kept: putting it back failed")

// requeueRequest puts a claimed request back as the pending one; the caller's
// deferred remove then drops the claimed name. A request written while this one
// was claimed is the owner's latest ask and is kept, and the claimed one is
// dropped rather than run as well. The link fails rather than replace it.
func (e *env) requeueRequest(claimed, path string) error {
	if err := linkRequest(claimed, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintln(e.stderr, "vani: run request: not run, the daemon is restarting; a newer request waits for the next daemon")
			return nil
		}
		// A newer pending file wins even when Link failed for another reason.
		if _, statErr := os.Lstat(path); statErr == nil {
			fmt.Fprintln(e.stderr, "vani: run request: not run, the daemon is restarting; a newer request waits for the next daemon")
			return nil
		}
		// Link failed and there is no newer file. Rename moves the only copy
		// back. If that fails too, leave the claimed file for the caller.
		if rerr := renameRequest(claimed, path); rerr != nil {
			fmt.Fprintf(e.stderr, "vani: run request: not run, the daemon is restarting; the claimed request was kept because putting it back failed: link: %v; rename: %v\n", err, rerr)
			return fmt.Errorf("%w: link: %v; rename: %w", errClaimKept, err, rerr)
		}
		fmt.Fprintln(e.stderr, "vani: run request: not run yet, the daemon is restarting; the request was moved back because linking it failed")
		return nil
	}
	fmt.Fprintln(e.stderr, "vani: run request: not run yet, the daemon is restarting; the request waits for the next daemon")
	return nil
}

// binaryMissing reports that the daemon's own binary is gone. A nil watch or
// an empty path means the check is off. Any other stat error, including a path
// that is not a regular file, is not missing.
func (e *env) binaryMissing() bool {
	if e == nil || e.binary == nil || e.binary.path == "" {
		return false
	}
	_, err := statExecutable(e.binary.path)
	return errors.Is(err, os.ErrNotExist)
}

// requestStillValidDespiteUnknownFields reports that strict decode failed only
// because of keys this binary does not know, and the fields it can read are
// inside the version, time, board-count and option bounds. A syntax error,
// trailing data, a bad version, a stale or future time, or too many boards is
// not still valid.
func requestStillValidDespiteUnknownFields(path string, at time.Time) bool {
	raw, err := readBoundedFile(path, maxRunRequest)
	if err != nil {
		return false
	}
	var strict runRequest
	if decodeStrict(raw, &strict) == nil {
		return false
	}
	var loose runRequest
	if decodeObject(raw, &loose, false) != nil {
		return false
	}
	if loose.V != runRequestVersion || loose.RequestedAt.IsZero() {
		return false
	}
	if loose.RequestedAt.After(at.Add(runRequestFutureSlack)) {
		return false
	}
	if len(loose.Options.Boards) > maxRunBoards {
		return false
	}
	if at.Sub(loose.RequestedAt) > runRequestMaxAge {
		return false
	}
	bounds := loose.Options
	bounds.Boards = nil
	return checkRunOptions(bounds, nil) == nil
}

// runRequestReply is `vani run-request --json`'s answer.
type runRequestReply struct {
	RequestedAt time.Time  `json:"requested_at"`
	Options     runOptions `json:"options"`
	DaemonPID   int        `json:"daemon_pid"`
}

// cmdRunRequest asks the running daemon for a manual run. It never opens the
// ledger: the daemon makes the run, with its own keys and its own lock.
func (e *env) cmdRunRequest(args []string) error {
	fs := e.newFlags("run-request")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: {requested_at, options, daemon_pid}, or {error}")
	opts, boards := runOptionFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	opts.Boards = *boards
	reply, err := e.requestRun(*opts)
	if err != nil && *asJSON && errors.As(err, new(usageError)) {
		// Under --json an option the watchlist cannot satisfy is a reply, not a
		// usage error: exit 2 means "this vani is too old" to the app.
		err = errors.New(err.Error())
	}
	return e.reply(*asJSON, reply, err, func() {
		fmt.Fprintf(e.stdout, "asked the daemon (pid %d) for a manual run; `vani status` shows it once it starts\n", reply.DaemonPID)
	})
}

func (e *env) requestRun(opts runOptions) (reply runRequestReply, err error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return runRequestReply{}, err
	}
	if err = checkRunOptions(opts, cfg.Watchlist); err != nil {
		return runRequestReply{}, err
	}
	d, err := liveDaemon(e.stateDir)
	if err != nil {
		return runRequestReply{}, fmt.Errorf("%w; with no daemon running, `vani run-once` makes the run itself", err)
	}
	at := now()
	path := runsPath(e.stateDir, runRequestFile)
	switch old, rerr := readRunRequest(path, at); {
	case rerr == nil:
		return runRequestReply{}, fmt.Errorf("a run request from %s is already waiting for the daemon", old.RequestedAt.UTC().Format(time.RFC3339))
	case errors.Is(rerr, os.ErrNotExist):
	default:
		// Stale or unreadable: replaced below (a rename replaces a symlink
		// itself, never its target), and the daemon would drop it anyway.
		fmt.Fprintf(e.stderr, "vani: replacing the waiting run request: %v\n", rerr)
	}
	if requestWriteGate != nil {
		requestWriteGate()
	}
	// Both callers can pass the read above. The lock and the re-read decide
	// which one writes; the other finds the winner already waiting.
	lockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(lockCtx, path+".lock")
	if err != nil {
		return runRequestReply{}, err
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if old, rerr := readRunRequest(path, at); rerr == nil {
		return runRequestReply{}, fmt.Errorf("a run request from %s is already waiting for the daemon", old.RequestedAt.UTC().Format(time.RFC3339))
	}
	req := runRequest{V: runRequestVersion, RequestedAt: at, Options: opts}
	raw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return runRequestReply{}, err
	}
	if len(raw) > maxRunRequest {
		return runRequestReply{}, fmt.Errorf("run request exceeds %d bytes", maxRunRequest)
	}
	if err = atomicfile.Write(path, raw); err != nil {
		return runRequestReply{}, err
	}
	return runRequestReply{RequestedAt: at, Options: opts, DaemonPID: d.PID}, nil
}
