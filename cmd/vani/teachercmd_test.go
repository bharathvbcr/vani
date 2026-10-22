package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	tcSecret   = "PRIVATE-VAULT-NOTE-SENTINEL-4410"
	tcHaiku    = "claude-haiku-5-5"
	tcPerCall  = int64(700_000) // what the fake charges a question, nano-USD
	tcDroppedT = "dropped posting body that must be read back"
)

type tcEnv struct{ cfgPath, state string }

// tcSetup seeds a ledger: two postings d1-ultra read (one of them dropped), one
// the llm read, one copied reading, one d1-ultra read with no text.
func tcSetup(t *testing.T) *tcEnv {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "early-career ML systems roles"
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", TriageModel: tcHaiku, InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
	cfg.Decide.DailyUSD.LLM = 5
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	type seed struct {
		key, backend, desc string
		status             posting.Status
		ann                map[string]string
	}
	long := strings.Repeat("You will build and operate services that serve language models. ", 40)
	for _, s := range []seed{
		{"gh:a:1", "d1-ultra", long + " first", posting.Explained, map[string]string{"note": tcSecret}},
		{"gh:a:2", "d1-ultra", tcDroppedT + long, posting.Dropped, map[string]string{"triage.dropped_by": "title rule"}},
		{"gh:a:3", "llm", long, posting.Explained, nil},
		{"gh:a:4", "d1-ultra", long, posting.Explained, map[string]string{ledger.AnnTriageInheritedFrom: "gh:a:1"}},
		{"gh:a:5", "d1-ultra", " ", posting.Explained, nil},
	} {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: "SynthCo", Title: "Engineer " + s.key, Description: s.desc}
		ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": s.backend}
		for k, v := range s.ann {
			ann[k] = v
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: s.status, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(state, "vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "vault", "notes.txt"), []byte(tcSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	return &tcEnv{cfgPath, state}
}

// tcFake answers every slot with its first option, remembers what it was sent
// and charges tcPerCall to a shared meter that teacherSpent reads.
type tcFake struct {
	mu    sync.Mutex
	sent  []string
	meter atomic.Int64
}

func (f *tcFake) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.mu.Lock()
	f.sent = append(f.sent, q.Prompt+"\n"+string(q.Context))
	f.mu.Unlock()
	f.meter.Add(tcPerCall)
	a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a, nil
}

func tcUse(t *testing.T, f *tcFake) {
	t.Helper()
	oldD, oldS := teacherDecider, teacherSpent
	t.Cleanup(func() { teacherDecider, teacherSpent = oldD, oldS })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	teacherSpent = func(context.Context, string) (int64, error) { return f.meter.Load(), nil }
}

func tcNoModel(t *testing.T) {
	t.Helper()
	oldD, oldS := teacherDecider, teacherSpent
	t.Cleanup(func() { teacherDecider, teacherSpent = oldD, oldS })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) {
		t.Fatal("the model's decider was built")
		return nil, nil
	}
	teacherSpent = func(context.Context, string) (int64, error) { return 0, nil }
}

func TestTeacherDryRunAsksNothingAndPricesFromTheTable(t *testing.T) {
	e := tcSetup(t)
	tcNoModel(t)
	o, _ := mustCode(t, 0, "teacher", "--dry-run", "--of", "d1-ultra")
	t.Log("\n" + o)
	table, ok := anthropic.PriceOf(tcHaiku)
	if !ok {
		t.Fatalf("no rate table entry for %s", tcHaiku)
	}
	for _, want := range []string{
		"2 postings read by d1-ultra",
		"1 with no text, 1 copied readings",
		"2 to ask",
		"input tokens per posting",
		"Anthropic's published rate table",
		fmt.Sprintf("input $%.4f and output $%.4f per million tokens", float64(table.Input)/1000, float64(table.Output)/1000),
		fmt.Sprintf("above %d prompt tokens input $%.4f", table.LongPromptAbove, float64(table.LongRates.Input)/1000),
		"daily llm cap is $5.00",
		"day(s)",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, o)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Error("a dry run wrote the readings log")
	}
}

func TestTeacherRefusesARealRunWithoutABudget(t *testing.T) {
	e := tcSetup(t)
	tcNoModel(t)
	for _, args := range [][]string{
		{"teacher", "--of", "d1-ultra"},
		{"teacher", "--of", "d1-ultra", "--budget-usd", "0"},
		{"teacher", "--of", "d1-ultra", "--budget-usd", "-1"},
		{"teacher", "--of", "d1-ultra", "--budget-usd", "NaN"},
		{"teacher", "--of", "d1-ultra", "--budget-usd", "1", "--concurrency", "0"},
	} {
		code, _, stderr := do(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want a usage error (2); stderr %s", args, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Error("a refused run wrote the readings log")
	}
}

func TestTeacherRunReadsDroppedTextKeepsItPrivateAndResumes(t *testing.T) {
	e := tcSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	// $0.0024 admits two questions at the worst case (prompt bytes + output budget) this posting size prices to;
	// the third could pass it.
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "0.0024", "--concurrency", "1")
	if !strings.Contains(o, "asked 2, stored 2") {
		t.Fatalf("output:\n%s", o)
	}
	for _, s := range f.sent {
		if strings.Contains(s, tcSecret) {
			t.Errorf("a prompt carries private data: %.200s", s)
		}
		if !strings.Contains(s, "BEGIN UNTRUSTED") && !strings.Contains(s, "early-career ML systems roles") {
			t.Errorf("prompt lacks the target summary: %.200s", s)
		}
	}
	joined := strings.Join(f.sent, "\n")
	if !strings.Contains(joined, tcDroppedT) {
		t.Error("the dropped posting's text was not read back and asked about")
	}
	raw, err := os.ReadFile(filepath.Join(e.state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{tcSecret, tcDroppedT, "SynthCo"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("the readings log holds %q:\n%s", bad, raw)
		}
	}
	for _, want := range []string{`"model":"` + tcHaiku + `"`, `"fixture_sha":"` + eval.FixtureSHA() + `"`, `"prompt_hash":"`, `"text_sha256":"`, `"slots":{`, `"key":"gh:a:1"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("readings lack %s:\n%s", want, raw)
		}
	}
	// Resume: nothing is asked twice and the stored rows stay as they were.
	f.meter.Store(0)
	o, _ = mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if !strings.Contains(o, "0 to ask") || len(f.sent) != 2 {
		t.Errorf("a second run asked again (sent %d):\n%s", len(f.sent), o)
	}
}

func TestTeacherRunStopsAtTheLimitCleanlyThenResumes(t *testing.T) {
	e := tcSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "0.0016", "--concurrency", "1")
	if !strings.Contains(o, "asked 1, stored 1") || !strings.Contains(o, "stopped: "+eval.TeacherStoppedAtLimit) || !strings.Contains(o, "run again to resume") {
		t.Fatalf("output:\n%s", o)
	}
	f.meter.Store(0)
	o, _ = mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if !strings.Contains(o, "asked 1, stored 1") || len(f.sent) != 2 {
		t.Fatalf("resume asked %d in all:\n%s", len(f.sent), o)
	}
	st, err := eval.OpenTeacherStore(filepath.Join(e.state, eval.TeacherFile))
	if err != nil || st.Len() != 2 {
		t.Errorf("store holds %d rows, %v", st.Len(), err)
	}
}

func TestTeacherRunRefusesWhenTheDailyCapIsSpent(t *testing.T) {
	tcSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	f.meter.Store(5_000_000_000) // the whole $5 cap
	code, _, stderr := do(t, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "already spent") || len(f.sent) != 0 {
		t.Errorf("exit %d, sent %d, stderr %s", code, len(f.sent), stderr)
	}
}
