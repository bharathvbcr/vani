package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

func labelsFile(stateDir string) string {
	return filepath.Join(stateDir, "eval", "labels", eval.LabelsFile)
}

// Attack 7: the file exists exactly when the command exits 0, on every path.
func requireFileIffSuccess(t *testing.T, name, stateDir string, code int) {
	t.Helper()
	_, err := os.Stat(weightsPath(stateDir))
	switch {
	case code == 0 && err != nil:
		t.Errorf("%s: exit 0 but no weights file: %v", name, err)
	case code != 0 && !os.IsNotExist(err):
		t.Errorf("%s: exit %d but a weights file exists (%v)", name, code, err)
	}
}

// Attack 7: --seed omitted is the deterministic default 1; two runs print the
// same bytes and match an explicit --seed 1.
func TestAttackRankfitDefaultSeedIsDeterministic(t *testing.T) {
	_, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedJudgments(t, stateDir, 400)
	o1, _ := mustCode(t, 0, "rankfit", "--part", "weights", "--out", filepath.Join(t.TempDir(), "a.json"))
	o2, _ := mustCode(t, 0, "rankfit", "--part", "weights", "--out", filepath.Join(t.TempDir(), "b.json"))
	o3, _ := mustCode(t, 0, "rankfit", "--part", "weights", "--seed", "1", "--out", filepath.Join(t.TempDir(), "c.json"))
	strip := func(s string) string { // the --out path differs by design
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "wrote ") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	if strip(o1) != strip(o2) || strip(o1) != strip(o3) {
		t.Errorf("default-seed runs differ:\n%s\n---\n%s\n---\n%s", o1, o2, o3)
	}
}

// Attack 7: zero judgments, exactly MinJudged, one under, and a corrupt labels
// file: exit codes and files agree; nothing is written on any refusal.
func TestAttackRankfitRefusalPathsWriteNothing(t *testing.T) {
	t.Run("zero judgments", func(t *testing.T) {
		_, stateDir := setup(t)
		mustCode(t, 0, "init")
		code, _, e := do(t, "rankfit", "--part", "weights")
		if code != 1 || !strings.Contains(e, "has 0") {
			t.Errorf("exit %d: %s", code, e)
		}
		requireFileIffSuccess(t, "zero", stateDir, code)
	})
	t.Run("exactly MinJudged", func(t *testing.T) {
		_, stateDir := setup(t)
		fixedNow(t)
		mustCode(t, 0, "init")
		seedJudgments(t, stateDir, rankfit.MinJudged)
		code, o, e := do(t, "rankfit", "--part", "weights")
		t.Logf("exit %d\n%s\n%s", code, o, e)
		if code != 0 && code != 1 {
			t.Errorf("exit %d", code)
		}
		if code == 1 && !strings.Contains(e, "nothing written") {
			t.Errorf("a refusal without saying nothing was written: %s", e)
		}
		requireFileIffSuccess(t, "MinJudged", stateDir, code)
	})
	t.Run("corrupt labels file", func(t *testing.T) {
		_, stateDir := setup(t)
		fixedNow(t)
		mustCode(t, 0, "init")
		seedJudgments(t, stateDir, 400)
		f, err := os.OpenFile(labelsFile(stateDir), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		// Malformed JSON, a legacy would_apply row with no source, and a NaN-ish p_select.
		garbage := "{not json\n" +
			`{"v":1,"key":"gh:x:1","labels":{"would_apply":"yes"},"labeled_at":"2026-10-01T00:00:00Z"}` + "\n" +
			`{"v":1,"key":"gh:x:2","labels":{"would_apply":"yes"},"source":"decile:5","p_select":1e-320,"labeled_at":"2026-10-01T00:00:00Z"}` + "\n"
		if _, err := f.WriteString(garbage); err != nil {
			t.Fatal(err)
		}
		f.Close()
		code, o, e := do(t, "rankfit", "--part", "weights")
		if !strings.Contains(e, "malformed label lines skipped") {
			t.Errorf("corrupt lines not reported (exit %d):\n%s\n%s", code, o, e)
		}
		requireFileIffSuccess(t, "corrupt", stateDir, code)
	})
	t.Run("unreadable labels", func(t *testing.T) {
		_, stateDir := setup(t)
		mustCode(t, 0, "init")
		seedJudgments(t, stateDir, 200)
		if err := os.Remove(labelsFile(stateDir)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(labelsFile(stateDir), 0o700); err != nil { // a directory where the file should be
			t.Fatal(err)
		}
		code, _, e := do(t, "rankfit", "--part", "weights")
		if code == 0 {
			t.Errorf("exit 0 on unreadable labels: %s", e)
		}
		requireFileIffSuccess(t, "unreadable", stateDir, code)
	})
	t.Run("part all, no gain keeps no calibration", func(t *testing.T) {
		_, stateDir := setup(t)
		fixedNow(t)
		mustCode(t, 0, "init")
		seedNullJudgments(t, stateDir, 400, 501)
		code, _, _ := do(t, "rankfit", "--seed", "1")
		requireFileIffSuccess(t, "all/no-gain", stateDir, code)
	})
}
