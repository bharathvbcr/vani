package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// advanceThrough runs one posting through a real Runner over router and returns
// its ledger record.
func advanceThrough(t *testing.T, router decide.Decider, key string) ledger.Record {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ctx := context.Background()
	p := posting.Posting{Key: posting.Key(key), Company: "Acme", Title: "ML Engineer", Description: "Build and ship ML systems. Fully remote."}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: time.Now(), Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
		t.Fatal(err)
	}
	r := &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: router}, SkipScoring: true, SkipExplain: true}
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	rec, ok := l.Get(p.Key)
	if !ok {
		t.Fatal("posting missing")
	}
	return rec
}

// partialPromotionRouter is the wire.go chain for one promoted slot with a
// Lappi shadow: Lappi(only promoted), d1-ultra(except promoted), llm; shadow is
// a second Lappi (except promoted).
func partialPromotionRouter(promoted []string) (*decide.Router, *fakeBackend, *fakeBackend, *fakeBackend, *fakeBackend) {
	chainL := &fakeBackend{name: "lappi"}
	shadowL := &fakeBackend{name: "lappi"}
	or := &fakeBackend{name: "d1-ultra"}
	llm := &fakeBackend{name: "llm"}
	rt := decide.NewRouter(onlySlots(chainL, promoted), exceptSlots(or, promoted), llm)
	rt.Shadow = exceptSlots(shadowL, promoted)
	return rt, chainL, shadowL, or, llm
}

// Attack 1. Re-ask counts under a config switch. Characterisation: it records
// how many of N already-triaged postings are asked again, so the cost of each
// switch is a number, and pins that equal routing keeps one id.
func TestAttackCacheIdentityReaskCounts(t *testing.T) {
	base := config.Default()
	base.TargetSummary = "ML systems"
	all := make([]string, len(jdtriage.Families))
	for i, f := range jdtriage.Families {
		all[i] = f.Slot.Name
	}
	a, b := all[0], all[1]
	mk := func(l *config.Lappi) string {
		c := base
		c.Decide.Lappi = l
		return triageModelID(c)
	}
	ids := map[string]string{
		"shadow":          mk(&config.Lappi{}),
		"primary":         mk(&config.Lappi{Primary: true}),
		"slots(a)":        mk(&config.Lappi{PrimarySlots: []string{a}}),
		"slots(b,a)":      mk(&config.Lappi{PrimarySlots: []string{b, a}}),
		"slots(a,b)":      mk(&config.Lappi{PrimarySlots: []string{a, b}}),
		"slots(all four)": mk(&config.Lappi{PrimarySlots: all}),
	}
	if ids["slots(b,a)"] != ids["slots(a,b)"] {
		t.Error("slot order changes the id")
	}

	const n = 20
	reask := func(from, to string) int {
		l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		ctx := context.Background()
		var asked atomic.Int64
		d := decideFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
			asked.Add(1)
			out := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{}}
			for _, s := range q.Slots {
				out.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0], Jev: &decide.JevCalib{Probability: 0.99}}
			}
			return out, nil
		})
		run := func(id string) {
			r := &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: d}, SkipScoring: true, SkipExplain: true,
				Funnel: pipeline.Funnel{Cache: pipeline.NewReadCache(), ModelID: id}}
			for i := 0; i < n; i++ {
				key := posting.Key("gh:cache:" + string(rune('a'+i)))
				p := posting.Posting{Key: key, Company: "C", Title: "T" + string(rune('a'+i)), Description: "distinct description " + string(rune('a'+i)) + " body text"}
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: time.Now(), Key: key, Status: posting.Live, Posting: &p}); err != nil {
					t.Fatal(err)
				}
				if err := r.Advance(ctx, key); err != nil {
					t.Fatalf("advance %s: %v", key, err)
				}
			}
		}
		run(from)
		first := asked.Load()
		if first != n {
			t.Fatalf("first pass asked %d, want %d", first, n)
		}
		asked.Store(0)
		// Fresh copies of the same postings (same hash/title/location) under the new id.
		for i := 0; i < n; i++ {
			key := posting.Key("gh:cache:dup:" + string(rune('a'+i)))
			p := posting.Posting{Key: key, Company: "C", Title: "T" + string(rune('a'+i)), Description: "distinct description " + string(rune('a'+i)) + " body text"}
			if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: time.Now(), Key: key, Status: posting.Live, Posting: &p}); err != nil {
				t.Fatal(err)
			}
		}
		r := &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: d}, SkipScoring: true, SkipExplain: true,
			Funnel: pipeline.Funnel{Cache: pipeline.NewReadCache(), ModelID: to}}
		for i := 0; i < n; i++ {
			if err := r.Advance(ctx, posting.Key("gh:cache:dup:"+string(rune('a'+i)))); err != nil {
				t.Fatal(err)
			}
		}
		return int(asked.Load())
	}
	for _, tc := range []struct {
		from, to string
		want     int
	}{
		{"slots(a,b)", "slots(b,a)", 0},   // same routing: inherited
		{"shadow", "shadow", 0},           // control: same id inherits
		{"shadow", "slots(a)", n},         // routing changed: all re-asked
		{"primary", "slots(all four)", n}, // see report: all-four is not primary:true
		{"slots(a)", "slots(a,b)", n},     // growing the set re-asks everything, not just b
	} {
		got := reask(ids[tc.from], ids[tc.to])
		t.Logf("re-asked %d of %d postings: %s -> %s", got, n, tc.from, tc.to)
		if got != tc.want {
			t.Errorf("%s -> %s: re-asked %d, want %d", tc.from, tc.to, got, tc.want)
		}
	}
}

// pricedLLM is a metered fake LLM: every call costs usd against a daily cap, and
// a call past the cap fails like budget.Provider's refusal.
type pricedLLM struct {
	usd, cap float64
	mu       sync.Mutex
	spent    float64
	calls    int
	refused  int
	slotsAsk map[string]int
}

func (p *pricedLLM) Name() string       { return "llm" }
func (p *pricedLLM) Serves(string) bool { return true }
func (p *pricedLLM) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spent+p.usd > p.cap {
		p.refused++
		return decide.Answer{}, errBudget
	}
	p.spent += p.usd
	p.calls++
	out := decide.Answer{Backend: "llm", CostUSD: p.usd, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		p.slotsAsk[s.Name]++
		out.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return out, nil
}

var errBudget = &budgetErr{}

type budgetErr struct{}

func (*budgetErr) Error() string { return "daily budget exhausted" }

// Attack 2. A Lappi outage moves every promoted-slot question to the LLM tier.
// The spend is bounded by the LLM daily cap and nothing else; this test pins
// the path and reports the numbers.
func TestAttackLappiOutageSpendPathIsBoundedByLLMCap(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	const postings = 200
	run := func(lappiDown bool, perPromotion bool) (*pricedLLM, *fakeBackend, int) {
		l := &fakeBackend{name: "lappi", down: lappiDown}
		or := &fakeBackend{name: "d1-ultra"}
		llm := &pricedLLM{usd: 0.002, cap: 0.10, slotsAsk: map[string]int{}}
		var rt *decide.Router
		if perPromotion {
			rt = decide.NewRouter(onlySlots(l, promoted), exceptSlots(or, promoted), llm)
		} else {
			rt = decide.NewRouter(l, or, llm) // primary:true
		}
		unread := 0
		for i := 0; i < postings; i++ {
			ra, err := rt.Route(context.Background(), triageQ())
			if err != nil {
				t.Fatal(err)
			}
			if ra.Slots[promoted[0]].Reading != decide.Answered {
				unread++
			}
		}
		return llm, or, unread
	}
	healthy, _, _ := run(false, true)
	if healthy.calls != 0 {
		t.Errorf("healthy Lappi: llm called %d times", healthy.calls)
	}
	llm, or, unread := run(true, true)
	t.Logf("per-slot, Lappi down: %d postings -> llm calls %d ($%.3f spent, cap $%.2f, %d refused), promoted slot unread on %d postings; d1-ultra asked %d slot-reads",
		postings, llm.calls, llm.spent, llm.cap, llm.refused, unread, len(or.asked))
	if llm.spent > llm.cap+1e-9 {
		t.Errorf("spend $%.4f exceeds cap $%.2f", llm.spent, llm.cap)
	}
	if llm.calls == 0 {
		t.Error("expected the outage to reach the llm tier (the documented path)")
	}
	// Control: primary:true with Lappi down never reaches the LLM (d1-ultra takes it).
	ctl, _, _ := run(true, false)
	t.Logf("primary:true, Lappi down: llm calls %d", ctl.calls)
	if ctl.calls != 0 {
		t.Errorf("primary:true control reached the llm %d times", ctl.calls)
	}
}

// Attack 5. Two Lappi clients in one chain+shadow, hammered in parallel under
// -race through a real Runner-free router: no shared mutable state between the
// slotFilters, and the shadow never reads a promoted slot.
func TestAttackParallelChainAndShadowRace(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	chainL := &syncBackend{name: "lappi"}
	shadowL := &syncBackend{name: "lappi"}
	or := &syncBackend{name: "d1-ultra"}
	rt := decide.NewRouter(onlySlots(chainL, promoted), exceptSlots(or, promoted))
	rt.Shadow = exceptSlots(shadowL, promoted)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := rt.Route(context.Background(), triageQ()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := shadowL.count(promoted[0]); n != 0 {
		t.Errorf("shadow asked the promoted slot %d times", n)
	}
	if n := chainL.count(promoted[0]); n != 16*50 {
		t.Errorf("chain lappi asked promoted slot %d times, want %d", n, 16*50)
	}
	for _, f := range jdtriage.Families[1:] {
		if chainL.count(f.Slot.Name) != 0 || or.count(promoted[0]) != 0 {
			t.Errorf("leak: chain lappi asked %s or d1-ultra asked the promoted slot", f.Slot.Name)
		}
	}
}

type syncBackend struct {
	name string
	mu   sync.Mutex
	n    map[string]int
}

func (s *syncBackend) Name() string       { return s.name }
func (s *syncBackend) Serves(string) bool { return true }
func (s *syncBackend) count(slot string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[slot]
}
func (s *syncBackend) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[string]int{}
	}
	out := decide.Answer{Backend: s.name, Slots: map[string]decide.SlotReading{}}
	for _, sl := range q.Slots {
		s.n[sl.Name]++
		out.Slots[sl.Name] = decide.SlotReading{Reading: decide.Answered, Value: sl.Options[0]}
	}
	return out, nil
}

// Attack 5 (wiring). With a Lappi shadow and partial promotion the wiring
// builds two Lappi clients; lappi.Backend holds no process and no connection
// between asks (one Unix-socket exchange per ask), so nothing needs closing.
// Both report under the name "lappi" (one backend meter row for two clients).
func TestAttackWiredLappiShadowAndChainBothNamedLappi(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	promoted := []string{jdtriage.Families[0].Slot.Name}
	cfg.Decide.Lappi = &config.Lappi{Socket: "/tmp/vani-test-no-agent.sock", PrimarySlots: promoted}
	cfg.Decide.Shadow = "lappi"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &strings.Builder{}}
	r, err := w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if rt.Shadow == nil {
		t.Fatal("no shadow")
	}
	if _, err := rt.Route(context.Background(), triageQ()); err != nil {
		t.Fatal(err)
	}
	// Both clients report under the name "lappi" into the one backend meter.
	names := map[string]bool{}
	for _, n := range rt.Backends() {
		names[n] = true
	}
	names[rt.Shadow.Name()] = true
	if !names["lappi"] {
		t.Fatalf("names %v", names)
	}
}
