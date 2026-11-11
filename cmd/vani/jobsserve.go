package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// `vani jobs serve` answers the Jobs views (list, stats, gaps, show) for the
// app from one long-lived process, so a sort, filter or view change does not
// pay a process start, a checkpoint load and a facts store load every time.
//
// The wire is Manvi's serve contract (manvi/serve/protocol.go), copied, not
// imported: NDJSON on stdin and stdout, one request per line,
//
//	{"id":"7","op":"list","params":{"args":["--view","new","--sort","company"]}}
//
// and exactly one response line per request,
//
//	{"id":"7","ok":true,"result":{…}}       the command's --json reply, verbatim
//	{"id":"7","ok":false,"error":{"code":"…","message":"…","retryable":false}}
//
// params.args are the flags of `vani jobs <op>` (without --json, which serve
// adds), so every request is parsed, checked and answered by the same code as
// the command, and a result is exactly the reply `vani jobs <op> --json` prints.
// Errors are values: a failed request never ends the session. Unknown fields are
// ignored. The process exits when stdin closes, once every accepted request has
// its response.
//
// What serve keeps between requests is the ledger Reader (advanced to the
// events file's end before each read) and the text facts store
// (jobsResident); marks, config, the clock and every other store are read per
// request, as the command reads them, so a reply is never staler than the
// command's would be.

// jobsServeProtocol is the wire's major version: it moves only when an existing
// field changes meaning (Manvi's rule).
const jobsServeProtocol = 1

const (
	// serveMaxLine bounds a request line; a longer one is refused and the
	// session lives on.
	serveMaxLine = 64 << 10
	// serveMaxRunning bounds the requests being computed at once, superseded
	// and timed-out ones included (their work runs to its end; only its answer
	// is dropped). A request past it is refused E_BUSY, retryable.
	serveMaxRunning = 4
	// serveDeadline bounds how long a request waits for its answer. A first
	// list after a vocabulary change derives every posting's facts (several
	// seconds of CPU at 60k postings, more on a loaded machine), so it is
	// generous; hello reports it so a host sizes its own timeout from it.
	serveDeadline = 30 * time.Second
)

// Error codes. A host must treat a code it does not know as a generic failure.
const (
	serveErrBadRequest = "E_BAD_REQUEST" // malformed line, unknown op, bad params or flags; never retryable
	serveErrTooLarge   = "E_TOO_LARGE"   // a line past serveMaxLine
	serveErrBusy       = "E_BUSY"        // serveMaxRunning requests are running; retryable
	serveErrTimeout    = "E_TIMEOUT"     // no answer within serveDeadline; retryable
	serveErrSuperseded = "E_SUPERSEDED"  // a newer request of the same op replaced it
	serveErrInternal   = "E_INTERNAL"    // a defect in vani (a panic, a reply that is not JSON)
	serveErrCommand    = "E_COMMAND"     // the command failed; message is its error
)

type serveRequest struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

type serveResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *serveError     `json:"error,omitempty"`
}

type serveError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	// Reply is the command's own failure reply ({"error","code"} or a view
	// with its error set), when it printed one: what `vani jobs <op> --json`
	// would have printed.
	Reply json.RawMessage `json:"reply,omitempty"`
}

type serveArgs struct {
	Args []string `json:"args"`
}

// serveHello is hello's result.
type serveHello struct {
	Protocol int                     `json:"protocol"`
	Ops      []string                `json:"ops"`
	OpLimits map[string]serveOpLimit `json:"op_limits"`
	// Supersedes names the ops for which a newer request answers every older
	// one still running E_SUPERSEDED, at once.
	Supersedes []string `json:"supersedes"`
}

type serveOpLimit struct {
	DeadlineMS int64 `json:"deadline_ms"`
}

// serveOps are the Jobs view commands serve answers, by op.
var serveOps = map[string]func(*env, []string) error{
	"list":  (*env).jobsList,
	"stats": (*env).jobsStatsCmd,
	"gaps":  (*env).jobsGapsCmd,
	"show":  (*env).jobsShow,
}

// serveSupersedes are the ops whose newer request replaces an older one: the
// views a host re-asks for as its controls change. show is per job and is not.
var serveSupersedes = []string{"list", "stats", "gaps"}

func (e *env) jobsServe(args []string) error {
	fs := e.newFlags("jobs serve")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("jobs serve: takes no arguments (requests arrive on stdin)")
	}
	setServeMemoryLimit(os.Getenv)
	res, err := e.openJobsResident()
	if err != nil {
		return err
	}
	defer res.Close()
	s := newJobsServer(e, res, e.stdout)
	return s.serve(serveStdin)
}

// serveMemoryLimit is serve's soft heap limit (runtime/debug.SetMemoryLimit)
// when GOMEMLIMIT does not set one. Measured on a 60k-posting state
// (docs/BENCHMARKS.md, 2026-11-11): serve's live heap is about 480 MiB, and
// without a limit the collector lets the process peak at 0.91-0.95 GB. At
// 800 MiB the peak was 0.84-0.85 GB for no measurable CPU; at 650 MiB,
// 0.73 GB for about a third more CPU, which is why the default is not lower.
// It is soft: past it the collector runs more often (the runtime caps that at
// about half the CPU), it never refuses memory. Set GOMEMLIMIT to choose
// another, or GOMEMLIMIT=off for none.
const serveMemoryLimit = 800 << 20

// setServeMemoryLimit sets serveMemoryLimit unless getenv("GOMEMLIMIT") names
// one (the runtime has read it). It reports whether it set the limit.
func setServeMemoryLimit(getenv func(string) string) bool {
	if getenv("GOMEMLIMIT") != "" {
		return false
	}
	debug.SetMemoryLimit(serveMemoryLimit)
	return true
}

// serveStdin is the request stream; a test replaces it.
var serveStdin io.Reader = os.Stdin

type jobsServer struct {
	base     *env
	res      *jobsResident
	deadline time.Duration

	outMu sync.Mutex
	out   *bufio.Writer
	errw  io.Writer // stderr, one line at a time

	mu      sync.Mutex
	running int
	pending sync.WaitGroup // accepted requests not yet answered
	latest  map[string]*serveCall
}

// serveCall is one accepted request: answered exactly once (answer).
type serveCall struct {
	id   string
	once sync.Once
	done chan struct{}
}

func newJobsServer(e *env, res *jobsResident, out io.Writer) *jobsServer {
	return &jobsServer{base: e, res: res, deadline: serveDeadline, out: bufio.NewWriter(out),
		errw: &lockedWriter{w: e.stderr}, latest: map[string]*serveCall{}}
}

// serve reads requests until r ends, then waits for every accepted request's
// answer. A read error other than the end of input is returned.
func (s *jobsServer) serve(r io.Reader) error {
	br := bufio.NewReaderSize(r, serveMaxLine)
	defer s.pending.Wait()
	for {
		line, err := readServeLine(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		var big lineTooLarge
		if errors.As(err, &big) {
			s.write(serveResponse{ID: recoverServeID(big.head), Error: &serveError{Code: serveErrTooLarge,
				Message: fmt.Sprintf("request line exceeds %d bytes", serveMaxLine)}})
			continue
		}
		if err != nil {
			return fmt.Errorf("jobs serve: read a request: %w", err)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		s.accept(line)
	}
}

// accept answers one request line: at once for a malformed one, hello or an
// unknown op, and from a goroutine of its own for a view.
func (s *jobsServer) accept(line []byte) {
	var req serveRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(serveResponse{Error: badServeRequest("malformed request line: %v", err)})
		return
	}
	if req.ID == "" {
		s.write(serveResponse{Error: badServeRequest("request has no id; a response the host cannot route looks like a hang")})
		return
	}
	if req.Op == "hello" {
		s.write(s.hello(req.ID))
		return
	}
	run, ok := serveOps[req.Op]
	if !ok {
		s.write(serveResponse{ID: req.ID, Error: badServeRequest("unknown op %q", req.Op)})
		return
	}
	var p serveArgs
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.write(serveResponse{ID: req.ID, Error: badServeRequest("params: %v", err)})
			return
		}
	}
	if bad := slices.IndexFunc(p.Args, isJSONFlag); bad >= 0 {
		s.write(serveResponse{ID: req.ID, Error: badServeRequest("args: %q: serve always replies in JSON", p.Args[bad])})
		return
	}
	s.mu.Lock()
	if s.running >= serveMaxRunning {
		s.mu.Unlock()
		s.write(serveResponse{ID: req.ID, Error: &serveError{Code: serveErrBusy, Retryable: true,
			Message: fmt.Sprintf("%d requests are already running", serveMaxRunning)}})
		return
	}
	s.running++
	call := &serveCall{id: req.ID, done: make(chan struct{})}
	s.pending.Add(1)
	var older *serveCall
	if slices.Contains(serveSupersedes, req.Op) {
		older = s.latest[req.Op]
		s.latest[req.Op] = call
	}
	s.mu.Unlock()
	if older != nil {
		s.answer(older, serveResponse{ID: older.id, Error: &serveError{Code: serveErrSuperseded,
			Message: fmt.Sprintf("request %s replaced it", req.ID)}})
	}
	go s.compute(call, req.Op, run, p.Args)
	go func() {
		t := time.NewTimer(s.deadline)
		defer t.Stop()
		select {
		case <-call.done:
		case <-t.C:
			s.answer(call, serveResponse{ID: call.id, Error: &serveError{Code: serveErrTimeout, Retryable: true,
				Message: fmt.Sprintf("no answer within %s", s.deadline)}})
		}
	}()
}

// compute runs the command for one request and answers it, unless a newer
// request or the deadline answered it first.
func (s *jobsServer) compute(call *serveCall, op string, run func(*env, []string) error, args []string) {
	defer func() {
		s.mu.Lock()
		s.running--
		if s.latest[op] == call {
			delete(s.latest, op)
		}
		s.mu.Unlock()
	}()
	s.answer(call, s.runOp(call.id, run, args))
}

// runOp runs one view command as `vani jobs <op> --json` would, in an env of
// its own over the resident Reader and facts store.
func (s *jobsServer) runOp(id string, run func(*env, []string) error, args []string) (resp serveResponse) {
	resp.ID = id
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(s.errw, "vani: jobs serve: internal defect: %v\n%s\n", p, debug.Stack())
			resp = serveResponse{ID: id, Error: &serveError{Code: serveErrInternal, Message: fmt.Sprintf("internal defect: %v", p)}}
		}
	}()
	var stdout bytes.Buffer
	e := &env{configPath: s.base.configPath, stateDir: s.base.stateDir, paths: s.base.paths,
		stdout: &stdout, stderr: s.errw, resident: s.res}
	err := run(e, append([]string{"--json"}, args...)) // flags stop at the first positional (show KEY)
	reply := bytes.TrimSpace(stdout.Bytes())
	switch {
	case err == nil && json.Valid(reply):
		resp.OK, resp.Result = true, json.RawMessage(reply)
	case err == nil:
		resp.Error = &serveError{Code: serveErrInternal, Message: "the command's reply is not one JSON value"}
	case errors.Is(err, errAlreadyReported) && json.Valid(reply):
		resp.Error = &serveError{Code: serveErrCommand, Message: replyMessage(reply), Reply: json.RawMessage(reply)}
	case errors.As(err, new(usageError)) || errors.Is(err, flag.ErrHelp):
		resp.Error = badServeRequest("%v", err)
	default:
		resp.Error = &serveError{Code: serveErrCommand, Message: err.Error()}
	}
	return resp
}

// replyMessage is a failure reply's "error" text.
func replyMessage(reply []byte) string {
	var r struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(reply, &r) == nil && r.Error != "" {
		return r.Error
	}
	return "the command failed"
}

func (s *jobsServer) hello(id string) serveResponse {
	h := serveHello{Protocol: jobsServeProtocol, OpLimits: map[string]serveOpLimit{}, Supersedes: serveSupersedes}
	for op := range serveOps {
		h.Ops = append(h.Ops, op)
		h.OpLimits[op] = serveOpLimit{DeadlineMS: s.deadline.Milliseconds()}
	}
	h.Ops = append(h.Ops, "hello")
	slices.Sort(h.Ops)
	b, err := json.Marshal(h)
	if err != nil {
		return serveResponse{ID: id, Error: &serveError{Code: serveErrInternal, Message: err.Error()}}
	}
	return serveResponse{ID: id, OK: true, Result: b}
}

// answer writes call's one response; later answers for it are dropped.
func (s *jobsServer) answer(call *serveCall, resp serveResponse) {
	call.once.Do(func() {
		s.write(resp)
		close(call.done)
		s.pending.Done()
	})
}

// write writes one response line, encoded as the commands encode their replies
// (writeJSON: no HTML escaping), so a result's bytes are the command's.
func (s *jobsServer) write(resp serveResponse) {
	var b bytes.Buffer
	if err := writeJSON(&b, resp); err != nil {
		b.Reset()
		writeJSON(&b, serveResponse{ID: resp.ID, Error: &serveError{Code: serveErrInternal, Message: "response did not encode: " + err.Error()}})
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(b.Bytes())
	if err := s.out.Flush(); err != nil {
		fmt.Fprintf(s.errw, "vani: jobs serve: write a response: %v\n", err)
	}
}

func badServeRequest(format string, a ...any) *serveError {
	return &serveError{Code: serveErrBadRequest, Message: fmt.Sprintf(format, a...)}
}

// isJSONFlag is any spelling of --json, which serve adds itself.
func isJSONFlag(a string) bool {
	name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
	return strings.HasPrefix(a, "-") && name == "json"
}

// lineTooLarge is a request line past serveMaxLine; head is its start, from
// which the id may be recovered.
type lineTooLarge struct{ head []byte }

func (lineTooLarge) Error() string { return "request line too large" }

// readServeLine reads one line without its newline. A line longer than
// serveMaxLine is read to its end and dropped (lineTooLarge), so the next
// line is read whole. A last line with no newline is a line.
func readServeLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	var head []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !over {
			if len(line)+len(chunk) > serveMaxLine+1 {
				over, head = true, append([]byte(nil), append(line, chunk...)[:min(len(line)+len(chunk), 4096)]...)
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case err == nil:
			if over {
				return nil, lineTooLarge{head}
			}
			return bytes.TrimSuffix(line, []byte("\n")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && !over && len(line) > 0:
			return line, nil
		case errors.Is(err, io.EOF) && over:
			return nil, lineTooLarge{head}
		default:
			return nil, err
		}
	}
}

// recoverServeID finds "id" in the head of a line too large to parse, so its
// refusal can still be routed; "" when it cannot.
func recoverServeID(head []byte) string {
	i := bytes.Index(head, []byte(`"id"`))
	if i < 0 {
		return ""
	}
	rest := bytes.TrimLeft(head[i+4:], " \t")
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	rest = bytes.TrimLeft(rest[1:], " \t")
	var id string
	dec := json.NewDecoder(bytes.NewReader(rest))
	if dec.Decode(&id) != nil {
		return ""
	}
	return id
}

// lockedWriter writes each Write whole, so lines from concurrent requests do
// not interleave.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
