package suggest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Rotation and append are one exclusive step across processes, not only inside
// one: two commands that both see a full log must not both rotate it (the second
// move would replace the generation the first just kept with a near-empty file).

const (
	racerDirEnv   = "VANI_OUTCOME_RACER_DIR"
	racerStartEnv = "VANI_OUTCOME_RACER_START"
	racerIDEnv    = "VANI_OUTCOME_RACER_ID"
	racerAppends  = 3
)

var raceTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func raceLine(code string) []byte {
	b, err := (Outcome{V: 1, Time: raceTime.Format(time.RFC3339), Op: OpSuggest, Code: code}).line()
	if err != nil {
		panic(err)
	}
	return b
}

// prefillNearFull writes whole "fill" records until the live log has room for
// fewer than headroom more lines, and returns how many it wrote.
func prefillNearFull(t *testing.T, dir string, headroom int) int {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, OutcomeDir), 0o700); err != nil {
		t.Fatal(err)
	}
	fill, racer := raceLine("fill"), raceLine("w0")
	n := (MaxOutcomeBytes - headroom*len(racer)) / len(fill)
	if err := os.WriteFile(filepath.Join(dir, OutcomeDir, OutcomeFile), bytes.Repeat(fill, n), 0o600); err != nil {
		t.Fatal(err)
	}
	return n
}

// countCodes counts records by Code across the live log and its one generation,
// and the lines that are not records.
func countCodes(t *testing.T, dir string) (map[string]int, int) {
	t.Helper()
	got, broken := map[string]int{}, 0
	for _, name := range []string{OutcomeFile, OutcomeRotated} {
		raw, err := os.ReadFile(filepath.Join(dir, OutcomeDir, name))
		if err != nil {
			continue
		}
		for _, ln := range bytes.Split(raw, []byte{'\n'}) {
			if len(bytes.TrimSpace(ln)) == 0 {
				continue
			}
			var o Outcome
			if json.Unmarshal(ln, &o) != nil {
				broken++
				continue
			}
			got[o.Code]++
		}
	}
	return got, broken
}

// TestOutcomeRacerChild is the body of one racing process; it does nothing unless
// the parent test re-executed the test binary for it.
func TestOutcomeRacerChild(t *testing.T) {
	dir := os.Getenv(racerDirEnv)
	if dir == "" {
		t.Skip("only runs as a child of TestOutcomeRotationAcrossProcesses")
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(os.Getenv(racerStartEnv)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no start signal")
		}
		time.Sleep(time.Millisecond)
	}
	code := "w" + os.Getenv(racerIDEnv)
	for range racerAppends {
		if err := AppendOutcome(dir, Outcome{V: 1, Time: raceTime.Format(time.RFC3339), Op: OpSuggest, Code: code}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func TestOutcomeRotationAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	const procs, rounds = 6, 12
	for round := range rounds {
		dir := t.TempDir()
		filled := prefillNearFull(t, dir, 2)
		start := filepath.Join(dir, "go")
		var wg sync.WaitGroup
		errs := make([]string, procs)
		for i := range procs {
			cmd := exec.Command(os.Args[0], "-test.run=^TestOutcomeRacerChild$")
			cmd.Env = append(os.Environ(), racerDirEnv+"="+dir, racerStartEnv+"="+start, fmt.Sprintf("%s=%d", racerIDEnv, i))
			wg.Add(1)
			go func() {
				defer wg.Done()
				if out, err := cmd.CombinedOutput(); err != nil {
					errs[i] = fmt.Sprintf("%v: %s", err, out)
				}
			}()
		}
		time.Sleep(150 * time.Millisecond) // let every child reach its wait
		if err := os.WriteFile(start, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		for i, e := range errs {
			if e != "" {
				t.Errorf("round %d: racer %d failed: %s", round, i, strings.TrimSpace(e))
			}
		}
		got, broken := countCodes(t, dir)
		if broken != 0 {
			t.Errorf("round %d: %d broken lines", round, broken)
		}
		if got["fill"] != filled {
			t.Errorf("round %d: %d of the %d earlier records survive: a generation was lost", round, got["fill"], filled)
		}
		for i := range procs {
			if c := got[fmt.Sprintf("w%d", i)]; c != racerAppends {
				t.Errorf("round %d: racer %d has %d of %d records", round, i, c, racerAppends)
			}
		}
		if t.Failed() {
			return
		}
	}
}

// Goroutines that each open their own lock handle are as separate as processes
// to the lock: none may drop a record or a generation.
func TestOutcomeRotationAcrossHandles(t *testing.T) {
	dir := t.TempDir()
	filled := prefillNearFull(t, dir, 2)
	const writers = 24
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := AppendOutcome(dir, Outcome{V: 1, Time: raceTime.Format(time.RFC3339), Op: OpSuggest, Code: fmt.Sprintf("w%d", i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, broken := countCodes(t, dir)
	total := 0
	for c, n := range got {
		if c != "fill" {
			total += n
		}
	}
	if broken != 0 || got["fill"] != filled || total != writers {
		t.Errorf("fill %d/%d, racers %d/%d, %d broken lines", got["fill"], filled, total, writers, broken)
	}
}
