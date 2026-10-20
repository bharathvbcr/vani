package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/decide"
)

func budgetFileAt(stateDir, name string) string { return filepath.Join(stateDir, "budget", name) }

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	return err == nil
}

func TestMigrateBudgetFilesOldOnlyMovesBoth(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, "budget/jev.json", `{"version":1,"days":{}}`)
	writeStateFile(t, dir, "budget/jev-notes.json", `{"version":1,"days":{"2026-10-19":{"jev":0.02}}}`)
	if err := migrateBudgetFiles(dir); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"jev.json", "jev-notes.json"} {
		if exists(t, budgetFileAt(dir, old)) {
			t.Errorf("%s still exists", old)
		}
	}
	got, err := os.ReadFile(budgetFileAt(dir, "openrouter-notes.json"))
	if err != nil || !strings.Contains(string(got), `"jev":0.02`) {
		t.Errorf("notes content not carried over: %q, %v", got, err)
	}
	if !exists(t, budgetFileAt(dir, "openrouter.json")) {
		t.Error("openrouter.json missing")
	}
	// Idempotent.
	if err := migrateBudgetFiles(dir); err != nil {
		t.Errorf("second run: %v", err)
	}
}

func TestMigrateBudgetFilesNeitherAndNewOnlyAreNoOps(t *testing.T) {
	dir := t.TempDir()
	if err := migrateBudgetFiles(dir); err != nil { // no budget dir at all
		t.Fatalf("neither: %v", err)
	}
	if exists(t, filepath.Join(dir, "budget")) {
		t.Error("migration created the budget directory")
	}
	writeStateFile(t, dir, "budget/openrouter.json", `{"version":1,"days":{}}`)
	if err := migrateBudgetFiles(dir); err != nil {
		t.Fatalf("new only: %v", err)
	}
	if !exists(t, budgetFileAt(dir, "openrouter.json")) {
		t.Error("new file disappeared")
	}
}

func TestMigrateBudgetFilesBothExistRefusesAndTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, "budget/jev.json", `{"version":1,"days":{"2026-10-19":{"jev":1}}}`)
	writeStateFile(t, dir, "budget/openrouter.json", `{"version":1,"days":{"2026-10-20":{"jev":2}}}`)
	err := migrateBudgetFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "both") || !strings.Contains(err.Error(), "jev.json") || !strings.Contains(err.Error(), "openrouter.json") {
		t.Fatalf("err = %v, want a refusal naming both files", err)
	}
	for name, want := range map[string]string{"jev.json": `"jev":1`, "openrouter.json": `"jev":2`} {
		got, rerr := os.ReadFile(budgetFileAt(dir, name))
		if rerr != nil || !strings.Contains(string(got), want) {
			t.Errorf("%s was changed: %q, %v", name, got, rerr)
		}
	}
	if _, err := budgetPath(dir, openRouterBudgetFile); err == nil {
		t.Error("budgetPath must refuse while both exist")
	}
	if _, err := newBudgetCap(dir, openRouterBudgetFile, 0.25); err == nil {
		t.Error("a cap must not open while both exist")
	}
	if _, err := budgetReadPath(dir, openRouterBudgetFile); err == nil {
		t.Error("budgetReadPath must refuse while both exist")
	}
}

func TestMigrateBudgetFilesUnreadableOldRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(budgetFileAt(dir, "jev.json"), 0o700); err != nil { // a directory, not a file
		t.Fatal(err)
	}
	if err := migrateBudgetFiles(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory in the old name: err = %v", err)
	}

	denied := t.TempDir()
	writeStateFile(t, denied, "budget/jev.json", `{"version":1,"days":{}}`)
	if err := os.Chmod(filepath.Join(denied, "budget"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(denied, "budget"), 0o700) })
	if _, err := os.Lstat(budgetFileAt(denied, "jev.json")); err == nil {
		t.Skip("the budget directory is readable despite mode 0 (running as root?)")
	}
	err := migrateBudgetFiles(denied)
	if err == nil || !strings.Contains(err.Error(), "cannot examine") {
		t.Errorf("unreadable budget directory: err = %v", err)
	}
	if _, err := budgetReadPath(denied, openRouterBudgetFile); err == nil {
		t.Error("budgetReadPath must not report an unreadable directory as no file")
	}
}

// A day's spend recorded in jev.json still counts after the rename.
func TestMigratedBudgetCarriesTheDaysSpend(t *testing.T) {
	dir := t.TempDir()
	old, err := decide.NewDailyCap(budgetFileAt(dir, "jev.json"), 0.25)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Charge(context.Background(), openRouterSpendKey, 0.25); err != nil {
		t.Fatal(err)
	}
	c, err := newBudgetCap(dir, openRouterBudgetFile, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Allow(context.Background(), openRouterSpendKey); err == nil {
		t.Error("the cap was lifted by the rename")
	}
}

// Status writes nothing: it reads an old file in place.
func TestStatusReadsLegacyBudgetInPlace(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	writeStateFile(t, stateDir, "budget/jev.json", `{"version":1,"days":{"`+today()+`":{"jev":0.1}}}`)
	or := openRouterItem(t, statusOf(t))
	if !or.OK || or.SpentUSD == nil || *or.SpentUSD != 0.1 {
		t.Errorf("openrouter: %+v", or)
	}
	if exists(t, budgetFileAt(stateDir, "openrouter.json")) || !exists(t, budgetFileAt(stateDir, "jev.json")) {
		t.Error("status migrated the file")
	}
}

func TestWireAppMigratesBudgetFilesAndRefusesWhenBothExist(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	writeStateFile(t, dir, "budget/jev.json", `{"version":1,"days":{}}`)
	app, err := wireApp(cfg, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = app.Closer()
	if exists(t, budgetFileAt(dir, "jev.json")) || !exists(t, budgetFileAt(dir, "openrouter.json")) {
		t.Error("wireApp did not migrate")
	}

	both := t.TempDir()
	writeStateFile(t, both, "budget/jev.json", `{"version":1,"days":{}}`)
	writeStateFile(t, both, "budget/openrouter.json", `{"version":1,"days":{}}`)
	if _, err := wireApp(cfg, both, nil); err == nil {
		t.Error("wireApp must refuse when both exist")
	}
	// The refusal released the ledger lock.
	if err := os.Remove(budgetFileAt(both, "jev.json")); err != nil {
		t.Fatal(err)
	}
	app, err = wireApp(cfg, both, nil)
	if err != nil {
		t.Fatalf("after the owner resolved it: %v", err)
	}
	_ = app.Closer()
}
