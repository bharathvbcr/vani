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
)

// The daemon holds the ledger's lock for its whole life, so while it runs a
// `vani run-once` is refused at once. A manual run then goes through the daemon
// instead: `vani run-request` writes the run's options to <state dir>/runs/
// request.json, and the daemon, which polls for it, runs it itself between its
// other ledger work (the scheduled run, the inbox poll), never beside it.
//
// The daemon announces itself in <state dir>/runs/daemon.json (pid and start
// time) once it holds the ledger, and removes it when it exits. run-request
// refuses when that file names no live process: nobody would pick the request
// up. A pid can be reused after a crash; a request no daemon picks up is then
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
		return nil, fmt.Errorf("%s changed while it was opened", filepath.Base(path))
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

// liveDaemon returns the daemon record when its process lives, else an error
// wrapping errNoDaemon that says why none is known.
func liveDaemon(stateDir string) (daemonRecord, error) {
	d, err := readDaemonRecord(stateDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return daemonRecord{}, fmt.Errorf("%w: no daemon has announced itself in %s", errNoDaemon, runsPath(stateDir, daemonRecordFile))
	case err != nil:
		return daemonRecord{}, fmt.Errorf("%w: %w", errNoDaemon, err)
	case !pidAlive(d.PID):
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
// is removed and reported, and no run is made. The error is the run's, or why
// the request was dropped; nil when there was none.
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
	// Claimed under the mutex: a request waiting behind the scheduled run
	// stays pending, as status shows it, until its own run starts.
	if err := os.Rename(path, claimed); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("claiming it: %w", err)
	}
	defer func() {
		if rerr := os.Remove(claimed); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing the claimed request: %w", rerr))
		}
	}()
	req, err := readRunRequest(claimed, now())
	if err != nil {
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
	return run(ctx, req.Options)
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
	asJSON := fs.Bool("json", false, "one JSON object on stdout: {requested_at, options, daemon_pid}, or {error}")
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
	return e.jobsReply(*asJSON, reply, err, func() {
		fmt.Fprintf(e.stdout, "asked the daemon (pid %d) for a manual run; `vani status` shows it once it starts\n", reply.DaemonPID)
	})
}

func (e *env) requestRun(opts runOptions) (runRequestReply, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return runRequestReply{}, err
	}
	if err := checkRunOptions(opts, cfg.Watchlist); err != nil {
		return runRequestReply{}, err
	}
	d, err := liveDaemon(e.stateDir)
	if err != nil {
		return runRequestReply{}, fmt.Errorf("%w; with no daemon running, `vani run-once` makes the run itself", err)
	}
	at := now()
	path := runsPath(e.stateDir, runRequestFile)
	switch old, err := readRunRequest(path, at); {
	case err == nil:
		return runRequestReply{}, fmt.Errorf("a run request from %s is already waiting for the daemon", old.RequestedAt.UTC().Format(time.RFC3339))
	case errors.Is(err, os.ErrNotExist):
	default:
		// Stale or unreadable: replaced below (a rename replaces a symlink
		// itself, never its target), and the daemon would drop it anyway.
		fmt.Fprintf(e.stderr, "vani: replacing the waiting run request: %v\n", err)
	}
	req := runRequest{V: runRequestVersion, RequestedAt: at, Options: opts}
	raw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return runRequestReply{}, err
	}
	if len(raw) > maxRunRequest {
		return runRequestReply{}, fmt.Errorf("run request exceeds %d bytes", maxRunRequest)
	}
	if err := atomicfile.Write(path, raw); err != nil {
		return runRequestReply{}, err
	}
	return runRequestReply{RequestedAt: at, Options: opts, DaemonPID: d.PID}, nil
}
