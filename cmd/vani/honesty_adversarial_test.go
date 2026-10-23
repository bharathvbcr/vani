package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func trackBank(t *testing.T, cfgPath string) {
	t.Helper()
	bank := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(bank, []byte(`{"entries": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	cfg.KB.Bank = bank
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Only the words check cannot run (bank set, no words file): show says
// incomplete naming just that check, and use refuses without changing the config.
func TestAdversarialTrackOnlyWordsMissing(t *testing.T) {
	cfgPath, _ := trackSetup(t)
	trackBank(t, cfgPath)
	before := readFile(t, cfgPath)
	o, e := mustCode(t, 1, "track", "show", "ml-systems")
	if !strings.Contains(o, "check: incomplete (not run: personal-name words)") || strings.Contains(o, "evidence") || strings.Contains(e, "evidence check skipped") {
		t.Fatalf("show %q / %q", o, e)
	}
	for _, args := range [][]string{{"track", "use", "ml-systems"}, {"track", "use", "--dry-run", "ml-systems"}} {
		o, e = mustCode(t, 1, args...)
		if !strings.Contains(e, "personal-name words") || strings.Contains(o, "saved") {
			t.Errorf("%v: %q / %q", args, o, e)
		}
		if readFile(t, cfgPath) != before {
			t.Fatalf("%v changed the config", args)
		}
	}
}

// Only the bank is missing (words present).
func TestAdversarialTrackOnlyEvidenceMissing(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte("zorblax corp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, cfgPath)
	o, _ := mustCode(t, 1, "track", "show", "ml-systems")
	if !strings.Contains(o, "check: incomplete (not run: evidence)") {
		t.Fatalf("show %q", o)
	}
	mustCode(t, 1, "track", "use", "ml-systems")
	if readFile(t, cfgPath) != before {
		t.Fatal("refused use changed the config")
	}
}

// A words file that is a directory or unreadable is a hard error (exit 1), the
// config is untouched, and neither show nor use prints "ok".
func TestAdversarialTrackWordsDirectoryAndUnreadable(t *testing.T) {
	for _, mode := range []string{"dir", "chmod0"} {
		t.Run(mode, func(t *testing.T) {
			cfgPath, dir := trackSetup(t)
			trackBank(t, cfgPath)
			p := filepath.Join(dir, trackWordsFile())
			if mode == "dir" {
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(p, []byte("x\n"), 0o000); err != nil {
					t.Fatal(err)
				}
				if f, err := os.Open(p); err == nil {
					f.Close()
					t.Skip("running with privileges that ignore file modes")
				}
			}
			before := readFile(t, cfgPath)
			for _, args := range [][]string{{"track", "show", "ml-systems"}, {"track", "use", "ml-systems"}, {"track", "use", "--allow-unchecked", "ml-systems"}, {"track", "use", "--allow-unchecked", "--dry-run", "ml-systems"}} {
				code, o, e := do(t, args...)
				if code != 1 || strings.Contains(o, "check: ok") || strings.Contains(o, "saved") {
					t.Errorf("%v: exit %d stdout %q stderr %q", args, code, o, e)
				}
				if readFile(t, cfgPath) != before {
					t.Fatalf("%v changed the config", args)
				}
			}
		})
	}
}

// With every check able to run, --allow-unchecked is a no-op: no stderr noise
// about unchecked work, exit 0, config saved.
func TestAdversarialTrackAllowUncheckedWhenFullyChecked(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	trackBank(t, cfgPath)
	if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte("zorblax corp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, cfgPath)
	_, e := mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	for _, noise := range []string{"NOT checked", "skipped", "unchecked", "did not run"} {
		if strings.Contains(e, noise) {
			t.Errorf("stderr noise %q: %q", noise, e)
		}
	}
	if readFile(t, cfgPath) == before {
		t.Fatal("not saved")
	}
}

// Counts are consistent with eligibility: rescored never exceeds eligible, and
// a repeat run rescoring nothing reports 0 rescored / 0 eligible.
func TestAdversarialRescoreCommandCountsSumToEligible(t *testing.T) {
	l := rescoreLedger(t, []rescoreSeed{
		{"1", posting.Explained, "jd", ""},
		{"2", posting.Explained, "nomh", ""},
		{"3", posting.Explained, "jd", "70"},
	})
	rep, err := rescoreWith(context.Background(), l, 10, 2, rescoreRunner(l, noMustHaveScorer{}).Rescore, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Plan.Eligible != 2 || rep.Rescored != 2 || rep.Plan.Eligible-rep.Rescored != 0 {
		t.Errorf("eligible %d rescored %d", rep.Plan.Eligible, rep.Rescored)
	}
	rep, err = rescoreWith(context.Background(), l, 10, 2, rescoreRunner(l, noMustHaveScorer{}).Rescore, t.Logf)
	if err != nil || rep.Plan.Eligible != 0 || rep.Rescored != 0 || rep.Plan.Unknown != 1 {
		t.Errorf("second run: %+v %v", rep, err)
	}
}

// With an unreadable marks store the warm still warms kept postings, and the
// stderr line carries no posting text, company, title or personal data.
func TestAdversarialSemanticWarmUnreadableMarksStderrIsClean(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Quuxcorp", title: "Zyzzyva Engineer",
		desc: "Run k8s services ZZ-SECRET-JD", status: posting.Explained, posted: daysAgo(2), seenAgo: time.Hour})
	dir := marks.Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marks.jsonl"), []byte("{\"key\":\"gh:a:1\",\"mark\":\"ZZ-SECRET-MARK\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustCode(t, 0, "semantic", "warm")
	if !strings.Contains(out, "scored 1") {
		t.Fatalf("kept posting not warmed: %q", out)
	}
	for _, s := range []string{"Quuxcorp", "Zyzzyva", "ZZ-SECRET", "k8s"} {
		if strings.Contains(errOut, s) || strings.Contains(out, s) {
			t.Errorf("output leaks %q: %q / %q", s, out, errOut)
		}
	}
	if !strings.Contains(errOut, "marks") {
		t.Errorf("no marks line: %q", errOut)
	}
}
