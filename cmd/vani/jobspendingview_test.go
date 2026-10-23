package main

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedAwaiting is a ledger with untriaged (live), triaged, dead and dropped
// postings, so every view has something to include and to leave out. Each
// board's oldest posting is a triaged or closed one: a posting first seen at
// its board's first crawl has no known age (jobinsight.FreshAge), so the live
// ones here are all newer than that and dated.
func seedAwaiting(t *testing.T, state string) {
	t.Helper()
	seedJobs(t, state,
		seedJob{key: "gh:acme:digested", title: "ML Engineer", fit: "strong_fit", status: posting.Digested, age: 40 * 24 * time.Hour},
		seedJob{key: "gh:beta:closed", title: "Closed Engineer", status: posting.Dead, age: 30 * 24 * time.Hour, company: "Beta"},
		seedJob{key: "gh:acme:old", title: "Platform Engineer", status: posting.Live, age: 20 * 24 * time.Hour},
		seedJob{key: "gh:beta:mid", title: "Data Engineer", status: posting.Live, age: 3 * 24 * time.Hour, company: "Beta"},
		seedJob{key: "gh:acme:fresh", title: "Inference Engineer", status: posting.Live, age: time.Hour},
		seedJob{key: "gh:acme:dead", title: "Closed Engineer", status: posting.Dead, age: 10 * 24 * time.Hour},
		seedJob{key: "gh:acme:dropped", title: "Recruiter", status: posting.Dropped, age: 10 * 24 * time.Hour},
	)
}

// A first crawl lists a whole board in one instant, so none of its postings has
// a known age: they sort after dated ones, grouped by company.
func TestJobsPendingFirstCrawlHasNoAge(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:zeta:1", title: "Engineer", status: posting.Live, company: "Zeta"},
		seedJob{key: "gh:acme:2", title: "Engineer", status: posting.Live},
		seedJob{key: "gh:acme:1", title: "Designer", status: posting.Live},
	)
	l := listJSON(t, "--view", "pending")
	if keys(l) != "gh:acme:1 gh:acme:2 gh:zeta:1" {
		t.Fatalf("order %q", keys(l))
	}
	for _, j := range l.Jobs {
		if j.AgeDays != nil {
			t.Fatalf("%s: a first-crawl posting has an age %d", j.Key, *j.AgeDays)
		}
	}
}

// The Jobs views list only triaged roles, so 8,525 postings read from boards
// were invisible: this view lists them, freshest first, and nothing else.
func TestJobsPendingViewListsOnlyUntriagedPostings(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	l := listJSON(t, "--view", "pending")
	if got, want := keys(l), "gh:acme:fresh gh:beta:mid gh:acme:old"; got != want {
		t.Fatalf("pending view %q, want %q (live only, freshest first)", got, want)
	}
	if l.View != "pending" || l.Total != 3 || l.Shown != 3 || l.Sort != "fresh" {
		t.Fatalf("reply %+v", l)
	}
	if l.Counts["pending"] != 3 || l.Counts["new"] != 1 || l.Counts["all"] != 1 {
		t.Fatalf("counts %+v", l.Counts)
	}
	if l.Pending.Total != l.Counts["pending"] {
		t.Fatalf("footer says %d awaiting triage, the view counts %d", l.Pending.Total, l.Counts["pending"])
	}
	// The ranked views keep exactly what they had: no live posting leaks in.
	for view, want := range map[string]string{"new": "gh:acme:digested", "all": "gh:acme:digested", "saved": "", "hidden": "", "applied": ""} {
		if got := keys(listJSON(t, "--view", view)); got != want {
			t.Fatalf("--view %s lists %q, want %q", view, got, want)
		}
	}
}

// An untriaged posting has no fit, relevance or priority: the reply carries
// none of them (absent is unknown, 0 would read as a very low score).
func TestJobsPendingRowsCarryNoRanking(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	o, _ := mustCode(t, 0, "jobs", "list", "--json", "--view", "pending")
	var raw struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(o), &raw); err != nil || len(raw.Jobs) != 3 {
		t.Fatalf("reply %q: %v", o, err)
	}
	for _, j := range raw.Jobs {
		for _, f := range []string{"relevance", "priority", "relevance_basis", "relevance_known", "score_factors", "keywords", "reasons", "semantic", "row", "rank", "coverage", "triage"} {
			if v, ok := j[f]; ok {
				t.Errorf("%s: an untriaged row carries %s=%s", j["key"], f, v)
			}
		}
		if string(j["untriaged"]) != "true" || string(j["status"]) != `"live"` {
			t.Errorf("%s: untriaged=%s status=%s", j["key"], j["untriaged"], j["status"])
		}
		if _, ok := j["first_seen"]; !ok {
			t.Errorf("%s: no first_seen", j["key"])
		}
		if _, ok := j["age_days"]; !ok {
			t.Errorf("%s: no age: the freshest-first order must be visible", j["key"])
		}
	}
	// A triaged row keeps its numbers, zero or not, and is not flagged.
	o, _ = mustCode(t, 0, "jobs", "list", "--json", "--view", "new")
	for _, want := range []string{`"relevance":`, `"priority":`, `"row":{`} {
		if !strings.Contains(o, want) {
			t.Fatalf("a triaged row lost %s: %s", want, o)
		}
	}
	if strings.Contains(o, "untriaged") {
		t.Fatalf("a triaged row is flagged untriaged: %s", o)
	}
}

func TestJobsPendingSortsAreRankFree(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	// priority and relevance sort by scores this view does not have: the reply
	// says which order it used rather than pretend.
	for _, s := range []string{"default", "priority", "relevance"} {
		l := listJSON(t, "--view", "pending", "--sort", s)
		if keys(l) != "gh:acme:fresh gh:beta:mid gh:acme:old" || l.Sort != "fresh" {
			t.Fatalf("--sort %s: %q reported as %q", s, keys(l), l.Sort)
		}
	}
	if l := listJSON(t, "--view", "pending", "--sort", "company"); keys(l) != "gh:acme:fresh gh:acme:old gh:beta:mid" || l.Sort != "company" {
		t.Fatalf("company order %q (%s)", keys(l), l.Sort)
	}
	// Other views keep what they report.
	if l := listJSON(t, "--view", "new", "--sort", "priority"); l.Sort != "priority" {
		t.Fatalf("new view sort %q", l.Sort)
	}
}

func TestJobsPendingQueryLimitAndEmpty(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	if l := listJSON(t, "--view", "pending", "--q", " DATA "); keys(l) != "gh:beta:mid" || l.Counts["pending"] != 3 || l.Pending.Matching != 1 {
		t.Fatalf("query: %q counts %+v pending %+v", keys(l), l.Counts, l.Pending)
	}
	if l := listJSON(t, "--view", "pending", "--q", "recruiter"); l.Total != 0 || l.Jobs == nil {
		t.Fatalf("a dropped posting matched the pending view: %+v", l)
	}
	if l := listJSON(t, "--view", "pending", "--limit", "2"); l.Total != 3 || l.Shown != 2 || len(l.Jobs) != 2 {
		t.Fatalf("limit: total %d shown %d", l.Total, l.Shown)
	}
	mustCode(t, 2, "jobs", "list", "--view", "pending", "--limit", "0")
	mustCode(t, 2, "jobs", "list", "--view", "pending", "--limit", "5001")
}

func TestJobsPendingEmptyLedger(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state)
	l := listJSON(t, "--view", "pending")
	if l.Jobs == nil || len(l.Jobs) != 0 || l.Total != 0 || l.Counts["pending"] != 0 || l.Records != 0 {
		t.Fatalf("empty ledger: %+v", l)
	}
	if text, _ := mustCode(t, 0, "jobs", "list", "--view", "pending"); !strings.Contains(text, "pending: 0 of 0 shown") {
		t.Fatalf("text %q", text)
	}
}

// A marked posting leaves the pending view (hidden means hidden) and the
// footer count follows, so the two numbers the app shows never disagree.
func TestJobsPendingMarkLeavesTheView(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	o, _ := mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:fresh", "saved")
	var j jobview.Job
	if err := json.Unmarshal([]byte(o), &j); err != nil || j.Mark != marks.Saved || j.Status != posting.Live {
		t.Fatalf("mark reply %q: %v", o, err)
	}
	if !j.Untriaged || j.Row != nil || strings.Contains(o, `"priority"`) || strings.Contains(o, `"relevance"`) {
		t.Fatalf("a saved untriaged posting must stay unranked: %s", o)
	}
	mustCode(t, 0, "jobs", "mark", "gh:beta:mid", "hidden")
	l := listJSON(t, "--view", "pending")
	if keys(l) != "gh:acme:old" || l.Counts["pending"] != 1 || l.Pending.Total != 1 || l.Counts["saved"] != 1 || l.Counts["hidden"] != 1 {
		t.Fatalf("after marking: %q counts %+v pending %+v", keys(l), l.Counts, l.Pending)
	}
	saved := listJSON(t, "--view", "saved")
	if keys(saved) != "gh:acme:fresh" || !saved.Jobs[0].Untriaged {
		t.Fatalf("saved view %q: it keeps its contents, flagged unranked", keys(saved))
	}
	raw, _ := mustCode(t, 0, "jobs", "list", "--json", "--view", "saved")
	if strings.Contains(raw, `"priority"`) || strings.Contains(raw, `"row":{`) {
		t.Fatalf("a saved untriaged row looks ranked: %s", raw)
	}
	mustCode(t, 0, "jobs", "mark", "gh:acme:fresh", "clear")
	if l := listJSON(t, "--view", "pending"); l.Counts["pending"] != 2 || l.Pending.Total != 2 {
		t.Fatalf("clear returns it: %+v", l.Counts)
	}
}

func TestJobsPendingViewOnlyForList(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	for _, args := range [][]string{
		{"jobs", "stats", "--view", "pending"},
		{"jobs", "gaps", "--view", "pending"},
	} {
		_, e := mustCode(t, 2, args...)
		if !strings.Contains(e, "pending") || !strings.Contains(e, "jobs list") {
			t.Fatalf("%v: stderr %q does not say why", args, e)
		}
	}
	// Folders counts what a view holds: untriaged postings have no category.
	o, _ := mustCode(t, 0, "jobs", "folders", "--json", "--view", "pending")
	var f jobsFoldersJSON
	if err := json.Unmarshal([]byte(o), &f); err != nil || f.Total != 3 {
		t.Fatalf("folders %q: %v", o, err)
	}
	for _, it := range f.Folders {
		if (it.Category == "none") != (it.Count == 3) || (it.Category != "none" && it.Count != 0) {
			t.Fatalf("folder %+v", it)
		}
	}
}

func TestJobsPendingTextHasNoScores(t *testing.T) {
	_, state := setup(t)
	seedAwaiting(t, state)
	text, _ := mustCode(t, 0, "jobs", "list", "--view", "pending")
	if !strings.Contains(text, "Acme: Inference Engineer") || strings.Contains(text, "0.0  ") {
		t.Fatalf("text list shows a score for an untriaged posting:\n%s", text)
	}
	if !strings.Contains(text, "pending: 3 of 3 shown") {
		t.Fatalf("text %q", text)
	}
	ranked, _ := mustCode(t, 0, "jobs", "list")
	if !strings.Contains(ranked, "--view pending") {
		t.Fatalf("the ranked list's footer does not point at the pending view:\n%s", ranked)
	}
}

// Untriaged rows still show the company's cached icon.
func TestJobsPendingRowsCarryTheLogo(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--domain", "www.acme.com", "--allow-host")
	seedAwaiting(t, state)
	icon := cacheIcon(t, state, "www.acme.com.png")
	byKey := map[posting.Key]string{}
	for _, j := range listJSON(t, "--view", "pending").Jobs {
		byKey[j.Key] = j.Logo
	}
	if byKey["gh:acme:fresh"] != icon || byKey["gh:acme:old"] != icon || byKey["gh:beta:mid"] != "" {
		t.Fatalf("logos %v", byKey)
	}
}

// A row the next run's prefilter would drop before triage is flagged, from the
// same policy `vani prefilter` applies; the rule itself is never sent.
func TestJobsPendingFlagsWhatThePrefilterWouldDrop(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DropRules = []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}
	cfg.TitleSignals.Read = []config.TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}}
	cfg.Exclude.Titles = []string{"sommelier"}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedJobs(t, state,
		seedJob{key: "gh:acme:eng", title: "Inference Engineer", status: posting.Live},
		seedJob{key: "gh:acme:mgr", title: "Engineering Manager", status: posting.Live, age: time.Hour},
		seedJob{key: "gh:acme:wine", title: "Head Sommelier", status: posting.Live, age: 2 * time.Hour},
		seedJob{key: "gh:acme:savedwine", title: "Sommelier Engineer", status: posting.Live, age: 3 * time.Hour},
	)
	// Exclusions and title readings are the prefilter; a posting the owner
	// marked is never dropped by it (protectMarked), so it is not flagged.
	mustCode(t, 0, "jobs", "mark", "gh:acme:savedwine", "saved")
	flags := func(view string) (map[posting.Key]string, string) {
		o, _ := mustCode(t, 0, "jobs", "list", "--json", "--view", view)
		var l jobview.List
		if err := json.Unmarshal([]byte(o), &l); err != nil {
			t.Fatal(err)
		}
		got := map[posting.Key]string{}
		for _, j := range l.Jobs {
			got[j.Key] = j.PrefilterDrop
		}
		return got, o
	}
	got, o := flags("pending")
	if got["gh:acme:mgr"] != "title" || got["gh:acme:wine"] != "excluded" || got["gh:acme:eng"] != "" || len(got) != 3 {
		t.Fatalf("prefilter flags %v", got)
	}
	if saved, _ := flags("saved"); saved["gh:acme:savedwine"] != "" || len(saved) != 1 {
		t.Fatalf("a marked posting is flagged: %v", saved)
	}
	// The reply names the kind only: not the slot, the matched phrase or the
	// reason text (the titles themselves are posting data).
	for _, leak := range []string{"seniority", "slot", "excluded:", "matched", "min_openrouter"} {
		if strings.Contains(o, leak) {
			t.Fatalf("%q leaked into the reply: %s", leak, o)
		}
	}
}

// `jobs find` answers for a live posting too, without a row (which would carry
// scores it does not have): the extension then keeps its labelled estimate.
func TestJobsFindAnswersForAnUntriagedPostingWithoutARow(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:a:live", url: "https://careers.example.com/open/1", status: posting.Live},
		findSeed{key: "gh:a:kept", url: "https://careers.example.com/open/2", status: posting.Digested},
	)
	live := findJSON(t, "https://careers.example.com/open/1")
	if live.Job == nil || live.Job.Key != "gh:a:live" || live.Job.Status != posting.Live || live.Job.Row != nil {
		t.Fatalf("live posting: %+v", live.Job)
	}
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", "https://careers.example.com/open/1")
	if strings.Contains(o, `"row"`) || strings.Contains(o, `"priority"`) || strings.Contains(o, `"relevance"`) {
		t.Fatalf("a live posting's find reply carries a ranking: %s", o)
	}
	if kept := findJSON(t, "https://careers.example.com/open/2"); kept.Job == nil || kept.Job.Row == nil {
		t.Fatalf("a triaged posting keeps its row: %+v", kept.Job)
	}
}

// 10,000 postings awaiting triage: the reply is bounded by --limit and the pass
// does a bounded amount of work per posting (no keyword extraction, no scoring).
func TestJobsPendingScalesTo10kLiveRecords(t *testing.T) {
	snap := scaleSnapshot(10000)
	for i := range snap.Records {
		snap.Records[i].Status = posting.Live
		snap.Records[i].Annotations = nil
	}
	e := scaleState(t, snap)
	var buf bytes.Buffer
	e.stdout = &buf
	if err := e.jobsList([]string{"--json", "--view", "pending", "--limit", "1000"}); err != nil {
		t.Fatal(err)
	}
	var out jobview.List
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 10000 || out.Shown != 1000 || len(out.Jobs) != 1000 || out.Counts["pending"] != 10000 || out.Pending.Total != 10000 {
		t.Fatalf("total %d shown %d counts %v pending %d", out.Total, out.Shown, out.Counts, out.Pending.Total)
	}
	if buf.Len() > 2<<20 {
		t.Fatalf("a 1,000-row reply is %d bytes: descriptions must not ride along", buf.Len())
	}
	for _, j := range out.Jobs {
		if !j.Untriaged || j.Row != nil || j.Keywords != nil || j.Priority != 0 {
			t.Fatalf("%s was scored: %+v", j.Key, j)
		}
	}
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	o := jobsOpts{Opts: jobview.Opts{View: "pending", Sort: "default", HalfLife: jobinsight.DefaultScoreHalfLifeDays}}
	in := scaleInsight(v, snap)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	sel, _ := jobview.Select(snap, nil, &o.Opts, in, listKeywords)
	jobview.Sort(sel, o.View, o.Sort)
	runtime.ReadMemStats(&after)
	if len(sel) != 10000 {
		t.Fatalf("selected %d", len(sel))
	}
	if per := (after.Mallocs - before.Mallocs) / 10000; per > 200 {
		t.Errorf("%d allocations per untriaged posting: it is being scored or tokenised", per)
	}
}
