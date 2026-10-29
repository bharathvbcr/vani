// Package fmbackend is a decide.Backend that asks Apple's on-device Foundation
// Models for the career.form_field question, by running the vani-fm helper
// (macos/Sources/vani-fm) once per question. It costs nothing and sends
// nothing off the machine; when the model is off, absent or unsure the router
// moves on to the next backend.
//
// The helper sees the field's label and type (fieldtriage.Question: Prompt and
// Context) and the option descriptions, never a value, a saved answer or a job
// description. The process contract is in macos/README.md ("On-device
// decisions"). Rule 7: a reading only annotates, a value is always one of the
// caller's options, and a Detail is a closed code, never model text.
package fmbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/decide"
)

const (
	// Name is the backend name answers and ledger annotations carry. Config
	// reserves it (config.ReservedBackendNames), so an OpenRouter model cannot
	// answer under it.
	Name = "apple_fm"

	// EnvBinary names the helper explicitly (an absolute path).
	EnvBinary = "VANI_FM_BINARY"
	// AppBinary is where the installed app keeps the helper.
	AppBinary = "/Applications/Vani Apply.app/Contents/MacOS/vani-fm"

	DefaultTimeout = 12 * time.Second
	// MaxTimeout bounds Config.Timeout: the helper's own watchdog is 8 s.
	MaxTimeout = 12 * time.Second
	// MaxConcurrent bounds the helper processes alive at once.
	MaxConcurrent = 2
	// DefaultBudget is the helper wall time a Backend spends per process run
	// before it stands down as "slow". Suggest's triage bound is 45 s and later
	// backends need their share of it, while a healthy answer takes about a
	// second (verified live: 0.4 to 1.2 s), so 15 s is room for a dozen
	// questions or a few slow ones.
	DefaultBudget = 15 * time.Second
	// MaxBudget bounds Config.Budget well under the triage bound.
	MaxBudget = 30 * time.Second

	protocolVersion = 1
	// Request bounds, held equal to OnDeviceDecider (macos/Sources/VaniApplyKit).
	maxQuestionBytes     = 8 << 10
	maxInstructionsBytes = 4 << 10
	maxOptionBytes       = 64
	maxRequestBytes      = 16 << 10
	// maxReplyBytes bounds the helper's stdout; a real reply is under 200 bytes.
	maxReplyBytes = 4 << 10
	waitDelay     = 2 * time.Second
)

// Config configures New. The zero value uses the default locations.
type Config struct {
	// Path is the helper's absolute path. When set it is the only candidate.
	Path string
	// Timeout bounds one helper process. Zero means DefaultTimeout; above
	// MaxTimeout it is MaxTimeout.
	Timeout time.Duration
	// Budget bounds the helper wall time (summed over every question, however
	// many run at once) this Backend spends in one process run. A model that
	// answers slowly but inside Timeout would otherwise use up the caller's whole
	// triage bound one question at a time. Once the sum reaches Budget the
	// backend stands down with Unavailable/"slow" and the router asks the next
	// backend at once; the question that crossed it keeps its own reading. Zero
	// or negative means DefaultBudget; above MaxBudget it is MaxBudget.
	Budget time.Duration
	// Descriptions is slot -> option -> one-line description, as
	// fieldtriage.Descriptions returns; it is sent to the helper as
	// instructions and holds no personal data.
	Descriptions map[string]map[string]string

	// Seams for tests; nil means the os functions and AppBinary.
	Getenv     func(string) string
	Executable func() (string, error)
	AppPath    string
}

// Backend implements decide.Backend over the vani-fm helper.
type Backend struct {
	cfg Config
	sem chan struct{}
	// down holds the detail of the first Unavailable reading. The model being
	// off, not ready, missing or too slow holds for the whole run, so every later
	// question answers Unavailable at once and the router asks the next backend
	// without spending the triage budget on more helper starts or timeouts.
	down atomic.Pointer[string]
	// spent is the helper wall time used so far, in nanoseconds; see Config.Budget.
	spent atomic.Int64
}

var _ decide.Backend = (*Backend)(nil)

// New returns a Backend. A missing helper is not an error here: it is an
// Unavailable reading on each question, so the router moves on.
func New(cfg Config) *Backend {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	cfg.Timeout = min(cfg.Timeout, MaxTimeout)
	if cfg.Budget <= 0 {
		cfg.Budget = DefaultBudget
	}
	cfg.Budget = min(cfg.Budget, MaxBudget)
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	if cfg.Executable == nil {
		cfg.Executable = os.Executable
	}
	if cfg.AppPath == "" {
		cfg.AppPath = AppBinary
	}
	return &Backend{cfg: cfg, sem: make(chan struct{}, MaxConcurrent)}
}

// Name implements decide.Backend.
func (*Backend) Name() string { return Name }

// Serves implements decide.Backend: only the form-field question.
func (*Backend) Serves(task string) bool { return task == fieldtriage.Task }

// Binary reports the helper that would run, or "" when none is usable.
func (b *Backend) Binary() string {
	candidates := b.candidates()
	for _, c := range candidates {
		if usable(c) {
			return c
		}
	}
	return ""
}

// candidates lists where the helper may be, in order. An explicit Path or
// VANI_FM_BINARY is the only candidate: naming a helper and not finding it is
// "unavailable", not a reason to run a different binary.
func (b *Backend) candidates() []string {
	if b.cfg.Path != "" {
		return []string{b.cfg.Path}
	}
	if p := b.cfg.Getenv(EnvBinary); p != "" {
		return []string{p}
	}
	var out []string
	// vani runs from Contents/Helpers/vani inside the app bundle, the helper
	// from Contents/MacOS.
	if exe, err := b.cfg.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		out = append(out, filepath.Join(filepath.Dir(exe), "..", "MacOS", "vani-fm"))
	}
	return append(out, b.cfg.AppPath)
}

func usable(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

type request struct {
	V            int      `json:"v"`
	Task         string   `json:"task"`
	Question     string   `json:"question"`
	Options      []string `json:"options"`
	Instructions string   `json:"instructions,omitempty"`
}

type reply struct {
	V       int    `json:"v"`
	Reading string `json:"reading"`
	Value   string `json:"value"`
	Detail  string `json:"detail"`
}

// Decide implements decide.Backend. Each slot is a separate helper process. A
// helper that is missing, off, slow or wrong is a reading, not an error; an
// error is only the caller's context ending.
func (b *Backend) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	ans := decide.Answer{Backend: Name, Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	if q.Task != fieldtriage.Task {
		for _, s := range q.Slots {
			ans.Slots[s.Name] = decide.SlotReading{Reading: decide.NotAsked, Detail: "not_served"}
		}
		return ans, nil
	}
	for _, s := range q.Slots {
		rd, err := b.slot(ctx, q, s)
		if err != nil {
			return decide.Answer{}, err
		}
		ans.Slots[s.Name] = rd
	}
	return ans, nil
}

func (b *Backend) slot(ctx context.Context, q decide.Question, s decide.Slot) (decide.SlotReading, error) {
	req, rd := b.buildRequest(q, s)
	if rd != nil {
		return *rd, nil
	}
	if d := b.down.Load(); d != nil {
		return decide.SlotReading{Reading: decide.Unavailable, Detail: *d}, nil
	}
	path := b.Binary()
	if path == "" {
		return b.unavailable(decide.SlotReading{Reading: decide.Unavailable, Detail: "no_binary"}), nil
	}
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
	case <-ctx.Done():
		return decide.SlotReading{}, ctx.Err()
	}
	// The wait for a free slot above may have been behind slow questions, or behind the one that found the model
	// off: look again before starting another helper.
	if d := b.down.Load(); d != nil {
		return decide.SlotReading{Reading: decide.Unavailable, Detail: *d}, nil
	}
	if b.overBudget() {
		return b.slow(), nil
	}
	start := time.Now()
	out, status := runHelper(ctx, path, req, b.cfg.Timeout)
	if status == statusCanceled {
		return decide.SlotReading{}, ctx.Err()
	}
	b.spent.Add(int64(time.Since(start)))
	var got decide.SlotReading
	if status == statusOK {
		got = mapReply(out, s.Options)
	} else {
		got = status.reading()
	}
	got = b.unavailable(got)
	if b.overBudget() {
		// This question keeps its own reading; the ones behind it stand down.
		b.trip("slow")
	}
	return got, nil
}

func (b *Backend) overBudget() bool { return time.Duration(b.spent.Load()) >= b.cfg.Budget }

// slow stands the backend down as too slow for this run.
func (b *Backend) slow() decide.SlotReading {
	return b.unavailable(decide.SlotReading{Reading: decide.Unavailable, Detail: "slow"})
}

// trip records detail as why the backend is down, unless it already is.
func (b *Backend) trip(detail string) { b.down.CompareAndSwap(nil, &detail) }

// unavailable passes rd through, and trips down when it is Unavailable.
func (b *Backend) unavailable(rd decide.SlotReading) decide.SlotReading {
	if rd.Reading == decide.Unavailable {
		d := rd.Detail
		b.down.CompareAndSwap(nil, &d)
	}
	return rd
}

// buildRequest returns the helper request for slot s, or a reading when the
// question is outside the helper's bounds (refused, fail closed and loud).
func (b *Backend) buildRequest(q decide.Question, s decide.Slot) ([]byte, *decide.SlotReading) {
	refuse := func(d string) ([]byte, *decide.SlotReading) {
		return nil, &decide.SlotReading{Reading: decide.Refused, Detail: d}
	}
	question := q.Prompt + "\n\n" + string(q.Context)
	if len(question) > maxQuestionBytes {
		return refuse("question_too_large")
	}
	if n := len(s.Options); n < 1 || n > decide.MaxOptions {
		return refuse("bad_options")
	}
	for _, o := range s.Options {
		// What the helper accepts (OnDeviceDecider.parse): "none" is its decline choice.
		if len(o) > maxOptionBytes || o == "none" || o != strings.TrimSpace(o) || strings.ContainsAny(o, "\r\n") {
			return refuse("bad_options")
		}
	}
	instr := instructions(s, b.cfg.Descriptions[s.Name])
	if len(instr) > maxInstructionsBytes {
		return refuse("instructions_too_large")
	}
	raw, err := json.Marshal(request{V: protocolVersion, Task: q.Task, Question: question, Options: s.Options, Instructions: instr})
	if err != nil || len(raw) > maxRequestBytes {
		return refuse("request_too_large")
	}
	return raw, nil
}

// instructions renders the option descriptions the caller supplied, in option
// order. They are the caller's own fixed text, never site text.
func instructions(s decide.Slot, desc map[string]string) string {
	var sb strings.Builder
	for _, o := range s.Options {
		if d := strings.TrimSpace(desc[o]); d != "" {
			fmt.Fprintf(&sb, "- %s: %s\n", o, d)
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return "The kinds:\n" + sb.String()
}

// knownDetails is the closed set of codes the helper may report (the details
// in OnDeviceDecider.swift; a test holds the two equal). Anything else, or
// anything shaped like text, is reported as "unknown".
var knownDetails = map[string]bool{
	"refusal": true, "guardrail": true, "context_size": true, "unsupported_locale": true,
	"rate_limited": true, "timeout": true, "model_not_ready": true, "device_not_eligible": true,
	"apple_intelligence_not_enabled": true, "os_too_old": true, "unavailable": true,
	"decode": true, "invalid_value": true, "error": true, "declined": true,
	"bad_request_json": true, "bad_request_too_large": true, "bad_request_version": true,
	"bad_request_task": true, "bad_request_question": true, "bad_request_instructions": true,
	"bad_request_options": true,
}

func cleanDetail(d string) string {
	if knownDetails[d] {
		return d
	}
	return "unknown"
}

// mapReply turns the helper's stdout into a reading. An answered value must be
// one of options (Rule 7); the router checks this again.
func mapReply(out []byte, options []string) decide.SlotReading {
	var r reply
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&r); err != nil || dec.More() {
		return decide.SlotReading{Reading: decide.Failed, Detail: "bad_output"}
	}
	if r.V != protocolVersion {
		return decide.SlotReading{Reading: decide.Failed, Detail: "bad_version"}
	}
	detail := ""
	if r.Detail != "" {
		detail = cleanDetail(r.Detail)
	}
	switch r.Reading {
	case "answered":
		for _, o := range options {
			if o == r.Value {
				return decide.SlotReading{Reading: decide.Answered, Value: o}
			}
		}
		return decide.SlotReading{Reading: decide.Failed, Detail: "invalid_value"}
	case "abstained":
		return decide.SlotReading{Reading: decide.Abstained, Detail: detail}
	case "refused":
		return decide.SlotReading{Reading: decide.Refused, Detail: detail}
	case "unavailable":
		return decide.SlotReading{Reading: decide.Unavailable, Detail: detail}
	case "failed":
		return decide.SlotReading{Reading: decide.Failed, Detail: detail}
	}
	return decide.SlotReading{Reading: decide.Failed, Detail: "unknown_reading"}
}

type helperStatus int

const (
	statusOK helperStatus = iota
	statusCanceled
	statusTimeout
	statusTooLarge
	statusStart
	statusExit
	statusEmpty
)

func (s helperStatus) reading() decide.SlotReading {
	switch s {
	case statusTimeout:
		return decide.SlotReading{Reading: decide.Unavailable, Detail: "timeout"}
	case statusStart:
		return decide.SlotReading{Reading: decide.Unavailable, Detail: "start_failed"}
	case statusTooLarge:
		return decide.SlotReading{Reading: decide.Failed, Detail: "output_too_large"}
	case statusExit:
		return decide.SlotReading{Reading: decide.Failed, Detail: "helper_exit"}
	default:
		return decide.SlotReading{Reading: decide.Failed, Detail: "bad_output"}
	}
}

var errOutputTooLarge = errors.New("fmbackend: helper output over the limit")

// capWriter keeps up to maxReplyBytes and, on the first byte past it, stops the
// helper: a child blocked on a pipe nobody reads would otherwise run to the
// timeout.
type capWriter struct {
	buf    bytes.Buffer
	over   bool
	cancel context.CancelCauseFunc
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := maxReplyBytes - w.buf.Len(); len(p) > room {
		w.over = true
		w.cancel(errOutputTooLarge)
		w.buf.Write(p[:room])
		return len(p), nil
	}
	return w.buf.Write(p)
}

// runHelper runs path once with request on stdin and a minimal environment (HOME
// only: nothing of the daemon's keys or settings reaches the helper). It
// returns the helper's stdout when it exited 0 with a non-empty reply.
func runHelper(ctx context.Context, path string, request []byte, timeout time.Duration) ([]byte, helperStatus) {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()

	cmd := exec.CommandContext(runCtx, path)
	cmd.Dir = "/"
	cmd.Env = minimalEnv()
	cmd.Stdin = bytes.NewReader(request)
	out := &capWriter{cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = nil
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error { return cmd.Process.Kill() }

	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, statusCanceled
		}
		return nil, statusStart
	}
	err := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, statusCanceled
	case out.over:
		return nil, statusTooLarge
	case err != nil && errors.Is(context.Cause(runCtx), context.DeadlineExceeded):
		return nil, statusTimeout
	case err != nil:
		return nil, statusExit
	case out.buf.Len() == 0:
		return nil, statusEmpty
	case !utf8.Valid(out.buf.Bytes()):
		return nil, statusEmpty
	}
	return out.buf.Bytes(), statusOK
}

func minimalEnv() []string {
	if h := os.Getenv("HOME"); h != "" {
		return []string{"HOME=" + h}
	}
	return []string{}
}
