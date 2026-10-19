package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func seedPrefilter(t *testing.T, stateDir string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	read := func(fit, backend string) map[string]string {
		pre := ledger.AnnTriagePrefix + "role_fit"
		return map[string]string{pre: fit, pre + ledger.AnnTriageSuffixReading: "answered", pre + ledger.AnnTriageSuffixBackend: backend}
	}
	for i, c := range []struct {
		title string
		st    posting.Status
		ann   map[string]string
	}{
		{"Staff Software Engineer", posting.Live, nil},
		{"Software Engineer, Inference", posting.Live, nil},
		{"Engineering Manager", posting.Live, nil},
		{"Member of Technical Staff - Inference Runtime", posting.Live, nil},
		{"Staff\x1b[31m Engineer", posting.Live, nil},
		{"", posting.Live, nil},
		{"Technical Recruiter", posting.Explained, read("mismatch_domain", "d1-ultra")},
		{"Principal Engineer, Inference", posting.Explained, read("strong_fit", "d1-ultra")},
		{"Director of Sales", posting.Dropped, read("mismatch_domain", "title")}, // the title's own reading is no evidence
		{"Account Executive", posting.Explained, map[string]string{"triage.role_fit.reading": "abstained"}},
	} {
		p := posting.Posting{Key: posting.Key("gh:acme:" + string(rune('a'+i))), ATS: "gh", Company: "Acme", Title: c.title, Description: "jd"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: c.st, Posting: &p, Annotations: c.ann}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrefilterDryRun(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	out, _ := mustCode(t, 0, "prefilter")
	if !strings.Contains(out, "title prefilter is off") {
		t.Fatalf("no read entries: %q", out)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DropRules = []config.DropRule{{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	cfg.TitleSignals.Read = []config.TitleRead{
		{Slot: "seniority", Value: "staff_plus", Match: []string{"Staff", "Principal"}, Unless: []string{"Member of Technical Staff"}},
		{Slot: "seniority", Value: "manager", Match: []string{"Manager", "Director"}},
		{Slot: "role_fit", Value: "mismatch_domain", Match: []string{"Recruiter", "Sales", "Account Executive"}, Unless: []string{"Engineer"}},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedPrefilter(t, stateDir)
	events := filepath.Join(stateDir, "ledger", "events.jsonl")
	before, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}

	out, _ = mustCode(t, 0, "prefilter", "--list")
	for _, want := range []string{
		"3 of 6 live posting(s) would be dropped before triage (10 in the ledger)",
		`2  slot "seniority" = "staff_plus": title matched "Staff"`,
		`1  slot "seniority" = "manager": title matched "Manager"`,
		"read role_fit on 2 posting(s) it would drop (any status): mismatch_domain 1, strong_fit 1",
		"read strong_fit or partial_fit (false drops): 1",
		"gh:acme:h  Principal Engineer, Inference [strong_fit, explained]",
		"gh:acme:a\tStaff Software Engineer\t",
		"gh:acme:e\tStaff [31m Engineer\t", // the escape is shown as text, not sent to the terminal
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "Technical Staff") || strings.Contains(out, "gh:acme:b\t") {
		t.Errorf("unexpected output:\n%s", out)
	}
	after, err := os.ReadFile(events)
	if err != nil || string(after) != string(before) {
		t.Fatalf("prefilter wrote the ledger (%v)", err)
	}

	// Without --list only the summary is printed.
	out, _ = mustCode(t, 0, "prefilter")
	if strings.Contains(out, "gh:acme:a\t") {
		t.Errorf("listed without --list:\n%s", out)
	}
	if code, _, _ := do(t, "prefilter", "extra"); code != 2 {
		t.Errorf("a stray argument exited %d, want 2", code)
	}
}

func TestPrefilterSaysWhenNothingWasReadByAModel(t *testing.T) {
	rep := prefilterReport{ByReason: map[string]int{}, RoleFit: map[string]int{}}
	var b strings.Builder
	writePrefilter(&b, rep, 0, false)
	if !strings.Contains(b.String(), "precision is unmeasured, not perfect") {
		t.Fatalf("%q", b.String())
	}
}

func TestDropPolicyWiresTitleReadsOnlyWhenConfigured(t *testing.T) {
	if p := dropPolicy(config.Config{}, "d1-ultra"); p.ReadTitle != nil || p.OpenRouter != "d1-ultra" {
		t.Fatalf("%+v", p)
	}
	cfg := config.Config{
		DropRules:    []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}},
		TitleSignals: config.TitleSignals{Read: []config.TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}}},
	}
	p := dropPolicy(cfg, "d1-ultra")
	if _, reason, dropped := p.EvaluateTitle("Engineering Manager"); !dropped || !strings.Contains(reason, `title matched "Manager"`) {
		t.Fatalf("dropped %v %q", dropped, reason)
	}
	if len(p.Rules) != 1 || p.Rules[0].MinOpenRouterProb != 0.9 {
		t.Fatalf("rules %+v", p.Rules)
	}
}
