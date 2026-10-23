package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func savedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, valid()); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, path string) Config {
	t.Helper()
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Two updaters of different fields, interleaved: the second starts while the
// first is between its read and its save. Both edits survive.
func TestUpdateInterleavedMutatorsBothSurvive(t *testing.T) {
	path := savedConfig(t)
	started := make(chan struct{})
	var wg sync.WaitGroup
	var errB error
	wg.Add(1)
	err := Update(path, func(c *Config) error {
		go func() {
			defer wg.Done()
			errB = Update(path, func(c *Config) error { c.MotivationNotes = "notes from B"; return nil })
		}()
		close(started)
		time.Sleep(150 * time.Millisecond) // B is now waiting on the lock
		c.Exclude.Companies = []string{"Initech"}
		return nil
	})
	<-started
	wg.Wait()
	if err != nil || errB != nil {
		t.Fatalf("A: %v, B: %v", err, errB)
	}
	got := mustLoad(t, path)
	if !slices.Equal(got.Exclude.Companies, []string{"Initech"}) || got.MotivationNotes != "notes from B" {
		t.Errorf("an edit was lost: exclude %v, notes %q", got.Exclude.Companies, got.MotivationNotes)
	}
}

func TestUpdateHeldLockIsBusyNeverAnOverwrite(t *testing.T) {
	path := savedConfig(t)
	old := lockWait
	lockWait = 80 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	before, _ := os.ReadFile(path)

	holding, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Update(path, func(c *Config) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	ran := false
	err := Update(path, func(c *Config) error { ran = true; c.MotivationNotes = "x"; return nil })
	if !errors.Is(err, ErrBusy) || ran {
		t.Errorf("err %v, fn ran %v; want ErrBusy without running fn", err, ran)
	}
	if err := Save(path, valid()); !errors.Is(err, ErrBusy) {
		t.Errorf("Save under a held lock: %v, want ErrBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("the file changed although the only completed update changed nothing")
	}
}

func TestUpdateFailureChangesNothing(t *testing.T) {
	path := savedConfig(t)
	before, _ := os.ReadFile(path)
	boom := errors.New("boom")
	if err := Update(path, func(c *Config) error { c.MotivationNotes = "half"; return boom }); !errors.Is(err, boom) {
		t.Errorf("fn error: %v", err)
	}
	err := Update(path, func(c *Config) error { c.Schedule.DailyAt = "25:99"; return nil })
	if err == nil || !strings.Contains(err.Error(), "daily_at") {
		t.Errorf("validation error: %v", err)
	}
	err = Update(path, func(c *Config) error { c.AllowedHosts = append(c.AllowedHosts, "https://x.example"); return nil })
	if err == nil || !strings.Contains(err.Error(), "allowed_hosts[") {
		t.Errorf("allowed_hosts error: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("a failed update changed the file")
	}
}

func TestUpdateReleasesTheLockWhenFnPanics(t *testing.T) {
	path := savedConfig(t)
	old := lockWait
	lockWait = 200 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = Update(path, func(*Config) error { panic("mutator bug") })
	}()
	if err := Update(path, func(c *Config) error { c.MotivationNotes = "after"; return nil }); err != nil {
		t.Fatalf("lock still held after a panic: %v", err)
	}
}

func TestUpdateNeedsAnExistingReadableConfig(t *testing.T) {
	path := savedConfig(t)
	if err := os.Remove(path); err != nil { // deleted between the caller's look and the update
		t.Fatal(err)
	}
	ran := false
	if err := Update(path, func(*Config) error { ran = true; return nil }); err == nil || ran {
		t.Errorf("err %v, fn ran %v; want an error without running fn", err, ran)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Update created a config: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(*Config) error { ran = true; return nil }); err == nil || ran {
		t.Errorf("unreadable config: err %v, fn ran %v", err, ran)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Errorf("an unreadable config was overwritten: %q", b)
	}
}

func TestCreateRefusesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := Create(path, Default()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := Create(path, Default()); !errors.Is(err, os.ErrExist) {
		t.Errorf("second Create: %v, want ErrExist", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Error("Create overwrote")
	}
}

// Separate processes, each adding its own allowed host: every one survives.
func TestUpdateAcrossProcesses(t *testing.T) {
	if p := os.Getenv("VANI_UPDATE_HELPER_PATH"); p != "" {
		host := os.Getenv("VANI_UPDATE_HELPER_HOST")
		for i := 0; i < 5; i++ {
			err := Update(p, func(c *Config) error {
				c.AllowedHosts = append(c.AllowedHosts, fmt.Sprintf("%s-%d.example", host, i))
				return nil
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(3)
			}
		}
		return
	}
	path := savedConfig(t)
	const procs = 8
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := range cmds {
		outs[i] = &bytes.Buffer{}
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestUpdateAcrossProcesses$")
		cmds[i].Env = append(os.Environ(), "VANI_UPDATE_HELPER_PATH="+path, fmt.Sprintf("VANI_UPDATE_HELPER_HOST=p%d", i))
		cmds[i].Stderr = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Errorf("process %d: %v: %s", i, err, outs[i])
		}
	}
	got := mustLoad(t, path)
	for i := 0; i < procs; i++ {
		for j := 0; j < 5; j++ {
			if h := fmt.Sprintf("p%d-%d.example", i, j); !slices.Contains(got.AllowedHosts, h) {
				t.Errorf("%s was lost", h)
			}
		}
	}
}

// Every allowed_hosts entry Validate accepts, egress.New accepts: a config that
// passes `vani config check` cannot stop the daily run at egress.New.
func TestValidateAllowedHostsAgreesWithEgress(t *testing.T) {
	frags := []string{"a", "b.c", "*.", "*", "https://", "://", ":443", "/p", "10.0.0.5", "::1", "[::1]", "A", "-", ".", "x.example", "%41", " ", "xn--a", "1", "@"}
	var cands []string
	cands = append(cands, "", "example.com", "*.example.com", "Example.COM.", " api.example.com ")
	for _, a := range frags {
		cands = append(cands, a)
		for _, b := range frags {
			cands = append(cands, a+b)
			for _, c := range frags {
				cands = append(cands, a+b+c)
			}
		}
	}
	accepted := 0
	for _, cand := range cands {
		c := valid()
		c.AllowedHosts = append(c.AllowedHosts, cand)
		verr := c.Validate()
		_, nerr := egress.New(egress.Config{AllowedHosts: c.AllowedHosts})
		if verr == nil {
			accepted++
			if nerr != nil {
				t.Errorf("Validate accepted %q but egress.New refuses it: %v", cand, nerr)
			}
		} else if nerr != nil && !strings.Contains(verr.Error(), "allowed_hosts[") {
			t.Errorf("Validate refused %q without naming the entry: %v", cand, verr)
		}
	}
	if accepted < 20 {
		t.Errorf("only %d candidates accepted; the property was barely exercised", accepted)
	}
}

func TestValidateNamesTheBadAllowedHost(t *testing.T) {
	for _, bad := range []string{"https://x.example", "example.com:443", "a.example/p", "10.0.0.5", "*.com", "HTTPS://X.example", ""} {
		c := valid()
		c.AllowedHosts = append(c.AllowedHosts, bad)
		err := c.Validate()
		want := fmt.Sprintf("allowed_hosts[%d]", len(c.AllowedHosts)-1)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", bad, err, want)
		}
	}
}

// ValidBoard and srcutil.CheckBoard answer one question about a token.
func TestBoardTokenRuleIsShared(t *testing.T) {
	for _, tok := range []string{"acme", "Acme-1", "a_b", "gh/acme:eu", "a:b", ":", "", " ", "a b", "a\tb", "a?b", "a#b", "a..b", "acme:", "x.y"} {
		b := sources.Board{ATS: posting.Greenhouse, Token: tok, Company: "Co"}
		verr, cerr := ValidBoard(b), srcutil.CheckBoard(b, posting.Greenhouse)
		// Whatever the config accepts, every adapter accepts. The adapters are
		// looser on spaces, '?', '#' and ".." (they escape those themselves).
		if verr == nil && cerr != nil {
			t.Errorf("token %q: ValidBoard accepts, CheckBoard refuses: %v", tok, cerr)
		}
		// ':' and an empty token are refused by both.
		if (tok == "" || strings.TrimSpace(tok) == "" || strings.Contains(tok, ":")) && (verr == nil || cerr == nil) {
			t.Errorf("token %q: ValidBoard %v, CheckBoard %v; both must refuse", tok, verr, cerr)
		}
	}
	for _, tok := range []string{"acme", "Acme-1", "a_b", "x.y"} {
		if err := ValidBoard(sources.Board{ATS: posting.Greenhouse, Token: tok, Company: "Co"}); err != nil {
			t.Errorf("token %q refused: %v", tok, err)
		}
	}
}
