package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rankedFitSeed is one triaged posting whose role_fit the ranking may read from
// somewhere other than the primary's annotation.
type rankedFitSeed struct {
	key, fit, p string
	// shadow is an escalation reading of role_fit ("" for none).
	shadow string
	// manual is the owner's manual reading of role_fit ("" for none); stale
	// records it against text the posting no longer has.
	manual string
	stale  bool
}

// The postings, and the role_fit the ranking reads for each with escalation on.
var rankedFitSeeds = []struct {
	rankedFitSeed
	ranked string
}{
	// An unsure primary the escalation tier read differently: ranked on the escalation.
	{rankedFitSeed{key: "gh:t:esc", fit: "strong_fit", p: "0.40", shadow: "partial_fit"}, "partial_fit"},
	// A manual reading outranks a sure primary.
	{rankedFitSeed{key: "gh:t:man", fit: "strong_fit", p: "0.95", manual: "partial_fit"}, "partial_fit"},
	// A manual reading outranks the escalation too.
	{rankedFitSeed{key: "gh:t:both", fit: "strong_fit", p: "0.40", shadow: "adjacent", manual: "mismatch_seniority"}, "mismatch_seniority"},
	// A primary at or above below_p keeps its reading, escalation or not.
	{rankedFitSeed{key: "gh:t:sure", fit: "strong_fit", p: "0.90", shadow: "partial_fit"}, "strong_fit"},
	// An escalation reading outside the listed options is not used.
	{rankedFitSeed{key: "gh:t:unlisted", fit: "strong_fit", p: "0.40", shadow: "great"}, "strong_fit"},
	// A manual reading of text the posting no longer has is stale and not used.
	{rankedFitSeed{key: "gh:t:stale", fit: "strong_fit", p: "0.95", manual: "adjacent", stale: true}, "strong_fit"},
	// The primary alone.
	{rankedFitSeed{key: "gh:t:plain", fit: "strong_fit", p: "0.95"}, "strong_fit"},
	// Triaged, with no role_fit reading: "none".
	{rankedFitSeed{key: "gh:t:none"}, ""},
}

func rankedFitPosting(key string) posting.Posting {
	return posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Inference Engineer " + key,
		Description: "Serve models on GPUs.", PostedAt: jobsT0}
}

// seedRankedFit writes the postings, their escalation readings and manual
// readings, and a config with escalation on (escalate) or off.
func seedRankedFit(t *testing.T, cfgPath, stateDir string, escalate bool) {
	t.Helper()
	cfg := config.Default()
	if escalate {
		// decide.escalate needs an llm tier configured; jobs list never builds one.
		cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-test", TriageModel: "claude-test-small",
			InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}
		cfg.Decide.DailyUSD.LLM = 10
		cfg.Decide.Escalate = &config.Escalate{Enabled: true}
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, s := range rankedFitSeeds {
		p := rankedFitPosting(s.key)
		ann := map[string]string{ledger.AnnRank: "60", "triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
		if s.fit != "" {
			ann["triage.role_fit"], ann["triage.role_fit.reading"], ann["triage.role_fit.p"] = s.fit, "answered", s.p
		}
		if s.shadow != "" {
			pre := ledger.AnnTriagePrefix + "role_fit"
			ann[pre+ledger.AnnTriageSuffixShadow] = s.shadow
			ann[pre+ledger.AnnTriageSuffixShadowBackend] = "escalate"
			ann[pre+ledger.AnnTriageSuffixShadowReading] = "answered"
		}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if s.manual == "" {
			continue
		}
		slots := map[string]string{}
		for slot, values := range jdtriage.SlotValues() {
			slots[slot] = values[0]
		}
		slots["role_fit"] = s.manual
		text := p
		if s.stale {
			text.Description = "Text the posting no longer has."
		}
		if err := manualtriage.Set(manualtriage.Dir(stateDir), manualtriage.Reading{Key: p.Key, Slots: slots,
			Model: "test", At: jobsT0, Text: manualtriage.TextSHA256(text), Prompt: manualtriage.PromptSHA256("p")}); err != nil {
			t.Fatal(err)
		}
	}
}

// fitKeys is the keys a `jobs list --fit` selects, sorted.
func fitKeys(t *testing.T, fits string) []string {
	t.Helper()
	var ks []string
	for _, j := range listJSON(t, "--view", "all", "--fit", fits, "--limit", "100").Jobs {
		ks = append(ks, string(j.Key))
	}
	slices.Sort(ks)
	return ks
}

// The role fit a job shows (its row's label), the --fit filter and the stats'
// fit counts all read the reading the ranking reads (the owner's manual reading,
// else an unsure primary's escalation, else the primary's), so a "Strong fit"
// label never sits on a job ranked as a partial fit. The triage map keeps the
// primary's reading: it is what triage answered, and the drop policy reads it.
func TestJobsFitIsTheRankedReading(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	seedRankedFit(t, cfgPath, state, true)

	want := map[string][]string{}
	l := listJSON(t, "--view", "all", "--limit", "100")
	if len(l.Jobs) != len(rankedFitSeeds) {
		t.Fatalf("listed %d jobs, want %d", len(l.Jobs), len(rankedFitSeeds))
	}
	byKey := map[string]jobview.Job{}
	for _, j := range l.Jobs {
		byKey[string(j.Key)] = j
	}
	for _, s := range rankedFitSeeds {
		j, ok := byKey[s.key]
		if !ok || j.Row == nil {
			t.Fatalf("%s: not listed with a row", s.key)
		}
		if j.Row.Fit != s.ranked || j.Row.FitLabel != jobview.FitLabel(s.ranked) {
			t.Errorf("%s: row fit %q (%q), want the ranked reading %q (%q)", s.key, j.Row.Fit, j.Row.FitLabel, s.ranked, jobview.FitLabel(s.ranked))
		}
		if got := j.Triage["role_fit"]; got != s.fit {
			t.Errorf("%s: triage role_fit %q, want the primary's %q", s.key, got, s.fit)
		}
		f := s.ranked
		if f == "" {
			f = "none"
		}
		want[f] = append(want[f], s.key)
	}

	for fit, keys := range want {
		slices.Sort(keys)
		if got := fitKeys(t, fit); !slices.Equal(got, keys) {
			t.Errorf("--fit %s: %v, want %v", fit, got, keys)
		}
	}

	o, _ := mustCode(t, 0, "jobs", "stats", "--json", "--view", "all")
	var st jobview.Stats
	if err := json.Unmarshal([]byte(o), &st); err != nil {
		t.Fatalf("stats %q: %v", o, err)
	}
	for fit, keys := range want {
		if st.Fit[fit] != len(keys) {
			t.Errorf("stats fit[%s] = %d, want %d (%v)", fit, st.Fit[fit], len(keys), st.Fit)
		}
	}
	for _, c := range st.Companies {
		if c.Company == "Acme" && c.Strong != len(want["strong_fit"]) {
			t.Errorf("stats Acme strong %d, want %d", c.Strong, len(want["strong_fit"]))
		}
	}

	// The export's Role fit column names the same reading.
	out := filepath.Join(t.TempDir(), "fit.xlsx")
	if r := exportJSON(t, "--view", "all", "--top", "100", "--out", out); r.Rows != len(rankedFitSeeds) {
		t.Fatalf("exported %d rows, want %d", r.Rows, len(rankedFitSeeds))
	}
	rows := exportRows(t, out)
	ranked := map[string]string{}
	for _, s := range rankedFitSeeds {
		ranked[s.key] = jobview.FitLabel(s.ranked)
	}
	for _, row := range rows[1:] {
		if k := row[col("Key")]; row[col("Role fit")] != ranked[k] {
			t.Errorf("export %s: Role fit %q, want %q", k, row[col("Role fit")], ranked[k])
		}
	}
}

// With escalation off the ranking reads no escalation reading, and neither does
// the label: an unsure primary shows its own reading again.
func TestJobsFitWithEscalationOff(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	seedRankedFit(t, cfgPath, state, false)
	got := map[string]string{}
	for _, j := range listJSON(t, "--view", "all", "--limit", "100").Jobs {
		got[string(j.Key)] = j.Row.Fit
	}
	for key, want := range map[string]string{"gh:t:esc": "strong_fit", "gh:t:man": "partial_fit", "gh:t:both": "mismatch_seniority",
		"gh:t:unlisted": "strong_fit"} {
		if got[key] != want {
			t.Errorf("%s: row fit %q with escalation off, want %q", key, got[key], want)
		}
	}
	if keys := fitKeys(t, "strong_fit"); !slices.Contains(keys, "gh:t:esc") {
		t.Errorf("--fit strong_fit with escalation off: %v, want gh:t:esc in it", keys)
	}
}

// Serve keeps rows between requests on one ledger generation, but a manual
// reading lives outside the ledger: a reading set between two requests changes
// the next one's label and filter, as a fresh command's.
func TestJobsServeFitFollowsManualReading(t *testing.T) {
	g := newReadKeepRig(t)
	const key = posting.Key("gh:gamma:05")
	fitOf := func(step string) string {
		t.Helper()
		resp := g.s.runOp(step, serveOps["list"], []string{"--view", "all", "--limit", "500"})
		if !resp.OK {
			t.Fatalf("%s: %+v", step, resp.Error)
		}
		var l jobview.List
		if err := json.Unmarshal(resp.Result, &l); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		for _, j := range l.Jobs {
			if j.Key == key {
				if j.Row == nil {
					t.Fatalf("%s: %s has no row", step, key)
				}
				return j.Row.Fit
			}
		}
		t.Fatalf("%s: %s not listed", step, key)
		return ""
	}
	g.last = g.check("before")
	before := fitOf("before")
	manual := "adjacent"
	if before == manual {
		manual = "partial_fit"
	}
	g.setManual(key, "role_fit", manual)
	g.changed("a manual reading") // serve answers as a fresh command
	if got := fitOf("after"); got != manual {
		t.Fatalf("after a manual reading the row fit is %q (was %q), want %q", got, before, manual)
	}
	resp := g.s.runOp("filter", serveOps["list"], []string{"--view", "all", "--fit", manual, "--limit", "500"})
	if !resp.OK || !strings.Contains(string(resp.Result), string(key)) {
		t.Fatalf("--fit %s after the reading: ok %v, %s not selected", manual, resp.OK, key)
	}
}
