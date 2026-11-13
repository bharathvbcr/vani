package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/track"
	"github.com/bharathvbcr/decide"
)

// The review's findings on the auditor (spend accounting, the identifying-word
// check, the baseline's disclosure, the gate's reachability), each pinned
// against what the code did before the fix.

// auLedgerPath is the day's real llm budget ledger, the one teacherSpent reads
// when no test replaces it.
func auLedgerPath(e *tcEnv) string {
	return filepath.Join(e.state, "budget", "llm-"+budgetClock().UTC().Format("2006-01-02")+".json")
}

func auWriteLedger(t *testing.T, path string, s budget.Snapshot) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func auReadLedger(t *testing.T, path string) budget.Snapshot {
	t.Helper()
	s, err := budget.Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// auOtherProcessLedger writes a ledger holding one in-flight reservation of
// reserved nano-USD made by another caller (the daemon, say), and returns a
// decider that settles that reservation at 0 on the first question (the other
// call answered for less than it reserved) and settles each question of this
// run at tcPerCall, as Manvi's budget provider would.
func auOtherProcessLedger(t *testing.T, e *tcEnv, reserved int64) *auFake {
	t.Helper()
	path := auLedgerPath(e)
	auWriteLedger(t, path, budget.Snapshot{SchemaVersion: 2, NextID: 2, InFlight: []budget.Attempt{
		{ID: 1, Model: "other-model", State: "accepted", ReservedNanoUSD: reserved, PID: os.Getpid(), StartedUnix: time.Now().Unix()},
	}})
	if s := auReadLedger(t, path); s.CommittedNanoUSD() != reserved {
		t.Fatalf("the seeded ledger reads back %d committed, want %d", s.CommittedNanoUSD(), reserved)
	}
	var mu sync.Mutex
	f := &auFake{}
	f.answer = func(_ context.Context, q decide.Question) (decide.Answer, error) {
		mu.Lock()
		s := auReadLedger(t, path)
		s.InFlight = nil // the other caller's attempt settled at nothing
		s.SpentNanoUSD += tcPerCall
		s.Settled++
		auWriteLedger(t, path, s)
		mu.Unlock()
		a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
		for _, sl := range q.Slots {
			a.Slots[sl.Name] = decide.SlotReading{Reading: decide.Answered, Value: sl.Options[0]}
		}
		return a, nil
	}
	old := teacherDecider
	t.Cleanup(func() { teacherDecider = old })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	return f
}

func auCapSpent(t *testing.T, e *tcEnv) float64 {
	t.Helper()
	spent, err := readOpenRouterSpend(filepath.Join(e.state, "budget", auditorBudgetFile), time.Now().UTC().Format("2006-01-02"), auditorSpendKey)
	if err != nil {
		t.Fatal(err)
	}
	return spent
}

// Finding 1: the cap was charged with how much the shared ledger's committed
// total grew. Another caller's reservation that settles for less during the
// run made that growth low or negative, so the auditor's cap was charged less
// than its own questions cost (here, nothing).
func TestAuditorCapIsNeverChargedLessThanItsQuestionsCost(t *testing.T) {
	e := auSetup(t)
	auOtherProcessLedger(t, e, 50*tcPerCall)
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1", "--concurrency", "1")
	if !strings.Contains(o, "asked 4, stored 4") {
		t.Fatalf("output:\n%s", o)
	}
	if got, want := auCapSpent(t, e), 4*float64(tcPerCall)/1e9; got < want-1e-12 {
		t.Errorf("the auditor's cap was charged $%.6f for questions that cost $%.6f", got, want)
	}
}

// Finding 1, the admission side: the same arithmetic let a run pass its
// --budget-usd once another caller's reservation settled for less.
func TestAuditorBudgetHoldsWhenAnotherCallersReservationSettlesLower(t *testing.T) {
	e := auSetup(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d, summary := tkDigest(t, e)
	task := auditorTask(summary, d)
	prices, _, err := teacherPrices(cfg, tcHaiku)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("You will build and operate services that serve language models. ", 40)
	var worst int64
	for k, desc := range map[string]string{"gh:a:1": long + " first", "gh:a:2": tcDroppedT + long, "gh:a:3": long, "gh:a:4": long} {
		_, _ = task.Ask(context.Background(), deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
			worst = max(worst, teacherWorstNano(prices, q))
			return decide.Answer{}, nil
		}), posting.Posting{Key: posting.Key(k), ATS: "gh", Company: "SynthCo", Title: "Engineer " + k, Description: desc})
	}
	// The budget admits one question at its worst case and no second.
	f := auOtherProcessLedger(t, e, 20*worst)
	budgetUSD := float64(worst+tcPerCall/2) / 1e9
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--budget-usd", fmt.Sprintf("%.9f", budgetUSD), "--concurrency", "1")
	if len(f.sent) != 1 || !strings.Contains(o, "asked 1, stored 1") {
		t.Errorf("a $%.6f budget that admits one question asked %d:\n%s", budgetUSD, len(f.sent), o)
	}
}

// Finding 2: the cap was charged once, after the run, so a run that died
// mid-way (or whose last ledger read failed) charged nothing. It must be
// charged as questions settle: while the third question is being asked, the
// first two are already on the cap.
func TestAuditorChargesItsCapAsQuestionsSettle(t *testing.T) {
	e := auSetup(t)
	f := &auFake{}
	auUse(t, f)
	var duringThird float64 = -1
	var calls int
	f.answer = func(_ context.Context, q decide.Question) (decide.Answer, error) {
		f.mu.Lock()
		calls++
		n := calls
		f.mu.Unlock()
		if n == 3 {
			duringThird = auCapSpent(t, e)
		}
		a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
		for _, sl := range q.Slots {
			a.Slots[sl.Name] = decide.SlotReading{Reading: decide.Answered, Value: sl.Options[0]}
		}
		return a, nil
	}
	mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1", "--concurrency", "1")
	if want := 2 * float64(tcPerCall) / 1e9; duringThird < want-1e-12 {
		t.Errorf("while the third question was asked the cap held $%.6f; the two settled questions cost $%.6f", duringThird, want)
	}
}

// Finding 3: with the owner's identifying-words list missing, only the
// built-in list was checked, yet a real run went ahead with a warning. It must
// refuse unless the owner says so explicitly, and the reply must record which
// list was checked either way.
func TestAuditorRefusesARealRunWithoutTheOwnersWordsList(t *testing.T) {
	e := auSetup(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(track.Dir(cfg, e.state), track.WordsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	f := &auFake{}
	auUse(t, f)
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || len(f.sent) != 0 || !strings.Contains(stderr, track.WordsFile) {
		t.Errorf("without the owner's list: exit %d, sent %d, stderr %s", code, len(f.sent), stderr)
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.AuditorFile)); err == nil {
		t.Error("a refused run wrote the auditor's log")
	}
	// The dry run still prices it, and says which list was checked.
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--dry-run", "--json")
	var r map[string]any
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	plan, _ := r["plan"].(map[string]any)
	if plan["identifying_words"] != "built-in list only" {
		t.Errorf("the plan does not record that only the built-in list was checked: %v", plan["identifying_words"])
	}
	// The explicit override runs, and says so.
	o, _ = mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1", "--no-identifying-words-file", "--json")
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	plan, _ = r["plan"].(map[string]any)
	if len(f.sent) != 4 || plan["identifying_words"] != "built-in list only" {
		t.Errorf("with the override: sent %d, words %v", len(f.sent), plan["identifying_words"])
	}
}

// Finding 4: the refusal and the dry run printed the identifying terms they
// found, so the owner's private list reached any agent reading the output.
// They say how many terms and where; only --show-terms, in a terminal, names
// them, on stderr.
func TestAuditorNeverPrintsTheIdentifyingTermsItFound(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := tkDigest(t, e)
	word := ""
	for _, f := range strings.Fields(strings.ToLower(d.Text)) {
		f = strings.Trim(f, ".,:;()*-")
		if len(f) >= 5 && strings.Trim(f, "abcdefghijklmnopqrstuvwxyz") == "" {
			word = f
			break
		}
	}
	if word == "" {
		t.Fatalf("no plain word in the test digest:\n%s", d.Text)
	}
	if err := os.MkdirAll(track.Dir(cfg, e.state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(track.Dir(cfg, e.state), track.WordsFile), []byte("# test\n"+word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"auditor", "calibrate", "--dry-run"},
		{"auditor", "calibrate", "--dry-run", "--json"},
		{"auditor", "calibrate", "--budget-usd", "1"},
		{"auditor", "calibrate", "--budget-usd", "1", "--json"},
	} {
		_, stdout, stderr := do(t, args...)
		if strings.Contains(stdout+stderr, word) {
			t.Errorf("%v printed the identifying term", args)
		}
		if !strings.Contains(stdout+stderr, "the knowledge-base digest holds 1 identifying term") {
			t.Errorf("%v does not say how many terms and where:\nstdout %s\nstderr %s", args, stdout, stderr)
		}
	}
	// --show-terms names them on stderr for the owner's terminal, never in JSON.
	_, stdout, stderr := do(t, "auditor", "calibrate", "--dry-run", "--show-terms")
	if !strings.Contains(stderr, word) || strings.Contains(stdout, word) {
		t.Errorf("--show-terms: the term on stderr %v, on stdout %v", strings.Contains(stderr, word), strings.Contains(stdout, word))
	}
	_, stdout, _ = do(t, "auditor", "calibrate", "--dry-run", "--show-terms", "--json")
	if strings.Contains(stdout, word) {
		t.Error("--show-terms put the term in the JSON reply")
	}
}

// Finding 5: the report counted current-prompt teacher readings over every
// judged posting, not over the readings the comparison scored, and gave no
// denominator. It reports both over the scored set, and --teacher-current-only
// restricts the baseline to readings asked under the current prompt.
func TestAuditorReportDisclosesTheBaselineItScored(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	_, summary := tkDigest(t, e)
	ts, err := eval.OpenTeacherStore(filepath.Join(e.state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	for k, prompt := range map[string]string{"gh:a:1": eval.PromptHash(summary), "gh:a:2": "older", "gh:a:3": "older"} {
		if err := ts.Append(eval.TeacherRow{Key: posting.Key(k), TextSHA256: "t", Model: tcHaiku, FixtureSHA: eval.FixtureSHA(), PromptHash: prompt,
			Slots: map[string]eval.TeacherSlot{"role_fit": {Reading: "answered", Value: "partial_fit"}}, At: jobsT0}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(args ...string) map[string]any {
		o, _ := mustCode(t, 0, append([]string{"auditor", "report", "--json"}, args...)...)
		var r map[string]any
		if err := json.Unmarshal([]byte(o), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := read()
	b, _ := r["teacher_baseline"].(map[string]any)
	// gh:a:1 is labelled and marked saved (two scored items), gh:a:2 and gh:a:3
	// labelled: four scored items hold a teacher reading, two of them current.
	if b["scored"] != float64(4) || b["scored_current_prompt"] != float64(2) || b["current_only"] != false {
		t.Errorf("baseline disclosure %v", b)
	}
	r = read("--teacher-current-only")
	b, _ = r["teacher_baseline"].(map[string]any)
	if b["scored"] != float64(2) || b["scored_current_prompt"] != float64(2) || b["current_only"] != true {
		t.Errorf("current-only baseline %v", b)
	}
	text, _ := mustCode(t, 0, "auditor", "report")
	if !strings.Contains(text, "2 of the 4 scored teacher readings were asked under the current prompt") {
		t.Errorf("the report text does not give the baseline's share with its denominator:\n%s", text)
	}
}

// Finding 6: say when the gate can decide at all, so a calibration run on a
// handful of labels is not read as a test of the auditor.
func TestAuditorReportSaysWhenTheGateCanDecide(t *testing.T) {
	auSetup(t)
	tcNoModel(t)
	text, _ := mustCode(t, 0, "auditor", "report")
	for _, want := range []string{
		fmt.Sprintf("The gate can decide only when the kept and the dropped postings each hold at least %d wanted and %d not wanted with all three scores", eval.MinGateClass, eval.MinGateClass),
		"now: kept 0 wanted / 0 not wanted, dropped 0 wanted / 0 not wanted",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}
