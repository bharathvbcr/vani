package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The manual Sonnet reading (`vani jobs retriage`). Every provider here is a
// fake: no test reaches a network, a key or the Keychain.

const (
	rtSonnet  = "claude-sonnet-5-5"
	rtSummary = "ML inference roles"
	rtAnswer  = `{"role_fit":"strong_fit","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"}`
	rtDesc    = "Build and serve low-latency inference systems for large models. Own the runtime, the batching and the GPU kernels."
)

// retriageLLM is a fake llm.Provider that records every request and answers with
// a scripted reply, usage and stop reason.
type retriageLLM struct {
	mu       sync.Mutex
	text     string
	usage    llm.Usage
	stop     llm.StopReason
	err      error
	reqs     []llm.Request
	onStream func(ctx context.Context) error // runs inside Stream; its error fails the call
}

func (p *retriageLLM) Name() string { return "fake" }
func (p *retriageLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 8192}, true
}
func (p *retriageLLM) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	if p.onStream != nil {
		if err := p.onStream(ctx); err != nil {
			return nil, err
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return &retriageStream{p: p}, nil
}
func (p *retriageLLM) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

type retriageStream struct {
	p    *retriageLLM
	done bool
}

func (s *retriageStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.p.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *retriageStream) Response() (llm.Response, error) {
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.p.text}}},
		StopReason: s.p.stop, Usage: s.p.usage,
	}, nil
}
func (s *retriageStream) Close() error { return nil }

// rtJob is one seeded posting.
type rtJob struct {
	key, title, desc string
	status           posting.Status
	fit              string  // role_fit; "" leaves no triage reading
	p                string  // its probability annotation
	rank             string  // the run's rank annotation
	extra            map[string]string
}

func rtSeed(t *testing.T, state string, jobs ...rtJob) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, j := range jobs {
		rtAppend(t, l, j)
	}
	return l
}

func rtAppend(t *testing.T, l *ledger.File, j rtJob) {
	t.Helper()
	if j.desc == "" {
		j.desc = rtDesc
	}
	if j.status == "" {
		j.status = posting.Scored
	}
	p := posting.Posting{Key: posting.Key(j.key), ATS: "gh", Company: "Acme", Title: j.title, Location: "Austin, TX", Description: j.desc, URL: "https://boards.invalid/" + j.key}
	ann := map[string]string{ledger.AnnExplanation: "Fits: Inference serving and runtimes (88%)."}
	if j.fit != "" {
		ann["triage.role_fit"], ann["triage.role_fit.reading"], ann["triage.role_fit.backend"] = j.fit, "answered", "d1-ultra"
		if j.p != "" {
			ann["triage.role_fit.p"] = j.p
		}
		ann["triage.seniority"], ann["triage.seniority.reading"] = "senior", "answered"
		ann["triage.sponsorship"], ann["triage.sponsorship.reading"] = "no_sponsorship", "answered"
		ann["triage.work_mode"], ann["triage.work_mode.reading"] = "onsite", "answered"
	}
	if j.rank != "" {
		ann[ledger.AnnRank] = j.rank
	}
	for k, v := range j.extra {
		ann[k] = v
	}
	e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: j.status, Posting: &p, Annotations: ann}
	if err := l.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

// rtSetup writes a config with a Claude triage model and gives the command the
// fake. mutate edits the config before it is saved.
func rtSetup(t *testing.T, p *retriageLLM, mutate func(*config.Config)) (cfgPath, state string) {
	t.Helper()
	cfgPath, state = setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSummary = rtSummary
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-4-7", TriageModel: rtSonnet,
		InputNanoUSDPerToken: 5000, OutputNanoUSDPerToken: 25000}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	old := retriageProvider
	retriageProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { retriageProvider = old })
	return cfgPath, state
}

func rtRun(t *testing.T, want int, args ...string) string {
	t.Helper()
	o, _ := mustCode(t, want, append([]string{"jobs", "retriage", "--json"}, args...)...)
	return o
}

func rtDecode(t *testing.T, raw string) retriageReply {
	t.Helper()
	var r retriageReply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("reply %q: %v", raw, err)
	}
	return r
}

func rtFail(t *testing.T, args ...string) retriageErrorReply {
	t.Helper()
	raw := rtRun(t, 1, args...)
	var e retriageErrorReply
	if err := json.Unmarshal([]byte(raw), &e); err != nil || e.Error == "" || e.Code == "" {
		t.Fatalf("not an error reply: %q (%v)", raw, err)
	}
	return e
}

func rtShow(t *testing.T, key string) jobview.Job {
	t.Helper()
	o, _ := mustCode(t, 0, "jobs", "show", "--json", key)
	var j jobview.Job
	if err := json.Unmarshal([]byte(o), &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func rtFit(j jobview.Job) (backend, reading string) {
	for _, f := range j.ScoreFactors {
		if f.Name == "fit" {
			return f.Backend, f.Reading
		}
	}
	return "", ""
}

func rtKeys(t *testing.T, args ...string) []string {
	t.Helper()
	l := listJSON(t, args...)
	var ks []string
	for _, j := range l.Jobs {
		ks = append(ks, string(j.Key))
	}
	return ks
}

func rtStoreFile(state string) string {
	return filepath.Join(manualtriage.Dir(state), "readings.jsonl")
}

func rtNoStore(t *testing.T, state string) {
	t.Helper()
	if raw, err := os.ReadFile(rtStoreFile(state)); err == nil && len(raw) != 0 {
		t.Fatalf("a manual reading was stored: %s", raw)
	}
}

// rtQuestion is the question a run would send for the posting: the one
// definition (jdtriage.Triager) the command must reuse.
func rtQuestion(t *testing.T, p posting.Posting, summary string) decide.Question {
	t.Helper()
	var q decide.Question
	d := askFunc(func(_ context.Context, got decide.Question) (decide.Answer, error) { q = got; return decide.Answer{}, nil })
	if _, err := (jdtriage.Triager{D: d, TargetSummary: summary}).Triage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return q
}

func rtPosting(key, title string) posting.Posting {
	return posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: title, Location: "Austin, TX", Description: rtDesc, URL: "https://boards.invalid/" + key}
}

// --dry-run says what asking would cost and asks nothing: the provider is never
// built, no key is read, and nothing is written.
func TestJobsRetriageDryRunNeverCallsTheProvider(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	retriageProvider = func(context.Context, *wired) (llm.Provider, error) {
		t.Fatal("--dry-run built the provider")
		return nil, nil
	}
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	r := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:1"))
	if !r.DryRun || r.Model != rtSonnet || r.Estimate == nil || r.Cost != nil || r.After != nil {
		t.Fatalf("reply %+v", r)
	}
	q := rtQuestion(t, rtPosting("gh:acme:1", "Inference Engineer"), rtSummary)
	sys, user := llmbackend.Messages(q)
	if want := int64((len(sys) + len(user) + 3) / 4); r.Estimate.InputTokens != want {
		t.Fatalf("input tokens %d, want %d (the question a run sends)", r.Estimate.InputTokens, want)
	}
	e := r.Estimate
	if !(e.TypicalUSD > 0 && e.TypicalUSD < e.MaxUSD) || e.MaxOutputTokens != llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom || e.TypicalOutputTokens != 150 {
		t.Fatalf("estimate %+v", e)
	}
	if e.DailyCapUSD != config.Default().Decide.DailyUSD.LLM || e.SpentUSD == nil || *e.SpentUSD != 0 || e.RemainingUSD == nil || e.CapReached || e.PriceSource == "" {
		t.Fatalf("cap facts %+v", e)
	}
	rtNoStore(t, state)
	if _, err := os.Stat(manualtriage.Dir(state)); err == nil {
		t.Fatal("--dry-run created the store directory")
	}
	if p.calls() != 0 {
		t.Fatalf("%d provider calls", p.calls())
	}
}

// A cap that cannot cover what a call reserves is said before Confirm, not after.
func TestJobsRetriageDryRunSaysWhenTheCapIsTooLow(t *testing.T) {
	_, state := rtSetup(t, &retriageLLM{}, func(c *config.Config) { c.Decide.DailyUSD.LLM = 0.0001 })
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	e := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:1")).Estimate
	if !e.CapReached || e.RemainingUSD == nil || *e.RemainingUSD > 0.0001 {
		t.Fatalf("estimate %+v", e)
	}
}

// Today's spend that cannot be read is absent, never zero.
func TestJobsRetriageDryRunLeavesUnreadableSpendOut(t *testing.T) {
	_, state := rtSetup(t, &retriageLLM{}, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	w := &wired{stateDir: state}
	w.cfg.LLM = config.LLM{}
	path := w.budgetLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not a ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:1")).Estimate
	if e.SpentUSD != nil || e.RemainingUSD != nil || e.CapReached {
		t.Fatalf("an unreadable spend was reported: %+v", e)
	}
}

func TestJobsRetriageStoresTheReadingAndReportsBeforeAfter(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 1800, OutputTokens: 210}, stop: llm.StopEndTurn}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "partial_fit", rank: "0.6", p: "0.9"},
		rtJob{key: "gh:acme:2", title: "Platform Engineer", fit: "adjacent", rank: "0.6", p: "0.9"},
	)
	// A private note on the job never reaches the model.
	mustCode(t, 0, "jobs", "note", "--json", "gh:acme:2", "PRIVATE-NOTE-4471")
	if before := rtKeys(t); len(before) != 2 || before[0] != "gh:acme:1" {
		t.Fatalf("order before: %v", before)
	}

	raw := rtRun(t, 0, "gh:acme:2")
	r := rtDecode(t, raw)
	if p.calls() != 1 {
		t.Fatalf("%d provider calls, want 1", p.calls())
	}
	req := p.reqs[0]
	if req.Model != rtSonnet {
		t.Fatalf("asked %q, want llm.triage_model %q", req.Model, rtSonnet)
	}
	// The exact triage question: the system prompt and user message a run builds,
	// the posting between the untrusted-content markers.
	q := rtQuestion(t, rtPosting("gh:acme:2", "Platform Engineer"), rtSummary)
	sys, user := llmbackend.Messages(q)
	if req.System != sys || req.Messages[0].Text() != user {
		t.Fatalf("the request is not the triage question:\n%s", req.Messages[0].Text())
	}
	for _, s := range []string{llmbackend.BeginMarker, llmbackend.EndMarker, rtDesc, rtSummary} {
		if !strings.Contains(req.Messages[0].Text(), s) {
			t.Errorf("prompt lacks %q", s)
		}
	}
	for _, leak := range []string{"PRIVATE-NOTE-4471", "d1-ultra", "triage.role_fit"} {
		if strings.Contains(req.Messages[0].Text(), leak) || strings.Contains(req.System, leak) {
			t.Errorf("%q reached the prompt", leak)
		}
	}

	if r.Model != rtSonnet || r.Status != posting.Scored || r.Before == nil || r.After == nil || r.Cost == nil {
		t.Fatalf("reply %s", raw)
	}
	if b := r.Before.Slots["role_fit"]; b.Value != "adjacent" || b.Backend != "d1-ultra" || b.P == nil || *b.P != 0.9 {
		t.Fatalf("before.role_fit = %+v", b)
	}
	if got := r.Before.Slots["seniority"]; got.Value != "senior" {
		t.Fatalf("before.seniority = %+v", got)
	}
	want := map[string]string{"role_fit": "strong_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"}
	if len(r.After.Slots) != len(want) {
		t.Fatalf("after slots %+v", r.After.Slots)
	}
	for slot, v := range want {
		if a := r.After.Slots[slot]; a.Value != v || a.Backend != "manual" {
			t.Errorf("after.%s = %+v, want %s from manual", slot, a, v)
		}
	}
	if !(r.After.Relevance > r.Before.Relevance) || !(r.After.Priority > r.Before.Priority) || r.After.Model != rtSonnet || r.After.At == nil {
		t.Fatalf("before %+v after %+v", r.Before, r.After)
	}
	pin, pout, _, err := kbmatchPrices(mustLoad(t), rtSonnet)
	if err != nil || pin <= 0 || pout <= 0 {
		t.Fatalf("prices %d/%d: %v", pin, pout, err)
	}
	if r.Cost.InputTokens != 1800 || r.Cost.OutputTokens != 210 || r.Cost.USD != float64(1800*pin+210*pout)/1e9 {
		t.Fatalf("cost %+v (prices %d/%d)", r.Cost, pin, pout)
	}

	// Kept beside the ledger, keyed to this text, and used by the next read.
	m, err := manualtriage.Read(manualtriage.Dir(state))
	if err != nil || len(m) != 1 {
		t.Fatalf("store: %v %v", m, err)
	}
	rd := m["gh:acme:2"]
	if rd.Model != rtSonnet || rd.Text != manualtriage.TextSHA256(rtPosting("gh:acme:2", "Platform Engineer")) ||
		rd.Prompt != manualtriage.PromptSHA256(sys, rtSummary) || rd.Slots["work_mode"] != "remote" {
		t.Fatalf("stored %+v", rd)
	}
	j := rtShow(t, "gh:acme:2")
	if j.ManualTriage == nil || !j.ManualTriage.Current || j.ManualTriage.StaleReason != "" || j.ManualTriage.Slots["role_fit"] != "strong_fit" || j.ManualTriage.Model != rtSonnet {
		t.Fatalf("show.manual_triage = %+v", j.ManualTriage)
	}
	if b, rd := rtFit(j); b != "manual" || rd != "strong_fit" {
		t.Fatalf("fit factor backend %q reading %q", b, rd)
	}
	if j.Relevance != r.After.Relevance || j.Priority != r.After.Priority {
		t.Fatalf("show %v/%v disagrees with the reply %v/%v", j.Relevance, j.Priority, r.After.Relevance, r.After.Priority)
	}
	if got := rtKeys(t); len(got) != 2 || got[0] != "gh:acme:2" {
		t.Fatalf("the list did not re-sort: %v", got)
	}
	// The list carries the reading too, and the triage map still shows the run's.
	for _, lj := range listJSON(t).Jobs {
		if lj.Key == "gh:acme:2" && (lj.ManualTriage == nil || lj.Triage["role_fit"] != "adjacent") {
			t.Fatalf("list row %+v / triage %v", lj.ManualTriage, lj.Triage)
		}
		if lj.Key == "gh:acme:1" && lj.ManualTriage != nil {
			t.Fatalf("a reading appeared on a job nobody asked about")
		}
	}
}

func mustLoad(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The contract the Mac app decodes: exactly these keys.
func TestJobsRetriageJSONContract(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 100, OutputTokens: 20}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	keys := func(raw string) []string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sortStrings(out)
		return out
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s keys %v, want %v", what, got, want)
		}
	}
	eq("dry run", keys(rtRun(t, 0, "--dry-run", "gh:acme:1")), "dry_run", "estimate", "key", "model", "status")
	raw := rtRun(t, 0, "gh:acme:1")
	eq("success", keys(raw), "after", "before", "cost", "key", "model", "status")
	var top map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &top)
	eq("before", keys(string(top["before"])), "priority", "relevance", "slots")
	eq("after", keys(string(top["after"])), "at", "model", "priority", "relevance", "slots")
	eq("cost", keys(string(top["cost"])), "input_tokens", "output_tokens", "usd")
	eq("dry run with a reading", keys(rtRun(t, 0, "--dry-run", "gh:acme:1")), "dry_run", "estimate", "key", "manual_triage", "model", "status")
	eq("clear", keys(rtRun(t, 0, "--clear", "gh:acme:1")), "cleared", "had_manual", "key", "priority", "relevance")
	p.err = errors.New("connection refused")
	eq("error", keys(rtRun(t, 1, "gh:acme:1")), "code", "error")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestJobsRetriageClear(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	was := rtShow(t, "gh:acme:1")

	var c retriageClearReply
	if err := json.Unmarshal([]byte(rtRun(t, 0, "--clear", "gh:acme:1")), &c); err != nil || c.HadManual || !c.Cleared {
		t.Fatalf("clearing nothing: %+v %v", c, err)
	}
	r := rtDecode(t, rtRun(t, 0, "gh:acme:1"))
	if err := json.Unmarshal([]byte(rtRun(t, 0, "--clear", "gh:acme:1")), &c); err != nil || !c.HadManual {
		t.Fatalf("clear: %+v %v", c, err)
	}
	if c.Relevance != was.Relevance || c.Priority != was.Priority || c.Relevance == r.After.Relevance {
		t.Fatalf("clear reports %v/%v, was %v/%v, manual %v", c.Relevance, c.Priority, was.Relevance, was.Priority, r.After.Relevance)
	}
	if j := rtShow(t, "gh:acme:1"); j.ManualTriage != nil || j.Relevance != was.Relevance {
		t.Fatalf("after clear: %+v relevance %v", j.ManualTriage, j.Relevance)
	}
	// A clear never calls the model.
	if p.calls() != 1 {
		t.Fatalf("%d provider calls", p.calls())
	}
}

// Rule 7: a manual reading orders and never drops or undrops. A dropped posting
// stays out of the views and in its status; a mismatch reading lowers the score
// of a kept one and leaves it listed.
func TestJobsRetriageNeverDropsOrUndrops(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	l := rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Kept Engineer", fit: "strong_fit", rank: "0.8", p: "0.95"},
		rtJob{key: "gh:acme:2", title: "Dropped Engineer", fit: "mismatch_domain", status: posting.Dropped, p: "0.99"},
	)
	r := rtDecode(t, rtRun(t, 0, "gh:acme:2"))
	if r.Status != posting.Dropped || r.After.Slots["role_fit"].Value != "strong_fit" {
		t.Fatalf("reply %+v", r)
	}
	if got := rtKeys(t, "--view", "all"); len(got) != 1 || got[0] != "gh:acme:1" {
		t.Fatalf("a dropped posting came back: %v", got)
	}
	if rec, _ := l.Get("gh:acme:2"); rec.Status != posting.Dropped {
		t.Fatalf("status changed to %s", rec.Status)
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range snap.Records {
		if rec.Key == "gh:acme:2" && (rec.Status != posting.Dropped || rec.Annotations["triage.role_fit"] != "mismatch_domain") {
			t.Fatalf("the ledger changed: %s %v", rec.Status, rec.Annotations)
		}
	}

	// A manual mismatch on a kept posting: still listed, ranked lower, status kept.
	before := rtShow(t, "gh:acme:1")
	p.text = `{"role_fit":"mismatch_domain","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"}`
	rtDecode(t, rtRun(t, 0, "gh:acme:1"))
	after := rtShow(t, "gh:acme:1")
	if !(after.Priority < before.Priority) || after.Status != before.Status {
		t.Fatalf("priority %v -> %v, status %s -> %s", before.Priority, after.Priority, before.Status, after.Status)
	}
	if got := rtKeys(t); len(got) != 1 || got[0] != "gh:acme:1" {
		t.Fatalf("the mismatch reading dropped the posting: %v", got)
	}
}

// A reading made for other text is shown as stale and ranks nothing; making a new
// one replaces it.
func TestJobsRetriageStaleReadingIsReportedAndIgnored(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	l := rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	was := rtShow(t, "gh:acme:1")
	rtRun(t, 0, "gh:acme:1")
	if j := rtShow(t, "gh:acme:1"); j.Relevance == was.Relevance {
		t.Fatal("the reading changed nothing")
	}

	rtAppend(t, l, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9", desc: rtDesc + " The role now also owns on-call."})
	j := rtShow(t, "gh:acme:1")
	if j.ManualTriage == nil || j.ManualTriage.Current || j.ManualTriage.StaleReason != string(manualtriage.TextChanged) {
		t.Fatalf("manual_triage = %+v", j.ManualTriage)
	}
	if b, _ := rtFit(j); b == "manual" {
		t.Fatal("a stale reading still ranks")
	}
	if j.Relevance != was.Relevance {
		t.Fatalf("relevance %v with the stale reading ignored, want the run's %v", j.Relevance, was.Relevance)
	}
	for _, lj := range listJSON(t).Jobs {
		if lj.ManualTriage == nil || lj.ManualTriage.Current {
			t.Fatalf("list row %+v", lj.ManualTriage)
		}
	}
	// A new reading replaces the stale one and is current.
	r := rtDecode(t, rtRun(t, 0, "gh:acme:1"))
	if r.Before.Slots["role_fit"].Backend == "manual" {
		t.Fatalf("before names the stale reading: %+v", r.Before.Slots["role_fit"])
	}
	if j := rtShow(t, "gh:acme:1"); j.ManualTriage == nil || !j.ManualTriage.Current {
		t.Fatalf("after re-reading: %+v", j.ManualTriage)
	}
}

// A reading the store cannot be trusted with is not applied, said so on the
// list, and no model is paid for a reading that could not be kept.
func TestJobsRetriageCorruptStoreIsWarnedAndNotPaidFor(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	if err := os.MkdirAll(manualtriage.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rtStoreFile(state), []byte("{broken}\n{\"v\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := listJSON(t)
	if len(l.Jobs) != 1 || !strings.Contains(l.Insight.Warning, "manual Sonnet readings not used") {
		t.Fatalf("warning %q, jobs %d", l.Insight.Warning, len(l.Jobs))
	}
	if e := rtFail(t, "gh:acme:1"); e.Code != "store" || e.Cost != nil {
		t.Fatalf("error %+v", e)
	}
	if p.calls() != 0 {
		t.Fatalf("a model was paid for a reading that could not be kept (%d calls)", p.calls())
	}
}

// Every failure is one honest code, stores nothing, and a call that was paid
// for carries its cost.
func TestJobsRetriageErrors(t *testing.T) {
	paid := llm.Usage{InputTokens: 900, OutputTokens: 40}
	type tc struct {
		name   string
		code   string
		key    string
		jobs   []rtJob
		llm    *retriageLLM
		cfg    func(*config.Config)
		seam   func(context.Context, *wired) (llm.Provider, error)
		before func(t *testing.T, state string)
		cost   bool
		words  string
	}
	ok := []rtJob{{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"}}
	cases := []tc{
		{name: "unknown key", code: "unknown_key", key: "gh:acme:404", jobs: ok, words: "no posting"},
		{name: "malformed key", code: "bad_request", key: "nocolon", jobs: ok},
		{name: "untriaged", code: "untriaged", key: "gh:acme:1", jobs: []rtJob{{key: "gh:acme:1", title: "T", status: posting.Live}}, words: "no run has triaged"},
		{name: "no text", code: "text_gone", key: "gh:acme:1", jobs: []rtJob{{key: "gh:acme:1", title: "T", fit: "adjacent", desc: " "}}},
		{name: "no llm", code: "no_llm", key: "gh:acme:1", jobs: ok, cfg: func(c *config.Config) { c.LLM = config.LLM{} }},
		{name: "no key", code: "no_key", key: "gh:acme:1", jobs: ok,
			seam: func(context.Context, *wired) (llm.Provider, error) {
				return nil, &credentials.ErrMissing{Provider: "anthropic", EnvVars: []string{"ANTHROPIC_API_KEY"}}
			}},
		{name: "cap spent at the provider", code: "cap_reached", key: "gh:acme:1", jobs: ok,
			llm: &retriageLLM{err: fmt.Errorf("budget: reserve: %w", budget.ErrExhausted)}},
		{name: "cap spent building", code: "cap_reached", key: "gh:acme:1", jobs: ok,
			seam: func(context.Context, *wired) (llm.Provider, error) { return nil, fmt.Errorf("llm budget: %w", budget.ErrExhausted) }},
		{name: "provider down", code: "provider", key: "gh:acme:1", jobs: ok, llm: &retriageLLM{err: errors.New("dial tcp: connection refused")}},
		{name: "refused", code: "refused", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: "", stop: llm.StopRefusal, usage: paid}},
		{name: "unsure", code: "unsure", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: `{"role_fit":"none","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"}`, usage: paid}},
		{name: "prose", code: "unreadable", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: "I cannot help with that.", usage: paid}},
		{name: "unlisted option", code: "unreadable", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: `{"role_fit":"perfect","seniority":"mid","sponsorship":"not_stated","work_mode":"remote"}`, usage: paid}},
		{name: "missing slot", code: "unreadable", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: `{"role_fit":"strong_fit","seniority":"mid"}`, usage: paid}},
		{name: "busy", code: "busy", key: "gh:acme:1", jobs: ok,
			before: func(t *testing.T, state string) {
				unlock, err := manualtriage.TryAskLock(manualtriage.Dir(state))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(unlock)
			}},
		{name: "store unreadable is found before asking", code: "store", key: "gh:acme:1", jobs: ok,
			llm: &retriageLLM{text: rtAnswer, usage: paid},
			before: func(t *testing.T, state string) {
				if err := os.MkdirAll(rtStoreFile(state), 0o700); err != nil { // a directory where the file goes
					t.Fatal(err)
				}
			}},
		{name: "store cannot be written after asking", code: "store", key: "gh:acme:1", jobs: ok, cost: true,
			llm: &retriageLLM{text: rtAnswer, usage: paid},
			before: func(t *testing.T, state string) {
				dir := manualtriage.Dir(state)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"ask.lock", "lock"} {
					if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// Readable and lockable, but no file can be created in it.
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.llm
			if p == nil {
				p = &retriageLLM{}
			}
			_, state := rtSetup(t, p, c.cfg)
			if c.seam != nil {
				retriageProvider = c.seam
			}
			rtSeed(t, state, c.jobs...)
			if c.before != nil {
				c.before(t, state)
			}
			e := rtFail(t, c.key)
			if e.Code != c.code {
				t.Fatalf("code %q (%s), want %q", e.Code, e.Error, c.code)
			}
			if c.words != "" && !strings.Contains(e.Error, c.words) {
				t.Errorf("error %q lacks %q", e.Error, c.words)
			}
			if c.cost != (e.Cost != nil) {
				t.Fatalf("cost %+v, want one: %v", e.Cost, c.cost)
			}
			if e.Cost != nil && (e.Cost.InputTokens != 900 || e.Cost.USD <= 0) {
				t.Fatalf("cost %+v", e.Cost)
			}
			if c.code != "store" {
				rtNoStore(t, state)
			}
			if strings.Contains(e.Error, "\n") || len(e.Error) > 500 {
				t.Errorf("error is not one short line: %q", e.Error)
			}
		})
	}
}

// A call that outlives its bound reads as a provider that did not answer.
func TestJobsRetriageTimeoutIsAProviderFailure(t *testing.T) {
	p := &retriageLLM{onStream: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	old := retriageAskTimeout
	retriageAskTimeout = 60 * time.Millisecond
	t.Cleanup(func() { retriageAskTimeout = old })
	e := rtFail(t, "gh:acme:1")
	if e.Code != "provider" || !strings.Contains(e.Error, "in time") {
		t.Fatalf("%+v", e)
	}
	rtNoStore(t, state)
}

// Stop (SIGTERM, which signalContext turns into a cancelled context) is a
// cancellation, not a fault, and stores nothing.
func TestJobsRetriageCancelledStoresNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	old := signalContext
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { signalContext = old })
	entered := make(chan struct{})
	p := &retriageLLM{usage: llm.Usage{InputTokens: 10}, onStream: func(c context.Context) error {
		close(entered)
		cancel()
		<-c.Done()
		return c.Err()
	}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	e := rtFail(t, "gh:acme:1")
	<-entered
	if e.Code != "cancelled" {
		t.Fatalf("%+v", e)
	}
	rtNoStore(t, state)
	// The ask lock is released: the next reading is not "busy".
	p.onStream = nil
	p.text = rtAnswer
	signalContext = old
	if r := rtDecode(t, rtRun(t, 0, "gh:acme:1")); r.After == nil {
		t.Fatalf("after a cancel: %+v", r)
	}
}

// Two readings of one posting at once pay once: the second is told to wait.
func TestJobsRetriageSecondConcurrentAskIsRefused(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := &retriageLLM{text: rtAnswer, onStream: func(context.Context) error {
		close(started)
		<-release
		return nil
	}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	done := make(chan string, 1)
	go func() {
		var o, e strings.Builder
		code := run([]string{"jobs", "retriage", "--json", "gh:acme:1"}, &o, &e)
		done <- fmt.Sprintf("%d %s", code, o.String())
	}()
	<-started
	if e := rtFail(t, "gh:acme:1"); e.Code != "busy" {
		t.Fatalf("second ask: %+v", e)
	}
	close(release)
	if out := <-done; !strings.HasPrefix(out, "0 ") {
		t.Fatalf("first ask: %s", out)
	}
	if p.calls() != 1 {
		t.Fatalf("%d provider calls, want 1", p.calls())
	}
}

func TestJobsRetriageUsageAndText(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 100, OutputTokens: 20}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	for _, args := range [][]string{
		{"jobs", "retriage"},
		{"jobs", "retriage", "--json", "a", "b"},
		{"jobs", "retriage", "--dry-run", "--clear", "gh:acme:1"},
		{"jobs", "retriage", "--nope", "gh:acme:1"},
	} {
		if code, _, stderr := do(t, args...); code != 2 || stderr == "" {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	text, _ := mustCode(t, 0, "jobs", "retriage", "--dry-run", "gh:acme:1")
	if !strings.Contains(text, "would ask "+rtSonnet) || strings.Contains(text, "{") {
		t.Fatalf("dry-run text %q", text)
	}
	text, _ = mustCode(t, 0, "jobs", "retriage", "gh:acme:1")
	for _, s := range []string{rtSonnet + " read it", "role_fit", "adjacent -> strong_fit", "never drops a posting"} {
		if !strings.Contains(text, s) {
			t.Errorf("text %q lacks %q", text, s)
		}
	}
	// In text mode a failure is an ordinary error on stderr, exit 1.
	p.err = errors.New("connection refused")
	if code, _, stderr := do(t, "jobs", "retriage", "gh:acme:1"); code != 1 || !strings.Contains(stderr, "provider") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// The command is not the older `vani retriage`, which asks no model.
func TestJobsRetriageIsNotTheRetriageCommand(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	rtSetup(t, p, nil)
	mustCode(t, 0, "retriage", "--dry-run")
	if p.calls() != 0 {
		t.Fatal("`vani retriage` asked a model")
	}
	if code, _, stderr := do(t, "jobs"); code != 2 || !strings.Contains(stderr, "retriage") {
		t.Fatalf("jobs usage: exit %d %q", code, stderr)
	}
}

// The digest ranks on the same manual readings the Jobs list does: one lookup,
// one rule for what is current.
func TestDigestOptionsCarryTheManualReadings(t *testing.T) {
	_, state := rtSetup(t, &retriageLLM{}, nil)
	p := rtPosting("gh:acme:1", "Inference Engineer")
	rec := ledger.Record{Key: p.Key, Status: posting.Scored, Posting: p}
	if opts, _ := digestOptions(mustLoad(t), state); opts.Manual != nil {
		t.Fatal("a lookup with no readings")
	}
	err := manualtriage.Set(manualtriage.Dir(state), manualtriage.Reading{
		Key: p.Key, Slots: map[string]string{"role_fit": "strong_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"},
		Model: rtSonnet, At: jobsT0, Text: manualtriage.TextSHA256(p), Prompt: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	opts, warn := digestOptions(mustLoad(t), state)
	if opts.Manual == nil || strings.Contains(warn, "manual") {
		t.Fatalf("lookup %v, warning %q", opts.Manual != nil, warn)
	}
	if v, ok := opts.Manual(rec, "role_fit"); !ok || v != "strong_fit" {
		t.Fatalf("current reading: %q %v", v, ok)
	}
	rec.Posting.Description = "The role changed."
	if _, ok := opts.Manual(rec, "role_fit"); ok {
		t.Fatal("a stale reading ranked the digest")
	}
	if err := os.WriteFile(rtStoreFile(state), []byte("{broken}\n{\"v\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, warn = digestOptions(mustLoad(t), state)
	if opts.Manual != nil || !strings.Contains(warn, "manual Sonnet readings not used") {
		t.Fatalf("a corrupt store: lookup %v, warning %q", opts.Manual != nil, warn)
	}
}
