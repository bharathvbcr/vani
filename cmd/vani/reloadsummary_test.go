package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A daemon that started with a summary the words file passed must not keep
// sending it once the words file changes under it. The run refuses, naming
// target_summary, and no model-prompt stage starts. Terms are synthetic.
func TestDailyRunRefusesASummaryTheWordsFileNowFails(t *testing.T) {
	words := func(state string) string { return filepath.Join(state, "tracks", trackWordsFile()) }
	for name, change := range map[string]func(t *testing.T, state string){
		"a term in the summary is added": func(t *testing.T, state string) { writeKBWords(t, state, "inference") },
		"the words file is deleted": func(t *testing.T, state string) {
			if err := os.Remove(words(state)); err != nil {
				t.Fatal(err)
			}
		},
		"the words file is emptied": func(t *testing.T, state string) {
			if err := os.WriteFile(words(state), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, cfgPath, start, _ := advEnv(t)
			start.TargetSummary = cleanSummary
			writeConfig(t, cfgPath, start)
			writeKBWords(t, e.stateDir, kbTestOwnWord)
			if err := checkPromptSummary(start, e.stateDir, false); err != nil {
				t.Fatalf("the daemon would not have started: %v", err)
			}
			runs := 0
			_, app := lifecycleApp(t, start)
			app.RunOnce = func(context.Context) error { runs++; return nil }
			job := e.dailyJob(app, start, triggerSchedule, runOptions{})
			if err := job(context.Background()); err != nil || runs != 1 {
				t.Fatalf("the first run: err %v, runs %d", err, runs)
			}

			change(t, e.stateDir)
			err := job(context.Background())
			if !errors.Is(err, errTargetSummary) || runs != 1 {
				t.Fatalf("after the change: err %v, runs %d; want a refusal naming target_summary and no run", err, runs)
			}
			noTerm(t, "the run error", err.Error(), cleanSummary)
			if r := lastRecord(t, e.stateDir); r.Outcome != outcomeFailed {
				t.Fatalf("run record %+v; want a failed run", r)
			}
		})
	}
}

// A run that reloads nothing (an app with no Reload) is checked the same way.
func TestDailyStepsChecksTheSummaryWithoutAReload(t *testing.T) {
	e, _, start, _ := advEnv(t)
	start.TargetSummary = cleanSummary
	// no words file at all
	runs := 0
	_, app := lifecycleApp(t, start)
	app.Reload = nil
	app.RunOnce = func(context.Context) error { runs++; return nil }
	if _, err := e.dailySteps(context.Background(), app, start, runOptions{}, &runRecord{}); !errors.Is(err, errTargetSummary) || runs != 0 {
		t.Fatalf("err %v, runs %d; want a refusal naming target_summary and no run", err, runs)
	}
}
