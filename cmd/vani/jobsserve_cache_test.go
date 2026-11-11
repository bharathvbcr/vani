package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// sameFixture seeds relistings: three companies, one role title each listed
// every ten days, so rows carry repost notes, and an application at Acme, so
// its other rows carry a repeat-apply note.
func sameFixture(t *testing.T) (*env, config.Config) {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "Platform engineering in Go."
	cfg.Watchlist = []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"},
		{ATS: posting.Greenhouse, Token: "beta", Company: "Beta"},
		{ATS: posting.Greenhouse, Token: "gamma", Company: "Gamma"},
	}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "boards-api.greenhouse.io")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	var seeds []insightSeed
	for i := range 18 {
		co := []string{"acme", "beta", "gamma"}[i%3]
		seeds = append(seeds, insightSeed{
			key: fmt.Sprintf("gh:%s:%02d", co, i), company: strings.ToUpper(co[:1]) + co[1:],
			title: "Platform Engineer", fit: "strong_fit", status: posting.Digested,
			desc:    "Go services on Kubernetes. Location: Austin, TX.",
			seenAgo: time.Duration(i) * 10 * 24 * time.Hour,
		})
	}
	t.Run("seed", func(t *testing.T) { seedInsight(t, state, seeds...) })
	mustCode(t, 0, "jobs", "mark", "gh:acme:03", "applied")
	return &env{stateDir: state, configPath: cfgPath, stdout: io.Discard, stderr: io.Discard}, cfg
}

// A kept company-history index changes no answer: after every kind of change
// between two requests (the marks, the clock, the ledger, the watchlist
// domains, other config), each reply is the command's, and the index is
// rebuilt exactly when the records or the domains changed.
func TestJobsServeSecondRequestAnswersAsTheCommand(t *testing.T) {
	e, cfg := sameFixture(t)
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	cfgPath := e.configPath

	type ask struct {
		op   string
		args []string
	}
	asks := []ask{
		{"list", []string{"--view", "all"}},
		{"list", []string{"--view", "new", "--sort", "company"}},
		{"list", []string{"--view", "applied"}},
		{"show", []string{"gh:acme:00"}},
		{"show", []string{"gh:beta:01"}},
	}
	var last string
	check := func(step string, wantBuilds int) string {
		t.Helper()
		var all strings.Builder
		for _, a := range asks {
			resp := s.runOp(step, serveOps[a.op], a.args)
			if !resp.OK {
				t.Fatalf("%s: %s %v: %+v", step, a.op, a.args, resp.Error)
			}
			if want := cliReply(t, e, serveOps[a.op], a.args...); string(resp.Result) != want {
				t.Fatalf("%s: %s %v: serve and the command differ: %s", step, a.op, a.args, firstDiff(string(resp.Result), want))
			}
			all.Write(resp.Result)
			all.WriteByte('\n')
		}
		if res.sameBuilds != wantBuilds {
			t.Fatalf("%s: company-history groups built %d times, want %d", step, res.sameBuilds, wantBuilds)
		}
		return all.String()
	}
	changed := func(step, got string) {
		t.Helper()
		if got == last {
			t.Fatalf("%s: the replies did not change, so the step tested nothing", step)
		}
		last = got
	}
	saveCfg := func(c config.Config) {
		t.Helper()
		if err := config.Save(cfgPath, c); err != nil {
			t.Fatal(err)
		}
	}
	setNow := func(at time.Time) {
		now = func() time.Time { return at }
	}

	last = check("first", 1)
	if !strings.Contains(last, `"repost"`) || !strings.Contains(last, `"repeat_apply"`) {
		t.Fatal("setup: no row carries a repost and a repeat-apply note")
	}
	if got := check("again", 1); got != last {
		t.Fatal("again: an unchanged state answered differently")
	}
	built := res.same.Built()
	check("again 2", 1)
	if res.same.Built() != built {
		t.Fatal("again: the kept groups built their role groups again")
	}

	mustCode(t, 0, "jobs", "mark", "gh:acme:06", "applied")
	changed("a mark", check("a mark", 1))

	setNow(jobsT0.Add(24 * time.Hour))
	changed("a day later", check("a day later", 1))
	setNow(jobsT0.Add(61 * 24 * time.Hour)) // both applications leave the window
	changed("past the apply window", check("past the apply window", 1))
	setNow(jobsT0)
	check("clock back", 1)

	appendPosting(t, e.stateDir, insightSeed{key: "gh:beta:90", company: "Beta", title: "Platform Engineer",
		fit: "strong_fit", status: posting.Digested, desc: "Go services. Location: Austin, TX.", seenAgo: 25 * 24 * time.Hour})
	changed("an appended relisting", check("an appended relisting", 2))

	appendPosting(t, e.stateDir, insightSeed{key: "gh:beta:01", company: "Beta", title: "Staff Platform Engineer",
		fit: "strong_fit", status: posting.Digested, desc: "Go services. Location: Austin, TX.", seenAgo: 10 * 24 * time.Hour})
	changed("a retitled posting", check("a retitled posting", 3))

	joined := cfg
	joined.Watchlist = []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme", Domain: "acme.example"},
		{ATS: posting.Greenhouse, Token: "beta", Company: "Beta", Domain: "acme.example"},
		{ATS: posting.Greenhouse, Token: "gamma", Company: "Gamma"},
	}
	joined.AllowedHosts = append(slices.Clone(cfg.AllowedHosts), "acme.example")
	saveCfg(joined)
	changed("two boards on one domain", check("two boards on one domain", 4))

	other := joined
	other.TargetSummary = "Data engineering in Python."
	saveCfg(other)
	check("other config", 4)

	// The per-title ranks are kept per signals: each change is answered anew.
	other.TitleSignals = config.TitleSignals{Include: []string{"platform"}}
	saveCfg(other)
	changed("title signals", check("title signals", 4))
	other.TitleSignals = config.TitleSignals{Include: []string{"platform"}, Exclude: []string{"staff"}}
	saveCfg(other)
	changed("an excluded title word", check("an excluded title word", 4))
	if lookups, misses := res.memo.stats(); misses == 0 || lookups <= misses {
		t.Fatalf("the per-title and per-place memo answered %d of %d from memory", lookups-misses, lookups)
	}

	saveCfg(cfg)
	changed("the domains removed", check("the domains removed", 5))
}
