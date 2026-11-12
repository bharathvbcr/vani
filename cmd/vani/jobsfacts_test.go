package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// factsFixture seeds a state with every status, marks, pay forms and places,
// and a config whose exclusions are x. Seeding runs in a subtest, so the
// ledger it opens is closed (and unlocked) when seeding returns.
func factsFixture(t *testing.T, x config.Exclude) (*env, []insightSeed) {
	t.Helper()
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "Inference engineering in Go and Rust, with Kubernetes."
	cfg.Exclude = x
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	pays := []string{"$150,000 - $210,000 per year", "€90k–€120k", "USD 60/hour", "", "competitive"}
	places := []string{"Remote - US", "Austin, TX", "Berlin, Germany", "", "San Francisco, CA or Remote"}
	var seeds []insightSeed
	for i := range 30 {
		seeds = append(seeds, insightSeed{
			key: fmt.Sprintf("gh:co%d:%02d", i%5, i), company: fmt.Sprintf("Co %d", i%5),
			title: []string{"Inference Engineer", "Platform Engineer", "Sales Intern", "ML Engineer"}[i%4],
			desc: fmt.Sprintf("Go and Rust on Kubernetes. Location: %s. Pay: %s. Python, CUDA. %s", places[i%5], pays[i%5],
				[]string{"", "Security clearance required.", ""}[i%3]),
			fit:    []string{"strong_fit", "partial_fit", "weak_fit", ""}[i%4],
			status: []posting.Status{posting.Digested, posting.Scored, posting.Live, posting.Dead, posting.Dropped}[i%5],
			posted: jobsT0.AddDate(0, 0, -i),
		})
	}
	t.Run("seed", func(t *testing.T) { seedInsight(t, state, seeds...) })
	mustCode(t, 0, "jobs", "mark", "gh:co1:01", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:co3:03", "applied") // closed and marked: its text is read
	mustCode(t, 0, "jobs", "mark", "gh:co4:04", "hidden")
	return &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}, seeds
}

// jobsDump is every view in every order, its stats and pending counts, and the
// filters that read pay, relevance, age and place, as JSON.
func jobsDump(t *testing.T, snap ledger.Snapshot, m map[posting.Key]marks.Entry, in *insight) string {
	t.Helper()
	hl := jobinsight.DefaultScoreHalfLifeDays
	var opts []jobview.Opts
	for _, v := range jobview.Views {
		for _, s := range jobview.Sorts {
			opts = append(opts, jobview.Opts{View: v, Sort: s, HalfLife: hl})
		}
	}
	opts = append(opts,
		jobview.Opts{View: "all", Sort: "priority", HalfLife: hl, More: jobview.Filter{MinPay: 100_000, Currency: "USD"}},
		jobview.Opts{View: "new", Sort: "relevance", HalfLife: hl, More: jobview.Filter{MinRelevance: 10}},
		jobview.Opts{View: "new", Sort: "company", HalfLife: hl, Query: "engineer"},
	)
	var b strings.Builder
	for _, o := range opts {
		a, s := o, o
		l := jobview.BuildList(snap, m, &a, in, in.info, listKeywords, defaultJobsLimit)
		l.Pending = jobview.PendingOf(snap, m, "")
		sel, _ := jobview.Select(snap, m, &s, in, 0)
		st := jobview.StatsOf(sel, in.info, in.target)
		st.Salary = jobview.PayStatsOf(sel, "USD")
		for _, v := range []any{l, st} {
			j, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(j)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// textDump is jobsDump over the read every view made before facts: every
// in-scope description loaded and every fact derived from it.
func textDump(t *testing.T, e *env) string {
	t.Helper()
	snap, m, err := e.jobsState()
	if err != nil {
		t.Fatal(err)
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		t.Fatal(err)
	}
	return jobsDump(t, snap, m, in)
}

// factsDump is jobsDump over jobsListRead, failing on a lean record a deriver
// was asked about.
func factsDump(t *testing.T, e *env) (string, jobsRead) {
	t.Helper()
	rd, in, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays)
	if err != nil {
		t.Fatal(err)
	}
	out := jobsDump(t, rd.snap, rd.m, in)
	if err := in.facts.err(); err != nil {
		t.Fatal(err)
	}
	return out, rd
}

// cliReply is what `vani jobs <op> --json` prints for args, run once.
func cliReply(t *testing.T, e *env, run func(*env, []string) error, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	one := &env{stateDir: e.stateDir, configPath: e.configPath, stdout: &out, stderr: io.Discard}
	run(one, append([]string{"--json"}, args...))
	return strings.TrimSpace(out.String())
}

func inScope(rd jobsRead) (n, lean int) {
	keep := jobsTextScope(rd.m)
	for _, r := range rd.snap.Records {
		if keep(r) {
			n++
			if r.BodyEvicted {
				lean++
			}
		}
	}
	return n, lean
}

func appendPosting(t *testing.T, state string, s insightSeed) {
	t.Helper()
	t.Run("append", func(t *testing.T) { seedInsight(t, state, s) })
}

// A Jobs view read from stored facts answers byte for byte what one reading
// every description answers: with no store (every fact derived and saved),
// with a full one (every fact served, no description loaded), and after a
// posting's text changed (that one derived again).
func TestJobsViewFromFactsMatchesText(t *testing.T) {
	e, seeds := factsFixture(t, config.Exclude{})
	want := textDump(t, e)

	got, rd := factsDump(t, e)
	n, _ := inScope(rd)
	if f := e.lastFacts; f.Derived != n || f.Served != 0 {
		t.Fatalf("cold: derived %d, served %d; want %d derived", f.Derived, f.Served, n)
	}
	if got != want {
		t.Fatal("cold: the facts read answers differently from the text read")
	}

	got, rd = factsDump(t, e)
	_, lean := inScope(rd)
	if f := e.lastFacts; f.Served != n || f.Derived != 0 || lean != n || f.Store != "loaded" {
		t.Fatalf("warm: served %d, derived %d, lean %d of %d, store %q", f.Served, f.Derived, lean, n, f.Store)
	}
	if got != want {
		t.Fatal("warm: the facts read answers differently from the text read")
	}

	changed := seeds[0]
	changed.desc = "Now Haskell and Erlang only. Pay: $300,000 - $350,000 per year."
	appendPosting(t, e.stateDir, changed)
	want = textDump(t, e)
	got, _ = factsDump(t, e)
	if f := e.lastFacts; f.Derived != 1 || f.Served != n-1 {
		t.Fatalf("changed: derived %d, served %d; want 1 and %d", f.Derived, f.Served, n-1)
	}
	if got != want {
		t.Fatal("changed: the facts read answers differently from the text read")
	}
}

// What the view shows is the stored fact: a stored keyword the text never
// names shows, so a passing comparison above was a read of the store.
func TestJobsViewShowsTheStoredFact(t *testing.T) {
	e, seeds := factsFixture(t, config.Exclude{})
	if _, _, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays); err != nil {
		t.Fatal(err)
	}
	k := posting.Key(seeds[0].key)
	rd, err := ledger.OpenReader(filepath.Join(e.stateDir, "ledger"), ledger.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fp, ok := rd.BodyFP(k)
	snap := rd.Snapshot()
	rd.Close()
	r, found := findRecord(snap, k)
	if !ok || !found {
		t.Fatal("no fingerprint for the seeded posting")
	}
	v, _ := jobinsight.Default()
	s := jobfacts.Open(e.jobsFactsPath(), jobfacts.Version(v))
	f, ok := s.Get(k, jobfacts.InputOf(r.Posting, fp))
	if !ok {
		t.Fatalf("the read did not save the posting's facts (%s)", s.Info())
	}
	f.Hits = append([]jobinsight.Hit{{Term: "zz-sentinel", Category: "language", Count: 99}}, f.Hits...)
	s.Put(k, jobfacts.InputOf(r.Posting, fp), f)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := factsDump(t, e)
	if !strings.Contains(got, "zz-sentinel") {
		t.Fatal("a stored keyword did not reach the view")
	}
}

// With exclusions that read no description, a posting held without its text
// is still hidden (a kept one) or flagged for the prefilter (a live one), as it
// is with its text; with a description exclusion, the read loads every
// in-scope description, so the rule judges the text.
func TestJobsViewExclusionsWithFacts(t *testing.T) {
	for name, x := range map[string]config.Exclude{
		"titles":       {Titles: []string{"intern"}},
		"descriptions": {Descriptions: []string{"security clearance"}},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := factsFixture(t, x)
			want := textDump(t, e)
			if !strings.Contains(want, `"prefilter_drop":"excluded"`) {
				t.Fatal("the fixture flags no live posting: the test reaches nothing")
			}
			for _, run := range []string{"cold", "warm"} {
				got, rd := factsDump(t, e)
				_, lean := inScope(rd)
				if wantLean := x.ReadsDescriptions(); run == "warm" && (lean == 0) == !wantLean {
					t.Fatalf("%s: %d in-scope records lean with descriptions read %v", run, lean, wantLean)
				}
				if got != want {
					t.Fatalf("%s: the facts read answers differently from the text read", run)
				}
			}
		})
	}
}

// `jobs show` (which loads one company's text) scores a job as `jobs list`
// (which loads every view's) does, under exclusions that drop other companies'
// postings: a posting held without its text is matched by the rules that read
// none, so both reads hide the same postings and rank against the same set.
func TestJobsShowScoresAsTheListUnderExclusions(t *testing.T) {
	for name, x := range map[string]config.Exclude{
		"none":         {},
		"titles":       {Titles: []string{"intern"}},
		"companies":    {Companies: []string{"Co 2"}},
		"descriptions": {Descriptions: []string{"security clearance"}},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := factsFixture(t, x)
			var list struct {
				Jobs []map[string]json.RawMessage `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(cliReply(t, e, (*env).jobsList, "--view", "all", "--limit", "5000")), &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Jobs) == 0 {
				t.Fatal("the list is empty: the test compares nothing")
			}
			for _, lj := range list.Jobs {
				var key string
				json.Unmarshal(lj["key"], &key)
				var show map[string]json.RawMessage
				if err := json.Unmarshal([]byte(cliReply(t, e, (*env).jobsShow, key)), &show); err != nil {
					t.Fatal(err)
				}
				for _, f := range []string{"priority", "relevance", "relevance_basis", "relevance_known", "score_factors", "age_days", "pay", "prefilter_drop", "status"} {
					if string(lj[f]) != string(show[f]) {
						t.Errorf("%s %s: list %s, show %s", key, f, lj[f], show[f])
					}
				}
			}
		})
	}
}

// A lean record a deriver is asked about without facts fails the command
// instead of answering for an empty description.
func TestJobsViewFailsOnALeanRecordWithoutFacts(t *testing.T) {
	e, _ := factsFixture(t, config.Exclude{})
	if _, _, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays); err != nil {
		t.Fatal(err)
	}
	rd, err := e.jobsListRead()
	if err != nil {
		t.Fatal(err)
	}
	var dropped posting.Key
	for _, r := range rd.snap.Records {
		if r.BodyEvicted && jobview.InView("all", r, rd.m[r.Key].Mark) {
			dropped = r.Key
			delete(rd.facts.m, r.Key)
			break
		}
	}
	if dropped == "" {
		t.Fatal("no lean record in view")
	}
	in, err := e.newListInsight(jobinsight.DefaultScoreHalfLifeDays, rd)
	if err != nil {
		t.Fatal(err)
	}
	o := jobview.Opts{View: "all", Sort: "default", HalfLife: jobinsight.DefaultScoreHalfLifeDays}
	jobview.BuildList(rd.snap, rd.m, &o, in, in.info, listKeywords, defaultJobsLimit)
	if err := in.facts.err(); err == nil || !strings.Contains(err.Error(), string(dropped)) {
		t.Fatalf("a lean record without facts: %v", err)
	}
}

// The knowledge-base match identity of a posting held without its text is the
// one its whole text gives, and a damaged stored half is noted, not answered.
func TestKBHashOfALeanRecord(t *testing.T) {
	e, _ := factsFixture(t, config.Exclude{})
	if _, _, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays); err != nil {
		t.Fatal(err)
	}
	snap, _, err := e.jobsState()
	if err != nil {
		t.Fatal(err)
	}
	_, in, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays)
	if err != nil {
		t.Fatal(err)
	}
	d := kbmatch.Digest{SHA256: strings.Repeat("cd", 32)}
	checked := 0
	for _, whole := range snap.Records {
		f, ok := in.facts.get(whole.Key)
		if !ok {
			continue
		}
		lean := whole
		lean.Posting.Description, lean.BodyEvicted = "", true
		if got, want := in.kbHashOf(lean, "summary", d), kbmatch.TextSHA256(whole.Posting, "summary", d); got != want {
			t.Fatalf("%s: lean %s, whole %s", whole.Key, got, want)
		}
		checked++
		if checked == 1 {
			f.KBText = []byte("damaged")
			in.facts.m[whole.Key] = f
			in.kbHashOf(lean, "summary", d)
			if err := in.facts.err(); err == nil {
				t.Fatal("a damaged stored half was answered")
			}
		}
	}
	if checked < 2 {
		t.Fatalf("only %d records checked", checked)
	}
}
