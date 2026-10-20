package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestTriageModelIDTracksWhatAReadingDependsOn(t *testing.T) {
	base := config.Default()
	base.TargetSummary = "ML systems"
	id := triageModelID(base)
	if id != triageModelID(base) {
		t.Fatal("model id is not stable")
	}
	changes := map[string]func(c *config.Config){
		"openrouter model": func(c *config.Config) { c.Decide.OpenRouterModel = "other/model" },
		"llm model":        func(c *config.Config) { c.LLM.Model = "other" },
		"llm provider":     func(c *config.Config) { c.LLM.Provider = "anthropic" },
		"target summary":   func(c *config.Config) { c.TargetSummary = "data engineering" },
		"lappi shadow":     func(c *config.Config) { c.Decide.Lappi = &config.Lappi{} },
		"lappi primary":    func(c *config.Config) { c.Decide.Lappi = &config.Lappi{Primary: true} },
	}
	seen := map[string]string{id: "base"}
	for name, mut := range changes {
		c := base
		mut(&c)
		got := triageModelID(c)
		if prev, dup := seen[got]; dup {
			t.Errorf("%s gives the same model id as %s: %s", name, prev, got)
		}
		seen[got] = name
	}
}

func TestNotesSpendIsSeparateFromTriageSpend(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Decide.DailyUSD.OpenRouter = 0.25
	triage, err := decide.NewDailyCap(dir+"/budget/"+openRouterBudgetFile, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := notesSpend(dir, cfg, triage)
	if err != nil || notes == nil {
		t.Fatalf("notes cap: %v %v", notes, err)
	}
	ctx := context.Background()
	if err := notes.Charge(ctx, openRouterSpendKey, cfg.Funnel.NotesCapUSD()); err != nil {
		t.Fatal(err)
	}
	if err := notes.Allow(ctx, openRouterSpendKey); !errors.Is(err, decide.ErrBudget) {
		t.Fatalf("notes cap not spent: %v", err)
	}
	if err := triage.Allow(ctx, openRouterSpendKey); err != nil {
		t.Fatalf("spending the notes cap starved triage: %v", err)
	}
	if none, err := notesSpend(dir, cfg, nil); none != nil || err != nil {
		t.Fatalf("no OpenRouter, got %v %v", none, err)
	}
	// An unreadable cap fails closed rather than leaving notes unmetered.
	bad := dir + "/bad"
	if err := os.MkdirAll(bad+"/budget", 0o700); err != nil || os.WriteFile(bad+"/budget/"+notesBudgetFile, []byte("not json"), 0o600) != nil {
		t.Fatal(err)
	}
	if _, err := notesSpend(bad, cfg, triage); err == nil {
		t.Fatal("a corrupt notes budget was accepted")
	}
}

func TestBuildFunnel(t *testing.T) {
	cfg := config.Default()
	f := buildFunnel(cfg, true, nil)
	if f.FailOpen == nil || f.Cache == nil || f.ModelID == "" || !f.ScoreFirst || f.Notes == nil || f.MinDescriptionRunes != config.DefaultMinDescriptionRunes {
		t.Fatalf("defaults: %+v", f)
	}
	if f.Notes.MinRank != config.DefaultNotesMinRank || f.Notes.Spend != nil {
		t.Fatalf("notes gate: %+v", f.Notes)
	}
	if off := buildFunnel(cfg, false, nil); off.Notes != nil || off.ScoreFirst {
		t.Fatal("with scoring off the notes gate would defer every note for good")
	}
	cfg.Funnel.ScoreAfterTriage = true
	if buildFunnel(cfg, true, nil).ScoreFirst {
		t.Fatal("score_after_triage ignored")
	}
}

func TestFillFunnelReport(t *testing.T) {
	var warns []string
	warnf := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }
	r := &pipeline.Runner{Funnel: buildFunnel(config.Default(), true, nil)}
	var rep advanceReport
	fillFunnelReport(&rep, r, warnf)
	if rep.FailOpen.Total() != 0 || len(warns) != 0 {
		t.Fatalf("a clean run reported fail-open: %+v %v", rep, warns)
	}
	r.FailOpen.Note(pipeline.CauseEmptyDescription)
	r.FailOpen.Note(pipeline.CauseLocationUnreadable)
	fillFunnelReport(&rep, r, warnf)
	if rep.FailOpen.EmptyDescription != 1 || rep.FailOpen.LocationUnreadable != 1 || len(warns) != 1 {
		t.Fatalf("%+v %v", rep, warns)
	}
	raw, err := json.Marshal(rep)
	if err != nil || !strings.Contains(string(raw), `"fail_open":{"empty_description":1,"location_unreadable":1}`) {
		t.Fatalf("not in the run record: %s %v", raw, err)
	}
	var plain advanceReport
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "fail_open") || strings.Contains(string(raw), "notes") {
		t.Fatalf("an empty tally is written into the record: %s", raw)
	}
}

func TestOrderAdvanceSendsDeferredNotesLast(t *testing.T) {
	mk := func(id string, st posting.Status, deferred bool) ledger.Record {
		r := ledger.Record{Key: posting.Key("gh:acme:" + id), Status: st, Annotations: map[string]string{}}
		if deferred {
			r.Annotations[pipeline.AnnNotesDeferred] = pipeline.DeferBudget
		}
		return r
	}
	todo := []ledger.Record{mk("1", posting.Scored, true), mk("2", posting.Live, false), mk("3", posting.Scored, true), mk("4", posting.Scored, false)}
	orderAdvance(todo, todo, nil)
	var got []string
	for _, r := range todo {
		got = append(got, string(r.Key))
	}
	// Deferred notes cost no call but still use a slot; they wait behind work that can advance.
	if want := "gh:acme:2,gh:acme:4,gh:acme:1,gh:acme:3"; strings.Join(got, ",") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}
