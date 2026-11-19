package main

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// vanishOnRead makes every history read see its file gone after it was listed:
// the entry is removed, then the real reader runs, which reports a missing
// file as (nil, nil).
func vanishOnRead(t *testing.T, onlyFirst bool) {
	t.Helper()
	old := readHistoryRecord
	var mu sync.Mutex
	gone := false
	readHistoryRecord = func(path string) (*runRecord, error) {
		mu.Lock()
		defer mu.Unlock()
		if !onlyFirst || !gone {
			_ = os.Remove(path)
			gone = true
		}
		return old(path)
	}
	t.Cleanup(func() { readHistoryRecord = old })
}

func TestRunsListSkipsAnEntryThatVanishedAfterListing(t *testing.T) {
	_, stateDir := setup(t)
	id := putHistoryRaw(t, stateDir, histRec(1, outcomeOK))
	vanishOnRead(t, false)
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	for _, r := range l.Runs {
		if r.ID == id {
			t.Errorf("a vanished entry was listed: %+v", r)
		}
	}
	if len(l.Problems) != 0 {
		t.Errorf("a vanished entry is a skip, not a problem: %+v", l.Problems)
	}
}

func TestRunsShowOfAnEntryThatVanishedIsNoSuchRunNotAPanic(t *testing.T) {
	_, stateDir := setup(t)
	id := putHistoryRaw(t, stateDir, histRec(1, outcomeOK))
	vanishOnRead(t, false)
	code, o, e := do(t, "runs", "show", id)
	if code == 0 || code == 2 {
		t.Fatalf("exit %d, want a runtime failure (not success, not the usage code)\n%s\n%s", code, o, e)
	}
	if !strings.Contains(o+e, "no run "+id) {
		t.Errorf("want a no-such-run error: %s %s", o, e)
	}
}

func TestDoctorSkipsAHistoryEntryThatVanishedAfterListing(t *testing.T) {
	doctorSetup(t)
	doctorSeams(t, &doctorFake{}, 0)
	vanishOnRead(t, true)
	mustCode(t, 0, "doctor", "--dry-run")
}
