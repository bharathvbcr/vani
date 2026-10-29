package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A --list-only run asks no decision backend anything, so it does not need
// one: it lists, reconciles, annotates near-duplicates, refreshes logos and
// skips the advance phase exactly as it does with a backend (the stage order is
// pinned by TestCharRunOnceStageOrderListOnly). A run that advances still
// fails, before it reads a board, when none can be built.

// noBackendConfigs are the ways no decision backend is available: the default
// OpenRouter model with no key in the environment (jev refuses to build), and
// no model or llm configured at all.
func noBackendConfigs() map[string]config.Config {
	absent := config.Default()
	absent.Decide.OpenRouterModel = ""
	absent.LLM = config.LLM{}
	return map[string]config.Config{
		"openrouter model set, key missing": config.Default(),
		"nothing configured":                absent,
	}
}

func TestListOnlyRunWithoutBackendLists(t *testing.T) {
	for name, cfg := range noBackendConfigs() {
		t.Run(name, func(t *testing.T) {
			clearKeys(t)
			w, warn := openWired(t, cfg)
			spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer", "Platform Engineer")
			w.tune(runOptions{ListOnly: true})
			if err := w.runOnce(context.Background()); err != nil {
				t.Fatalf("list-only run with no backend failed: %v\n%s", err, warn.String())
			}
			out := warn.String()
			a, b, c := strIdx(out, "inbox: folded"), strIdx(out, "near-duplicates:"), strIdx(out, "advance phase skipped")
			if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
				t.Fatalf("stage order inbox<near-duplicates<skip violated (%d %d %d):\n%s", a, b, c, out)
			}
			rep := w.runReport()
			if rep.Boards == nil || rep.Advance != nil {
				t.Fatalf("boards %v advance %v; want a board report and no advance report", rep.Boards, rep.Advance)
			}
			recs := w.led.All()
			if len(recs) != 2 {
				t.Fatalf("%d records, want the 2 spooled jobs listed", len(recs))
			}
			for _, r := range recs {
				if r.Status != posting.Live {
					t.Errorf("%s %s: list-only must leave postings Live", r.Key, r.Status)
				}
			}
		})
	}
}

// A backend that is configured and reachable-in-principle is not called either:
// the key is set, no egress host is listed for a board, and the run is the same.
func TestListOnlyRunWithBackendConfiguredStillCallsNone(t *testing.T) {
	clearKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	w, warn := openWired(t, config.Default())
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	w.tune(runOptions{ListOnly: true})
	if err := w.runOnce(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
	}
	if strings.Contains(warn.String(), "no decision backend") {
		t.Errorf("a configured backend was reported missing:\n%s", warn.String())
	}
	if rep := w.runReport(); rep.Advance != nil {
		t.Errorf("advance report %v on a list-only run", rep.Advance)
	}
}

// Without --list-only the same wired app with no backend still refuses, and
// before it folds or lists anything. The tuning is per run: a list-only run
// does not leave the next full one tolerant.
func TestFullRunWithoutBackendStillRefusedAfterListOnly(t *testing.T) {
	for name, cfg := range noBackendConfigs() {
		t.Run(name, func(t *testing.T) {
			clearKeys(t)
			w, warn := openWired(t, cfg)
			spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")

			w.tune(runOptions{ListOnly: true})
			if err := w.runOnce(context.Background()); err != nil {
				t.Fatalf("list-only: %v\n%s", err, warn.String())
			}
			before := len(w.led.All())

			spoolBatch(t, w.stateDir, "careers-beta-com", "ML Engineer")
			w.tune(runOptions{})
			err := w.runOnce(context.Background())
			if err == nil || !strings.Contains(err.Error(), "no decision backend is available (configure decide.openrouter_model or llm)") {
				t.Fatalf("full run err = %v, want the no-backend refusal", err)
			}
			if n := len(w.led.All()); n != before {
				t.Errorf("a refused full run changed the ledger: %d -> %d records", before, n)
			}
		})
	}
}

// The inbox poll advances postings, so it needs a backend whatever the last
// tune said: it does not inherit --list-only.
func TestPollInboxIgnoresListOnlyTune(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM = config.LLM{}
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	w.tune(runOptions{ListOnly: true})
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
	}
	if !strings.Contains(warn.String(), "1 new job(s) wait for the next run: no decision backend is available") {
		t.Errorf("poll did not report the missing backend:\n%s", warn.String())
	}
}

// The runner a list-only pass gets holds an empty chain, which answers nothing
// (an unanswered slot keeps the posting, Rule 7), and an advancing pass is
// refused.
func TestRunnerForWithoutBackend(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM = config.LLM{}
	w, _ := openWired(t, cfg)

	if _, err := w.runnerFor(context.Background(), nil, true); err == nil || !strings.Contains(err.Error(), "no decision backend") {
		t.Fatalf("advancing runner err = %v", err)
	}
	r, err := w.runnerFor(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := r.Triager.(*jdtriage.Triager)
	if !ok {
		t.Fatalf("triager is %T", r.Triager)
	}
	router, ok := tr.D.(*decide.Router)
	if !ok {
		t.Fatalf("triage decider is %T, want *decide.Router", tr.D)
	}
	if got := router.Backends(); len(got) != 0 {
		t.Fatalf("list-only runner holds backends %v", got)
	}
}

// The CLI path the owner hit: no key in the environment, real wiring, nothing
// stubbed. --list-only (with and without the digest) succeeds; the same
// command without it is refused as before.
func TestRunOnceCLIListOnlyWithoutBackend(t *testing.T) {
	setup(t)
	clearKeys(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "run-once", "--list-only", "--no-digest")
	mustCode(t, 0, "run-once", "--list-only")
	_, e := mustCode(t, 1, "run-once", "--no-digest")
	if !strings.Contains(e, "no decision backend") {
		t.Fatalf("full run-once stderr %q", e)
	}
}
