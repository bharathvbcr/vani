package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const ltSummary = "early-career ML systems roles"

// ltSetup seeds six triaged postings gh:q:0..5, all read "adjacent" on
// role_fit by the primary, and a config naming the teacher's model and summary.
func ltSetup(t *testing.T) string {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	cfg := config.Default()
	cfg.TargetSummary = ltSummary
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", TriageModel: tcHaiku, InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 6; i++ {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:q:%d", i)), ATS: "gh", Company: "SynthCo", Title: "Engineer", Description: "Build things."}
		ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Triaged, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func ltStore(t *testing.T, state string, rows ...eval.TeacherRow) {
	t.Helper()
	s, err := eval.OpenTeacherStore(filepath.Join(state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}
}

func ltRow(key, prompt, value string) eval.TeacherRow {
	return eval.TeacherRow{
		Key: posting.Key(key), TextSHA256: eval.HashText("Build things."), Model: tcHaiku,
		FixtureSHA: eval.FixtureSHA(), PromptHash: prompt,
		Slots: map[string]eval.TeacherSlot{"role_fit": {Reading: "answered", Value: value}},
		At:    time.Unix(1, 0).UTC(),
	}
}

var ltHeader = regexp.MustCompile(`=== (\d+)/\d+  (gh:q:\d+) ===`)

// ltOrder runs `vani label` for a seed, quits at the first posting and returns
// the keys in the order shown (just the first, since the session ends there)
// together with stderr.
func ltFirst(t *testing.T, seed int, extra ...string) (first, stderr string) {
	t.Helper()
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("q\n")
	args := append([]string{"label", "-n", "6", "--seed", fmt.Sprint(seed)}, extra...)
	o, e := mustCode(t, 0, args...)
	m := ltHeader.FindStringSubmatch(o)
	if m == nil {
		t.Fatalf("no posting shown:\n%s\n%s", o, e)
	}
	return m[2], e
}

// ltSeedWhereFirstIsNot finds a seed whose teacherless order does not start
// with key, so a teacher moving key first is not a coincidence of the shuffle.
func ltSeedWhereFirstIsNot(t *testing.T, key string) int {
	t.Helper()
	for seed := 1; seed < 50; seed++ {
		if first, _ := ltFirst(t, seed); first != key {
			return seed
		}
	}
	t.Fatal("no seed puts another posting first")
	return 0
}

func TestLabelPutsTeacherDisagreementFirst(t *testing.T) {
	state := ltSetup(t)
	const target = "gh:q:4"
	seed := ltSeedWhereFirstIsNot(t, target)
	base, _ := ltFirst(t, seed)
	ph := eval.PromptHash(ltSummary)
	ltStore(t, state,
		ltRow(target, ph, "core"),       // disagrees with "adjacent"
		ltRow("gh:q:1", ph, "adjacent")) // agrees: no priority
	if first, e := ltFirst(t, seed); first != target || e != "" {
		t.Fatalf("first %s (teacherless %s), stderr %q; want %s", first, base, e, target)
	}
}

func TestLabelWithoutTeacherStoreKeepsTodaysOrder(t *testing.T) {
	state := ltSetup(t)
	seed := ltSeedWhereFirstIsNot(t, "gh:q:4")
	missing, e := ltFirst(t, seed)
	if e != "" {
		t.Fatalf("a missing store must be silent: %q", e)
	}
	// An empty store file and a store holding only agreeing readings order the same.
	if err := os.WriteFile(filepath.Join(state, eval.TeacherFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if first, e := ltFirst(t, seed); first != missing || e != "" {
		t.Fatalf("empty store: %s %q, missing store: %s", first, e, missing)
	}
	ltStore(t, state, ltRow("gh:q:4", eval.PromptHash(ltSummary), "adjacent"))
	if first, e := ltFirst(t, seed); first != missing || e != "" {
		t.Fatalf("agreeing reading: %s %q, missing store: %s", first, e, missing)
	}
}

func TestLabelIgnoresStalePromptReadings(t *testing.T) {
	state := ltSetup(t)
	const target = "gh:q:4"
	seed := ltSeedWhereFirstIsNot(t, target)
	want, _ := ltFirst(t, seed)
	// A disagreeing reading made under a different target summary is not current.
	ltStore(t, state, ltRow(target, eval.PromptHash("a different summary"), "core"))
	if first, e := ltFirst(t, seed); first != want || e != "" {
		t.Fatalf("stale reading moved %s first (want %s), stderr %q", first, want, e)
	}
}

func TestLabelWarnsOnUnreadableTeacherStoreAndContinues(t *testing.T) {
	state := ltSetup(t)
	seed := ltSeedWhereFirstIsNot(t, "gh:q:4")
	want, _ := ltFirst(t, seed)
	// A directory where the log should be: opening it fails with something other than not-exist.
	if err := os.Mkdir(filepath.Join(state, eval.TeacherFile), 0o700); err != nil {
		t.Fatal(err)
	}
	first, e := ltFirst(t, seed)
	if first != want {
		t.Fatalf("unreadable store changed the order: %s, want %s", first, want)
	}
	if !strings.Contains(e, "teacher readings unreadable") {
		t.Fatalf("no warning on stderr: %q", e)
	}
}

func TestLabelWindowAlsoUsesTeacher(t *testing.T) {
	state := ltSetup(t)
	// Give every posting a shadow reading so --window takes them.
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:q:%d", i)), ATS: "gh", Company: "SynthCo", Title: "Engineer", Description: "Build things."}
		ann := map[string]string{
			"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra",
			"triage.role_fit.shadow": "adjacent", "triage.role_fit.shadow_reading": "answered", "triage.role_fit.shadow_backend": "local",
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0.Add(time.Hour), Key: p.Key, Status: posting.Triaged, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	const target = "gh:q:4"
	seed := ltSeedWhereFirstIsNot(t, target)
	ltStore(t, state, ltRow(target, eval.PromptHash(ltSummary), "core"))
	if first, e := ltFirst(t, seed, "--window", "window-1"); first != target {
		t.Fatalf("--window: first %s, stderr %q; want %s", first, e, target)
	}
}
