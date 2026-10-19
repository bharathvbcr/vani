package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedEval writes n triaged postings with a "local" shadow reading on
// work_mode, half of them under the legacy primary name, and labels each.
func seedEval(t *testing.T, stateDir string, n int) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		key := posting.Key(fmt.Sprintf("gh:acme:%03d", i))
		p := posting.Posting{Key: key, ATS: "gh", Company: "Acme", Title: "Engineer", Description: "Build things."}
		backend := "d1-ultra"
		if i%2 == 0 {
			backend = eval.LegacyOpenRouterName
		}
		got, shadow := "remote", "remote"
		if i%5 == 0 {
			got = "onsite"
		}
		if i%3 == 0 {
			shadow = "hybrid"
		}
		ann := map[string]string{
			"triage.work_mode": got, "triage.work_mode.reading": "answered",
			"triage.work_mode.backend": backend, "triage.work_mode.p": "0.9000",
			"triage.work_mode.shadow": shadow, "triage.work_mode.shadow_reading": "answered",
			"triage.work_mode.shadow_backend": "local",
		}
		e := ledger.Event{V: 1, At: jobsT0, Key: key, Status: posting.Triaged, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(eval.Entry{Key: key, Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// Without the flag, vani eval prints what it printed before. With it, the
// output begins with exactly that report and adds the held-out section.
func TestEvalHeldOutFlag(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 45)

	plain, _ := mustCode(t, 0, "eval")
	if strings.Contains(plain, "Held-out") {
		t.Fatalf("plain eval mentions the held-out split:\n%s", plain)
	}
	if !strings.Contains(plain, "## Promotion") {
		t.Fatalf("plain eval has no promotion table:\n%s", plain)
	}
	held, _ := mustCode(t, 0, "eval", "--held-out", "d1-vani-jdtriage-2026-10-18")
	if !strings.HasPrefix(held, plain) {
		t.Fatalf("held-out output does not begin with the all-labels report:\n--- plain\n%s\n--- held\n%s", plain, held)
	}
	section := strings.TrimPrefix(held, plain)
	for _, frag := range []string{
		"## Held-out promotion: d1-vani-jdtriage-2026-10-18",
		"labelled postings ranked: 45, held out: 30, fit: 15",
		"| work_mode | local |",
		"30. \"gh:acme:",
	} {
		if !strings.Contains(section, frag) {
			t.Errorf("held-out section lacks %q:\n%s", frag, section)
		}
	}
	if strings.Contains(section, "31. ") {
		t.Errorf("more than 30 keys held out:\n%s", section)
	}
}

func TestEvalHeldOutRefusals(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 3)

	_, e := mustCode(t, 2, "eval", "--held-out", "any-salt-i-like")
	if !strings.Contains(e, "d1-vani-jdtriage-2026-10-18") {
		t.Fatalf("unknown split error does not list the registered ones: %q", e)
	}

	// A torn label line: the plain report counts it, the held-out run refuses
	// and prints no figures.
	f, err := os.OpenFile(filepath.Join(stateDir, "eval", "labels", eval.LabelsFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"key":"gh:acme:999","lab`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if o, _ := mustCode(t, 0, "eval"); !strings.Contains(o, "label lines skipped as unreadable: 1") {
		t.Fatalf("plain eval:\n%s", o)
	}
	o, e := mustCode(t, 1, "eval", "--held-out", "d1-vani-jdtriage-2026-10-18")
	if o != "" || !strings.Contains(e, "unreadable") {
		t.Fatalf("torn line: stdout %q stderr %q", o, e)
	}
}
