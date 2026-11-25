package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/vault"
)

// adzunaKeychain replaces keychainRead for one test with the Adzuna item.
func adzunaKeychain(t *testing.T, value string, err error) *atomic.Int32 {
	t.Helper()
	t.Setenv(noKeychainEnv, "")
	var n atomic.Int32
	old := keychainRead
	keychainRead = func(_ context.Context, service string) (string, error) {
		n.Add(1)
		if service != vault.AdzunaService {
			t.Errorf("read service %q", service)
		}
		return value, err
	}
	t.Cleanup(func() { keychainRead = old })
	return &n
}

func TestAdzunaCredentialsFromTheEnvironment(t *testing.T) {
	w, _ := keyWired(t)
	n := adzunaKeychain(t, "kc-id:kc-key", nil)
	t.Setenv(adzunaAppIDEnv, " env-id ")
	t.Setenv(adzunaAppKeyEnv, "env-key")
	id, key, err := w.adzunaCredentials(context.Background())
	if err != nil || id != "env-id" || key != "env-key" || n.Load() != 0 {
		t.Fatalf("got %q %q %v, keychain reads %d", id, key, err, n.Load())
	}
	// One variable alone is a mistake, not a reason to read the Keychain.
	t.Setenv(adzunaAppKeyEnv, "")
	if _, _, err := w.adzunaCredentials(context.Background()); err == nil || !strings.Contains(err.Error(), "only one of") || n.Load() != 0 {
		t.Fatalf("err = %v, reads %d", err, n.Load())
	}
}

func TestAdzunaCredentialsFromTheKeychain(t *testing.T) {
	w, _ := keyWired(t)
	t.Setenv(adzunaAppIDEnv, "")
	t.Setenv(adzunaAppKeyEnv, "")
	n := adzunaKeychain(t, " kc-id : kc/key+= \n", nil)
	for range 2 {
		id, key, err := w.adzunaCredentials(context.Background())
		if err != nil || id != "kc-id" || key != "kc/key+=" {
			t.Fatalf("got %q %q %v", id, key, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("keychain reads = %d, want 1 (cached)", n.Load())
	}
}

func TestAdzunaCredentialsFailWithoutTheValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		err   error
		want  string
	}{
		{"absent", "", vault.ErrNotFound, "no app id and key"},
		{"denied", "", vault.ErrKeychainDenied, "no app id and key"},
		{"no colon", "secretvalue", nil, "is not"},
		{"empty id", ":secretvalue", nil, "is not"},
		{"empty key", "secretid:", nil, "is not"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := keyWired(t)
			t.Setenv(adzunaAppIDEnv, "")
			t.Setenv(adzunaAppKeyEnv, "")
			adzunaKeychain(t, tc.value, tc.err)
			_, _, err := w.adzunaCredentials(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secretvalue") || strings.Contains(err.Error(), "secretid") {
				t.Fatalf("error carries the value: %v", err)
			}
		})
	}
}

func TestAdzunaCredentialsHonourNoKeychain(t *testing.T) {
	w, _ := keyWired(t)
	t.Setenv(adzunaAppIDEnv, "")
	t.Setenv(adzunaAppKeyEnv, "")
	n := adzunaKeychain(t, "id:key", nil)
	t.Setenv(noKeychainEnv, "1")
	_, _, err := w.adzunaCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), noKeychainEnv) || n.Load() != 0 {
		t.Fatalf("err = %v, reads %d", err, n.Load())
	}
	if errors.Is(err, vault.ErrNotFound) {
		t.Fatal("unexpected wrap")
	}
}

func TestAdzunaKeyNote(t *testing.T) {
	ad := []sources.Board{{ATS: posting.Greenhouse, Token: "acme"}, {ATS: posting.Adzuna, Token: "us"}}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if n := adzunaKeyNote(ad, get); !strings.Contains(n, adzunaAppIDEnv) || !strings.Contains(n, vault.AdzunaService) {
		t.Fatalf("note = %q", n)
	}
	env[adzunaAppIDEnv], env[adzunaAppKeyEnv] = "id", "key"
	if n := adzunaKeyNote(ad, get); n != "" {
		t.Fatalf("note with both variables set: %q", n)
	}
	if n := adzunaKeyNote(ad[:1], func(string) string { return "" }); n != "" {
		t.Fatalf("note without an Adzuna board: %q", n)
	}
}
