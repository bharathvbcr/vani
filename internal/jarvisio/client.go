// Package jarvisio talks to `jarvis serve` on stdin and stdout. There is no
// network listener. Contact values travel only on that pipe, in the get
// result, and never into a log line this package writes.
package jarvisio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Application is Vani's inputs profile.
	Application = "vani"

	opPut  = "jarvis.inputs.put"
	opList = "jarvis.inputs.list-keys"
	opGet  = "jarvis.inputs.get"

	// CodeJarvis is a refused input. CodeBadRequest is a request jarvis could
	// not decode. CodeUnavailable means serve was not asked or did not answer.
	CodeJarvis      = "E_JARVIS"
	CodeBadRequest  = "E_BAD_REQUEST"
	CodeUnavailable = "unavailable"

	bankMissing = "bank executable is missing"

	defaultTimeout = 8 * time.Second
	maxTimeout     = 20 * time.Second
	maxReplyBytes  = 1 << 20
)

// Error is a failed jarvis call. Message never includes a contact value this
// client sent or received.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// BankMissing reports that a read failed because the configured bank path is
// not a regular file. That is not an empty catalog.
func BankMissing(err error) bool {
	var e *Error
	return errors.As(err, &e) && strings.Contains(e.Message, bankMissing)
}

// Unavailable reports that jarvis serve was not asked or did not answer.
func Unavailable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeUnavailable
}

// Client runs one `jarvis serve` process per call.
type Client struct {
	// Binary is the jarvis executable. Root is the product root that holds
	// profiles/vani.json. Bank defaults to <Root>/build/jarvis-bank.
	Binary  string
	Root    string
	Bank    string
	Timeout time.Duration
}

// BankPath is the bank executable jarvis is pointed at.
func (c Client) BankPath() string {
	if c.Bank != "" {
		return c.Bank
	}
	return filepath.Join(c.Root, "build", "jarvis-bank")
}

func (c Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultTimeout
	}
	if c.Timeout > maxTimeout {
		return maxTimeout
	}
	return c.Timeout
}

type stringValue struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Put stores one sensitive string. A later list or get can still fail when
// the bank executable is missing; that does not undo the store.
func (c Client) Put(ctx context.Context, key, text string) error {
	var out struct {
		Stored bool `json:"stored"`
	}
	err := c.call(ctx, opPut, map[string]any{
		"application": Application,
		"key":         key,
		"value":       stringValue{Type: "string", Text: text},
	}, &out, text)
	if err != nil {
		return err
	}
	if !out.Stored {
		return &Error{Code: CodeJarvis, Message: "jarvis did not store the input"}
	}
	return nil
}

// List returns the stored keys. A bank-missing failure is an error, never an
// empty slice.
func (c Client) List(ctx context.Context) ([]string, error) {
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := c.call(ctx, opList, map[string]string{"application": Application}, &out, ""); err != nil {
		return nil, err
	}
	if out.Keys == nil {
		out.Keys = []string{}
	}
	return out.Keys, nil
}

// Get returns the stored text for key.
func (c Client) Get(ctx context.Context, key string) (string, error) {
	var out struct {
		Key   string      `json:"key"`
		Value stringValue `json:"value"`
	}
	if err := c.call(ctx, opGet, map[string]string{"application": Application, "key": key}, &out, ""); err != nil {
		return "", err
	}
	if out.Value.Type != "string" || out.Value.Text == "" {
		return "", &Error{Code: CodeJarvis, Message: "jarvis returned no string value"}
	}
	return out.Value.Text, nil
}

type request struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c Client) call(ctx context.Context, op string, params any, dest any, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Binary == "" || c.Root == "" {
		return &Error{Code: CodeUnavailable, Message: "jarvis serve is not configured"}
	}
	if !filepath.IsAbs(c.Binary) || !filepath.IsAbs(c.Root) {
		return &Error{Code: CodeUnavailable, Message: "jarvis binary and root must be absolute paths"}
	}
	info, err := os.Lstat(c.Binary)
	if err != nil || !info.Mode().IsRegular() {
		return &Error{Code: CodeUnavailable, Message: "jarvis executable is missing"}
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return &Error{Code: CodeBadRequest, Message: "could not encode the request"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Binary, "serve", "--root", c.Root, "--bank", c.BankPath())
	// Wait must never depend on a descendant: it closes the pipes once the
	// child has exited, and WaitDelay bounds anything it would still wait on to
	// one second after the call's own deadline. The client starts the child in
	// the caller's process group and does not signal descendants: one that
	// outlives the child is left running, but its writes to the closed stderr
	// pipe fail, so a stderr-only flood ends with the call.
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not start"}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not start"}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not start"}
	}
	if err := cmd.Start(); err != nil {
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not start"}
	}
	// Drain stderr, keeping none of it: a child that writes more than the pipe
	// holds would otherwise block on it and never reply. This ends when the
	// pipe does, which is when Wait closes it (below), not when every holder of
	// the write end has gone: a descendant that inherited stderr cannot hold the
	// call.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	errCh := make(chan error, 1)
	go func() {
		// Wait closes the stdout pipe when the process has exited, and replies
		// the process wrote just before exiting (it exits on stdin EOF) are
		// still in that pipe: closing it under the reader below loses them, and
		// the call reads as "did not answer". So Wait runs only once the
		// replies are read (every path below calls cancel first) or the
		// deadline killed the process, whose pipe then holds nothing to lose.
		<-ctx.Done()
		errCh <- cmd.Wait()
	}()

	enc := json.NewEncoder(stdin)
	helloParams, _ := json.Marshal(map[string]any{"protocol": 1, "host": "vani"})
	callParams := rawParams
	writeErr := enc.Encode(request{ID: "h", Op: "hello", Params: helloParams})
	if writeErr == nil {
		writeErr = enc.Encode(request{ID: "c", Op: op, Params: callParams})
	}
	_ = stdin.Close()
	if writeErr != nil {
		cancel()
		<-errCh
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not start"}
	}

	dec := json.NewDecoder(io.LimitReader(stdout, maxReplyBytes))
	var hello, call response
	if err := dec.Decode(&hello); err != nil || hello.ID != "h" {
		cancel()
		<-errCh
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not answer"}
	}
	if !hello.OK {
		cancel()
		<-errCh
		return jarvisError(hello, secret)
	}
	if err := dec.Decode(&call); err != nil || call.ID != "c" {
		cancel()
		<-errCh
		return &Error{Code: CodeUnavailable, Message: "jarvis serve did not answer"}
	}
	// The reply is in hand. Stop the process rather than waiting out its shutdown.
	cancel()
	<-errCh
	if !call.OK {
		return jarvisError(call, secret)
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(call.Result, dest); err != nil {
		return &Error{Code: CodeBadRequest, Message: "jarvis reply could not be decoded"}
	}
	return nil
}

func jarvisError(r response, secret string) error {
	code, msg := CodeJarvis, "jarvis refused the input"
	if r.Error != nil {
		if r.Error.Code != "" {
			code = r.Error.Code
		}
		if r.Error.Message != "" {
			msg = r.Error.Message
		}
	}
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	if code != CodeJarvis && code != CodeBadRequest {
		code = CodeJarvis
	}
	return &Error{Code: code, Message: msg}
}

// NotStored reports a key with no value, so callers can tell it from a read
// that did not run.
func NotStored(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeJarvis && strings.Contains(e.Message, "not stored")
}
