package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// A capped warm embeds the posting the rank puts first, not the fresher one.
// The older posting's title carries a title signal, which Score weighs; the
// fresher one has none. On the old order (freshest first) the budget of one went
// to the fresher posting.
func TestWarmOrderFollowsScore(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	cfg.TitleSignals.Include = []string{"platform", "engineer"}

	// Score (measured, fake clock): 4-day "Platform Engineer" with a strong_fit
	// reading ~ 45, 1-day "Accountant" with none ~ 29. Freshness alone says the
	// opposite.
	older := semRec("gh:a:older", "Platform Engineer", infraDesc)
	older.Status = posting.Explained
	older.Posting.PostedAt = now().AddDate(0, 0, -4)
	older.Annotations = map[string]string{"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered"}
	fresher := semRec("gh:a:fresher", "Accountant", infraDesc+" Fresher.")
	fresher.Status = posting.Explained
	fresher.Posting.PostedAt = now().AddDate(0, 0, -1)
	recs := []ledger.Record{fresher, older} // ledger order puts the fresher one first too

	// The ordering itself, before anything is embedded.
	if got := docKeys(warmDocs(recs, nil, warmOrder(cfg)...)); !slices.Equal(got, []string{"gh:a:older", "gh:a:fresher"}) {
		t.Fatalf("warm order %v: the higher-Score posting must lead", got)
	}
	if got := docKeys(warmDocs(recs, nil)); !slices.Equal(got, []string{"gh:a:fresher", "gh:a:older"}) {
		t.Fatalf("with no rank options the order stays freshest first, got %v", got)
	}

	rep := warmSemantic(context.Background(), cfg, state, recs, nil, 1)
	if rep.Scored != 1 || rep.Skipped != 1 {
		t.Fatalf("report %+v, want one scored and one skipped by the budget", rep)
	}
	m, warning := semanticLookup(cfg, state)
	if m == nil {
		t.Fatal(warning)
	}
	if _, ok := semanticScore(m, older); !ok {
		t.Error("the higher-Score posting was not the one warmed")
	}
	if _, ok := semanticScore(m, fresher); ok {
		t.Error("the fresher, lower-Score posting was warmed instead")
	}
}

// The advance phase's warm orders its live postings the same way; and a
// posting whose note is deferred (relevance unknown, the very kind a warm is
// for) is not sent to the back as the advance phase would.
func TestPreAdvanceWarmOrderFollowsScore(t *testing.T) {
	cfgPath, _ := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	cfg.TitleSignals.Include = []string{"platform", "engineer"}

	fresher := semRec("gh:a:fresher", "Accountant", infraDesc)
	fresher.Posting.PostedAt = now().AddDate(0, 0, -1)
	older := semRec("gh:a:older", "Platform Engineer", infraDesc+" Older.")
	older.Posting.PostedAt = now().AddDate(0, 0, -4)
	recs := []ledger.Record{fresher, older}
	if got := docKeys(preAdvanceDocs(recs, nil, warmOrder(cfg)...)); !slices.Equal(got, []string{"gh:a:older", "gh:a:fresher"}) {
		t.Errorf("pre-advance warm order %v", got)
	}
	if got := docKeys(preAdvanceDocs(recs, nil)); !slices.Equal(got, []string{"gh:a:fresher", "gh:a:older"}) {
		t.Errorf("with no rank options the order stays freshest first, got %v", got)
	}

	older.Status = posting.Scored
	older.Annotations = map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferUnknownRelevance}
	byWarm := []ledger.Record{fresher, older}
	orderAdvance(byWarm, byWarm, nil, warmOrder(cfg)...)
	if got := keysOf(byWarm); got[0] != "gh:a:older" {
		t.Errorf("a deferred-notes posting was demoted in a warm order: %v", got)
	}
	rules, titleRank := rankRules(cfg)
	byAdvance := []ledger.Record{fresher, older}
	orderAdvance(byAdvance, byAdvance, nil, withSemantic(func(ledger.Record) (float64, bool) { return 0, false }), withRank(rules, titleRank))
	if got := keysOf(byAdvance); got[0] != "gh:a:fresher" {
		t.Errorf("the advance order must still demote a deferred-notes posting: %v", got)
	}
}

func docKeys(docs []semantic.Doc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Key
	}
	return out
}

// The vectors rescored without embedding are shown in the run record (`vani
// runs show`), next to what the vector log did; a log that could not be used
// is a problem, not a clean run. Rescored does not spend the run's budget.
func TestRunRecordShowsRescored(t *testing.T) {
	_, state := setup(t)
	r := histRec(1, outcomeOK)
	r.SemanticPre = &semanticReport{Ran: true, Scored: 4, Rescored: 6, VectorsCount: 10, VectorsBytes: 3 << 20}
	r.Semantic = &semanticReport{Ran: true, Rescored: 7, Cached: 2, VectorsCount: 12, VectorsBytes: 5 << 20, VectorsTornBytes: 17}
	writeLast(t, state, r)

	out, _ := mustCode(t, 0, "runs", "show")
	for _, want := range []string{
		"Semantic before advance: scored 4, cached 0, failed 0, skipped 0, rescored 6 from stored vectors; vectors: 10 posting(s), 3.0 MB",
		"Semantic: scored 0, cached 2, failed 0, skipped 0, rescored 7 from stored vectors; vectors: 12 posting(s), 5.0 MB; cut 17 bytes of an incomplete tail",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("runs show missing %q in:\n%s", want, out)
		}
	}

	// A note keeps the rescored count and the vector line next to it.
	if got := semanticShowLine(&semanticReport{Ran: true, Rescored: 3, VectorsErr: "log unreadable", Note: "semantic matching stopped early: context canceled"}); !strings.Contains(got, "stopped early") ||
		!strings.Contains(got, "rescored 3") || !strings.Contains(got, "problem: log unreadable") {
		t.Errorf("a note hid the rescored count or the vector problem: %q", got)
	}

	// An unusable vector log is a failed warm in the run's problem list; a log
	// that only holds postings is not.
	for name, rep := range map[string]*semanticReport{
		"error":   {Ran: true, VectorsErr: "vectors.log: checksum"},
		"refused": {Ran: true, VectorsRefused: 2},
		"rebuilt": {Ran: true, VectorsRebuilt: true},
	} {
		if !semanticFailed(rep) {
			t.Errorf("%s: a warm that lost or could not use its vector log must be flagged", name)
		}
	}
	if semanticFailed(&semanticReport{Ran: true, Rescored: 9, VectorsCount: 9, VectorsAppended: 3}) {
		t.Error("a warm that rescored and stored vectors is not a failure")
	}
}

// Only embedded postings spend semantic.warm_per_run; rescored ones, which cost
// no embedding, are not subtracted from what the post-advance warm may embed.
func TestRescoredDoesNotSpendTheRunBudget(t *testing.T) {
	if got := postAdvanceBudget(100, &semanticReport{Scored: 30, Rescored: 500}); got != 70 {
		t.Errorf("budget after a warm that scored 30 and rescored 500 = %d, want 70", got)
	}
	if got := postAdvanceBudget(100, &semanticReport{Rescored: 5000}); got != 100 {
		t.Errorf("rescoring alone spent budget: %d", got)
	}
	if got := postAdvanceBudget(100, nil); got != 100 {
		t.Errorf("no first warm: %d", got)
	}
	if got := postAdvanceBudget(10, &semanticReport{Scored: 99}); got != 0 {
		t.Errorf("budget must not go negative: %d", got)
	}
}

func TestVectorRanksAreTheTextHashesWithTheBestScore(t *testing.T) {
	rec := func(key, title, desc string) ledger.Record { return semRec(key, title, desc) }
	recs := []ledger.Record{rec("a", "T1", "d1"), rec("b", "T2", "d2"), rec("c", "T1", "d1")}
	scores := map[posting.Key]float64{"a": 10, "b": 20, "c": 30}
	got := vectorRanks(recs, scores)
	if len(got) != 2 || got[semantic.TextHash("T1", "d1")] != 30 || got[semantic.TextHash("T2", "d2")] != 20 {
		t.Errorf("ranks %v: one text under two postings takes the best Score", got)
	}
	if got := vectorRanks(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("no records: %v (an empty live set is not no live set)", got)
	}
	if got := vectorRanks(recs, nil); len(got) != 2 || got[semantic.TextHash("T1", "d1")] != 0 {
		t.Errorf("no Scores rank 0 and stay live: %v", got)
	}
}

// The status compares the vector log with ChunkVersion (how postings are cut),
// not with the scoring Version: a healthy log must not warn after a scoring
// bump, and a log cut under another ChunkVersion must.
func TestSemanticStatusVectorChunkVersion(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s and golang", posted: daysAgo(2), seenAgo: time.Hour})
	mustCode(t, 0, "semantic", "warm")
	if semantic.Version == semantic.ChunkVersion {
		t.Log("Version and ChunkVersion are equal; the healthy-log check cannot tell them apart")
	}
	out, _ := mustCode(t, 0, "semantic", "status")
	if strings.Contains(out, "chunked under") {
		t.Errorf("a healthy log warned:\n%s", out)
	}
	if want := fmt.Sprintf("chunking v%d", semantic.ChunkVersion); !strings.Contains(out, want) {
		t.Errorf("status does not show %q:\n%s", want, out)
	}

	// Header: magic (8) | format u32 | ChunkVersion u32 | ...
	log := filepath.Join(state, "semantic", "vectors.log")
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	raw[12] = byte(semantic.ChunkVersion + 1)
	if err := os.WriteFile(log, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ = mustCode(t, 0, "semantic", "status")
	if want := fmt.Sprintf("chunked under v%d, this binary chunks under v%d", semantic.ChunkVersion+1, semantic.ChunkVersion); !strings.Contains(out, want) {
		t.Errorf("a log from another ChunkVersion did not warn (%q):\n%s", want, out)
	}
}

// The status shows the profile's calibration (floor, ceiling, anchor chunks)
// once a warm has embedded it, and says "not embedded yet" before, never zeros.
func TestSemanticStatusShowsCalibration(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s and golang", posted: daysAgo(2), seenAgo: time.Hour})
	out, _ := mustCode(t, 0, "semantic", "status")
	if !strings.Contains(out, "calibration: not embedded yet") || strings.Contains(out, "floor") {
		t.Errorf("before any warm:\n%s", out)
	}
	mustCode(t, 0, "semantic", "warm")
	out, _ = mustCode(t, 0, "semantic", "status")
	var floor, ceil float64
	var chunks int
	i := strings.Index(out, "calibration: ")
	if i < 0 {
		t.Fatalf("no calibration line:\n%s", out)
	}
	if n, err := fmt.Sscanf(out[i:], "calibration: floor %f, ceiling %f, anchor chunks %d", &floor, &ceil, &chunks); n != 3 || err != nil {
		t.Fatalf("calibration line not parsed (%d, %v):\n%s", n, err, out[i:])
	}
	if !(ceil > floor) || chunks < 1 {
		t.Errorf("floor %.3f ceiling %.3f anchor chunks %d", floor, ceil, chunks)
	}
}

// `vani semantic warm` and `vani semantic status` show the vector log: how many
// postings, how big, its model; an incomplete tail; a log that cannot be read
// (said as such, never as empty); and a rebuild the last run or idle warm made.
func TestSemanticStatusShowsVectors(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s and golang", posted: daysAgo(2), seenAgo: time.Hour})

	if out, _ := mustCode(t, 0, "semantic", "status"); !strings.Contains(out, "vectors: none yet") {
		t.Errorf("before any warm: %q", out)
	}

	out, _ := mustCode(t, 0, "semantic", "warm")
	if !strings.Contains(out, "vectors: 1 posting(s)") {
		t.Errorf("warm output does not show the vector log: %q", out)
	}
	out, _ = mustCode(t, 0, "semantic", "status")
	model := semantic.NewFakeEmbedder(256, semSynonyms).Model()
	for _, want := range []string{"vectors: 1 posting(s), ", "model " + model, "chunking v"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "incomplete tail") || strings.Contains(out, "started it again") {
		t.Errorf("a clean log reported damage:\n%s", out)
	}

	// An incomplete tail (a crashed append: the last record cut short) is shown.
	log := filepath.Join(state, "semantic", "vectors.log")
	fi, err := os.Stat(log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(log, fi.Size()-5); err != nil {
		t.Fatal(err)
	}
	if out, _ = mustCode(t, 0, "semantic", "status"); !strings.Contains(out, "byte(s) of an incomplete tail") {
		t.Errorf("torn tail not shown:\n%s", out)
	}

	// A rebuild is known from the last run's record and the idle warm's state.
	r := histRec(1, outcomeOK)
	r.Semantic = &semanticReport{Ran: true, VectorsRebuilt: true}
	writeLast(t, state, r)
	if err := writeIdleState(state, idleState{Kind: idleDone, Reason: "caught up for now", VectorsRebuilt: true}); err != nil {
		t.Fatal(err)
	}
	out, _ = mustCode(t, 0, "semantic", "status")
	if !strings.Contains(out, "the last run found the log unreadable") || !strings.Contains(out, "the idle warm found the log unreadable") {
		t.Errorf("rebuilds not shown:\n%s", out)
	}

	// A log that cannot be read is said, not shown as empty.
	if err := os.WriteFile(log, []byte("not a vector log, and long enough to hold a header or so"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _ = mustCode(t, 0, "semantic", "status"); !strings.Contains(out, "vectors: unreadable") {
		t.Errorf("corrupt log not reported:\n%s", out)
	}
}
