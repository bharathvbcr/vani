package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func goodProgress() runProgress {
	return runProgress{
		V: runProgressVersion, PID: 7, ID: "20261102T150405.000Z-7", At: now(),
		Stage: progressListing, Phase: progressPhaseListing,
		Index: 1, Total: 1, ATS: "gh", Token: "acme", Company: "Acme",
	}
}

func TestRunProgressRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := goodProgress()
	p.Index, p.Total, p.Done, p.Read, p.Listed = 2, 5, 1, 1, 10
	p.Company = "Acme"
	if err := writeRunProgress(dir, p); err != nil {
		t.Fatal(err)
	}
	got, err := readRunProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != progressListing || got.Company != "Acme" || got.Index != 2 || got.Listed != 10 || got.ID != p.ID || got.Added != nil {
		t.Fatalf("%+v", got)
	}
	zero := 0
	p.Added = &zero
	if err := writeRunProgress(dir, p); err != nil {
		t.Fatal(err)
	}
	rawZero, err := os.ReadFile(runsPath(dir, runProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawZero), `"added":0`) {
		t.Fatalf("a reported zero was omitted: %s", rawZero)
	}
	got, err = readRunProgress(dir)
	if err != nil || got.Added == nil || *got.Added != 0 {
		t.Fatalf("zero added: %v %+v", err, got)
	}
	// A field this version does not know is ignored, not a failed read.
	raw, err := os.ReadFile(runsPath(dir, runProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	withExtra := strings.TrimSuffix(string(raw), "}") + `,"posting_key":"gh:acme:1"}`
	if err := os.WriteFile(runsPath(dir, runProgressFile), []byte(withExtra), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readRunProgress(dir)
	if err != nil || got.Company != "Acme" {
		t.Fatalf("extra field: %v %+v", err, got)
	}
}

func TestReadRunProgressRetriesAReplace(t *testing.T) {
	dir := t.TempDir()
	if err := writeRunProgress(dir, goodProgress()); err != nil {
		t.Fatal(err)
	}
	prev := readProgressFile
	t.Cleanup(func() { readProgressFile = prev })
	calls := 0
	readProgressFile = func(path string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("progress.json: %w", errChangedWhileOpen)
		}
		return prev(path)
	}
	got, err := readRunProgress(dir)
	if err != nil || got.Company != "Acme" {
		t.Fatalf("retry: %v %+v", err, got)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}

	calls = 0
	readProgressFile = func(string) ([]byte, error) {
		calls++
		return nil, os.ErrNotExist
	}
	if _, err := readRunProgress(dir); !errors.Is(err, os.ErrNotExist) || calls != 1 {
		t.Fatalf("missing file retried: calls %d err %v", calls, err)
	}

	calls = 0
	readProgressFile = func(string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("progress.json: %w", errChangedWhileOpen)
	}
	if _, err := readRunProgress(dir); !errors.Is(err, errChangedWhileOpen) || calls != progressReadAttempts {
		t.Fatalf("gave up: calls %d err %v", calls, err)
	}
}

func TestRunProgressRefusesGarbage(t *testing.T) {
	stamp := now().Format(time.RFC3339Nano)
	id := "20261102T150405.000Z-7"
	base := `"v":1,"pid":7,"id":"` + id + `","updated_at":"` + stamp + `"`
	cases := []struct {
		name string
		body string
	}{
		{"negative", "{" + base + `,"stage":"listing","failed":-1}`},
		{"negative added", "{" + base + `,"stage":"listing","added":-1}`},
		{"huge added", "{" + base + `,"stage":"listing","added":` + fmt.Sprint(maxProgressCount+1) + `}`},
		{"rate limit", "{" + base + `,"stage":"listing","done":1,"failed":1,"rate_limited":2}`},
		{"attempted", "{" + base + `,"stage":"advance","attempted":5,"limit":3,"eligible":9}`},
		{"index", "{" + base + `,"stage":"listing","index":2,"total":1}`},
		{"unknown stage", "{" + base + `,"stage":"teleport"}`},
		{"unknown phase", "{" + base + `,"stage":"listing","phase":"sideways"}`},
		{"unknown outcome", "{" + base + `,"stage":"listing","phase":"done","outcome":"teleport"}`},
		{"early outcome", "{" + base + `,"stage":"listing","phase":"listing","outcome":"failed"}`},
		{"long company", "{" + base + `,"stage":"listing","company":"` + strings.Repeat("a", maxBoardNameRunes+1) + `"}`},
		{"newline", "{" + base + `,"stage":"listing","company":"Acme\nCorp"}`},
		{"version", `{"v":2,"pid":7,"id":"` + id + `","updated_at":"` + stamp + `","stage":"starting"}`},
		{"trailing", "{" + base + `,"stage":"starting"} {}`},
		{"ahead", "{" + base + `,"stage":"starting","updated_at":"` + now().Add(2*time.Minute).Format(time.RFC3339Nano) + `"}`},
	}
	// The ahead case duplicates updated_at. JSON uses the last one. Put it cleanly.
	cases[len(cases)-1].body = `{"v":1,"pid":7,"id":"` + id + `","stage":"starting","updated_at":"` + now().Add(2*time.Minute).Format(time.RFC3339Nano) + `"}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, runRecordDir), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runsPath(dir, runProgressFile), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readRunProgress(dir); err == nil {
				t.Fatal("read a snapshot that should be refused")
			}
		})
	}
	t.Run("oversized", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, runRecordDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(runsPath(dir, runProgressFile), bytes.Repeat([]byte("x"), maxRunProgress+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRunProgress(dir); err == nil {
			t.Fatal("read an oversized snapshot")
		}
	})
}

func TestRunProgressRefusesASymlinkAndADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"company":"SecretPortal"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := runsPath(dir, runProgressFile)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunProgress(dir); err == nil {
		t.Fatal("read progress through a symlink")
	}
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != `{"company":"SecretPortal"}` {
		t.Fatalf("symlink target changed: %v %q", err, raw)
	}
	if err := removeRunProgress(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink leaf still there: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunProgress(dir); err == nil {
		t.Fatal("read a directory as progress")
	}
	if err := removeRunProgress(dir); err == nil {
		t.Fatal("removed a directory")
	}
	if fi, err := os.Stat(link); err != nil || !fi.IsDir() {
		t.Fatalf("directory leaf: %v %+v", err, fi)
	}
}

func TestRunProgressEmptyStateDirWritesNothing(t *testing.T) {
	var stderr bytes.Buffer
	e := &env{stderr: &stderr}
	e.beginLive("20261102T150405.000Z-1")
	e.noteProgress(runProgress{Stage: progressListing, Company: "Acme"})
	e.endLive()
	if _, err := os.Stat(filepath.Join("runs", runProgressFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("progress under the working directory: %v", err)
	}
	if err := writeRunProgress("", goodProgress()); !errors.Is(err, errNoStateDir) {
		t.Fatalf("write: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestNoteProgressBoundsNamesAndWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	e := &env{stateDir: dir, stderr: &stderr}
	e.beginLive("20261102T150405.000Z-" + "1")
	// The id above is valid. beginLive wrote "starting".
	huge := "Acme\nCorp " + strings.Repeat("x", 500)
	e.noteProgress(runProgress{
		Stage: progressListing, Phase: progressPhaseListing, Index: 1, Total: 1,
		Company: huge, Token: "tok\ten", ATS: "gh",
	})
	got, err := readRunProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got.Company, "\r\n") || strings.ContainsAny(got.Token, "\r\n\t") {
		t.Fatalf("name not one line: company %q token %q", got.Company, got.Token)
	}
	if n := utf8.RuneCountInString(got.Company); n > maxBoardNameRunes || n == 0 {
		t.Fatalf("company runes %d: %q", n, got.Company)
	}
	if !strings.HasPrefix(got.Company, "Acme Corp") {
		t.Fatalf("company %q", got.Company)
	}

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	e = &env{stateDir: blocker, stderr: &stderr}
	e.beginLive("20261102T150405.000Z-1")
	e.noteProgress(runProgress{Stage: progressListing, Company: "Acme"})
	e.endLive()
	if n := strings.Count(stderr.String(), "live progress was not written"); n != 1 {
		t.Fatalf("warnings %d:\n%s", n, stderr.String())
	}
}

func TestBindLiveWithoutABoxDoesNothing(t *testing.T) {
	var e env
	e.bindLive(&App{})
	e.bindLive(nil)
	(&App{}).BindLive(func(runProgress) { t.Fatal("called") })
}

func TestProgressLines(t *testing.T) {
	listing := &progressFacet{
		facet: facet{OK: true}, Stage: progressListing, Phase: progressPhaseListing,
		Index: 2, Total: 5, Done: 1, Read: 1, Listed: 10, Company: "Acme", ATS: "gh", Token: "acme",
	}
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: reading Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("listing %q", got)
	}
	listing.Phase = progressPhaseReconciling
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: saving Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("saving %q", got)
	}
	listing.Phase = progressPhaseDone
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: read Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("read %q", got)
	}
	listing.Outcome = boardFailed
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: could not read Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("failed %q", got)
	}
	listing.Outcome = boardRateLimited
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: rate-limited Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("limited %q", got)
	}
	listing.Outcome = boardSkipped
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: skipped Acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("skipped %q", got)
	}
	listing.Outcome = ""
	listing.Phase = progressPhaseListing
	listing.Company = ""
	if got := progressLine(lastRunFacet{Progress: listing}); got != "run: reading gh:acme (2 of 5); 1 read, 10 listed" {
		t.Fatalf("token %q", got)
	}
	listing.Failed, listing.RateLimited, listing.Done = 1, 1, 2
	if got := progressCounts(listing); got != "1 read, 1 failed, 1 rate-limited, 10 listed" {
		t.Fatalf("counts %q", got)
	}
	n := 4
	listing.Added = &n
	if got := progressCounts(listing); got != "1 read, 1 failed, 1 rate-limited, 10 listed, 4 added" {
		t.Fatalf("added counts %q", got)
	}
	if got := progressLine(lastRunFacet{Progress: &progressFacet{facet: facet{OK: true}, Stage: progressListing, Total: 5}}); got != "run: reading, 5 boards" {
		t.Fatalf("boards %q", got)
	}
	adv := &progressFacet{facet: facet{OK: true}, Stage: progressAdvance, Attempted: 40, Limit: 300, Eligible: 900}
	if got := progressLine(lastRunFacet{Progress: adv}); got != "run: triaging, 40 of 300 started, 900 eligible" {
		t.Fatalf("advance %q", got)
	}
	bad := &progressFacet{facet: facet{Reason: "live progress is from another run"}}
	if got := progressLine(lastRunFacet{Progress: bad}); got != "run: live progress could not be read: live progress is from another run" {
		t.Fatalf("bad %q", got)
	}
	if got := progressLine(lastRunFacet{}); got != "" {
		t.Fatalf("absent %q", got)
	}
	if got := progressStageWord(progressPreparing); got != "preparing the run" {
		t.Fatalf("stage %q", got)
	}
}

func TestListingTotalsCarryAndANewRunForgetsThem(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	e := &env{stateDir: dir, stderr: &stderr}
	id := "20261102T150405.000Z-1"
	e.beginLive(id)
	added := 4
	e.noteProgress(runProgress{
		Stage: progressListing, Phase: progressPhaseListing, Index: 12, Total: 48,
		Done: 11, Read: 9, Failed: 2, RateLimited: 1, Listed: 40, Added: &added,
		Company: "Acme", ATS: "gh", Token: "acme", Outcome: "",
	})
	e.noteProgress(runProgress{Stage: progressNearDup})
	got, err := readRunProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != progressNearDup || got.Company != "" || got.Index != 0 || got.Phase != "" || got.Outcome != "" || got.ATS != "" {
		t.Fatalf("carried the board in hand: %+v", got)
	}
	if got.Total != 48 || got.Done != 11 || got.Read != 9 || got.Failed != 2 || got.RateLimited != 1 || got.Skipped != 0 || got.Listed != 40 || got.Added == nil || *got.Added != 4 {
		t.Fatalf("totals dropped: %+v", got)
	}
	e.noteProgress(runProgress{Stage: progressAdvance, Attempted: 40, Limit: 300, Eligible: 900})
	got, err = readRunProgress(dir)
	if err != nil {
		t.Fatalf("carried snapshot is not sane: %v", err)
	}
	if got.Stage != progressAdvance || got.Attempted != 40 || got.Limit != 300 || got.Read != 9 || got.Added == nil || *got.Added != 4 {
		t.Fatalf("advance %+v", got)
	}
	e.noteProgress(runProgress{Stage: progressPreparing})
	got, err = readRunProgress(dir)
	if err != nil || got.Stage != progressPreparing || got.Total != 0 || got.Added != nil || got.Listed != 0 {
		t.Fatalf("a stage before listing took the totals: %v %+v", err, got)
	}
	e.noteProgress(runProgress{Stage: progressDigest})
	got, err = readRunProgress(dir)
	if err != nil || got.Stage != progressDigest || got.Read != 9 || got.Added == nil || *got.Added != 4 {
		t.Fatalf("digest %+v %v", got, err)
	}

	e.beginLive("20261102T160405.000Z-1")
	got, err = readRunProgress(dir)
	if err != nil || got.Stage != progressStarting || got.Total != 0 || got.Added != nil || got.Listed != 0 || got.Read != 0 {
		t.Fatalf("new run kept the previous totals: %v %+v", err, got)
	}
	e.noteProgress(runProgress{Stage: progressInbox})
	e.noteProgress(runProgress{Stage: progressNearDup})
	got, err = readRunProgress(dir)
	if err != nil || got.Stage != progressNearDup || got.Added != nil || got.Total != 0 || got.Read != 0 {
		t.Fatalf("totals leaked onto a run that has not listed: %v %+v", err, got)
	}
}

func TestInsaneCarriedSnapshotDoesNotReplaceAGoodFile(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	e := &env{stateDir: dir, stderr: &stderr}
	e.beginLive("20261102T150405.000Z-1")
	added := 3
	e.noteProgress(runProgress{
		Stage: progressListing, Phase: progressPhaseListing, Index: 2, Total: 5,
		Done: 1, Read: 1, Listed: 10, Added: &added, Company: "Acme",
	})
	before, err := os.ReadFile(runsPath(dir, runProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	e.liveMu.Lock()
	e.listing = listingTotals{set: true, total: 5, done: 4, read: 0, failed: 0, skipped: 0, added: progressCount(1)}
	e.liveMu.Unlock()
	e.noteProgress(runProgress{Stage: progressNearDup})
	after, err := os.ReadFile(runsPath(dir, runProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("insane merge overwrote the file:\n%s", after)
	}
	got, err := readRunProgress(dir)
	if err != nil || got.Stage != progressListing || got.Company != "Acme" || got.Added == nil || *got.Added != 3 {
		t.Fatalf("good snapshot gone: %v %+v", err, got)
	}
}

func TestArchiveInterruptedRunKeepsTotalsAfterTheSnapshotIsReplaced(t *testing.T) {
	dir := t.TempDir()
	started := now().Add(-time.Minute)
	rec := runRecord{PID: 4242, StartedAt: started, Outcome: outcomeRunning}
	if err := writeRunRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	added := 7
	snap := runProgress{
		V: runProgressVersion, PID: rec.PID, ID: runID(rec), At: now(),
		Stage: progressListing, Phase: progressPhaseListing, Index: 12, Total: 48,
		Done: 11, Read: 9, Failed: 2, RateLimited: 1, Listed: 40, Added: &added,
		Company: "Acme", ATS: "gh", Token: "acme",
	}
	if err := writeRunProgress(dir, snap); err != nil {
		t.Fatal(err)
	}
	got, err := archiveInterruptedRun(dir)
	if err != nil || got == nil || got.Outcome != outcomeInterrupted {
		t.Fatalf("archive: %v %+v", err, got)
	}
	if got.Boards == nil || got.Boards.Added == nil || *got.Boards.Added != 7 || got.Boards.NotReached != 37 || got.Boards.Unrecorded != 11 || len(got.Boards.Items) != 0 || got.Boards.Read != 9 || got.Boards.Failed != 2 || got.Boards.RateLimited != 1 || got.Boards.Listed != 40 || got.Boards.Configured != 48 {
		t.Fatalf("history boards %+v", got.Boards)
	}
	raw, err := os.ReadFile(runHistoryPath(dir, runID(rec)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Acme") {
		t.Fatalf("history named a company: %s", raw)
	}

	other := snap
	other.ID = "20260101T000000.000Z-9"
	other.Company = "OtherCo"
	other.At = now()
	otherAdded := 99
	other.Added = &otherAdded
	if err := writeRunProgress(dir, other); err != nil {
		t.Fatal(err)
	}
	kept := historyRecord(t, dir, runID(rec))
	if kept.Boards == nil || kept.Boards.Added == nil || *kept.Boards.Added != 7 || kept.Boards.NotReached != 37 || len(kept.Boards.Items) != 0 {
		t.Fatalf("replaced snapshot changed history: %+v", kept.Boards)
	}
	raw, err = os.ReadFile(runHistoryPath(dir, runID(rec)))
	if err != nil || strings.Contains(string(raw), "OtherCo") {
		t.Fatalf("history picked up the replacement: %v %s", err, raw)
	}
}

func TestArchiveOfAPreListingSnapshotIsNotAZeroReport(t *testing.T) {
	dir := t.TempDir()
	started := now().Add(-time.Minute)
	rec := runRecord{PID: 4242, StartedAt: started, Outcome: outcomeRunning}
	if err := writeRunRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	snap := runProgress{
		V: runProgressVersion, PID: rec.PID, ID: runID(rec), At: now(),
		Stage: progressStarting,
	}
	if err := writeRunProgress(dir, snap); err != nil {
		t.Fatal(err)
	}
	got, err := archiveInterruptedRun(dir)
	if err != nil || got == nil || got.Boards != nil {
		t.Fatalf("pre-listing archive: %v boards %+v", err, got)
	}
	raw, err := os.ReadFile(runHistoryPath(dir, runID(rec)))
	if err != nil || strings.Contains(string(raw), `"boards"`) {
		t.Fatalf("a zero report was written: %v %s", err, raw)
	}
}

func TestFinishDailyRunFillsMissingBoardsAndKeepsAStoredReport(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	e := &env{stateDir: dir, stderr: &stderr}
	started := now().Add(-time.Second)
	rec := runRecord{PID: os.Getpid(), StartedAt: started, Outcome: outcomeRunning}
	e.beginLive(runID(rec))
	added := 4
	e.noteProgress(runProgress{
		Stage: progressListing, Phase: progressPhaseDone, Index: 2, Total: 5,
		Done: 2, Read: 1, Failed: 1, Listed: 10, Added: &added, Outcome: boardRead,
		Company: "Acme",
	})

	stored := boardsReportOf(1, []boardReport{{ATS: "gh", Token: "kept", Outcome: boardRead, Reconcile: &reconcileCounts{New: 8}}})
	keep := &App{RunReport: func() runDetail { return runDetail{Boards: &stored} }}
	if err := e.finishDailyRun(context.Background(), keep, &rec, stagePipeline, context.Canceled, func() {}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if rec.Outcome != outcomeCancelled || rec.Boards == nil || len(rec.Boards.Items) != 1 || rec.Boards.Items[0].Token != "kept" || rec.Boards.Added == nil || *rec.Boards.Added != 8 {
		t.Fatalf("stored report replaced: outcome %s boards %+v", rec.Outcome, rec.Boards)
	}
	e.endLive()
	if _, err := readRunProgress(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean stop left the snapshot: %v", err)
	}
	saved, err := readRunRecord(dir)
	if err != nil || saved == nil || saved.Boards == nil || len(saved.Boards.Items) != 1 {
		t.Fatalf("saved %+v %v", saved, err)
	}

	// A panic before the in-memory report is stored keeps the snapshot totals.
	dir = t.TempDir()
	stderr.Reset()
	e = &env{stateDir: dir, stderr: &stderr}
	rec = runRecord{PID: os.Getpid(), StartedAt: started, Outcome: outcomeRunning}
	e.beginLive(runID(rec))
	e.noteProgress(runProgress{
		Stage: progressListing, Phase: progressPhaseDone, Index: 2, Total: 5,
		Done: 2, Read: 1, Failed: 1, Listed: 10, Added: &added, Outcome: boardRead,
	})
	panicApp := &App{RunReport: func() runDetail { panic("inside listBoards") }}
	err = e.finishDailyRun(context.Background(), panicApp, &rec, stagePipeline, nil, func() {})
	if err == nil || rec.Outcome != outcomeFailed || rec.Boards == nil || rec.Boards.Added == nil || *rec.Boards.Added != 4 || rec.Boards.NotReached != 3 || rec.Boards.Unrecorded != 2 || len(rec.Boards.Items) != 0 || rec.Boards.Read != 1 || rec.Boards.Listed != 10 {
		t.Fatalf("panic finish err %v boards %+v", err, rec.Boards)
	}
	e.endLive()
	saved, err = readRunRecord(dir)
	if err != nil || saved == nil || saved.Boards == nil || saved.Boards.Added == nil || *saved.Boards.Added != 4 {
		t.Fatalf("record lost the totals: %v %+v", err, saved)
	}
	if _, err := readRunProgress(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot still there: %v", err)
	}
}
