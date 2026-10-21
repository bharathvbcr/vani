package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func advEnv(t *testing.T) (e *env, cfgPath string, start config.Config, stderr *bytes.Buffer) {
	t.Helper()
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr = &bytes.Buffer{}
	return &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: stderr}, cfgPath, start, stderr
}

// Concurrent reloads from the request/inbox path and the daily path, against
// readers that take w.mu as a run does, under -race.
func TestAdvConcurrentReloadsAreRaceFree(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	w, app := lifecycleApp(t, start)
	e.stderr = io.Discard
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // the Mac app saving the config
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := start
			if i%2 == 0 {
				c.Exclude.Companies = []string{"Acme"}
			}
			writeConfig(t, cfgPath, c)
		}
	}()
	var rw sync.WaitGroup
	for g := 0; g < 8; g++ {
		rw.Add(1)
		go func() {
			defer rw.Done()
			for i := 0; i < 50; i++ {
				if i%3 == 0 {
					_, _ = e.reloadConfig(app, start, []string{})
				} else {
					poll := e.inboxPoll(&App{Reload: app.Reload, PollInbox: func(context.Context) error { return nil }}, start)
					_ = poll(context.Background())
				}
				w.mu.Lock()
				_ = len(w.cfg.Exclude.Companies)
				w.mu.Unlock()
				e.lastApplied(start)
			}
		}()
	}
	rw.Wait()
	close(stop)
	wg.Wait()
}

// A run that still holds the config it was handed must not see a later
// reload (or a boards cut) write through shared slices into it.
func TestAdvReloadDoesNotMutateTheConfigARunHolds(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}}
	start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
	start.Exclude.Companies = []string{"Old"}
	writeConfig(t, cfgPath, start)
	var applied []config.Config
	app := &App{Reload: func(c config.Config) { applied = append(applied, c) }}
	held, err := e.reloadConfig(app, start, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := append([]sources.Board(nil), held.Watchlist...)
	snapEx := append([]string(nil), held.Exclude.Companies...)

	next := start
	next.Exclude.Companies = []string{"New"}
	next.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "c"}, {ATS: posting.Greenhouse, Token: "d"}}
	writeConfig(t, cfgPath, next)
	cut, err := e.reloadConfig(app, start, []string{"gh:c"})
	if err != nil || len(cut.Watchlist) != 1 {
		t.Fatalf("cut run: %v %+v", err, cut.Watchlist)
	}
	if len(held.Watchlist) != len(snap) || held.Watchlist[0].Token != snap[0].Token || held.Exclude.Companies[0] != snapEx[0] {
		t.Fatalf("the config a run holds changed under it: %+v", held)
	}
	// The boards cut is the run's, never remembered.
	if got := e.lastApplied(start); len(got.Watchlist) != 2 {
		t.Fatalf("applied %+v", got.Watchlist)
	}
}

// The config is edited during a run: that run keeps one snapshot (what the wired
// app holds does not change mid-run), the next run takes the edit.
func TestAdvConfigEditedDuringARunIsOneSnapshot(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	w, app := lifecycleApp(t, start)
	var seenStart, seenEnd []string
	inner := app.RunOnce
	app.RunOnce = func(ctx context.Context) error {
		w.mu.Lock()
		seenStart = append([]string(nil), w.cfg.Exclude.Companies...)
		w.mu.Unlock()
		edited := start
		edited.Exclude.Companies = []string{"Edited"}
		writeConfig(t, cfgPath, edited)
		err := inner(ctx)
		w.mu.Lock()
		seenEnd = append([]string(nil), w.cfg.Exclude.Companies...)
		w.mu.Unlock()
		return err
	}
	job := e.dailyJob(app, start, triggerSchedule, runOptions{})
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seenStart) != 0 || len(seenEnd) != 0 {
		t.Fatalf("mid-run edit leaked into the run: start %v end %v", seenStart, seenEnd)
	}
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seenStart) != 1 || seenStart[0] != "Edited" {
		t.Fatalf("the next run did not take the edit: %v", seenStart)
	}
}

// Malformed, fixed, malformed again: runs/last.json says config_stale, then
// clears it, then says it again.
func TestAdvStaleThenFixedThenStaleRecords(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	_, app := lifecycleApp(t, start)
	job := e.dailyJob(app, start, triggerSchedule, runOptions{})
	bad := func() {
		if err := os.WriteFile(cfgPath, []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i, step := range []struct {
		prep  func()
		stale bool
	}{{bad, true}, {func() { writeConfig(t, cfgPath, start) }, false}, {bad, true}, {func() { writeConfig(t, cfgPath, start) }, false}} {
		step.prep()
		err := job(context.Background())
		r := lastRecord(t, e.stateDir)
		if step.stale {
			if !errors.Is(err, errConfigStale) || r.Outcome != outcomeFailed || r.Cause != causeConfigStale {
				t.Fatalf("step %d: err %v outcome %s cause %q, want failed config_stale", i, err, r.Outcome, r.Cause)
			}
		} else if err != nil || r.Outcome != outcomeOK || r.Cause != "" || r.Error != "" {
			t.Fatalf("step %d: err %v record %+v, want a clean ok record", i, err, r)
		}
	}
}

// A config path that is a directory, a huge file, or a symlink to a good file.
func TestAdvConfigPathShapes(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	app := &App{Reload: func(config.Config) {}}
	good, _ := os.ReadFile(cfgPath)

	// symlink to a valid config works.
	real := filepath.Join(filepath.Dir(cfgPath), "real.json")
	if err := os.WriteFile(real, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, cfgPath); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reloadConfig(app, start, nil); err != nil {
		t.Fatalf("symlinked config: %v", err)
	}

	// huge: bounded, stale, quick.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	huge := bytes.Repeat([]byte(" "), 64<<20)
	if err := os.WriteFile(cfgPath, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := e.reloadConfig(app, start, nil); !errors.Is(err, errConfigStale) {
		t.Fatalf("huge config: %v, want errConfigStale", err)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("huge config took %v", d)
	}

	// a directory.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reloadConfig(app, start, nil); !errors.Is(err, errConfigStale) {
		t.Fatalf("directory config: %v, want errConfigStale", err)
	}
}
