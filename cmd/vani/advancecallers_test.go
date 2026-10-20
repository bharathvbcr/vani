package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Characterization of the three advance callers (the daily run, the inbox
// poll, rescore) and of runOnce's stage order. These pin behaviour, not
// design: they hold across the move of the advance and run sequences into
// internal/pipeline.

func charConfig(t *testing.T) config.Config {
	t.Helper()
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM = config.LLM{Provider: "gemini", Model: "gemini-3.8-flash"}
	return cfg
}

// The daily run drains the inbox, lists boards, annotates near-duplicates and
// only then reaches the advance phase; --list-only ends the run there with no
// advance report. The board report exists even with no board configured.
func TestCharRunOnceStageOrderListOnly(t *testing.T) {
	w, warn := openWired(t, charConfig(t))
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	w.tune(0, true)
	if err := w.runOnce(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
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
	for _, r := range w.led.All() {
		if r.Status != posting.Live {
			t.Errorf("%s %s: list-only must not advance", r.Key, r.Status)
		}
	}
}

// An advance phase that refuses (limit must be above 0) still runs after the
// near-duplicate stage, and leaves no advance report.
func TestCharRunOnceReachesAdvanceAfterNearDups(t *testing.T) {
	cfg := charConfig(t)
	cfg.AdvancePerRun = -1
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	err := w.runOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "advance limit -1 must be above 0") {
		t.Fatalf("err = %v\n%s", err, warn.String())
	}
	if strIdx(warn.String(), "near-duplicates:") < 0 {
		t.Errorf("near-duplicate stage did not run before advance:\n%s", warn.String())
	}
}

// The inbox poll, with no decision backend to advance with, leaves the folded
// postings Live and returns nil: they wait for the next run.
func TestCharPollInboxWithoutBackendLeavesPostingsLive(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.LLM = config.LLM{Provider: "gemini", Model: "gemini-3.8-flash"}
	cfg.LLM = config.LLM{}
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer", "Platform Engineer")
	if err := w.pollInbox(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
	}
	if !strings.Contains(warn.String(), "2 new job(s) wait for the next run") {
		t.Errorf("warnings: %s", warn.String())
	}
	recs := w.led.All()
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
	for _, r := range recs {
		if r.Status != posting.Live {
			t.Errorf("%s %s", r.Key, r.Status)
		}
	}
}

func titledRecs(titles ...string) []ledger.Record {
	out := make([]ledger.Record, len(titles))
	for i, t := range titles {
		k := posting.Key(fmt.Sprintf("gh:acme:%03d", i))
		out[i] = ledger.Record{Key: k, Status: posting.Live, Posting: posting.Posting{Key: k, Title: t}}
	}
	return out
}

// The run and the inbox poll rank by title signals (titleRank), spend a cap
// of limit, and count title drops against it without asking a backend; the
// report says what the cap left waiting.
func TestCharAdvanceCallerShapeRankCapTitleDrop(t *testing.T) {
	cfg := config.Config{TitleSignals: config.TitleSignals{Include: []string{"Inference"}, Exclude: []string{"Sales"}}}
	w := &wired{cfg: cfg}
	recs := titledRecs("Software Engineer", "Sales Engineer", "Inference Engineer", "Data Engineer", "Platform Engineer")
	var got []string
	var warn strings.Builder
	rep, err := advanceAll(context.Background(), recs, w.titleRank(), 3, 1, nil, func(_ context.Context, k posting.Key) error {
		got = append(got, string(k))
		return nil
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if err != nil {
		t.Fatal(err)
	}
	// Inference first, Sales last; the rest keep ledger order.
	if want := []string{"gh:acme:002", "gh:acme:000", "gh:acme:003"}; !slices.Equal(got, want) {
		t.Fatalf("advanced %v, want %v", got, want)
	}
	if rep.Eligible != 5 || rep.Limit != 3 || rep.Attempted != 3 || rep.Deferred != 2 || rep.Stopped != stopCap {
		t.Fatalf("report %+v", rep)
	}
	if !strings.Contains(warn.String(), "advance cap 3 reached; 2 posting(s) wait for the next run (advance_per_run)") {
		t.Errorf("warnings: %s", warn.String())
	}
}

// Rescore's shape: no rank, only the records its selector names, and the cap
// line (which names advance_per_run) is not printed.
func TestCharAdvanceCallerShapeRescoreSelectorAndQuietCap(t *testing.T) {
	recs := liveRecs(6)
	for _, i := range []int{1, 3} {
		recs[i].Status = posting.Explained
		recs[i].Posting.Description = "a described posting"
	}
	if !rescorable(recs[1]) || rescorable(recs[0]) {
		t.Fatal("fixture: records 1 and 3 must be the rescorable ones")
	}
	var got []string
	var warn strings.Builder
	rep, err := pipeline.Advance(context.Background(), baseAdvancePlan(kindRescore, 1, 2, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) }),
		pipeline.AdvanceDeps{Records: recs, Advance: func(_ context.Context, k posting.Key) error {
			got = append(got, string(k))
			return nil
		}})
	if err != nil || len(got) != 1 || rep.Eligible != 2 || rep.Deferred != 1 || rep.Stopped != stopCap {
		t.Fatalf("%v %v %+v", err, got, rep)
	}
	if warn.Len() != 0 {
		t.Errorf("a rescore prints no cap line, got %q", warn.String())
	}
}

func strIdx(s, sub string) int { return strings.Index(s, sub) }

// runPlan is the daily run's advance cap and whether it skips the phase.
func runPlan(w *wired) (limit int, skip bool) {
	p := w.advancePlan(kindRun, pipeline.DropPolicy{})
	return p.Limit, p.Skip
}
