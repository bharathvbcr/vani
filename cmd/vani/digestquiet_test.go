package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/marks"
)

// The daily digest lists applications that went quiet, from the marks store
// the run reads; a stage file it cannot read is said in the digest, which is
// still written (the marks themselves read, so nothing is held back).
func TestDigestListsQuietApplicationsAndSaysWhenItCannotCheck(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	dir := marks.Dir(stateDir)
	applied := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	if _, err := marks.SetMark(dir, "gh:a:1", marks.Applied, applied); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetStage(dir, "gh:a:1", marks.StageInterview, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetNote(dir, "gh:a:1", "private-note-canary", time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "run-once")
	md := readDigest(t, stateDir)
	if !strings.Contains(md, "## Quiet since applied (1)") || !strings.Contains(md, "interview, 16 days quiet") || !strings.Contains(md, "key `gh:a:1`") {
		t.Errorf("the quiet application is not in the digest:\n%s", md)
	}
	if strings.Contains(md, "private-note-canary") {
		t.Errorf("a note reached the digest:\n%s", md)
	}

	if err := os.WriteFile(filepath.Join(dir, "stages.jsonl"), []byte("{broken\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, "digests", "2026-10-18.md")); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "run-once")
	md = readDigest(t, stateDir)
	if !strings.Contains(md, "## Quiet since applied") || !strings.Contains(md, "could not be read") || strings.Contains(md, "days quiet") {
		t.Errorf("unreadable stages must be said, not rendered as nothing quiet:\n%s", md)
	}
}

func readDigest(t *testing.T, stateDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "digests", "2026-10-18.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
