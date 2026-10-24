package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

// A synthetic bank: no real résumé content.
const kbmBank = `{"entries":{
  "e1":{"title":"LLM Inference Runtime","org":"SyntheticCo","dates":"2024","location":"Nowhere","stack":"Go, Rust, CUDA","bullets":["b1","b2"]}},
 "bullets":{
  "b1":{"text":"Cut decode latency with fused attention kernels in Metal"},
  "b2":{"text":"Built a request batching scheduler in Rust"}}}`

const longDesc = "You will design and operate services that serve large language models to production traffic, tune GPU kernels for lower latency, " +
	"and build the batching and scheduling layer. You will measure throughput and memory use, write benchmarks, review code and mentor teammates on performance work."

type kbmSeed struct{ key, company, title, desc, fit string }

type kbmEnv struct {
	cfgPath, state, bankPath string
	cfg                      config.Config
}

func kbmSetup(t *testing.T, seeds ...kbmSeed) *kbmEnv {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	k := &kbmEnv{cfgPath: cfgPath, state: state, bankPath: filepath.Join(filepath.Dir(cfgPath), "bank.json")}
	if err := os.WriteFile(k.bankPath, []byte(kbmBank), 0o600); err != nil {
		t.Fatal(err)
	}
	k.cfg = config.Default()
	k.cfg.TargetSummary = "early-career ML systems and LLM inference roles"
	k.cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
	k.cfg.Decide.DailyUSD.LLM = 5
	k.cfg.KB.Bank = k.bankPath
	wireEligibleKB(t, &k.cfg, k.bankPath, kbmBank) // the digest carries eligible bullets only
	k.save(t)

	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, s := range seeds {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: s.company, Title: s.title, Description: s.desc}
		ann := map[string]string{}
		if s.fit != "" {
			ann["triage.role_fit"], ann["triage.role_fit.reading"], ann["triage.role_fit.p"] = s.fit, "answered", "0.9"
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: posting.Explained, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

func (k *kbmEnv) save(t *testing.T) {
	t.Helper()
	if err := config.Save(k.cfgPath, k.cfg); err != nil {
		t.Fatal(err)
	}
}

// fakeKB answers by title and counts the calls.
type fakeKB struct {
	mu       sync.Mutex
	answer   func(title string) decide.SlotReading
	contexts []string
}

func (f *fakeKB) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contexts = append(f.contexts, string(q.Context))
	title := ""
	if i := strings.Index(string(q.Context), "Title: "); i >= 0 {
		title = strings.SplitN(string(q.Context)[i+len("Title: "):], "\n", 2)[0]
	}
	return decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{kbmatch.Slot: f.answer(title)}}, nil
}

func useFake(t *testing.T, f *fakeKB) {
	t.Helper()
	oldD, oldF := kbmatchDecider, kbmatchFocus
	t.Cleanup(func() { kbmatchDecider, kbmatchFocus = oldD, oldF })
	kbmatchDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	// No embedding model in these tests unless one asks for it.
	kbmatchFocus = func(context.Context, config.Config, string, semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		return nil, nil, errors.New("no embedding model in this test")
	}
}

func byTitle(m map[string]string) func(string) decide.SlotReading {
	return func(title string) decide.SlotReading {
		if v, ok := m[title]; ok {
			return decide.SlotReading{Reading: decide.Answered, Value: v}
		}
		return decide.SlotReading{Reading: decide.Abstained}
	}
}

func twoPostings(t *testing.T) *kbmEnv {
	return kbmSetup(t,
		kbmSeed{"gh:a:1", "SecretCo", "Inference Engineer", longDesc, "partial_fit"},
		kbmSeed{"gh:a:2", "OtherCo", "Marketing Lead", longDesc, "partial_fit"},
		kbmSeed{"gh:a:3", "ThirdCo", "Sales Rep", longDesc, "mismatch_domain"},
		kbmSeed{"gh:a:4", "FourthCo", "Thin Posting", "too short to judge", "partial_fit"},
	)
}

func TestKBMatchDryRunSendsNothing(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(nil)}
	useFake(t, f)
	kbmatchDecider = func(context.Context, *wired, string) (decide.Decider, error) {
		t.Fatal("a dry run must not build the decider")
		return nil, nil
	}
	o, _ := mustCode(t, 0, "kbmatch", "run", "--dry-run")
	for _, want := range []string{"2 postings to ask about", "1 mismatch readings", "1 too thin", "digest of your knowledge base", "anthropic model claude-test", "at most $"} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, o)
		}
	}
	if len(f.contexts) != 0 {
		t.Errorf("a dry run asked the model")
	}
	if _, err := os.Stat(filepath.Join(k.state, kbmatch.FileName)); err == nil {
		t.Errorf("a dry run wrote the store")
	}
}

func TestKBMatchRunAsksKeepsAndRanks(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})}
	useFake(t, f)
	before := findJob(t, listJSON(t), "gh:a:1").Priority

	o, _ := mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(o, "asked 2, answered 2 (core 1, partial 0, adjacent 0, unrelated 1), not read 0") {
		t.Errorf("run summary:\n%s", o)
	}
	if len(f.contexts) != 2 {
		t.Fatalf("the model was asked %d times, want 2 (not the mismatch, not the thin one)", len(f.contexts))
	}
	for _, c := range f.contexts {
		for _, secret := range []string{"SecretCo", "OtherCo", "ThirdCo", "SyntheticCo", "Nowhere"} {
			if strings.Contains(c, secret) {
				t.Errorf("%q reached the question", secret)
			}
		}
		if !strings.Contains(c, "early-career ML systems") || !strings.Contains(c, "Cut decode latency") {
			t.Errorf("the question lacks the target summary or the digest")
		}
	}
	st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName))
	if err != nil || st.Len() != 2 {
		t.Fatalf("store: %v, %d entries", err, st.Len())
	}

	// The reads reach the ranking: core rises, unrelated falls, the others stay.
	list := listJSON(t)
	core, unrelated := findJob(t, list, "gh:a:1"), findJob(t, list, "gh:a:2")
	if !(core.Priority > before && core.Priority > unrelated.Priority) {
		t.Errorf("core %.1f (was %.1f) should lead unrelated %.1f", core.Priority, before, unrelated.Priority)
	}
	var names []string
	for _, f := range core.ScoreFactors {
		names = append(names, f.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "kb_match") {
		t.Errorf("the judged posting's factors lack kb_match: %v", names)
	}
	for _, f := range findJob(t, list, "gh:a:3").ScoreFactors {
		if f.Name == "kb_match" {
			t.Errorf("an unjudged posting shows a kb_match factor")
		}
	}

	// Asking again finds everything answered.
	f.contexts = nil
	o, _ = mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(o, "0 postings to ask about") || len(f.contexts) != 0 {
		t.Errorf("a second run asked again:\n%s", o)
	}
	o, _ = mustCode(t, 0, "kbmatch", "status")
	if !strings.Contains(o, "2 answers kept, 2 current (core 1, partial 0, adjacent 0, unrelated 1)") {
		t.Errorf("status:\n%s", o)
	}
}

func TestKBMatchEditingTheKnowledgeBaseAsksAgain(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "adjacent"})}
	useFake(t, f)
	mustCode(t, 0, "kbmatch", "run")
	f.contexts = nil
	edited := strings.Replace(kbmBank, "Built a request batching scheduler in Rust", "Built a request batching scheduler in Go", 1)
	// The edited bullet is re-reviewed on its new wording, so it stays eligible and
	// only the digest changes (the selection file the config lists still lists it).
	scratch := k.cfg
	wireEligibleKB(t, &scratch, k.bankPath, edited)
	o, _ := mustCode(t, 0, "kbmatch", "status")
	if !strings.Contains(o, "0 current") {
		t.Errorf("answers given another knowledge base are still current:\n%s", o)
	}
	// A stale answer is unknown to the ranker, not a score of its own.
	for _, fct := range findJob(t, listJSON(t), "gh:a:1").ScoreFactors {
		if fct.Name == "kb_match" {
			t.Errorf("a stale answer still ranks")
		}
	}
	mustCode(t, 0, "kbmatch", "run")
	if len(f.contexts) != 2 {
		t.Errorf("an edit should re-ask both postings, asked %d", len(f.contexts))
	}
}

func TestKBMatchTopBoundsTheRun(t *testing.T) {
	twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	mustCode(t, 0, "kbmatch", "run", "--top", "1")
	if len(f.contexts) != 1 {
		t.Errorf("--top 1 asked %d", len(f.contexts))
	}
	for _, bad := range []string{"0", "-3", "501"} {
		if code, _, _ := do(t, "kbmatch", "run", "--top", bad); code != exitUsage {
			t.Errorf("--top %s: exit %d, want usage", bad, code)
		}
	}
}

func TestKBMatchStopsWhenTheBackendIsGoneButKeepsWhatItGot(t *testing.T) {
	k := kbmSetup(t,
		kbmSeed{"gh:a:1", "A", "First", longDesc, "partial_fit"},
		kbmSeed{"gh:a:2", "B", "Second", longDesc, "partial_fit"},
		kbmSeed{"gh:a:3", "C", "Third", longDesc, "partial_fit"},
		kbmSeed{"gh:a:4", "D", "Fourth", longDesc, "partial_fit"},
		kbmSeed{"gh:a:5", "E", "Fifth", longDesc, "partial_fit"},
	)
	n := 0
	f := &fakeKB{answer: func(string) decide.SlotReading {
		if n++; n == 1 {
			return decide.SlotReading{Reading: decide.Answered, Value: "partial"}
		}
		return decide.SlotReading{Reading: decide.Unavailable, Detail: "budget"}
	}}
	useFake(t, f)
	code, o, _ := do(t, "kbmatch", "run")
	if code != exitRuntime || !strings.Contains(o, "stopped: the last 3 questions got no usable answer") {
		t.Fatalf("exit %d:\n%s", code, o)
	}
	if len(f.contexts) != 4 {
		t.Errorf("asked %d times; the run should stop after 1 answer and 3 unusable ones", len(f.contexts))
	}
	if st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName)); err != nil || st.Len() != 1 {
		t.Errorf("the one answer should be kept: %v, %d", err, st.Len())
	}
}

func TestKBMatchAbstainingIsNotAnOutage(t *testing.T) {
	kbmSetup(t,
		kbmSeed{"gh:a:1", "A", "First", longDesc, "partial_fit"},
		kbmSeed{"gh:a:2", "B", "Second", longDesc, "partial_fit"},
		kbmSeed{"gh:a:3", "C", "Third", longDesc, "partial_fit"},
		kbmSeed{"gh:a:4", "D", "Fourth", longDesc, "partial_fit"},
		kbmSeed{"gh:a:5", "E", "Fifth", longDesc, "partial_fit"},
	)
	f := &fakeKB{answer: byTitle(nil)}
	useFake(t, f)
	o, _ := mustCode(t, 0, "kbmatch", "run")
	if len(f.contexts) != 5 || !strings.Contains(o, "not read 5") {
		t.Errorf("a model that cannot tell should not stop the run: asked %d\n%s", len(f.contexts), o)
	}
}

func TestKBMatchRefusesWithoutWhatItNeeds(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(nil)})
	k.cfg.TargetSummary = ""
	k.save(t)
	if code, _, e := do(t, "kbmatch", "run", "--dry-run"); code != exitRuntime || !strings.Contains(e, "target_summary") {
		t.Errorf("no target summary: exit %d, %s", code, e)
	}
	k.cfg.TargetSummary = "x"
	k.cfg.LLM = config.LLM{}
	k.cfg.Decide.DailyUSD.LLM = 0
	k.save(t)
	if code, _, e := do(t, "kbmatch", "run"); code != exitRuntime || !strings.Contains(e, "llm provider") {
		t.Errorf("no llm: exit %d, %s", code, e)
	}
	k.cfg.LLM = config.LLM{Provider: "anthropic", Model: "m", InputNanoUSDPerToken: 1, OutputNanoUSDPerToken: 1}
	k.cfg.Decide.DailyUSD.LLM = 1
	k.cfg.KB.Bank = filepath.Join(filepath.Dir(k.bankPath), "missing.json")
	k.save(t)
	if code, _, e := do(t, "kbmatch", "run"); code != exitRuntime || !strings.Contains(e, "knowledge base") {
		t.Errorf("no bank: exit %d, %s", code, e)
	}
	if code, _, _ := do(t, "kbmatch"); code != exitUsage {
		t.Errorf("no subcommand should be a usage error")
	}
	if code, _, _ := do(t, "kbmatch", "bogus"); code != exitUsage {
		t.Errorf("an unknown subcommand should be a usage error")
	}
}

func TestKBMatchCorruptStoreStartsAgain(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	path := filepath.Join(k.state, kbmatch.FileName)
	if err := os.MkdirAll(k.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The listing ignores an unreadable store, and says so.
	var raw struct {
		Insight struct {
			Warning string `json:"warning"`
		} `json:"insight"`
	}
	o, _ := mustCode(t, 0, "jobs", "list", "--json")
	if err := json.Unmarshal([]byte(o), &raw); err != nil || !strings.Contains(raw.Insight.Warning, "language-model reads of your work not used") {
		t.Errorf("an unreadable store should warn on the listing: %q (%v)", raw.Insight.Warning, err)
	}
	_, e := mustCode(t, 0, "kbmatch", "run")
	if !strings.Contains(e, "starting it again") {
		t.Errorf("run did not say it started again: %s", e)
	}
	if st, err := kbmatch.OpenStore(path); err != nil || st.Len() != 2 {
		t.Errorf("store after the run: %v, %d", err, st.Len())
	}
}

// A second run that starts while the first is between opening the store and
// asking must not ask again: it waits for the lock (or is told the store is
// busy) and, once let in, reads what the first run kept.
func TestKBMatchRacingRunsDoNotDoubleSpend(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "partial"})}
	useFake(t, f)
	oldWait, oldHook := kbmatchLockWait, kbmatchAfterOpen
	t.Cleanup(func() { kbmatchLockWait, kbmatchAfterOpen = oldWait, oldHook })
	kbmatchLockWait = 10 * time.Second

	reached, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	kbmatchAfterOpen = func() {
		if calls.Add(1) == 1 {
			close(reached)
			<-release
		}
	}
	type result struct {
		code int
		o, e string
	}
	var results [2]result
	var wg sync.WaitGroup
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var o, e bytes.Buffer
			code := run([]string{"kbmatch", "run"}, &o, &e)
			results[i] = result{code, o.String(), e.String()}
		}()
	}
	start(0)
	<-reached
	start(1)
	// Give the second run time to get past (or be stopped at) the lock.
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()

	if results[0].code != 0 {
		t.Fatalf("first run: exit %d\n%s\n%s", results[0].code, results[0].o, results[0].e)
	}
	if results[1].code != 0 && !strings.Contains(results[1].e, "another run is using the store") {
		t.Errorf("second run failed for another reason: exit %d\n%s", results[1].code, results[1].e)
	}
	if len(f.contexts) != 2 {
		t.Errorf("the model was asked %d times for 2 postings (double spend)", len(f.contexts))
	}
	st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName))
	if err != nil || st.Len() != 2 {
		t.Errorf("both answers should survive: %v, %d", err, st.Len())
	}
}

// A corrupt store is only ever removed by a run that holds the lock; a dry run
// takes no lock and removes nothing.
func TestKBMatchCorruptStoreIsNotRemovedWithoutTheLock(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core"})}
	useFake(t, f)
	oldWait := kbmatchLockWait
	t.Cleanup(func() { kbmatchLockWait = oldWait })
	kbmatchLockWait = 50 * time.Millisecond
	path := filepath.Join(k.state, kbmatch.FileName)
	if err := os.MkdirAll(k.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := kbmatch.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	code, _, e := do(t, "kbmatch", "run")
	if code == 0 || !strings.Contains(e, "another run is using the store") {
		t.Errorf("a run against a held store should say it is busy: exit %d, %s", code, e)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the corrupt store was removed without the lock: %v", err)
	}
	if len(f.contexts) != 0 {
		t.Errorf("a busy run asked the model")
	}
	// A dry run needs no lock and leaves the file alone.
	mustCode(t, 0, "kbmatch", "run", "--dry-run")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("a dry run removed the corrupt store: %v", err)
	}
}
