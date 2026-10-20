package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bharathvbcr/decide"
)

// The OpenRouter daily caps were budget/jev.json and budget/jev-notes.json
// until 2026-10-20 (the backend was named after the Jev client). They are now
// budget/openrouter.json and budget/openrouter-notes.json. The spend key inside
// the files stays "jev" (openRouterSpendKey): it is the persisted format, and
// renaming it would read as a day with nothing spent and so lift the cap.
const (
	openRouterBudgetFile = "openrouter.json"
	notesBudgetFile      = "openrouter-notes.json"

	legacyOpenRouterBudgetFile = "jev.json"
	legacyNotesBudgetFile      = "jev-notes.json"
	openRouterSpendKey         = "jev"
)

// budgetRenames maps each legacy budget file to its current name.
var budgetRenames = [...][2]string{
	{legacyOpenRouterBudgetFile, openRouterBudgetFile},
	{legacyNotesBudgetFile, notesBudgetFile},
}

// migrateBudgetFiles moves each legacy budget file to its new name, once.
//
//   - Neither file exists, or only the new one: nothing to do.
//   - Only the old file exists: it is linked to the new name (a link fails if
//     the name is taken, so a concurrent process can never be overwritten) and
//     the old name is removed.
//   - Both exist: an error. Two spend files are two different days of spend and
//     picking or merging one would move a cap, so the owner decides.
//   - The old name is not a regular file, or cannot be examined: an error.
//
// It is idempotent and safe to run from several processes at once.
func migrateBudgetFiles(stateDir string) error {
	dir := filepath.Join(stateDir, "budget")
	for _, r := range budgetRenames {
		if err := migrateBudgetFile(filepath.Join(dir, r[0]), filepath.Join(dir, r[1])); err != nil {
			return err
		}
	}
	return nil
}

func migrateBudgetFile(oldPath, newPath string) error {
	oldInfo, err := os.Lstat(oldPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("budget: cannot examine %s: %w", oldPath, err)
	}
	if !oldInfo.Mode().IsRegular() {
		return fmt.Errorf("budget: %s is not a regular file; move it aside (the spend file is now %s)", oldPath, filepath.Base(newPath))
	}
	switch err := os.Link(oldPath, newPath); {
	case err == nil:
	case errors.Is(err, os.ErrExist):
		newInfo, serr := os.Lstat(newPath)
		if serr != nil || !os.SameFile(oldInfo, newInfo) {
			return fmt.Errorf("budget: both %s and %s exist; keep the one with the spend you want counted and move the other aside, then run again", oldPath, newPath)
		}
		// Both names are one file: another process linked it and has not
		// removed the old name yet.
	default:
		return fmt.Errorf("budget: cannot move %s to %s: %w", oldPath, newPath, err)
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("budget: moved %s to %s but cannot remove the old name: %w", oldPath, newPath, err)
	}
	return nil
}

// budgetPath is the path of a budget file under stateDir, after migrating any
// legacy file. Every writer of a budget file goes through it.
func budgetPath(stateDir, name string) (string, error) {
	if err := migrateBudgetFiles(stateDir); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "budget", name), nil
}

// newBudgetCap opens the daily cap stored in the named budget file, migrating
// a legacy file first.
func newBudgetCap(stateDir, name string, capUSD float64) (*decide.DailyCap, error) {
	p, err := budgetPath(stateDir, name)
	if err != nil {
		return nil, err
	}
	return decide.NewDailyCap(p, capUSD)
}

// budgetReadPath is budgetPath for read-only callers (`vani status` writes
// nothing): the legacy file is read in place until a writing command migrates
// it, and both files existing is an error.
func budgetReadPath(stateDir, name string) (string, error) {
	dir := filepath.Join(stateDir, "budget")
	for _, r := range budgetRenames {
		if r[1] != name {
			continue
		}
		oldInfo, oerr := os.Lstat(filepath.Join(dir, r[0]))
		if oerr != nil {
			if errors.Is(oerr, os.ErrNotExist) {
				break
			}
			return "", fmt.Errorf("budget: cannot examine %s: %w", filepath.Join(dir, r[0]), oerr)
		}
		newInfo, nerr := os.Lstat(filepath.Join(dir, name))
		switch {
		case errors.Is(nerr, os.ErrNotExist):
			return filepath.Join(dir, r[0]), nil
		case nerr != nil:
			return "", fmt.Errorf("budget: cannot examine %s: %w", filepath.Join(dir, name), nerr)
		case os.SameFile(oldInfo, newInfo):
			return filepath.Join(dir, name), nil
		}
		return "", fmt.Errorf("budget: both %s and %s exist; keep the one with the spend you want counted and move the other aside", r[0], name)
	}
	return filepath.Join(dir, name), nil
}
