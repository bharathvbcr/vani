package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/decide"
)

type failingBackend struct{ errs []error }

func (f *failingBackend) Name() string       { return "llm" }
func (f *failingBackend) Serves(string) bool { return true }
func (f *failingBackend) Decide(context.Context, decide.Question) (decide.Answer, error) {
	err := f.errs[0]
	if len(f.errs) > 1 {
		f.errs = f.errs[1:]
	}
	return decide.Answer{Backend: "llm"}, err
}

func TestBackendErrorsAreLoggedOnceEachAndBounded(t *testing.T) {
	var lines []string
	warnf := func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	auth := errors.New("anthropic: HTTP 401 authentication_error: invalid x-api-key")
	var errs []error
	for i := 0; i < 3; i++ {
		errs = append(errs, auth)
	}
	for i := 0; i < 10; i++ {
		errs = append(errs, fmt.Errorf("distinct %d", i))
	}
	b := withoutBreaker(logErrors(&failingBackend{errs: errs}, warnf, nil))
	for i := 0; i < len(errs); i++ {
		if _, err := b.Decide(context.Background(), decide.Question{}); err == nil {
			t.Fatal("the error must pass through unchanged")
		}
	}
	if len(lines) != maxLoggedBackendErrors {
		t.Fatalf("%d lines logged, want %d: %v", len(lines), maxLoggedBackendErrors, lines)
	}
	if !strings.Contains(lines[0], "llm backend error: anthropic: HTTP 401") {
		t.Errorf("first line = %q", lines[0])
	}
	if strings.Count(strings.Join(lines, "\n"), "401") != 1 {
		t.Error("a repeated error was logged twice")
	}
	if b.Name() != "llm" || !b.Serves("x") {
		t.Error("the wrapper must keep the backend's identity")
	}
}

// The run record keeps every failed call, not only the five printed: 1,098
// refused calls left nothing but a stderr line that nobody kept.
func TestBackendErrorsAreCountedForTheRunRecord(t *testing.T) {
	sink := newBackendErrors()
	if sink.report() != nil {
		t.Fatal("a run with no failed call reports errors")
	}
	var errs []error
	for i := 0; i < 1098; i++ {
		errs = append(errs, fmt.Errorf("anthropic: http 429 (request req_%06d): rate_limit_error", i))
	}
	errs = append(errs, errors.New("openrouter: http 402: insufficient credits"))
	llm := withoutBreaker(logErrors(&failingBackend{errs: errs}, func(string, ...any) {}, sink))
	for range errs {
		llm.Decide(context.Background(), decide.Question{})
	}
	r := sink.report()
	if r == nil || len(r.Causes) != 2 || r.Unrecorded != 0 {
		t.Fatalf("report %+v", r)
	}
	if c := r.Causes[0]; c.Backend != "llm" || c.Count != 1098 || !strings.Contains(c.Example, "req_000000") {
		t.Fatalf("most frequent cause %+v; want 1098 calls with the first request id kept", c)
	}
	if r.Causes[1].Count != 1 {
		t.Fatalf("second cause %+v", r.Causes[1])
	}
}

// Distinct causes are bounded; calls past the bound are counted, never lost.
func TestBackendErrorCausesAreBounded(t *testing.T) {
	sink := newBackendErrors()
	var errs []error
	for i := 0; i < maxRecordedBackendErrors+7; i++ {
		errs = append(errs, fmt.Errorf("cause %d", i))
	}
	b := withoutBreaker(logErrors(&failingBackend{errs: errs}, func(string, ...any) {}, sink))
	for range errs {
		b.Decide(context.Background(), decide.Question{})
	}
	r := sink.report()
	if len(r.Causes) != maxRecordedBackendErrors || r.Unrecorded != 7 {
		t.Fatalf("%d causes, %d unrecorded", len(r.Causes), r.Unrecorded)
	}
}

// An oversized message is cut on a rune boundary, and a nil sink (commands
// outside a run) only prints.
func TestBackendErrorMessagesAreBoundedAndNilSinkIsSafe(t *testing.T) {
	long := strings.Repeat("é", 2*maxBackendErrorRunes)
	sink := newBackendErrors()
	logErrors(&failingBackend{errs: []error{errors.New(long)}}, func(string, ...any) {}, sink).Decide(context.Background(), decide.Question{})
	ex := sink.report().Causes[0].Example
	if n := len([]rune(ex)); n != maxBackendErrorRunes || !utf8.ValidString(ex) {
		t.Fatalf("example has %d runes, valid utf-8 %v", n, utf8.ValidString(ex))
	}
	var printed int
	nilSink := logErrors(&failingBackend{errs: []error{errors.New("x")}}, func(string, ...any) { printed++ }, nil)
	if _, err := nilSink.Decide(context.Background(), decide.Question{}); err == nil || printed != 1 {
		t.Fatalf("nil sink: err %v, %d printed", err, printed)
	}
	var none *backendErrors
	if none.report() != nil {
		t.Fatal("nil sink reports")
	}
}

// Backends are asked concurrently; the sink must count every call.
func TestBackendErrorsCountConcurrentCalls(t *testing.T) {
	sink := newBackendErrors()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := withoutBreaker(logErrors(&failingBackend{errs: []error{errors.New("down")}}, func(string, ...any) {}, sink))
			for i := 0; i < 100; i++ {
				b.Decide(context.Background(), decide.Question{})
			}
		}()
	}
	wg.Wait()
	if r := sink.report(); len(r.Causes) != 1 || r.Causes[0].Count != 800 {
		t.Fatalf("report %+v", r)
	}
}
