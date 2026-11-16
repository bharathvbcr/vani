package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The sample knowledge base in examples/ is what a new user starts from. These
// tests build the real rkb binary and run it on a copy of the sample the way
// Vani runs it (only PATH, HOME, TMPDIR and RKB_CODE_ROOT, no user config), so
// a change to rkb that breaks a new user's first run fails here. A missing
// sample or a failed build is a failure, never a skip.

const sampleSel = "career/CurriculumVitae/resume/resumes/sample.json"

var (
	rkbBinOnce sync.Once
	rkbBin     string
	rkbBinErr  error
	rkbBinDir  string
)

// builtRKB builds this module's rkb once per test run.
func builtRKB(t *testing.T) string {
	t.Helper()
	rkbBinOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			rkbBinErr = errors.New("go is not on PATH: " + err.Error())
			return
		}
		rkbBinDir, rkbBinErr = os.MkdirTemp("", "rkb-example-bin-")
		if rkbBinErr != nil {
			return
		}
		rkbBin = filepath.Join(rkbBinDir, "rkb")
		cmd := exec.Command(goBin, "build", "-o", rkbBin, ".")
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			rkbBinErr = errors.New("go build: " + err.Error() + "\n" + string(out))
		}
	})
	if rkbBinErr != nil {
		t.Fatalf("cannot build rkb for the sample tests: %v", rkbBinErr)
	}
	return rkbBin
}

// sampleCopy copies examples/ into a fresh code root, so rkb's lock files
// and outputs never land in the repository.
func sampleCopy(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs("examples")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, sampleSel)); err != nil {
		t.Fatalf("the sample knowledge base is missing: %v", err)
	}
	dst := t.TempDir()
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy sample: %v", err)
	}
	return dst
}

// runSample runs rkb on root with Vani's minimal environment and an empty HOME.
func runSample(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(builtRKB(t), args...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "RKB_CODE_ROOT=" + root}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("rkb %v did not run: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func TestSampleKnowledgeBaseNewUserPath(t *testing.T) {
	root := sampleCopy(t)
	sel := filepath.Join(root, sampleSel)

	out, code := runSample(t, root, "doctor")
	if !strings.Contains(out, "config     embedded default") {
		t.Errorf("a new user has no config file; doctor must say the embedded default is in use:\n%s", out)
	}
	_ = code // doctor warns about tools a CI machine may lack; only the config line is pinned

	if out, code := runSample(t, root, "bank", "check"); code != 0 || !strings.Contains(out, "0 error(s)") {
		t.Fatalf("bank check on the sample: exit %d\n%s", code, out)
	}

	claimsPath := filepath.Join(root, "claims.json")
	if out, code := runSample(t, root, "claims", "export", "--out", claimsPath); code != 0 || !strings.Contains(out, "3 claims") {
		t.Fatalf("claims export on the sample: exit %d\n%s", code, out)
	}
	raw, err := os.ReadFile(claimsPath)
	if err != nil {
		t.Fatal(err)
	}
	var cf struct {
		Claims map[string]struct {
			Value string `json:"value"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("claims.json: %v", err)
	}
	for id, want := range map[string]string{
		"clmShelfSearch":  "470 → 94 ms (-80%)",
		"clmSkyCalls":     "12 → 3 calls (-75%)",
		"clmShelfOverdue": "18% → 7% of loans",
	} {
		if got := cf.Claims[id].Value; got != want {
			t.Errorf("claim %s = %q, want %q", id, got, want)
		}
	}

	if out, code := runSample(t, root, "lint", sel, "--claims", claimsPath); code != 0 || !strings.Contains(out, "0 error(s)") {
		t.Fatalf("lint on the sample: exit %d\n%s", code, out)
	}

	// The release gate needs blind pass 2 and a validator report the sample
	// does not ship; it must refuse, and name the unreviewed bullet.
	out, code = runSample(t, root, "lint", sel, "--claims", claimsPath, "--final")
	if code == 0 {
		t.Fatalf("lint --final must refuse the sample (no pass 2, no validator):\n%s", out)
	}
	for _, want := range []string{
		"shelf-loan-reminders: HR reader has not passed this exact wording in both rounds",
		"validator report missing or stale",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lint --final lacks %q:\n%s", want, out)
		}
	}
	for _, id := range []string{"shelf-title-index", "sky-batched-calls"} {
		if strings.Contains(out, id+": HR reader has not passed") || strings.Contains(out, id+": HM reader has not passed") {
			t.Errorf("%s was reviewed in both rounds; the reader gate must pass it:\n%s", id, out)
		}
	}
}

// A user's ~/.config/rkb/config.json layers over the default even under
// Vani's minimal environment, and a broken one stops rkb.
func TestSampleHonorsHomeConfig(t *testing.T) {
	root := sampleCopy(t)
	home := t.TempDir()
	run := func(args ...string) (string, int) {
		cmd := exec.Command(builtRKB(t), args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(), "RKB_CODE_ROOT=" + root}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatalf("rkb did not run: %v", err)
		}
		if ee != nil {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	cfgPath := filepath.Join(home, ".config", "rkb", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"bullet_rules": {"codenames": ["Shelfwise", "Skycast"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := run("doctor")
	if !strings.Contains(out, cfgPath+" (over the embedded default)") {
		t.Errorf("doctor must name the home config:\n%s", out)
	}
	if out, code := run("bank", "check"); code != 0 {
		t.Errorf("a config holding only lists must keep the default paths: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"bullet_rules": `), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := run("bank", "check"); code == 0 || !strings.Contains(out, cfgPath) {
		t.Errorf("a broken home config must stop rkb and name the file: exit %d\n%s", code, out)
	}
}
