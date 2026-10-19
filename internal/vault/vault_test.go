package vault

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resume"
)

const secret = "s3cr3t-value-123"

type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status (argv: -w " + secret + ")" }
func (e exitErr) ExitCode() int { return e.code }

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []call
	out   map[string]string // account -> stdout
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, args})
	if f.err != nil {
		return nil, f.err
	}
	for i, a := range args {
		if args[0] == "find-generic-password" && a == "-a" && i+1 < len(args) {
			if v, ok := f.out[args[i+1]]; ok {
				return []byte(v + "\n"), nil
			}
			return nil, exitErr{44}
		}
	}
	return nil, nil
}

func TestGetArgs(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"email": "a@example.invalid"}}
	got, err := NewWithRunner(f).Get(context.Background(), "email")
	if err != nil || got != "a@example.invalid" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	want := call{"/usr/bin/security", []string{"find-generic-password", "-s", "vani", "-a", "email", "-w"}}
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("call = %+v, want %+v", f.calls[0], want)
	}
}

func TestSetArgs(t *testing.T) {
	f := &fakeRunner{}
	if err := NewWithRunner(f).Set(context.Background(), "phone", secret); err != nil {
		t.Fatal(err)
	}
	want := call{"/usr/bin/security", []string{"add-generic-password", "-U", "-s", "vani", "-a", "phone", "-w", secret}}
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("call = %+v, want %+v", f.calls[0], want)
	}
}

func TestExit44IsNotFoundOtherExitIsGeneric(t *testing.T) {
	v := NewWithRunner(&fakeRunner{})
	if _, err := v.Get(context.Background(), "name"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exit 44: err = %v, want ErrNotFound", err)
	}
	v = NewWithRunner(&fakeRunner{err: exitErr{51}})
	_, err := v.Get(context.Background(), "name")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "51") {
		t.Fatalf("exit 51: err = %v", err)
	}
	v = NewWithRunner(&fakeRunner{err: errors.New("spawn failed")})
	if _, err := v.Get(context.Background(), "name"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("plain error: err = %v", err)
	}
}

func TestUnknownKeyRefusedWithoutRunning(t *testing.T) {
	f := &fakeRunner{}
	v := NewWithRunner(f)
	for _, k := range []string{"password", "", "NAME", "name ", "../x", "ssn"} {
		if _, err := v.Get(context.Background(), k); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("Get(%q): err = %v", k, err)
		}
		if err := v.Set(context.Background(), k, secret); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("Set(%q): err = %v", k, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("security ran for a refused key: %v", f.calls)
	}
}

func TestAllowlistIsExactlyTheSpecifiedKeys(t *testing.T) {
	want := []string{"name", "email", "phone", "location", "linkedin_url", "github_url",
		"portfolio_url", "work_authorization", "requires_sponsorship"}
	if !reflect.DeepEqual(Allowed, want) {
		t.Fatalf("Allowed = %v", Allowed)
	}
}

func TestValueNeverInErrors(t *testing.T) {
	for _, runErr := range []error{exitErr{51}, exitErr{44}, errors.New(secret), context.DeadlineExceeded} {
		v := NewWithRunner(&fakeRunner{err: runErr})
		err := v.Set(context.Background(), "email", secret)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("Set error leaks the value: %v", err)
		}
		if _, err := v.Get(context.Background(), "email"); err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("Get error leaks: %v", err)
		}
	}
	if err := NewWithRunner(&fakeRunner{}).Set(context.Background(), "email", ""); err == nil {
		t.Fatal("empty value accepted")
	}
	err := NewWithRunner(&fakeRunner{}).Set(context.Background(), secret, "v")
	if err == nil || len(err.Error()) > 120 {
		t.Fatalf("unknown-key error should be bounded: %v", err)
	}
}

func TestContextErrorsPassThrough(t *testing.T) {
	v := NewWithRunner(&fakeRunner{err: context.DeadlineExceeded})
	if _, err := v.Get(context.Background(), "name"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestHeader(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"name": "Test Person", "email": "t@example.invalid", "location": "Nowhere",
		"github_url": "https://example.invalid/gh",
	}}
	var src resume.HeaderSource = NewWithRunner(f)
	h, err := src.Header(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := resume.Header{Name: "Test Person", ContactLines: []string{
		"t@example.invalid | Nowhere", "https://example.invalid/gh"}}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("Header = %+v, want %+v", h, want)
	}

	if _, err := NewWithRunner(&fakeRunner{}).Header(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing name: err = %v, want ErrNotFound", err)
	}
	if _, err := NewWithRunner(&fakeRunner{err: exitErr{51}}).Header(context.Background()); err == nil {
		t.Fatal("runner failure must propagate")
	}
}
