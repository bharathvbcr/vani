package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A run limited to some boards (the `only` selection: vani run --board, the Mac
// app's one-board run) takes the previous config when the fresh one fails the
// summary check, instead of failing the reload. It must still be refused when
// that config's summary fails the words check as it is now, as a run that names
// no boards is (TestDailyRunRefusesASummaryTheWordsFileNowFails). Terms are
// synthetic.
func TestOnlyRunRefusesASummaryTheWordsFileNowFails(t *testing.T) {
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
			start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}}
			start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
			writeConfig(t, cfgPath, start)
			writeKBWords(t, e.stateDir, kbTestOwnWord)
			if err := checkPromptSummary(start, e.stateDir, false); err != nil {
				t.Fatalf("the daemon would not have started: %v", err)
			}
			runs := 0
			_, app := lifecycleApp(t, start)
			app.RunOnce = func(context.Context) error { runs++; return nil }
			only := runOptions{Boards: []string{"gh:acme"}}
			job := e.dailyJob(app, start, triggerManual, only)
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
