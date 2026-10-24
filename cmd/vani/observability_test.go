package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/logo"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestSkippedSlotIsHistoryOnly(t *testing.T) {
	state := t.TempDir()
	var buf bytes.Buffer
	e := &env{stateDir: state, stderr: &buf}
	last := histRec(1, outcomeOK)
	if err := writeRunRecord(state, last); err != nil {
		t.Fatal(err)
	}
	slot := time.Date(2026, 10, 23, 15, 30, 0, 0, time.UTC)
	late := 7*time.Hour + 30*time.Minute
	e.recordSkippedSlot(slot, late)

	got, err := readRunRecord(state)
	if err != nil || got == nil || runID(*got) != runID(last) {
		t.Fatalf("last.json = %+v, %v; the skip must not replace it", got, err)
	}
	skip, err := readRunRecordFile(runHistoryPath(state, runID(runRecord{StartedAt: slot, PID: 0})))
	if err != nil || skip == nil {
		t.Fatalf("skip record: %v %+v", err, skip)
	}
	if skip.Outcome != outcomeSkipped || skip.PID != 0 || skip.Trigger != triggerSchedule {
		t.Fatalf("skip = %+v", skip)
	}
	if !skip.StartedAt.Equal(slot) || !skip.FinishedAt.Equal(slot.Add(late)) {
		t.Fatalf("times %s .. %s", skip.StartedAt, skip.FinishedAt)
	}
	if skip.Error == "" || strings.Contains(skip.Error, "\n") {
		t.Fatalf("error %q", skip.Error)
	}
	facet := e.lastSkipFacet()
	if facet == nil || !facet.OK || !facet.Slot.Equal(slot) || facet.LateSec != int(late.Seconds()) {
		t.Fatalf("facet %+v", facet)
	}
	if runHasFailures(*skip, slot.Add(late)) {
		t.Fatal("a skip is not a pipeline failure")
	}

	// The same slot recorded again replaces the one file.
	e.recordSkippedSlot(slot, 8*time.Hour)
	files, _, err := listRunHistory(state, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(runHistoryPath(state, f.ID), runID(runRecord{StartedAt: slot, PID: 0})+".json") || f.ID == runID(runRecord{StartedAt: slot, PID: 0}) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("skip files = %d, want 1 (%v)", n, files)
	}

	// A later real run hides the skip. A running last.json hides it too.
	newer := histRec(2, outcomeOK)
	newer.StartedAt = slot.Add(9 * time.Hour)
	newer.FinishedAt = newer.StartedAt.Add(time.Minute)
	if err := writeRunRecord(state, newer); err != nil {
		t.Fatal(err)
	}
	if e.lastSkipFacet() != nil {
		t.Fatal("skip still shown after a later run")
	}
	running := newer
	running.Outcome = outcomeRunning
	running.FinishedAt = time.Time{}
	running.StartedAt = slot.Add(-time.Hour)
	if err := writeRunRecord(state, running); err != nil {
		t.Fatal(err)
	}
	if e.lastSkipFacet() != nil {
		t.Fatal("skip shown while a run is recorded as running")
	}
}

func TestSkippedSlotWriteFailureDoesNotLookRecorded(t *testing.T) {
	state := t.TempDir()
	hist := filepath.Join(state, runRecordDir, runHistoryDir)
	if err := os.MkdirAll(filepath.Dir(hist), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hist, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	e := &env{stateDir: state, stderr: &buf}
	e.recordSkippedSlot(time.Date(2026, 10, 23, 15, 30, 0, 0, time.UTC), 7*time.Hour)
	if !strings.Contains(buf.String(), "could not record the skipped run") {
		t.Fatalf("stderr = %q", buf.String())
	}
	// The history folder is a file, so the skip cannot be written and cannot be
	// listed. An unreadable history is not a recorded skip: ok stays false.
	if facet := e.lastSkipFacet(); facet != nil && facet.OK {
		t.Fatalf("a failed write was reported as a skip: %+v", facet)
	}
}

func TestCappedTeeCountsDroppedLinesAndIgnoresLogErrors(t *testing.T) {
	var primary bytes.Buffer
	tee := newCappedTee(&primary)
	var extra bytes.Buffer
	tee.setExtra(&extra, 8)
	body := []byte("0123456789\nsecond\n")
	n, err := tee.Write(body)
	if err != nil || n != len(body) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if extra.String() != "01234567" {
		t.Fatalf("kept %q", extra.String())
	}
	if primary.String() != string(body) {
		t.Fatalf("primary %q", primary.String())
	}
	// "89\nsecond\n" holds two newlines.
	if got := tee.clearExtra(); got != 2 {
		t.Fatalf("dropped lines = %d, want 2", got)
	}

	extra.Reset()
	tee.setExtra(&extra, 4)
	if _, err := tee.Write([]byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	if extra.String() != "abcd" {
		t.Fatalf("kept %q", extra.String())
	}
	if got := tee.clearExtra(); got != 1 {
		t.Fatalf("a dropped write with no newline counted %d, want 1", got)
	}

	tee.setExtra(errWriter{}, 100)
	if n, err := tee.Write([]byte("still here\n")); err != nil || n != len("still here\n") {
		t.Fatalf("a log write failure failed the write: %d %v", n, err)
	}
	if !strings.Contains(primary.String(), "still here") {
		t.Fatalf("primary lost the line: %q", primary.String())
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestOpenOwnedRunLogRefusesASymlinkAndABadID(t *testing.T) {
	state := t.TempDir()
	if _, _, err := openOwnedRunLog(state, ""); err == nil {
		t.Fatal("empty id was accepted")
	}
	if _, _, err := openOwnedRunLog(state, "not an id"); err == nil {
		t.Fatal("bad id was accepted")
	}
	id := runID(histRec(1, outcomeOK))
	dir := filepath.Join(state, runRecordDir, runHistoryDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, id+".log")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openOwnedRunLog(state, id); err == nil {
		t.Fatal("opened a symlink")
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "SECRET" {
		t.Fatalf("symlink target = %q, %v", b, err)
	}

	if err := os.Remove(filepath.Join(dir, id+".log")); err != nil {
		t.Fatal(err)
	}
	f, path, err := openOwnedRunLog(state, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("vani: hello\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := checkRunLog(state, path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", st, err)
	}
}

func TestPruneRemovesTheRunsLogAndKeepsTheManualLog(t *testing.T) {
	state := t.TempDir()
	older, newer := histRec(1, outcomeOK), histRec(2, outcomeOK)
	if err := writeRunHistory(state, older); err != nil {
		t.Fatal(err)
	}
	if err := writeRunHistory(state, newer); err != nil {
		t.Fatal(err)
	}
	hist := filepath.Join(state, runRecordDir, runHistoryDir)
	sibling := filepath.Join(hist, runID(older)+".log")
	manual := filepath.Join(hist, "manual-"+runID(older)+".log")
	if err := os.WriteFile(sibling, []byte("run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manual, []byte("app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneRunHistory(state, 1, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sibling); !os.IsNotExist(err) {
		t.Fatalf("sibling log still there: %v", err)
	}
	if b, err := os.ReadFile(manual); err != nil || string(b) != "app" {
		t.Fatalf("manual log = %q, %v", b, err)
	}
	if _, err := os.Lstat(runHistoryPath(state, runID(newer))); err != nil {
		t.Fatal(err)
	}
}

func TestInboxStatusBoundsAndRejectsACorruptFile(t *testing.T) {
	state := t.TempDir()
	if _, err := readInboxStatus(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	var buf bytes.Buffer
	e := &env{stateDir: state, stderr: &buf}
	if e.inboxFacet() != nil {
		t.Fatal("a missing poll was reported")
	}
	long := strings.Repeat("inbox failed ", 40) + "\nsecond line"
	if err := writeInboxStatus(state, inboxStatus{At: time.Unix(1_700_000_000, 0).UTC(), Error: long, Bad: 2, Live: 1}); err != nil {
		t.Fatal(err)
	}
	st, err := readInboxStatus(state)
	if err != nil {
		t.Fatal(err)
	}
	// CleanText cuts at maxPanicText and may add one ellipsis rune.
	if st.Bad != 2 || st.Live != 1 || strings.Contains(st.Error, "\n") || utf8.RuneCountInString(st.Error) > maxPanicText+1 {
		t.Fatalf("%+v", st)
	}
	facet := e.inboxFacet()
	if facet == nil || !facet.OK || facet.Bad != 2 || facet.Error == "" {
		t.Fatalf("facet %+v", facet)
	}
	if err := os.WriteFile(inboxStatusPath(state), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInboxStatus(state); err == nil {
		t.Fatal("corrupt status was read")
	}
	bad := e.inboxFacet()
	if bad == nil || bad.OK {
		t.Fatalf("corrupt facet looked ok: %+v", bad)
	}
	if err := os.WriteFile(inboxStatusPath(state), bytes.Repeat([]byte("x"), maxInboxStatus+10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInboxStatus(state); err == nil {
		t.Fatal("oversized status was read")
	}
}

func TestPollInboxPanicKeepsABoundedStatusAndPrintsAStack(t *testing.T) {
	var buf bytes.Buffer
	e := &env{stateDir: t.TempDir(), stderr: &buf}
	err := e.pollInboxOnce(t.Context(), func(context.Context) error {
		panic("boom\n" + strings.Repeat("y", 500))
	}, &sync.Mutex{})
	if err == nil || !strings.Contains(err.Error(), "panic:") {
		t.Fatal(err)
	}
	st, rerr := readInboxStatus(e.stateDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(st.Error, "goroutine") || strings.Contains(st.Error, "\n") || utf8.RuneCountInString(st.Error) > maxPanicText+1 {
		t.Fatalf("status kept more than the value: %q", st.Error)
	}
	if !strings.Contains(st.Error, "boom") {
		t.Fatalf("status %q", st.Error)
	}
	if !strings.Contains(buf.String(), "goroutine") || !strings.Contains(buf.String(), "inbox poll panicked") {
		t.Fatalf("stderr:\n%s", buf.String())
	}
}

func TestStageSummariesKeepCountsAndOneLine(t *testing.T) {
	body := "Staff engineer\n" + strings.Repeat("must have distributed systems ", 30)
	nd := summarizeNearDup(pipeline.NearDupReport{Records: 4, Partial: true, SimilarityErrors: 2}, errors.New(body))
	if nd == nil || !nd.Partial || nd.SimilarityErrors != 2 || nd.Records != 4 {
		t.Fatalf("%+v", nd)
	}
	if strings.Contains(nd.Note, "\n") || utf8.RuneCountInString(nd.Note) > maxStageNote+1 || !strings.Contains(nd.Note, "Staff engineer") {
		t.Fatalf("note %q", nd.Note)
	}
	raw, err := marshalRunRecord(runRecord{PID: 1, StartedAt: histT0, FinishedAt: histT0.Add(time.Minute), Outcome: outcomeOK, runDetail: runDetail{NearDup: nd}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\nStaff")) || bytes.Contains(raw, []byte(body)) {
		t.Fatal("the record kept the posting text")
	}

	ch := pipeline.ExclusionChange{
		Drop:    []ledger.Record{{Key: posting.Key("k1")}, {Key: posting.Key("k2")}},
		Reasons: []string{"title matches a private note"}, Unread: []ledger.Record{{Key: posting.Key("k3")}},
		BodyUnavailable: 1,
	}
	ex := summarizeExclusions(ch)
	if ex.Dropped != 2 || ex.Unread != 1 || ex.Reopened != 0 || ex.BodyUnavailable != 1 {
		t.Fatalf("%+v", ex)
	}
	exRaw, err := marshalRunRecord(runRecord{PID: 1, StartedAt: histT0, FinishedAt: histT0.Add(time.Minute), Outcome: outcomeOK, runDetail: runDetail{Exclusions: ex}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(exRaw, []byte("private note")) || bytes.Contains(exRaw, []byte("k1")) {
		t.Fatal("exclusion summary kept a reason or a key")
	}

	lg := summarizeLogos(logo.Result{
		Failed: 2, Fetched: 1, Errors: []string{"cdn.example\nsecret-token", "other.example"},
		StateReset: strings.Repeat("reset ", 80),
	}, 4, nil)
	if lg.MissingHosts != 4 || lg.Failed != 2 || strings.Contains(lg.Example, "\n") || strings.Contains(lg.Example, "other.example") {
		t.Fatalf("%+v", lg)
	}
	if utf8.RuneCountInString(lg.StateReset) > maxStageNote+1 || utf8.RuneCountInString(lg.Example) > maxStageNote+1 {
		t.Fatalf("unbounded logo text: %+v", lg)
	}

	w := &wired{}
	w.mu.Lock()
	w.nearDup = nd
	w.exclusions = ex
	w.logos = lg
	w.shadow = &shadowNote{Local: oneLine(body, maxStageNote)}
	w.mu.Unlock()
	rep := w.runReport()
	if rep.NearDup != nd || rep.Exclusions != ex || rep.Logos != lg || rep.Shadow == nil || strings.Contains(rep.Shadow.Local, "\n") {
		t.Fatalf("%+v", rep)
	}

	rec := histRec(1, outcomeOK)
	rec.ImpressionsError = "could not record what the digest showed"
	// LogDropped lives on the embedded runDetail, so set it after that copy.
	rec.runDetail = rep
	rec.LogDropped = 3
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Source: "last", Path: "last.json", Rec: rec}, histT0, true)
	if !text.HasFailures {
		t.Fatal("stage gaps were not failures")
	}
	for _, want := range []string{"Impressions:", "Run log: 3 line", "Shadow:", "Exclusions: 1 description", "Logos: 2 failed", "Near-duplicates: partial=true"} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("report missing %q\n%s", want, text.Text)
		}
	}
	if strings.Contains(text.Text, "No failures in this run.") {
		t.Fatal("a gapped run was called clean")
	}
}

func TestQuietStageSummariesAreNotFailures(t *testing.T) {
	rec := histRec(1, outcomeOK)
	rec.NearDup = &nearDupSummary{}
	rec.Exclusions = &exclusionSummary{Dropped: 2, Reopened: 1}
	rec.Logos = &logoSummary{Fresh: 3}
	rec.Shadow = &shadowNote{}
	if runHasFailures(rec, histT0) {
		t.Fatal("a finished stage with nothing unread was a failure")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	if !strings.Contains(text.Text, "No failures in this run.") {
		t.Fatalf("report:\n%s", text.Text)
	}
}

func TestShadowNoteIsOnlyAStartFailure(t *testing.T) {
	w := &wired{}
	w.mu.Lock()
	w.shadow = &shadowNote{Local: oneLine("bind: "+strings.Repeat("a", 1000)+"\n127.0.0.1", maxStageNote)}
	w.mu.Unlock()
	got := w.runReport().Shadow
	if got == nil || strings.Contains(got.Local, "\n") || utf8.RuneCountInString(got.Local) > maxStageNote+1 {
		t.Fatalf("%+v", got)
	}
	if daemon.MissedRunWindow != 6*time.Hour {
		t.Fatal("skip wording depends on the window the daemon actually uses")
	}
}
