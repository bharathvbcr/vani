package notify

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct {
	name string
	args []string
	err  error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.name, f.args = name, args
	return []byte("boom"), f.err
}

func TestArgvSafety(t *testing.T) {
	f := &fakeRunner{}
	evil := `" & do shell script "x`
	if err := New(f).Notify(context.Background(), evil, `body " & quit`); err != nil {
		t.Fatal(err)
	}
	if f.name != "/usr/bin/osascript" {
		t.Errorf("binary %q", f.name)
	}
	// Script lines (every -e operand) must be the constant script: no user text.
	var scriptText []string
	for i, a := range f.args {
		if a == "-e" {
			scriptText = append(scriptText, f.args[i+1])
		}
	}
	if !slices.Equal(scriptText, script) {
		t.Errorf("script changed: %v", scriptText)
	}
	for _, s := range scriptText {
		if strings.Contains(s, "shell script") || strings.Contains(s, "quit") {
			t.Errorf("user text leaked into script: %q", s)
		}
	}
	n := len(f.args)
	if f.args[n-3] != "--" || f.args[n-2] != evil || f.args[n-1] != `body " & quit` {
		t.Errorf("title/body not passed as trailing argv: %q", f.args[n-3:])
	}
}

func TestLeadingDashIsData(t *testing.T) {
	f := &fakeRunner{}
	if err := New(f).Notify(context.Background(), "-e evil", "b"); err != nil {
		t.Fatal(err)
	}
	n := len(f.args)
	if f.args[n-3] != "--" || f.args[n-2] != "-e evil" {
		t.Errorf("args %q", f.args)
	}
}

func TestBoundsAndControlChars(t *testing.T) {
	f := &fakeRunner{}
	long := strings.Repeat("é", 1000)
	if err := New(f).Notify(context.Background(), long, long+"\x00\n"); err != nil {
		t.Fatal(err)
	}
	n := len(f.args)
	title, body := f.args[n-2], f.args[n-1]
	if r := []rune(title); len(r) != MaxTitleRunes+1 {
		t.Errorf("title runes %d", len(r))
	}
	if r := []rune(body); len(r) != MaxBodyRunes+1 {
		t.Errorf("body runes %d", len(r))
	}
	if err := New(f).Notify(context.Background(), "t", "a\x00b\nc"); err != nil {
		t.Fatal(err)
	}
	if got := f.args[len(f.args)-1]; got != "a b c" {
		t.Errorf("control chars kept: %q", got)
	}
}

func TestEmptyAndRunnerError(t *testing.T) {
	f := &fakeRunner{}
	if err := New(f).Notify(context.Background(), "", "b"); err == nil || f.name != "" {
		t.Errorf("empty title must fail without running: %v", err)
	}
	f.err = errors.New("exit 1")
	if err := New(f).Notify(context.Background(), "t", "b"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("runner failure not surfaced: %v", err)
	}
}

func TestNoop(t *testing.T) {
	var n Notifier = Noop{}
	if err := n.Notify(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
}
