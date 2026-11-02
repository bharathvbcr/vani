package main

import (
	"bytes"
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
	if got.Stage != progressListing || got.Company != "Acme" || got.Index != 2 || got.Listed != 10 || got.ID != p.ID {
		t.Fatalf("%+v", got)
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
