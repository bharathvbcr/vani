package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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
	b := logErrors(&failingBackend{errs: errs}, warnf)
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
