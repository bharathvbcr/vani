package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/track"
	"github.com/bharathvbcr/decide"
)

// auFake answers the would-apply question as answer says (default: every slot
// its first option), records what it was sent, and charges tcPerCall per
// question to the meter teacherSpent and teacherResolved read.
type auFake struct {
	mu     sync.Mutex
	sent   []string
	keys   []string
	meter  atomic.Int64
	answer func(ctx context.Context, q decide.Question) (decide.Answer, error)
}

func (f *auFake) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	sys, user := llmbackend.Messages(q)
	f.mu.Lock()
	f.sent = append(f.sent, sys+"\n"+user)
	f.keys = append(f.keys, q.RecordID)
	f.mu.Unlock()
	f.meter.Add(tcPerCall)
	if f.answer != nil {
		return f.answer(ctx, q)
	}
	a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a, nil
}

func auUse(t *testing.T, f *auFake) {
	t.Helper()
	oldD, oldS, oldR, oldT := teacherDecider, teacherSpent, teacherResolved, auditorTimeout
	t.Cleanup(func() { teacherDecider, teacherSpent, teacherResolved, auditorTimeout = oldD, oldS, oldR, oldT })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	teacherSpent = func(context.Context, string) (int64, error) { return f.meter.Load(), nil }
	teacherResolved = func(context.Context, string) (int64, error) { return f.meter.Load(), nil }
}

// auSetup is tkSetup (ledger gh:a:1..5, a knowledge base) with the owner's
// judgments: five would_apply answers (one for a posting the ledger does not
// hold, one for a posting with no text) and two marks, one on a posting also
// labelled. It writes an owner's identifying-words list whose one term occurs
// nowhere, since a real run refuses when that list is missing or empty.
func auSetup(t *testing.T) *tcEnv {
	t.Helper()
	e := tkSetup(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(track.Dir(cfg, e.state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(track.Dir(cfg, e.state), track.WordsFile), []byte("# test\nzzqxvterm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := eval.NewStore(filepath.Join(e.state, "eval", "labels"), filepath.Join(e.state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []struct{ key, ans, src string }{
		{"gh:a:1", "yes", "decile:6"},
		{"gh:a:2", "maybe", "dropped:model:seniority"},
		{"gh:a:3", "no", "decile:1"},
		{"gh:x:9", "no", "decile:2"},
		{"gh:a:5", "no", "decile:3"},
	} {
		if err := s.Put(eval.Entry{V: 1, Key: posting.Key(l.key), Labels: map[string]string{eval.WouldApply: l.ans}, Source: l.src, PSelect: 0.01}); err != nil {
			t.Fatal(err)
		}
	}
	for k, m := range map[string]marks.Mark{"gh:a:1": marks.Saved, "gh:a:4": marks.Hidden} {
		if _, err := marks.SetMark(marks.Dir(e.state), posting.Key(k), m, jobsT0); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return fmt.Sprintf("%x", s)
}

func TestAuditorDryRunCountsPricesAndAsksNothing(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--dry-run")
	t.Log("\n" + o)
	table, ok := anthropic.PriceOf(tcHaiku)
	if !ok {
		t.Fatalf("no rate table entry for %s", tcHaiku)
	}
	for _, want := range []string{
		"6 postings judged or marked (5 with a would_apply answer, 2 marked)",
		"1 not in the ledger, 1 with no text",
		"0 already answered", "4 to ask",
		"Anthropic's published rate table",
		fmt.Sprintf("input $%.4f and output $%.4f per million tokens", float64(table.Input)/1000, float64(table.Output)/1000),
		"$15.00 a day for the auditor",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run output lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.AuditorFile)); err == nil {
		t.Error("a dry run wrote the auditor's log")
	}
	if _, err := os.Stat(filepath.Join(e.state, "budget", auditorBudgetFile)); err == nil {
		t.Error("a dry run opened the auditor's cap")
	}
	// --json carries the same plan.
	o, _ = mustCode(t, 0, "auditor", "calibrate", "--dry-run", "--json")
	var r auditorCalibrateReply
	if err := json.Unmarshal([]byte(o), &r); err != nil || r.Plan == nil || r.Plan.ToAsk != 4 || r.Plan.MaxUSD <= 0 || r.Plan.Model != tcHaiku {
		t.Errorf("json plan %+v err %v:\n%s", r.Plan, err, o)
	}
}

func TestAuditorRefusesARealRunWithoutABoundedBudget(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	for _, args := range [][]string{
		{"auditor", "calibrate"},
		{"auditor", "calibrate", "--budget-usd", "0"},
		{"auditor", "calibrate", "--budget-usd", "-1"},
		{"auditor", "calibrate", "--budget-usd", "NaN"},
		{"auditor", "calibrate", "--budget-usd", "15.01"},
		{"auditor", "calibrate", "--budget-usd", "1", "--concurrency", "0"},
		{"auditor", "calibrate", "--budget-usd", "1", "--concurrency", "17"},
		{"auditor"},
		{"auditor", "nonsense"},
	} {
		code, _, stderr := do(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want a usage error (2); stderr %s", args, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.AuditorFile)); err == nil {
		t.Error("a refused run wrote the auditor's log")
	}
}

func TestAuditorRunKeepsAnswersApartPrivateAndResumes(t *testing.T) {
	e := auSetup(t)
	labels := filepath.Join(e.state, "eval", "labels", eval.LabelsFile)
	teacherLog := filepath.Join(e.state, eval.TeacherFile)
	before, beforeTeacher := fileSum(t, labels), fileSum(t, teacherLog)
	f := &auFake{}
	auUse(t, f)
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1", "--concurrency", "2")
	if !strings.Contains(o, "asked 4, stored 4") {
		t.Fatalf("output:\n%s", o)
	}
	joined := strings.Join(f.sent, "\n")
	for _, s := range f.sent {
		if strings.Contains(s, tcSecret) {
			t.Errorf("a prompt carries private data: %.200s", s)
		}
		if !strings.Contains(s, "would they apply?") || !strings.Contains(s, "early-career ML systems roles") || !strings.Contains(s, "JOB SEEKER'S EXPERIENCE") {
			t.Errorf("a prompt lacks the question, summary or digest: %.300s", s)
		}
	}
	if !strings.Contains(joined, tcDroppedT) {
		t.Error("the dropped posting was not asked about")
	}
	if strings.Contains(strings.Join(f.keys, ","), "gh:x:9") || strings.Contains(strings.Join(f.keys, ","), "gh:a:5") {
		t.Errorf("asked about a posting with no record or no text: %v", f.keys)
	}
	// The answers are in the auditor's log, nowhere else.
	if got := fileSum(t, labels); got != before {
		t.Error("the run changed the owner's labels")
	}
	if got := fileSum(t, teacherLog); got != beforeTeacher {
		t.Error("the run wrote the teacher's log")
	}
	raw, err := os.ReadFile(filepath.Join(e.state, eval.AuditorFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{tcSecret, tcDroppedT, "SynthCo"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the auditor's log holds %q", bad)
		}
	}
	for _, want := range []string{`"task":"` + wouldapply.Task + `"`, `"model":"` + tcHaiku + `"`, `"fixture_sha":"` + wouldapply.Fixture() + `"`, `"prompt_hash":"`, `"text_sha256":"`, `"kb_digest_sha":"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the auditor's log lacks %s", want)
		}
	}
	// The auditor's own cap was charged with what was metered (4 calls).
	spent, err := readOpenRouterSpend(filepath.Join(e.state, "budget", auditorBudgetFile), time.Now().UTC().Format("2006-01-02"), auditorSpendKey)
	if err != nil || spent < 4*float64(tcPerCall)/1e9-1e-12 {
		t.Errorf("auditor cap charged %v, %v; want at least %v", spent, err, 4*float64(tcPerCall)/1e9)
	}
	// Resume: nothing is asked twice.
	o, _ = mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1")
	if !strings.Contains(o, "4 already answered") || !strings.Contains(o, "0 to ask") || len(f.sent) != 4 {
		t.Errorf("a second run asked again (sent %d):\n%s", len(f.sent), o)
	}
}

func TestAuditorStopsAtTheLimitAndResumesWhereItStopped(t *testing.T) {
	e := auSetup(t)
	f := &auFake{}
	auUse(t, f)
	// The budget admits exactly one question at its worst case: after one call is
	// metered, the next worst case no longer fits.
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
	// The four askable postings as tcSetup wrote them.
	long := strings.Repeat("You will build and operate services that serve language models. ", 40)
	var worst int64
	for k, desc := range map[string]string{"gh:a:1": long + " first", "gh:a:2": tcDroppedT + long, "gh:a:3": long, "gh:a:4": long} {
		_, _ = task.Ask(context.Background(), deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
			worst = max(worst, teacherWorstNano(prices, q))
			return decide.Answer{}, nil
		}), posting.Posting{Key: posting.Key(k), ATS: "gh", Company: "SynthCo", Title: "Engineer " + k, Description: desc})
	}
	budget := float64(worst+tcPerCall/2) / 1e9
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--budget-usd", fmt.Sprintf("%.9f", budget), "--concurrency", "1")
	if !strings.Contains(o, "asked 1, stored 1") || !strings.Contains(o, "stopped: "+eval.TeacherStoppedAtLimit) || !strings.Contains(o, "run again to resume") {
		t.Fatalf("output:\n%s", o)
	}
	o, _ = mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1")
	if !strings.Contains(o, "asked 3, stored 3") || len(f.sent) != 4 {
		t.Fatalf("resume asked %d in all:\n%s", len(f.sent), o)
	}
}

func TestAuditorRefusesWhenItsOwnCapIsSpent(t *testing.T) {
	e := auSetup(t)
	f := &auFake{}
	auUse(t, f)
	c, err := decide.NewDailyCap(filepath.Join(e.state, "budget", auditorBudgetFile), auditorDailyCapUSD)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Charge(context.Background(), auditorSpendKey, auditorDailyCapUSD); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "budget is spent") || len(f.sent) != 0 {
		t.Errorf("exit %d, sent %d, stderr %s", code, len(f.sent), stderr)
	}
}

func TestAuditorKeepsRefusalsAndMalformedAnswersAsUnusableNotAsAnswers(t *testing.T) {
	e := auSetup(t)
	f := &auFake{answer: func(_ context.Context, q decide.Question) (decide.Answer, error) {
		a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
		for _, s := range q.Slots {
			switch q.RecordID {
			case "gh:a:1":
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Refused, Detail: "refusal"}
			case "gh:a:2":
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: "definitely"} // not an option
			case "gh:a:3":
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Abstained}
			default:
				a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
			}
		}
		return a, nil
	}}
	auUse(t, f)
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1", "--concurrency", "1")
	if !strings.Contains(o, "stored 4 (2 with an unusable slot)") {
		t.Fatalf("output:\n%s", o)
	}
	st, err := eval.OpenTeacherStore(filepath.Join(e.state, eval.AuditorFile))
	if err != nil {
		t.Fatal(err)
	}
	d, summary := tkDigest(t, e)
	cur := st.CurrentOf(tcHaiku, auditorTask(summary, d))
	for _, k := range []string{"gh:a:1", "gh:a:2", "gh:a:3"} {
		if rd, ok := cur.Reading(k); ok {
			t.Errorf("%s: an unusable or abstained reply counts as an answer: %v", k, rd)
		}
	}
	if rd, ok := cur.Reading("gh:a:4"); !ok || rd[wouldapply.Slot] != "yes" {
		t.Errorf("gh:a:4 answer %v %v", rd, ok)
	}
}

func TestAuditorStopsAfterUnreadQuestionsAndTimesOut(t *testing.T) {
	auSetup(t)
	f := &auFake{answer: func(ctx context.Context, _ decide.Question) (decide.Answer, error) {
		<-ctx.Done() // a provider that never answers
		return decide.Answer{}, ctx.Err()
	}}
	auUse(t, f)
	auditorTimeout = 30 * time.Millisecond
	code, o, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1", "--concurrency", "1")
	if code == 0 || !strings.Contains(o, "no reading 3") || !strings.Contains(stderr, "no reading") {
		t.Errorf("exit %d\nstdout %s\nstderr %s", code, o, stderr)
	}
}

func TestAuditorRefusesAQuestionThatWouldCarryAnIdentifyingTerm(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSummary = "early-career ML systems roles; needs visa support"
	if err := config.Save(e.cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	// A dry run sends nothing: it prices the plan and says a real run would refuse.
	o, _ := mustCode(t, 0, "auditor", "calibrate", "--dry-run")
	if !strings.Contains(o, "a real run would REFUSE") || !strings.Contains(o, "the target summary holds 1 identifying term") || !strings.Contains(o, "4 to ask") {
		t.Errorf("dry run:\n%s", o)
	}
	// A real run refuses before any decider is built (tcNoModel fails the test if one is).
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "identifying terms") || !strings.Contains(stderr, "nothing was sent") {
		t.Errorf("real run: exit %d, stderr %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.AuditorFile)); err == nil {
		t.Error("a refused run wrote the auditor's log")
	}
	// The report sends nothing, so the check does not block it.
	mustCode(t, 0, "auditor", "report")
}

// The owner's own identifying-words list is applied too, and a hit in the
// knowledge-base digest is named as the digest's.
func TestAuditorAppliesTheOwnersIdentifyingWordsToTheDigest(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := tkDigest(t, e)
	// Pick a word the digest holds and put it on the owner's list.
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
	dir := track.Dir(cfg, e.state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, track.WordsFile), []byte("# test\n"+word+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "the knowledge-base digest holds 1 identifying term") {
		t.Errorf("exit %d, stderr %s", code, stderr)
	}
}

func TestAuditorReportCannotGateBeforeAnyAnswerAndMeasuresAfter(t *testing.T) {
	e := auSetup(t)
	tcNoModel(t)
	o, _ := mustCode(t, 0, "auditor", "report", "--json")
	var r auditorReportReply
	if err := json.Unmarshal([]byte(o), &r); err != nil || r.Agreement == nil {
		t.Fatalf("report %v:\n%s", err, o)
	}
	if r.Agreement.Gate.Verdict != eval.GateCannot || r.AuditorAnswered != 0 || r.Labelled != 5 || r.Marked != 2 || r.NotInLedger != 1 {
		t.Errorf("before any answer: %+v", r)
	}
	// Teacher readings for two postings, auditor answers for every askable one.
	ts, err := eval.OpenTeacherStore(filepath.Join(e.state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"gh:a:1": "partial_fit", "gh:a:2": "mismatch_seniority"} {
		if err := ts.Append(eval.TeacherRow{Key: posting.Key(k), TextSHA256: "t", Model: tcHaiku, FixtureSHA: "f", PromptHash: "older",
			Slots: map[string]eval.TeacherSlot{"role_fit": {Reading: "answered", Value: v}}, At: jobsT0}); err != nil {
			t.Fatal(err)
		}
	}
	f := &auFake{}
	auUse(t, f)
	mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1")
	o, _ = mustCode(t, 0, "auditor", "report", "--json")
	r = auditorReportReply{}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.AuditorAnswered != 4 || r.TeacherModel != tcHaiku || r.TeacherBaseline.ScoredCurrent != 0 {
		t.Errorf("after answers: answered %d teacher %q current %d", r.AuditorAnswered, r.TeacherModel, r.TeacherBaseline.ScoredCurrent)
	}
	// Two labels in a group cannot pass a gate that needs ten of each class.
	if r.Agreement.Gate.Verdict == eval.GatePasses {
		t.Errorf("the gate passed on a handful of postings: %+v", r.Agreement.Gate)
	}
	text, _ := mustCode(t, 0, "auditor", "report")
	for _, want := range []string{"Agreement with the owner", "Gate:", "| kept | wanted |", "| dropped | wanted |", "| marks | liked |"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestAuditorRoleFitOrderCoversEveryTriageOption(t *testing.T) {
	opts := jdtriage.SlotValues()["role_fit"]
	if len(opts) == 0 {
		t.Fatal("no role_fit options")
	}
	for _, o := range opts {
		if _, ok := auditorRoleFitScore[o]; !ok {
			t.Errorf("role_fit option %q has no place in the baseline's order", o)
		}
	}
	if len(auditorRoleFitScore) != len(opts) {
		t.Errorf("the order names %d values, the fixture %d", len(auditorRoleFitScore), len(opts))
	}
}

// Rule 7: the auditor's answers are evidence about the auditor only. No
// non-test source outside the auditor command and its eval measure names the
// log, so nothing in the pipeline, ranking or drop policy can read it.
func TestNothingButTheAuditorReadsItsLog(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("cmd", "vani", "auditorcmd.go"):                      true,
		filepath.Join("internal", "eval", "auditor.go"):                    true,
		filepath.Join("internal", "decide", "wouldapply", "wouldapply.go"): true,
	}
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Go builds nothing from a dot or underscore directory, and the dot ones (.devcouncil, .state, .cursor,
		// .claude, macos/.build) hold live agent and tool state that changes during a run: walking them made
		// this test's `go test` cache key churn, so cmd/vani's shard holding it never replayed from cache.
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") ||
			d.Name() == "node_modules" || d.Name() == "tmp") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		scanned++
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if allowed[rel] {
			return nil
		}
		// Code only: a comment may mention the log (and the help text may name
		// its file), but no statement may reach for it.
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, ref := range []string{"eval.AuditorFile", `"auditor_readings`, "wouldapply.", "/internal/decide/wouldapply\""} {
				if strings.Contains(line, ref) {
					t.Errorf("%s:%d reaches for the auditor's log or question (%s)", rel, i+1, ref)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d Go files: the check did not run", scanned)
	}
}
