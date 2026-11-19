package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/doctor"
)

// A real run adds its drop verdicts to the silver log, once per (run,
// posting): a retried run at the same stamp adds nothing; a dry run never
// opens the log; the log holds no free text.
func TestDoctorRunAppendsVerdictsToTheSilverLog(t *testing.T) {
	state, _ := doctorSetup(t)
	doctorSeams(t, &doctorFake{boards: "echo", drops: "echo"}, 0)
	path := filepath.Join(state, doctor.SilverFile)

	mustCode(t, 0, "doctor", "--dry-run")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a dry run touched the silver log: %v", err)
	}
	out, _ := mustCode(t, 0, "doctor", "--budget-usd", "5")
	if !strings.Contains(out, "2 drop verdicts added to "+doctor.SilverFile) {
		t.Fatalf("stdout lacks the silver count:\n%s", out)
	}
	s, err := doctor.OpenSilverStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := s.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Verdict != doctor.DropCorrect || r.Model != "claude-test" || r.RunID == "" || len(r.PromptSHA256) != 64 || !strings.HasPrefix(string(r.Key), "gh:acme:") {
			t.Errorf("row %+v", r)
		}
	}
	// fixedNow: the same stamp, so the same run id; nothing is doubled.
	out, _ = mustCode(t, 0, "doctor", "--budget-usd", "5")
	if !strings.Contains(out, "0 drop verdicts added") {
		t.Errorf("a retried run added verdicts:\n%s", out)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"why"`) || strings.Contains(string(raw), "fine") || strings.Contains(string(raw), "Welder") {
		t.Fatalf("the silver log holds free text:\n%s", raw)
	}
	// A log that cannot be written fails the command after the report is written.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil { // a directory where the log goes
		t.Fatal(err)
	}
	o, e := mustCode(t, 1, "doctor", "--budget-usd", "5")
	if !strings.Contains(o, "report written to") || !strings.Contains(e, "not added to "+doctor.SilverFile) {
		t.Fatalf("stdout %q\nstderr %q", o, e)
	}
}

// A drops part that judged only some of what it was sent is not a successful
// part: the command fails, the report keeps the verdicts, and the silver log
// gets none.
func TestDoctorPartialDropsPartAddsNoSilver(t *testing.T) {
	state, _ := doctorSetup(t)
	doctorSeams(t, &doctorFake{boards: "echo", drops: `{"drops":[{"key":"gh:acme:1","verdict":"wrong_drop","cause":"other","why":"w"}],"patterns":[]}`}, 0)
	_, e := mustCode(t, 1, "doctor", "--budget-usd", "5")
	if !strings.Contains(e, "drops: judged 1 of the 2") {
		t.Fatalf("stderr %q", e)
	}
	if _, err := os.Stat(filepath.Join(state, doctor.SilverFile)); !os.IsNotExist(err) {
		t.Fatalf("a partial drops part wrote the silver log: %v", err)
	}
}

// Verdicts from strata the auditor leaves out (work mode here) are withheld.
func TestDoctorSilverWithholdsLeftOutStrata(t *testing.T) {
	state := t.TempDir()
	e := &env{stateDir: state}
	rep := doctor.Report{Model: "m", DropFinds: []doctor.DropFinding{
		{Key: "a", Verdict: doctor.DropWrong, Cause: "other"},
		{Key: "b", Verdict: doctor.DropWrong, Cause: "other"},
		{Key: "c", Verdict: doctor.DropWrong, Cause: "other"},
	}}
	cases := []doctor.DropCase{
		{Key: "a", Stratum: "dropped:model:role_fit", PSelect: 0.5},
		{Key: "b", Stratum: "dropped:model:work_mode", PSelect: 0.5},
		{Key: "c", Stratum: "dropped:exclude:title", PSelect: 0.5},
	}
	added, withheld, err := e.appendDoctorSilver(rep, cases, "r1", doctor.NewSilverRun("s", "t", nil))
	if err != nil || added != 1 || withheld != 2 {
		t.Fatalf("added %d withheld %d err %v, want 1, 2, nil", added, withheld, err)
	}
}
