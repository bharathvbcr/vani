package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func floatp(v float64) *float64 { return &v }

// The ranking's threshold is the config's below_p while decide.escalate is on, and 0
// (the primary's reading) otherwise, for every caller that builds a ranking.
func TestRankEscalateBelowPFollowsTheConfig(t *testing.T) {
	cases := []struct {
		name string
		esc  *config.Escalate
		want float64
	}{
		{"absent", nil, 0},
		{"disabled", &config.Escalate{Enabled: false}, 0},
		{"disabled with below_p", &config.Escalate{Enabled: false, BelowP: floatp(0.8)}, 0},
		{"default", &config.Escalate{Enabled: true}, config.DefaultEscalateBelowP},
		{"configured", &config.Escalate{Enabled: true, BelowP: floatp(0.65)}, 0.65},
	}
	for _, c := range cases {
		cfg := config.Default()
		cfg.Decide.Escalate = c.esc
		if got := rankEscalateBelowP(cfg); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		// The digest, the warm/advance order and the Jobs views read the same value.
		if opts, _ := digestOptions(cfg, t.TempDir()); opts.EscalateBelowP != c.want {
			t.Errorf("%s: digest options %v, want %v", c.name, opts.EscalateBelowP, c.want)
		}
		if got := settingsOf(warmOrder(cfg)).escalateBelowP; got != c.want {
			t.Errorf("%s: warm order %v, want %v", c.name, got, c.want)
		}
		w := &wired{cfg: cfg, stateDir: t.TempDir()}
		if got := settingsOf([]orderOption{w.advanceOrder()}).escalateBelowP; cfg.SemanticOn() && got != c.want {
			t.Errorf("%s: advance order %v, want %v", c.name, got, c.want)
		}
	}
}

func escSeed(role, p, shadow string) map[string]string {
	a := map[string]string{
		"triage.role_fit": role, "triage.role_fit.reading": "answered", "triage.role_fit.p": p, "triage.role_fit.backend": "d1-ultra",
	}
	if shadow != "" {
		a["triage.role_fit.shadow"], a["triage.role_fit.shadow_reading"], a["triage.role_fit.shadow_backend"] = shadow, "answered", "escalate"
		if shadow != role {
			a["triage.role_fit.disagree"] = "true"
		}
	}
	return a
}

// `vani jobs` ranks a posting on the escalation tier's fit where the primary was
// unsure, says so on the fit factor, and returns to the primary's reading when
// decide.escalate is switched off. The stored triage (the badge's source) is the
// primary's either way.
func TestJobsRankOnTheEscalationReadingWhenConfigured(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := anthropicConfig(t)
	cfg.LLM.TriageModel = "claude-sonnet-5-5"
	cfg.Decide.Escalate = &config.Escalate{Enabled: true}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	seedScores(t, state,
		scoreSeed{key: "gh:a:first", company: "Acme", title: "Old", status: posting.Dead, seenAgo: 90 * day},
		scoreSeed{key: "gh:a:over", company: "Acme", title: "Inference Engineer", cov: "60", posted: day, seenAgo: day, extra: escSeed("mismatch_domain", "0.3", "strong_fit")},
		scoreSeed{key: "gh:a:sure", company: "Acme", title: "Platform Engineer", cov: "60", posted: day, seenAgo: day, extra: escSeed("mismatch_domain", "0.9", "strong_fit")},
	)

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	on := listJSON(t)
	over, sure := findJob(t, on, "gh:a:over"), findJob(t, on, "gh:a:sure")
	of := factorNamed(t, over.ScoreFactors, "fit")
	if of.Backend != "escalate" || of.Reading != "strong_fit" || !of.Known {
		t.Fatalf("over's fit factor %+v does not say it is the escalation's strong_fit", of)
	}
	if sf := factorNamed(t, sure.ScoreFactors, "fit"); sf.Backend != "" || sf.Reading != "" {
		t.Fatalf("a confident primary's fit factor names a backend: %+v", sf)
	}
	if !(over.Priority > sure.Priority) {
		t.Fatalf("priority: escalated %v, confident mismatch %v: the better reading did not lead", over.Priority, sure.Priority)
	}
	if over.Triage["role_fit"] != "mismatch_domain" {
		t.Fatalf("the stored triage reading changed: %v", over.Triage)
	}

	cfg.Decide.Escalate = &config.Escalate{Enabled: false}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	off := listJSON(t)
	o2 := findJob(t, off, "gh:a:over")
	if f := factorNamed(t, o2.ScoreFactors, "fit"); f.Backend != "" || f.Reading != "" {
		t.Fatalf("escalation off, fit factor %+v", f)
	}
	if !(o2.Priority < over.Priority) {
		t.Fatalf("escalation off should rank the doubtful mismatch lower: %v vs %v", o2.Priority, over.Priority)
	}
}

func factorNamed(t *testing.T, fs []jobinsight.Factor, name string) jobinsight.Factor {
	t.Helper()
	for _, f := range fs {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %q factor in %+v", name, fs)
	return jobinsight.Factor{}
}

// The whole path on the real wiring: postings triaged before escalation was on are
// asked of the Claude tier by the backfill, on llm.triage_model, through the one
// metered provider; the readings land beside the primary's; and a second stage asks
// nothing.
func TestWiredBackfillEndToEnd(t *testing.T) {
	rig := newEscalationRig(t, nil, nil, map[string]string{"role_fit": "strong_fit"})
	l := bfLedger(t)
	rig.w.led, rig.r.Ledger = l, l
	a := bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3}), "60")
	b := bfSeed(t, l, 2, posting.Digested, bfPrimary(map[string]float64{"role_fit": 0.2}), "40")
	c := bfSeed(t, l, 3, posting.Explained, bfPrimary(nil), "80") // a confident primary: not asked

	rig.w.escalateBackfill(context.Background(), rig.r)
	bodies := *rig.claudeBody
	if len(bodies) != 2 {
		t.Fatalf("%d request(s) reached Claude, want 2", len(bodies))
	}
	for _, body := range bodies {
		if requestModel(t, body) != "claude-haiku-5-5" {
			t.Fatalf("asked %s, want llm.triage_model", requestModel(t, body))
		}
	}
	for _, k := range []posting.Key{a, b} {
		r, _ := l.Get(k)
		if r.Annotations["triage.role_fit.shadow"] != "strong_fit" || r.Annotations["triage.role_fit.shadow_backend"] != "escalate" ||
			r.Annotations["triage.role_fit.shadow_reading"] != "answered" || r.Annotations["triage.role_fit"] != "strong_fit" {
			t.Errorf("%s: %v", k, r.Annotations)
		}
	}
	if r, _ := l.Get(c); r.Annotations["triage.role_fit.shadow_reading"] != "" {
		t.Errorf("a confident primary was asked: %v", r.Annotations)
	}
	if st := rig.w.runReport().BackendStats["escalate"]; st.Calls != 2 || st.Readings["answered"] != 2 {
		t.Fatalf("escalate stats %+v", st)
	}
	files, _ := filepath.Glob(filepath.Join(rig.w.stateDir, "budget", "llm-*.json"))
	if len(files) != 1 {
		t.Fatalf("budget ledgers %v, want the one every LLM call is metered on", files)
	}
	snap, err := budget.Inspect(context.Background(), files[0])
	if err != nil || snap.Settled != 2 || snap.SpentNanoUSD <= 0 {
		t.Fatalf("budget %+v, %v: the backfill's spend is not on the daily cap", snap, err)
	}

	rig.w.escalateBackfill(context.Background(), rig.r)
	if n := len(*rig.claudeBody); n != 2 {
		t.Fatalf("a second stage sent %d more request(s)", n-2)
	}
}

// Past the llm daily cap the backfill is refused by the same gate: nothing is sent
// or recorded, the run says how many slots were unavailable, and once the cap is
// back the next run asks them. A triage-time outage reading of the tier is replaced.
func TestWiredBackfillStopsAtTheCapAndRetriesLater(t *testing.T) {
	rig := newEscalationRig(t, func(c *config.Config) { c.Decide.DailyUSD.LLM = 0.0000001 }, nil, map[string]string{"role_fit": "adjacent"})
	l := bfLedger(t)
	rig.w.led, rig.r.Ledger = l, l
	down := bfPrimary(map[string]float64{"role_fit": 0.3})
	down["triage.role_fit.shadow_reading"], down["triage.role_fit.shadow_backend"] = "unavailable", "escalate"
	a := bfSeed(t, l, 1, posting.Explained, bfPrimary(map[string]float64{"role_fit": 0.3}), "60")
	b := bfSeed(t, l, 2, posting.Explained, down, "50")
	before := map[posting.Key]ledger.Record{}
	for _, k := range []posting.Key{a, b} {
		before[k], _ = l.Get(k)
	}

	rig.w.escalateBackfill(context.Background(), rig.r)
	if n := len(*rig.claudeBody); n != 0 {
		t.Fatalf("%d request(s) sent past the cap", n)
	}
	rep := rig.w.runReport().EscalateBackfill
	if rep == nil || rep.Candidates != 2 || rep.Recorded != 0 || rep.Unavailable != 2 || rep.Answered != 0 {
		t.Fatalf("run record %+v", rep)
	}
	for k, was := range before {
		if got, _ := l.Get(k); !got.LastEvent.Equal(was.LastEvent) {
			t.Errorf("%s was written under a refused cap", k)
		}
	}

	// The cap is back: a new run (a new wired app and escalator) finishes the job.
	rig2 := newEscalationRig(t, nil, nil, map[string]string{"role_fit": "adjacent"})
	rig2.w.led, rig2.r.Ledger = l, l
	rig2.w.escalateBackfill(context.Background(), rig2.r)
	if n := len(*rig2.claudeBody); n != 2 {
		t.Fatalf("%d request(s) after the cap came back, want the 2 left", n)
	}
	for _, k := range []posting.Key{a, b} {
		if got, _ := l.Get(k); got.Annotations["triage.role_fit.shadow_reading"] != "answered" || got.Annotations["triage.role_fit.shadow"] != "adjacent" {
			t.Errorf("%s: %v", k, got.Annotations)
		}
	}
}

// Sonnet is metered at Sonnet's published rates, not at llm.model's (Opus), by the
// one provider every llm-tier call goes through (escalation, backfill, teacher,
// kbmatch, the llm backend): a request to the triage model reserves what the table
// prices it at, and one to llm.model keeps the configured rates.
func TestTriageModelSonnetIsReservedAtSonnetRates(t *testing.T) {
	cfg := anthropicConfig(t)
	cfg.LLM.TriageModel = "claude-sonnet-5-5"
	srvURL, _ := sseMessages(t, "ok")
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	w := &wired{cfg: cfg, stateDir: t.TempDir()}
	p, err := w.provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bp := p.(*budget.Provider)
	req := func(model string) llm.Request {
		return llm.Request{Model: model, MaxTokens: 1000, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "score this posting"}}}}}
	}
	sonnetTable, _ := anthropic.PriceOf("claude-sonnet-5-5")
	if sonnetTable.Input != 2000 || sonnetTable.Output != 10000 {
		t.Fatalf("the provider's table prices Sonnet at %+v, not $2/$10 per MTok", sonnetTable.Rates)
	}
	if got, ok := bp.PriceOf("claude-sonnet-5-5"); !ok || got != sonnetTable {
		t.Fatalf("Sonnet metered at %+v ok=%v, want the table's %+v", got, ok, sonnetTable)
	}
	sonnet, err := bp.Estimate(req("claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	// The same Sonnet request metered at llm.model's rates (a provider with no model
	// named meters every model at the configured pair): well over the reservation at the table (output and cache rates halve too).
	atOpus := &budget.Provider{Inner: newPricedProvider(anthropic.New("", noKey), "", 4000, 20000), Ledger: bp.Ledger}
	atOpusRates, err := atOpus.Estimate(req("claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	if sonnet <= 0 || atOpusRates < sonnet*3/2 {
		t.Fatalf("Sonnet reserves %d; at Opus's rates it would reserve %d, which is not the saving the table gives", sonnet, atOpusRates)
	}
	if got, _ := bp.PriceOf("claude-opus-5-5"); got.Input != 4000 || got.Output != 20000 {
		t.Fatalf("llm.model priced at %+v, want the configured 4000/20000", got)
	}
}
