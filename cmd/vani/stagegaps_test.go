package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestNotesOffIsRecordedOnlyWhenTheRunAsks(t *testing.T) {
	w := &wired{cfg: config.Default(), stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	exp, skip := w.notes(nil, nil)
	if exp != nil || !skip || w.explain != nil {
		t.Fatalf("list-only recorded notes: explainer %v skip %v explain %+v", exp, skip, w.explain)
	}
	w.recordGaps = true
	exp, skip = w.notes(nil, nil)
	if exp != nil || !skip || w.explain == nil || w.explain.Mode != explainOff {
		t.Fatalf("advance did not record notes off: explainer %v skip %v explain %+v", exp, skip, w.explain)
	}
	if strings.Contains(w.explain.Note, "\n") || w.explain.Note == "" {
		t.Fatalf("note %q", w.explain.Note)
	}
}

func TestCosineOffIsNotAShinglesPass(t *testing.T) {
	w := &wired{}
	w.noteSimilarity(true, "cosine off, shingles only: load failed")
	s := &nearDupSummary{}
	w.finishSimilarity(s)
	rec := runRecord{Outcome: outcomeOK, FinishedAt: histT0, runDetail: runDetail{NearDup: s}}
	if s.SimilarityOff == "" || s.SimilarityNote != "" || !runHasFailures(rec, histT0) {
		t.Fatalf("%+v failures %v", s, runHasFailures(rec, histT0))
	}

	w2 := &wired{}
	w2.noteSimilarity(false, "near-duplicate vector cache off: disk")
	w2.noteSimilarity(false, "near-duplicate vector cache not saved: disk")
	s2 := &nearDupSummary{}
	w2.finishSimilarity(s2)
	rec2 := runRecord{Outcome: outcomeOK, FinishedAt: histT0, runDetail: runDetail{NearDup: s2}}
	if s2.SimilarityOff != "" || !strings.Contains(s2.SimilarityNote, "cache off") || !strings.Contains(s2.SimilarityNote, "not saved") || runHasFailures(rec2, histT0) {
		t.Fatalf("%+v failures %v", s2, runHasFailures(rec2, histT0))
	}
}

func TestSemanticOffDoesNotRecordCosineOff(t *testing.T) {
	w := &wired{cfg: config.Default()}
	w.simOff = "stale"
	sim, finish := w.nearDupSimilarity(context.Background())
	finish()
	if sim != nil {
		t.Fatal("semantic off still returned a cosine")
	}
	s := &nearDupSummary{}
	w.finishSimilarity(s)
	if s.SimilarityOff != "" || s.SimilarityNote != "" {
		t.Fatalf("semantic off recorded %+v", s)
	}
}

func TestMarksErrorLandsOnTheSemanticNote(t *testing.T) {
	budget := &semanticReport{Ran: true, Note: "kept postings not warmed: semantic.warm_per_run was spent before the advance phase"}
	if semanticFailed(budget) {
		t.Fatal("a spent warm budget was a failure")
	}
	noteMarks(budget, nil)
	if strings.Contains(budget.Note, "marks") {
		t.Fatalf("a nil error was recorded: %q", budget.Note)
	}
	noteMarks(budget, errors.New("open "+strings.Repeat("m", 800)+"\nsecret"))
	if !strings.Contains(budget.Note, "marks could not be read") || !strings.Contains(budget.Note, "warm_per_run") || strings.Contains(budget.Note, "\n") {
		t.Fatalf("note %q", budget.Note)
	}
	if !semanticFailed(budget) {
		t.Fatal("an unreadable marks file was not a failure")
	}
	if utf8.RuneCountInString(budget.Note) > 2*maxStageNote+2 {
		t.Fatalf("note ran long: %d", utf8.RuneCountInString(budget.Note))
	}
}

func TestBoardFetchCountsSurviveACut(t *testing.T) {
	w := &wired{}
	long := strings.Repeat("t", maxBoardNameRunes+40)
	w.noteBoardFetch(sources.Board{ATS: posting.Workday, Token: "acme/External"}, boardFetch{Deferred: 4, Gone: 1})
	w.noteBoardFetch(sources.Board{ATS: posting.Greenhouse, Token: "payco"}, boardFetch{PayFailed: 2, PaySkipped: 3, PayError: "http 500\nbad " + strings.Repeat("x", 500)})
	w.noteBoardFetch(sources.Board{ATS: posting.Greenhouse, Token: "payco"}, boardFetch{PayFailed: 1, PaySkipped: 1, PayError: "second error"})
	w.noteBoardFetch(sources.Board{ATS: posting.Apple, Token: long}, boardFetch{})
	w.noteBoardFetch(sources.Board{ATS: posting.Eightfold, Token: "gone-co"}, boardFetch{Gone: 2})

	rep := &boardsReport{Items: []boardReport{{
		ATS: string(posting.Workday), Token: oneLine("acme/External", maxBoardNameRunes), Outcome: boardRead,
	}}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if rep.Items[0].Deferred != 4 || rep.Items[0].Gone != 1 || rep.Items[0].PayFailed != 0 {
		t.Fatalf("kept item %+v", rep.Items[0])
	}
	if rep.Deferred != 4 || rep.Gone != 3 || rep.PayFailed != 3 || rep.PaySkipped != 4 {
		t.Fatalf("totals deferred %d gone %d pay %d skipped %d", rep.Deferred, rep.Gone, rep.PayFailed, rep.PaySkipped)
	}
	if rep.PayError == "" || strings.Contains(rep.PayError, "\n") || strings.Contains(rep.PayError, "second") {
		t.Fatalf("pay error %q", rep.PayError)
	}
	if utf8.RuneCountInString(rep.PayError) > maxStageNote+1 {
		t.Fatalf("pay error length %d", utf8.RuneCountInString(rep.PayError))
	}
	if strings.Contains(rep.PayError, "gh:") || strings.Contains(rep.PayError, "description") {
		t.Fatalf("pay error carries a posting: %q", rep.PayError)
	}
	w.applyFetchesLocked(nil) // must not panic; the caller normally holds the lock
}

func TestShadowMissDoesNotWipeAStartFailure(t *testing.T) {
	w := &wired{}
	w.noteShadowMiss(-3, -1)
	if w.shadow != nil {
		t.Fatalf("zeroes allocated a note: %+v", w.shadow)
	}
	w.shadow = &shadowNote{Local: "bind"}
	w.noteShadowMiss(2, 1)
	if w.shadow.Local != "bind" || w.shadow.Errors != 2 || w.shadow.BusySkips != 1 {
		t.Fatalf("%+v", w.shadow)
	}
	w.noteShadowMiss(0, 0)
	if w.shadow.Local != "bind" || w.shadow.Errors != 2 {
		t.Fatalf("zeroes wiped the note: %+v", w.shadow)
	}
}

func TestListOnlyDoesNotRecordShadowOrNotes(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	w.noteShadow(`shadow "local" unavailable; running without a shadow`)
	w.noteExplain(explainPaused, "notes paused: no key")
	if w.shadow != nil || w.explain != nil {
		t.Fatalf("list-only recorded shadow %+v explain %+v", w.shadow, w.explain)
	}
	w.recordGaps = true
	w.shadow = &shadowNote{Local: "not started"}
	w.noteShadow("local decision model disabled: no endpoint")
	if w.shadow.Note != "" || w.shadow.Local != "not started" {
		t.Fatalf("start failure was counted twice: %+v", w.shadow)
	}
	w.noteShadow("lappi disabled: dial")
	w.noteShadow("lappi disabled: dial")
	if w.shadow.Note != "lappi disabled: dial" {
		t.Fatalf("note %q", w.shadow.Note)
	}
}

func TestStageGapReportNamesEachFailure(t *testing.T) {
	rec := histRec(1, outcomeOK)
	rec.DigestWarning = "rank weights missing"
	rec.Semantic = &semanticReport{Note: "semantic matching not run: model"}
	rec.Explain = &explainNote{Mode: explainPaused, Note: "notes paused: catalog"}
	rec.Advance = &advanceReport{FailOpen: pipeline.FailOpenCounts{EmptyDescription: 2, CoverageUnknown: 5}, Notes: pipeline.NotesCounts{UnknownRelevance: 1, Below: 4, BudgetSpent: 3}}
	rec.Boards = &boardsReport{Configured: 1, Read: 1, Listed: 1, PayFailed: 2, PayError: "http 500", Deferred: 3, Gone: 1}
	rec.Shadow = &shadowNote{Errors: 1, BusySkips: 2, Note: "lappi disabled"}
	rec.NearDup = &nearDupSummary{SimilarityOff: "cosine off, shingles only"}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	if !text.HasFailures {
		t.Fatal("gaps were not failures")
	}
	for _, want := range []string{
		"Digest warning: rank weights missing",
		"Semantic: semantic matching not run: model",
		"Notes: paused: notes paused: catalog",
		"Could not check: 2 empty description",
		"5 coverage unknown",
		"Notes gate: 4 below the floor",
		"1 relevance unknown",
		"3 notes budget spent",
		"Pay: 2 fetch(es) failed: http 500",
		"3 deferred",
		"1 gone",
		"Shadow: lappi disabled",
		"1 call(s) or slot(s) failed, 2 sampled posting(s) skipped while busy",
		"Near-duplicates: cosine off, shingles only",
	} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q\n%s", want, text.Text)
		}
	}
	if strings.Contains(text.Text, "No failures in this run.") {
		t.Fatal("a gapped run was called clean")
	}
}
