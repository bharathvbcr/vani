package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// Kept Blends change no answer: after each change to something a Blend reads
// (one per part of blendKey, and the failure paths: a weights file or a
// kbmatch store that cannot be read), serve answers every list, stats and
// gaps request as a fresh command does, and the replies changed, so the step
// tested something. A change to what a Blend does not read (a mark, the clock)
// computes no Blend again.
func TestJobsServeKeptBlendsAnswerAsTheCommand(t *testing.T) {
	e, cfg := sameFixture(t)
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	type ask struct {
		op   string
		args []string
	}
	asks := []ask{
		{"list", []string{"--view", "all"}},
		{"list", []string{"--view", "new", "--sort", "relevance"}},
		{"list", []string{"--view", "new"}},
		{"stats", []string{"--view", "new"}},
		{"gaps", []string{"--view", "all"}},
	}
	var last string
	check := func(step string) string {
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
		if err := config.Save(e.configPath, c); err != nil {
			t.Fatal(err)
		}
	}
	// reused runs step and fails unless every Blend it asked for was kept.
	reused := func(step string) string {
		t.Helper()
		lookups, misses := res.blends.lookups.Load(), res.blends.misses.Load()
		got := check(step)
		if res.blends.lookups.Load() == lookups {
			t.Fatalf("%s: no Blend was asked for", step)
		}
		if n := res.blends.misses.Load() - misses; n != 0 {
			t.Fatalf("%s: %d Blends computed again", step, n)
		}
		return got
	}

	last = check("first")
	if got := reused("again"); got != last {
		t.Fatal("again: an unchanged state answered differently")
	}

	// A new reading of a posting already listed: the next generation, with as
	// many records as before, so only the generation tells the Blends apart.
	appendPosting(t, e.stateDir, insightSeed{key: "gh:gamma:02", company: "Gamma", title: "Platform Engineer",
		fit: "mismatch_domain", status: posting.Digested, desc: "Go services on Kubernetes. Location: Austin, TX.",
		seenAgo: 20 * 24 * time.Hour})
	changed("a posting read again", check("a posting read again"))

	mustCode(t, 0, "jobs", "mark", "gh:beta:01", "saved")
	changed("a mark", reused("a mark"))
	now = func() time.Time { return jobsT0.Add(24 * time.Hour) }
	changed("a day later", reused("a day later"))

	// A fitted weights file that leans on the keyword part, so the target
	// summary below moves scores, not only the keywords shown.
	other := cfg
	weights := filepath.Join(e.stateDir, "rank", "weights.json")
	if err := rankfit.Write(weights, rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0,
		Shares: &rankfit.Shares{Fit: 0.1, Coverage: 0.1, Semantic: 0.05, Keyword: 0.6, Title: 0.05, Employer: 0.05, KBMatch: 0.05},
		Report: heldOut(0.7, 0.6, 400)}); err != nil {
		t.Fatal(err)
	}
	other.RankWeightsFile = weights
	saveCfg(other)
	changed("a weights file", check("a weights file"))
	if reused("the weights file again") != last {
		t.Fatal("the weights file again: an unchanged state answered differently")
	}

	// The target summary: the keyword part. The descriptions name Kubernetes,
	// so the first summary scores them on it and the second on nothing.
	other.TargetSummary = "Platform engineering on Kubernetes."
	saveCfg(other)
	changed("a target the postings name", check("a target the postings name"))
	if !strings.Contains(last, `"name":"keyword","value":0.`) {
		t.Fatal("setup: no keyword part above 0, so a stale one would not show")
	}
	other.TargetSummary = "Data engineering in Python and Spark."
	saveCfg(other)
	changed("a target they do not", check("a target they do not"))

	// A rank penalty on the fit every posting reads.
	other.RankPenalties = []config.RankPenalty{{Slot: "role_fit", Values: []string{"strong_fit"}}}
	saveCfg(other)
	changed("a rank penalty", check("a rank penalty"))
	if reused("the penalty again") != last {
		t.Fatal("the penalty again: an unchanged state answered differently")
	}

	// A weights file that cannot be read: the hand weights rank.
	if err := os.WriteFile(weights, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed("a broken weights file", check("a broken weights file"))

	// A kbmatch store that cannot be read, then none again.
	kb := filepath.Join(e.stateDir, kbmatch.FileName)
	if err := os.WriteFile(kb, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed("a broken kbmatch store", check("a broken kbmatch store"))
	if err := os.Remove(kb); err != nil {
		t.Fatal(err)
	}
	changed("no kbmatch store", check("no kbmatch store"))

	// A manual reading of one posting's fit.
	snap, _, err := e.jobsState()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := findRecord(snap, "gh:gamma:05")
	if !ok {
		t.Fatal("setup: gh:gamma:05 is not in the ledger")
	}
	slots := map[string]string{}
	for slot, values := range jdtriage.SlotValues() {
		slots[slot] = values[len(values)-1]
	}
	if err := manualtriage.Set(manualtriage.Dir(e.stateDir), manualtriage.Reading{Key: r.Key, Slots: slots,
		Model: "test", At: jobsT0, Text: manualtriage.TextSHA256(r.Posting), Prompt: manualtriage.PromptSHA256("p")}); err != nil {
		t.Fatal(err)
	}
	changed("a manual reading", check("a manual reading"))
	if reused("the reading again") != last {
		t.Fatal("the reading again: an unchanged state answered differently")
	}

	// An exclusion hides postings (their status, as this read sees it).
	other.Exclude.Companies = []string{"Gamma"}
	saveCfg(other)
	changed("an exclusion", check("an exclusion"))
	other.Exclude.Companies = nil
	saveCfg(other)
	changed("the exclusion lifted", check("the exclusion lifted"))

	if lookups, misses := res.blends.lookups.Load(), res.blends.misses.Load(); misses == 0 || lookups <= misses {
		t.Fatalf("Blends: %d asked for, %d computed", lookups, misses)
	}
}

// The kbmatch readings a run records between two requests, and an edit to the
// knowledge base (a new digest, under which those readings no longer apply),
// reach serve's next reply as they reach the command's.
func TestJobsServeKeptBlendsSeeKBMatchReadings(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})})
	e := &env{stateDir: k.state, configPath: k.cfgPath, stdout: io.Discard, stderr: io.Discard}
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	var last string
	step := func(name string) {
		t.Helper()
		resp := s.runOp(name, serveOps["list"], []string{"--view", "all"})
		if !resp.OK {
			t.Fatalf("%s: %+v", name, resp.Error)
		}
		if want := cliReply(t, e, serveOps["list"], "--view", "all"); string(resp.Result) != want {
			t.Fatalf("%s: serve and the command differ: %s", name, firstDiff(string(resp.Result), want))
		}
		if string(resp.Result) == last {
			t.Fatalf("%s: the reply did not change, so the step tested nothing", name)
		}
		last = string(resp.Result)
	}
	step("before any reading")
	mustCode(t, 0, "kbmatch", "run")
	step("readings recorded")
	misses := res.blends.misses.Load()
	if resp := s.runOp("again", serveOps["list"], []string{"--view", "all"}); !resp.OK || string(resp.Result) != last {
		t.Fatal("readings recorded, asked again: the reply changed")
	}
	if n := res.blends.misses.Load() - misses; n != 0 {
		t.Fatalf("readings recorded, asked again: %d Blends computed again", n)
	}
	bank := strings.Replace(kbmBank, "Cut decode latency", "Cut prefill latency", 1)
	if err := os.WriteFile(k.bankPath, []byte(bank), 0o600); err != nil {
		t.Fatal(err)
	}
	wireEligibleKB(t, &k.cfg, k.bankPath, bank)
	k.save(t)
	step("the knowledge base edited")
}

// Every part of the rank context a Blend reads keys the store: changing any
// one alone gives a new, empty store, and changing nothing gives the same one.
// The parity tests above change most parts through real files; this covers
// the ones no fixture reaches (the escalation threshold, an unreadable
// config, a changed kbmatch store, the vocabulary).
func TestBlendKeyNamesEveryPart(t *testing.T) {
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	base := func() *insight {
		return &insight{res: &jobsResident{}, vocab: v, gen: 3, target: map[string]bool{"go": true}}
	}
	cfg := config.Default()
	cfg.TargetSummary = "Go"
	res := &jobsResident{}
	keyed := func(edit func(in *insight, c *config.Config) (cfgOK bool, kb string, kbOK bool)) *blendStore {
		in := base()
		in.res = res
		c := cfg
		ok, kb, kbOK := edit(in, &c)
		in.keepBlends(c, ok, kb, kbOK, 4)
		return in.blends
	}
	plain := func(*insight, *config.Config) (bool, string, bool) { return true, "kb1", true }
	first := keyed(plain)
	if first == nil || keyed(plain) != first {
		t.Fatal("an unchanged context did not keep its store")
	}
	w := jobinsight.DefaultWeights()
	for name, edit := range map[string]func(*insight, *config.Config) (bool, string, bool){
		"generation":  func(in *insight, _ *config.Config) (bool, string, bool) { in.gen = 4; return true, "kb1", true },
		"config read": func(*insight, *config.Config) (bool, string, bool) { return false, "kb1", true },
		"weights": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.rc.Weights = &w
			return true, "kb1", true
		},
		"rules": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.rc.DropRules = []jobinsight.DropRule{{Slot: "sponsorship", Values: []string{"no"}}}
			return true, "kb1", true
		},
		"escalation": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.rc.EscalateBelowP = 0.5
			return true, "kb1", true
		},
		"title signals": func(in *insight, c *config.Config) (bool, string, bool) {
			in.rc.TitleRank = func(string) int { return 0 }
			c.TitleSignals = config.TitleSignals{Include: []string{"go"}}
			return true, "kb1", true
		},
		"target": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.rc.Target = true
			return true, "kb1", true
		},
		"kbmatch": func(*insight, *config.Config) (bool, string, bool) { return true, "kb2", true },
		"manual": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.manual = &manualReadingSet{m: map[posting.Key]manualtriage.Reading{"gh:a:1": {Key: "gh:a:1"}}}
			return true, "kb1", true
		},
		"semantic": func(in *insight, _ *config.Config) (bool, string, bool) {
			in.rc.Semantic = func(ledger.Record) (float64, bool) { return 0, false }
			in.sem = &semantic.Matcher{}
			return true, "kb1", true
		},
	} {
		got := keyed(edit)
		if got == nil || got == first {
			t.Errorf("%s: the store was kept across a change", name)
		}
		first = keyed(plain)
	}
	if got := keyed(func(*insight, *config.Config) (bool, string, bool) { return true, "kb1", false }); got != nil {
		t.Error("a kbmatch store that changed while it was read kept Blends")
	}
	in := base()
	in.res = nil
	in.keepBlends(cfg, true, "kb1", true, 4)
	if in.blends != nil {
		t.Error("a command run once kept Blends")
	}
}

// Each per-posting guard computes the Blend again when what it guards
// differs: the record in the slot, its status, whether its stored facts were
// there, and its company's blurb. A posting held without its text or facts is
// never kept, so its derivers note it missing every time.
func TestBlendForGuards(t *testing.T) {
	r := ledger.Record{Key: "gh:acme:1", Status: posting.Digested, Posting: posting.Posting{Key: "gh:acme:1", Company: "Acme", Title: "Engineer"}}
	in := &insight{res: &jobsResident{}, rc: jobinsight.NewRankContext([]ledger.Record{r}, nil, jobsT0)}
	in.blends = in.res.blends.store(blendKey{gen: 1}, 2)
	misses := func(step string, r ledger.Record, want int64) {
		t.Helper()
		before := in.res.blends.misses.Load()
		in.blendFor(0, r)
		if got := in.res.blends.misses.Load() - before; got != want {
			t.Fatalf("%s: %d computed, want %d", step, got, want)
		}
	}
	misses("first", r, 1)
	misses("again", r, 0)
	other := r
	other.Key = "gh:acme:2"
	misses("another record in the slot", other, 1)
	misses("the first back", r, 1)
	unsure := r
	unsure.Status = posting.Unsure
	misses("another status", unsure, 1)
	misses("the status back", r, 1)
	in.facts = &textFacts{m: map[posting.Key]jobfacts.Facts{r.Key: {}}}
	misses("its facts stored", r, 1)
	misses("again with facts", r, 0)
	in.boiler, in.boilerFPs = map[string]map[string]bool{"acme": {"go": true}}, nil
	misses("a blurb learned", r, 1)
	misses("again with the blurb", r, 0)
	in.facts = nil
	misses("its facts gone", r, 1)
	misses("with its text", r, 0)
	lean := r
	lean.BodyEvicted = true
	misses("the kept one, now held without text or facts", lean, 1)
	misses("held without text or facts", lean, 1)
	misses("still without", lean, 1)
	if in.res.blends.store(blendKey{gen: 2}, 2) == in.blends {
		t.Fatal("a new key kept the old store")
	}
	if in.res.blends.store(blendKey{gen: 2}, 3) == in.res.blends.store(blendKey{gen: 2}, 2) {
		t.Fatal("a store for another record count was reused")
	}
}

// A company's blurb fingerprint names its terms, not their order, and tells
// two blurbs apart; a company without one is 0.
func TestBoilerPrint(t *testing.T) {
	in := &insight{boiler: map[string]map[string]bool{
		"acme": {"go": true, "kubernetes": true}, "beta": {"kubernetes": true, "go": true}, "gamma": {"go": true},
		"delta": {"go": false},
	}}
	if a, b := in.boilerPrint("Acme"), in.boilerPrint(" beta "); a != b || a == 0 {
		t.Errorf("same terms: %x and %x", a, b)
	}
	if in.boilerPrint("Gamma") == in.boilerPrint("Acme") {
		t.Error("different terms share a fingerprint")
	}
	if in.boilerPrint("Nobody") != 0 || in.boilerPrint("Delta") != 0 {
		t.Error("a company with no blurb terms is not 0")
	}
}
