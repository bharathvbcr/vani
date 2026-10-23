package vault

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// keyRunner answers find-generic-password for OpenRouterService.
type keyRunner struct {
	calls []call
	out   string
	err   error
	wait  bool // block until ctx is done, as an unanswered prompt does
}

func (f *keyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, args})
	if f.wait {
		<-ctx.Done()
		return nil, exitErr{-1} // killed by CommandContext
	}
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.out), nil
}

func TestModelKeyArgsAndValue(t *testing.T) {
	f := &keyRunner{out: "  sk-or-test  \n"}
	got, err := NewWithRunner(f).ModelKey(context.Background(), OpenRouterService, "owner")
	if err != nil || got != "sk-or-test" {
		t.Fatalf("ModelKey = %q, %v", got, err)
	}
	want := call{"/usr/bin/security", []string{"find-generic-password", "-s", "openrouter-api-key", "-a", "owner", "-w"}}
	if !reflect.DeepEqual(f.calls, []call{want}) {
		t.Fatalf("calls = %+v", f.calls)
	}
}

func TestModelKeyErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		r    *keyRunner
		want error
	}{
		{"not found", &keyRunner{err: exitErr{44}}, ErrNotFound},
		{"denied", &keyRunner{err: exitErr{36}}, ErrKeychainDenied},
		{"no interaction allowed", &keyRunner{err: exitErr{51}}, ErrKeychainDenied},
		{"other failure", &keyRunner{err: errors.New("exec: boom -w " + secret)}, ErrKeychainDenied},
	} {
		_, err := NewWithRunner(c.r).ModelKey(context.Background(), OpenRouterService, "owner")
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error carries the command's text: %v", c.name, err)
		}
	}
}

func TestModelKeyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewWithRunner(&keyRunner{wait: true}).ModelKey(ctx, OpenRouterService, "owner")
	if !errors.Is(err, ErrKeychainTimeout) {
		t.Fatalf("err = %v, want ErrKeychainTimeout", err)
	}
}

func TestModelKeyRefusals(t *testing.T) {
	f := &keyRunner{out: "x"}
	v := NewWithRunner(f)
	for _, svc := range []string{"anthropic-api-key", "vani", "", "openrouter-api-key-other"} {
		if _, err := v.ModelKey(context.Background(), svc, "owner"); err == nil {
			t.Errorf("service %q read", svc)
		}
	}
	if _, err := v.ModelKey(context.Background(), OpenRouterService, ""); err == nil {
		t.Error("empty account read")
	}
	if len(f.calls) != 0 {
		t.Fatalf("security ran for a refused read: %+v", f.calls)
	}
	for _, bad := range []string{"sk-first\nsk-second\n", "sk-a\rsk-b"} {
		got, err := NewWithRunner(&keyRunner{out: bad}).ModelKey(context.Background(), OpenRouterService, "owner")
		if err == nil || got != "" || strings.Contains(err.Error(), "sk-") {
			t.Errorf("%q: %q, %v", bad, got, err)
		}
	}
}

func TestModelKeyOffMacOS(t *testing.T) {
	f := &keyRunner{out: "x"}
	v := &Vault{r: f, err: noKeychain("linux")}
	if _, err := v.ModelKey(context.Background(), OpenRouterService, "owner"); !errors.Is(err, ErrNoKeychain) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("security ran off macOS")
	}
}
