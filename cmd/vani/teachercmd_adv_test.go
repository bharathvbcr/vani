package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/adaptertest"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/prompt"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

type advPost struct{ key, backend, desc string }

// advSetup writes a config and a ledger of d1-ultra postings with the given
// descriptions. mut edits the config first.
func advSetup(t *testing.T, mut func(*config.Config), posts []advPost) string {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "early-career ML systems roles"
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", TriageModel: tcHaiku, InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
	cfg.Decide.DailyUSD.LLM = 5
	if mut != nil {
		mut(&cfg)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, s := range posts {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: "SynthCo", Title: "Engineer " + s.key, Description: s.desc}
		ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": s.backend}
		e := ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: posting.Explained, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func advN(n int, desc func(i int) string) []advPost {
	var ps []advPost
	for i := range n {
		ps = append(ps, advPost{fmt.Sprintf("gh:a:%03d", i), "d1-ultra", desc(i)})
	}
	return ps
}

func advProse(i int) string {
	return strings.Repeat(fmt.Sprintf("Posting %d: you will build and operate services that serve language models. ", i), 260)
}

func advToday() string { return time.Now().UTC().Format("2006-01-02") }

// A budget below one question's worst case must refuse to start: nothing sent.
func TestAdvBudgetBelowOneQuestionSendsNothing(t *testing.T) {
	tcSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "0.000001")
	if len(f.sent) != 0 || !strings.Contains(o, "asked 0") {
		t.Fatalf("sent %d:\n%s", len(f.sent), o)
	}
}

// The real budget ledger reader: a corrupt, unreadable or unlockable ledger
// must stop the run before any question is sent.
func TestAdvUnreadableSpendLedgerRefusesBeforeSending(t *testing.T) {
	for _, tc := range []string{"corrupt", "unreadable", "schema", "no_budget_dir"} {
		t.Run(tc, func(t *testing.T) {
			state := advSetup(t, nil, advN(3, advProse))
			f := &tcFake{}
			oldD := teacherDecider
			t.Cleanup(func() { teacherDecider = oldD })
			teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
			path := filepath.Join(state, "budget", "llm-"+advToday()+".json")
			switch tc {
			case "corrupt":
				_ = os.MkdirAll(filepath.Dir(path), 0o700)
				_ = os.WriteFile(path, []byte("{not json"), 0o600)
			case "schema":
				_ = os.MkdirAll(filepath.Dir(path), 0o700)
				_ = os.WriteFile(path, []byte(`{"schema_version":99}`), 0o600)
			case "unreadable":
				_ = os.MkdirAll(filepath.Dir(path), 0o700)
				_ = os.WriteFile(path, []byte(`{}`), 0o000)
				if os.Geteuid() == 0 {
					t.Skip("root reads anything")
				}
			}
			code, o, stderr := do(t, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
			if len(f.sent) != 0 {
				t.Errorf("%d questions were sent with an unreadable spend ledger (exit %d)\n%s%s", len(f.sent), code, o, stderr)
			}
			if code == 0 {
				t.Errorf("exit 0 with an unreadable spend ledger\n%s%s", o, stderr)
			}
		})
	}
}

// The spend read failing after the run began (a ledger corrupted mid-run) must
// stop further questions.
func TestAdvSpendReadFailingMidRunStopsQuestions(t *testing.T) {
	advSetup(t, nil, advN(8, advProse))
	f := &tcFake{}
	oldD, oldS := teacherDecider, teacherSpent
	t.Cleanup(func() { teacherDecider, teacherSpent = oldD, oldS })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return f, nil }
	var calls atomic.Int64
	teacherSpent = func(context.Context, string) (int64, error) {
		if calls.Add(1) > 2 { // cmd's headroom read, Run's base read, then every admit fails
			return 0, fmt.Errorf("ledger vanished")
		}
		return 0, nil
	}
	_, _, _ = do(t, "teacher", "--of", "d1-ultra", "--budget-usd", "1", "--concurrency", "1")
	if len(f.sent) != 0 {
		t.Errorf("%d questions sent after the spend could no longer be read", len(f.sent))
	}
}

// Zero or missing configured prices refuse (the provider would refuse too, but
// the cost report and gate must not price at 0).
func TestAdvPricesRefuseZeroRates(t *testing.T) {
	advSetup(t, func(c *config.Config) { c.LLM.TriageModel = "some-unlisted-model" }, advN(2, advProse))
	tcNoModel(t)
	// config.Save refuses zero rates, so zero them in the file as a hand edit would.
	raw, err := os.ReadFile(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.NewReplacer(`"input_nano_usd_per_token": 3000`, `"input_nano_usd_per_token": 0`, `"output_nano_usd_per_token": 15000`, `"output_nano_usd_per_token": 0`).Replace(string(raw))
	if edited == string(raw) {
		t.Fatalf("could not zero the rates in %s", raw)
	}
	if err := os.WriteFile(os.Getenv("VANI_CONFIG"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	code, o, stderr := do(t, "teacher", "--dry-run", "--of", "d1-ultra")
	if code == 0 {
		t.Errorf("dry run priced an unlisted model with zero configured rates (exit 0): %s%s", o, stderr)
	}
}

// A model the rate table does not list: report what it is priced at.
func TestAdvPricesUnlistedModelFallsBackToConfiguredRatesNotZero(t *testing.T) {
	advSetup(t, func(c *config.Config) { c.LLM.TriageModel = "some-unlisted-model" }, advN(2, advProse))
	tcNoModel(t)
	o, _ := mustCode(t, 0, "teacher", "--dry-run", "--of", "d1-ultra")
	if !strings.Contains(o, "the configured llm prices") || strings.Contains(o, "$0.0000 and output $0.0000") {
		t.Errorf("unlisted model pricing line:\n%s", o)
	}
}

// Haiku 5.5 tier boundary at 100,000 prompt tokens: the reservation and the
// charge must agree on which side of the boundary a prompt is on.
func TestAdvTierBoundaryReserveCoversCharge(t *testing.T) {
	p, ok := anthropic.PriceOf("claude-haiku-5-5")
	if !ok || p.LongPromptAbove != 100_000 {
		t.Skip("table entry changed")
	}
	for _, n := range []int{99_999, 100_000, 100_001} {
		w, err := p.Reserve(n, llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom)
		if err != nil {
			t.Fatal(err)
		}
		c, err := p.Charge(llm.Usage{InputTokens: n, OutputTokens: llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom})
		if err != nil {
			t.Fatal(err)
		}
		if c > w {
			t.Errorf("prompt %d: charge %d exceeds reservation %d", n, c, w)
		}
	}
	// The padded estimate is what the gate reserves with: an estimate of 80,000
	// pads to 100,000 (short tier) while a true 100,001-token prompt is billed
	// at the long tier. The context cap (24 KiB) keeps real prompts far below
	// this, so it is not reachable through jdtriage.
	if jdtriage.MaxContextBytes > 100_000 {
		t.Errorf("context cap %d bytes could reach the long-prompt tier", jdtriage.MaxContextBytes)
	}
}

// Hand-written readings log with a torn last line, then a resume.
func TestAdvTornTailThenResumeNoDuplicateRows(t *testing.T) {
	state := advSetup(t, nil, advN(4, advProse))
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1", "--max", "2")
	path := filepath.Join(state, eval.TeacherFile)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.WriteString(`{"v":1,"key":"gh:a:002","text_sha256":"ab`) // torn, no newline
	fh.Close()
	f.meter.Store(0)
	o, stderr := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if !strings.Contains(stderr, "unreadable lines skipped") {
		t.Errorf("no torn-tail warning: %s", stderr)
	}
	st, err := eval.OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Len() != 4 || len(f.sent) != 4 {
		t.Errorf("store %d rows, %d questions sent in all (want 4 and 4):\n%s", st.Len(), len(f.sent), o)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), `"key":"gh:a:000"`); n != 1 {
		t.Errorf("gh:a:000 stored %d times", n)
	}
}

// Changing the target summary changes prompt_hash: every posting is asked
// again. This is the cost the owner must know about; the test pins the
// behaviour (and that the dry run says how many it would re-ask).
func TestAdvChangedSummaryReasksEverything(t *testing.T) {
	state := advSetup(t, nil, advN(4, advProse))
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if len(f.sent) != 4 {
		t.Fatalf("first run sent %d", len(f.sent))
	}
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSummary += " and agents"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	// whitespace-only edits must NOT re-ask
	o, _ := mustCode(t, 0, "teacher", "--dry-run", "--of", "d1-ultra")
	t.Logf("after a summary edit the dry run reports:\n%s", o)
	if !strings.Contains(o, "4 to ask") {
		t.Errorf("a changed summary did not reopen the postings:\n%s", o)
	}
	cfg.TargetSummary = "early-career   ML systems roles\n"
	_ = config.Save(cfgPath, cfg)
	o, _ = mustCode(t, 0, "teacher", "--dry-run", "--of", "d1-ultra")
	if !strings.Contains(o, "0 to ask") {
		t.Errorf("a whitespace-only summary edit re-asks (cost):\n%s", o)
	}
	_ = state
}

// What a posting sends equals what normal triage sends minus the extra slot;
// nothing of the vault, and the target summary is the same text.
func TestAdvSentTextIsTheNormalTriageContext(t *testing.T) {
	advSetup(t, nil, advN(1, advProse))
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "1")
	if len(f.sent) != 1 {
		t.Fatalf("sent %d", len(f.sent))
	}
	p := posting.Posting{Key: "gh:a:000", ATS: "gh", Company: "SynthCo", Title: "Engineer gh:a:000", Description: advProse(0)}
	var want decide.Question
	_, _ = jdtriage.Triager{TargetSummary: "early-career ML systems roles", D: deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		want = q
		return decide.Answer{}, nil
	})}.Triage(context.Background(), p)
	if f.sent[0] != want.Prompt+"\n"+string(want.Context) {
		t.Errorf("teacher context differs from the normal triage context")
	}
	if strings.Contains(string(want.Context), "SynthCo") {
		t.Errorf("company name enters the context")
	}
}

// ---- end to end through the real Manvi meter, against a fake Anthropic server.

type advServer struct {
	*httptest.Server
	calls    atomic.Int64
	inflight atomic.Int64
	peak     atomic.Int64
}

// advServe answers every request with usage computed from the request body:
// density input tokens per body byte, outTok output tokens, cacheWrite tokens
// billed as cache creation. noUsage omits usage entirely.
func advServe(t *testing.T, density float64, outTok, cacheWrite int, noUsage bool, hold int) *advServer {
	t.Helper()
	s := &advServer{}
	var once sync.Once
	gate := make(chan struct{})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.calls.Add(1)
		cur := s.inflight.Add(1)
		for {
			p := s.peak.Load()
			if cur <= p || s.peak.CompareAndSwap(p, cur) {
				break
			}
		}
		if hold > 0 {
			if int(cur) >= hold {
				once.Do(func() { close(gate) })
			}
			select {
			case <-gate:
			case <-time.After(400 * time.Millisecond):
			}
		}
		s.inflight.Add(-1)
		in := int(float64(len(body)) * density)
		usageStart, usageDelta := "", ""
		if !noUsage {
			usageStart = fmt.Sprintf(`,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d}`, max(in-cacheWrite, 1), cacheWrite)
			usageDelta = fmt.Sprintf(`,"usage":{"output_tokens":%d}`, outTok)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"role\":\"assistant\"%s}}\n\n", usageStart)
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"{}\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}%s}\n\n", usageDelta)
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(s.Close)
	return s
}

// advReal wires the production stack the teacher uses (pricedProvider ->
// budget.Provider on the day's ledger file -> llmbackend) to srv.
func advReal(t *testing.T, state, url string, capUSD float64) *budget.Ledger {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(state, "budget"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := budget.Open(context.Background(), filepath.Join(state, "budget", "llm-"+advToday()+".json"), int64(capUSD*1e9))
	if err != nil {
		t.Fatal(err)
	}
	p := &budget.Provider{Inner: newPricedProvider(anthropic.New(url, adaptertest.Secret("fixture-key")), "claude-test", 3000, 15000), Ledger: l}
	lb, err := llmbackend.New(p, llmbackend.Config{Model: tcHaiku})
	if err != nil {
		t.Fatal(err)
	}
	oldD := teacherDecider
	t.Cleanup(func() { teacherDecider = oldD })
	teacherDecider = func(context.Context, *wired, string) (decide.Decider, error) { return lb, nil }
	return l
}

func advDense(i int) string {
	raw := make([]byte, 15000)
	for j := range raw {
		raw[j] = byte((j*131 + i*17 + j*j) % 251)
	}
	return base64.StdEncoding.EncodeToString(raw) // ~20 KB single unbroken run
}

// Spend vs --budget-usd through the real meter, for text the estimator
// handles well (prose) and badly (dense unbroken runs), with truthful usage at
// a stated tokens-per-body-byte density. Fails when the committed spend passes
// the --budget-usd the run was given.
func TestAdvRealMeterSpendStaysWithinBudget(t *testing.T) {
	type tc struct {
		name    string
		desc    func(int) string
		density float64
		out     int
	}
	for _, c := range []tc{
		{"prose_0.25tok_per_byte", advProse, 0.25, 150},
		{"prose_full_reply", advProse, 0.25, llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom},
		{"dense_0.60tok_per_byte", advDense, 0.60, 150},
		{"dense_1.00tok_per_byte_full_reply", advDense, 1.0, llmbackend.DefaultMaxTokens + llmbackend.ReasoningHeadroom},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := advSetup(t, nil, advN(40, c.desc))
			srv := advServe(t, c.density, c.out, 0, false, 16)
			l := advReal(t, state, srv.URL, 5)
			const budgetUSD = 0.05
			o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", fmt.Sprint(budgetUSD), "--concurrency", "16")
			snap, err := l.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			spent := snap.SpentNanoUSD + snap.RetainedNanoUSD
			t.Logf("calls %d peak %d: spent %s of %s (%.2fx); overruns %d\n%s", srv.calls.Load(), srv.peak.Load(), usd(spent), usd(int64(budgetUSD*1e9)), float64(spent)/(budgetUSD*1e9), snap.Overruns, o)
			if spent > int64(budgetUSD*1e9) {
				t.Errorf("spent %d nano-USD passed --budget-usd %v by %d", spent, budgetUSD, spent-int64(budgetUSD*1e9))
			}
		})
	}
}

// A reply with no usage, and a prompt-cache write: neither may be free.
func TestAdvRealMeterNoUsageAndCacheWriteAreCharged(t *testing.T) {
	for _, c := range []struct {
		name    string
		noUsage bool
		cw      int
	}{{"no_usage", true, 0}, {"cache_write", false, 1000}} {
		t.Run(c.name, func(t *testing.T) {
			state := advSetup(t, nil, advN(6, advProse))
			srv := advServe(t, 0.25, 150, c.cw, c.noUsage, 0)
			l := advReal(t, state, srv.URL, 5)
			const budgetUSD = 0.05
			_, o, _ := do(t, "teacher", "--of", "d1-ultra", "--budget-usd", fmt.Sprint(budgetUSD), "--concurrency", "4")
			snap, err := l.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			spent := snap.SpentNanoUSD + snap.RetainedNanoUSD + snap.InFlightNanoUSD()
			t.Logf("calls %d: spent %d retained %d inflight %d, limit %d\n%s", srv.calls.Load(), snap.SpentNanoUSD, snap.RetainedNanoUSD, snap.InFlightNanoUSD(), int64(budgetUSD*1e9), o)
			if spent == 0 && srv.calls.Load() > 0 {
				t.Errorf("%d requests went out and the ledger counts $0", srv.calls.Load())
			}
			if spent > int64(budgetUSD*1e9) {
				t.Errorf("spent %d passed the budget", spent)
			}
		})
	}
}

// Quantify the estimator against body bytes for text shapes (documented).
func TestAdvEstimatorVsBytes(t *testing.T) {
	for name, s := range map[string]string{"prose": advProse(1), "dense": advDense(1), "cjk": strings.Repeat("你好世界工程师", 1200)} {
		t.Logf("%s: %d bytes, EstimateTokens %d (x1.25 = %d)", name, len(s), prompt.EstimateTokens(s), prompt.EstimateTokens(s)*125/100)
	}
}
