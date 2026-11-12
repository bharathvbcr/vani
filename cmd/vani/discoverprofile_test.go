package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/discover/freehire"
	"github.com/bharathvbcr/Vani/internal/discover/searchplan"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// fhJob is one result in a fake freehire page.
type fhJob struct{ source, id, url, company, title string }

func fhBody(total int, jobs ...fhJob) []byte {
	rows := make([]map[string]string, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, map[string]string{"source": j.source, "external_id": j.id, "url": j.url, "company": j.company, "title": j.title})
	}
	b, _ := json.Marshal(map[string]any{"data": rows, "meta": map[string]int{"limit": 100, "offset": 0, "total": total}})
	return b
}

// profileFake answers each search by its q through answer and records the q
// of every request, in order.
type profileFake struct {
	mu   sync.Mutex
	qs   []string
	doer *srctest.Doer
}

func (f *profileFake) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.qs)
}

func fakeProfileFreehire(t *testing.T, answer func(q string) srctest.Reply) *profileFake {
	t.Helper()
	f := &profileFake{}
	f.doer = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != freehire.Host || u.Path != "/api/v1/jobs/search" {
			return srctest.Reply{}, false
		}
		q := u.Query().Get("q")
		f.mu.Lock()
		f.qs = append(f.qs, q)
		f.mu.Unlock()
		return answer(q), true
	})
	oldDoer, oldSleep := newDiscoverDoer, discoverSleep
	newDiscoverDoer = func() (egress.Doer, error) { return f.doer, nil }
	discoverSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { newDiscoverDoer, discoverSleep = oldDoer, oldSleep })
	return f
}

// fakeStacks replaces the knowledge-base read with stacks (or err).
func fakeStacks(t *testing.T, stacks []string, err error) {
	t.Helper()
	old := profileStacks
	profileStacks = func(config.Config) ([]string, error) { return stacks, err }
	t.Cleanup(func() { profileStacks = old })
}

// profileSetup is an initialised config with the owner's title rules, and the
// greenhouse and lever API hosts allowed (as a configured owner has them).
func profileSetup(t *testing.T, edit func(*config.Config)) string {
	t.Helper()
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cfg := watchlistOf(t, cfgPath)
	cfg.TitleSignals = config.TitleSignals{Include: []string{"Inference", "ML", "LLM Engineer"}, Exclude: []string{"Intern"}}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "boards-api.greenhouse.io", "api.lever.co")
	if edit != nil {
		edit(&cfg)
	}
	writeConfig(t, cfgPath, cfg)
	return cfgPath
}

// The stacks: Rust in four entries, CUDA in three, and two project names in
// one each. Only Rust and CUDA may ever be sent.
var profileTestStacks = []string{"Rust, CUDA, ProjectAurora", "Rust; CUDA", "Rust, CUDA", "Rust, [ProjectBorealis](https://x.test/b)"}

func profileDocOf(t *testing.T, out string) profileDiscoverJSON {
	t.Helper()
	var doc profileDiscoverJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("not the profile discover JSON: %v\n%s", err, out)
	}
	return doc
}

func profileBoardIn(doc profileDiscoverJSON, ats, token string) (profileBoardJSON, bool) {
	for _, b := range doc.Boards {
		if b.ATS == ats && strings.EqualFold(b.Token, token) {
			return b, true
		}
	}
	return profileBoardJSON{}, false
}

func TestDiscoverProfileDryRunAsksNothing(t *testing.T) {
	cfgPath := profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	f := fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 500} })
	before := watchlistOf(t, cfgPath)

	// freehire.me is not allowed, and a dry run needs it no more than it asks it.
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--dry-run")
	for _, want := range []string{
		"profile searches (4)", "role     Inference", "role     LLM Engineer", "kb_skill Rust  (4 entries)",
		"kb_skill CUDA  (3 entries)", `left out: role "ML": ` + searchplan.WhyShort,
		"2 skill(s) listed by fewer than 3 knowledge-base entries were not searched (not named)",
		"--dry-run: nothing was asked",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	for _, secret := range []string{"ProjectAurora", "ProjectBorealis", "x.test"} {
		if strings.Contains(o, secret) {
			t.Errorf("output names %q:\n%s", secret, o)
		}
	}
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile", "--dry-run", "--json")
	doc := profileDocOf(t, o)
	if !doc.DryRun || !doc.KBUsed || len(doc.Searches) != 4 || doc.SkillsBelowFloor != 2 || doc.MinEntries != 3 ||
		len(doc.Boards) != 0 || doc.Boards == nil || doc.Added == nil || doc.Searches[0].Read {
		t.Errorf("doc %+v", doc)
	}
	if n := len(f.asked()); n != 0 {
		t.Errorf("%d requests on a dry run", n)
	}
	after := watchlistOf(t, cfgPath)
	if !slices.Equal(before.AllowedHosts, after.AllowedHosts) || len(after.Watchlist) != 0 {
		t.Errorf("a dry run changed the config: hosts %v, %d boards", after.AllowedHosts, len(after.Watchlist))
	}
}

// What leaves the machine is exactly the planned searches, one request each,
// with the filters: never a skill under the floor, never anything else.
func TestDiscoverProfileSendsOnlyThePlannedSearches(t *testing.T) {
	profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	f := fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 200, Body: fhBody(0)} })
	mustCode(t, 0, "watchlist", "discover", "--profile", "--country", "US", "--allow-host", "--json")
	if got, want := f.asked(), []string{"Inference", "LLM Engineer", "Rust", "CUDA"}; !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	for _, u := range f.doer.URLs() {
		pu, _ := url.Parse(u)
		q := pu.Query()
		if q.Get("countries") != "US" || q.Get("limit") != "100" || q.Get("offset") != "0" || len(q) != 4 {
			t.Errorf("request %s", u)
		}
		for _, secret := range []string{"Aurora", "Borealis", "x.test"} {
			if strings.Contains(u, secret) {
				t.Errorf("request %s carries %q", u, secret)
			}
		}
	}
	// --no-kb sends the role words alone, and the knowledge base is not read.
	fakeStacks(t, nil, errors.New("must not be read"))
	f = fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 200, Body: fhBody(0)} })
	o, e := mustCode(t, 0, "watchlist", "discover", "--profile", "--no-kb", "--json")
	if got := f.asked(); !slices.Equal(got, []string{"Inference", "LLM Engineer"}) || strings.Contains(e, "must not be read") {
		t.Errorf("--no-kb asked %v, stderr %q", got, e)
	}
	if doc := profileDocOf(t, o); doc.KBUsed || doc.KBError != "" {
		t.Errorf("--no-kb doc kb_used %v kb_error %q", doc.KBUsed, doc.KBError)
	}
}

// profileResults is a two-search world: the boards, the off-profile titles
// the owner's rules leave out, a repeat, and a source Vani cannot read.
func profileResults(q string) srctest.Reply {
	acme1 := fhJob{"greenhouse", "acme:1", "https://boards.greenhouse.io/acme/jobs/1", "Acme", "Inference Engineer"}
	wd := fhJob{"workday", "w1", "https://r1rcm.wd1.myworkdayjobs.com/en-US/R1RCM/job/X/Inference-Engineer_R1", "R1", "ML Inference Engineer"}
	switch q {
	case "Inference":
		return srctest.Reply{Status: 200, Body: fhBody(900,
			acme1, wd,
			fhJob{"greenhouse", "acme:2", "https://boards.greenhouse.io/acme/jobs/2", "Acme", "Sales Manager"},
			fhJob{"lever", "b1", "https://jobs.lever.co/beta/b1", "Beta", "LLM Engineer"},
			fhJob{"greenhouse", "badco:1", "https://boards.greenhouse.io/badco/jobs/1", "BadCo", "Inference Engineer"},
			fhJob{"jazzhr", "j1", "https://x.applytojob.com/apply/j1", "Jazz", "Inference Engineer"},
		)}
	case "Rust":
		return srctest.Reply{Status: 200, Body: fhBody(50,
			acme1, wd,
			fhJob{"greenhouse", "gamma:1", "https://boards.greenhouse.io/gamma/jobs/1", "Gamma", "Rust Inference Engineer"},
			fhJob{"greenhouse", "acme:3", "https://boards.greenhouse.io/acme/jobs/3", "Acme", "Inference Intern"},
			fhJob{"greenhouse", "staff:1", "https://boards.greenhouse.io/staffco/jobs/1", "StaffCo", "Staff Inference Engineer"},
		)}
	}
	return srctest.Reply{Status: 200, Body: fhBody(0)}
}

// profileRules are the owner's rules profileResults is judged by: two role
// words, an excluded company, and a Staff title read and dropped.
func profileRules(c *config.Config) {
	c.TitleSignals.Include = []string{"Inference", "LLM Engineer"}
	c.Exclude = config.Exclude{Companies: []string{"BadCo"}}
	c.TitleSignals.Read = []config.TitleRead{{Slot: "seniority", Value: "staff_plus", Match: []string{"Staff"}}}
	c.DropRules = append(c.DropRules, config.DropRule{Slot: "seniority", Values: []string{"staff_plus"}})
}

func TestDiscoverProfileMergesAndKeepsOnlyOnProfileTitles(t *testing.T) {
	profileSetup(t, profileRules)
	fakeStacks(t, []string{"Rust", "Rust", "Rust"}, nil)
	f := fakeProfileFreehire(t, profileResults)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--json")
	if got := f.asked(); !slices.Equal(got, []string{"Inference", "LLM Engineer", "Rust"}) {
		t.Fatalf("asked %v", got)
	}
	doc := profileDocOf(t, o)
	// Off-profile: Sales Manager (no include word), BadCo (excluded company),
	// Inference Intern (title_signals.exclude), Staff (a drop rule read from
	// the title).
	if m := doc.Meta; m.OffProfile != 4 || m.Kept != 7 || m.Repeated != 2 || m.Distinct != 5 {
		t.Errorf("meta %+v", m)
	}
	if s := doc.Searches[0]; !s.Read || s.Total != 900 || s.Fetched != 6 || s.Kept != 4 || s.Error != "" {
		t.Errorf("first search %+v", s)
	}
	for _, b := range doc.Boards {
		if b.Token == "badco" || b.Token == "staffco" {
			t.Errorf("an off-profile board is listed: %+v", b)
		}
	}
	acme, ok := profileBoardIn(doc, "gh", "acme")
	if !ok || acme.Postings != 1 || !slices.Equal(acme.FoundBy, []string{"Inference", "Rust"}) || acme.Watched ||
		acme.AddCommand != "vani watchlist add --ats gh --token acme --company Acme" {
		t.Errorf("acme %+v", acme)
	}
	wd, ok := profileBoardIn(doc, "wd", "r1rcm/R1RCM")
	if want := "vani watchlist add --ats wd --token r1rcm/R1RCM --company R1 --host r1rcm.wd1.myworkdayjobs.com --search Inference --search Rust --allow-host"; !ok ||
		!slices.Equal(wd.Searches, []string{"Inference", "Rust"}) || wd.AddCommand != want {
		t.Errorf("workday %+v\nwant command %q", wd, want)
	}
	// Two searches outrank one.
	if len(doc.Boards) != 4 || len(doc.Boards[0].FoundBy) != 2 || len(doc.Boards[1].FoundBy) != 2 || len(doc.Boards[2].FoundBy) != 1 {
		t.Errorf("boards %+v", doc.Boards)
	}
	if !slices.Contains(doc.Unreadable, unreadableJSON{Source: "jazzhr", Reason: freehire.ReasonNoAdapter, Count: 1}) {
		t.Errorf("unreadable %+v", doc.Unreadable)
	}
	// The text form says the same.
	f = fakeProfileFreehire(t, profileResults)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile")
	for _, want := range []string{
		"role     Inference  read 6 of 900, 4 on-profile",
		"7 on-profile result(s), 5 distinct across searches; 4 off-profile title(s) left out by your title rules",
		"  2 search(es)    1  gh  acme  Acme  [new]",
		"      e.g. Inference Engineer",
		"jazzhr  " + freehire.ReasonNoAdapter,
	} {
		if !strings.Contains(o, want) {
			t.Errorf("text lacks %q:\n%s", want, o)
		}
	}
}

// The owner's location exclusions judge a result's location as a run judges a
// listing's: outside only_locations is off-profile, no location is kept.
func TestDiscoverProfileAppliesLocationExclusions(t *testing.T) {
	profileSetup(t, func(c *config.Config) {
		c.TitleSignals.Include = []string{"Inference"}
		c.Exclude = config.Exclude{OnlyLocations: []string{"United States"}}
	})
	body, _ := json.Marshal(map[string]any{
		"meta": map[string]int{"total": 3},
		"data": []map[string]string{
			{"source": "greenhouse", "external_id": "us:1", "url": "https://boards.greenhouse.io/usco/jobs/1", "company": "USCo", "title": "Inference Engineer", "location": "Remote, US-Based"},
			{"source": "greenhouse", "external_id": "de:1", "url": "https://boards.greenhouse.io/deco/jobs/1", "company": "DECo", "title": "Inference Engineer", "location": "Berlin, Germany"},
			{"source": "greenhouse", "external_id": "nl:1", "url": "https://boards.greenhouse.io/noloc/jobs/1", "company": "NoLoc", "title": "Inference Engineer"},
		},
	})
	fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 200, Body: body} })
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--no-kb", "--allow-host", "--json")
	doc := profileDocOf(t, o)
	var tokens []string
	for _, b := range doc.Boards {
		tokens = append(tokens, b.Token)
	}
	slices.Sort(tokens)
	if !slices.Equal(tokens, []string{"noloc", "usco"}) || doc.Meta.OffProfile != 1 {
		t.Errorf("boards %v, off-profile %d", tokens, doc.Meta.OffProfile)
	}
}

// A 429 ends the run: what was read stands, later searches are not asked.
func TestDiscoverProfileRateLimitStopsTheRun(t *testing.T) {
	profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	f := fakeProfileFreehire(t, func(q string) srctest.Reply {
		if q == "LLM Engineer" {
			return srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"3600"}}}
		}
		return profileResults(q)
	})
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--json")
	if got := f.asked(); !slices.Equal(got, []string{"Inference", "LLM Engineer"}) {
		t.Errorf("asked %v after a 429", got)
	}
	doc := profileDocOf(t, o)
	if !strings.Contains(doc.Stopped, "rate limited") || !strings.Contains(doc.Stopped, `"LLM Engineer"`) ||
		doc.Searches[1].Error == "" || doc.Searches[2].Read || len(doc.Boards) == 0 {
		t.Errorf("stopped %q searches %+v", doc.Stopped, doc.Searches)
	}
}

func TestDiscoverProfileFailures(t *testing.T) {
	// Every search failing is an error, not an empty result.
	cfgPath := profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	f := fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 500} })
	_, e := mustCode(t, 1, "watchlist", "discover", "--profile", "--allow-host")
	if !strings.Contains(e, "no search could be read") || !strings.Contains(e, "HTTP 500") || len(f.asked()) != 4 {
		t.Errorf("stderr %q after %d requests", e, len(f.asked()))
	}
	// One failing search is reported on its row; the others stand.
	fakeProfileFreehire(t, func(q string) srctest.Reply {
		if q == "Rust" {
			return srctest.Reply{Status: 200, Body: []byte("<html>")}
		}
		return profileResults(q)
	})
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--json")
	doc := profileDocOf(t, o)
	if r := doc.Searches[2]; r.Text != "Rust" || r.Read || !strings.Contains(r.Error, "not the search envelope") || doc.Stopped != "" || doc.Searches[3].Read != true {
		t.Errorf("searches %+v stopped %q", doc.Searches, doc.Stopped)
	}
	// The endpoint gone (404) is the whole command's error.
	fakeProfileFreehire(t, func(string) srctest.Reply { return srctest.Reply{Status: 404} })
	if _, e := mustCode(t, 1, "watchlist", "discover", "--profile"); !strings.Contains(e, "search endpoint was not found") {
		t.Errorf("404: %q", e)
	}
	// freehire.me not allowed and no --allow-host: refused before any request.
	cfg := watchlistOf(t, cfgPath)
	cfg.AllowedHosts = slices.DeleteFunc(cfg.AllowedHosts, func(h string) bool { return h == freehire.Host })
	writeConfig(t, cfgPath, cfg)
	f = fakeProfileFreehire(t, profileResults)
	if _, e := mustCode(t, 1, "watchlist", "discover", "--profile"); !strings.Contains(e, "--allow-host") || len(f.asked()) != 0 {
		t.Errorf("not allowed: %q, %d requests", e, len(f.asked()))
	}
	if len(watchlistOf(t, cfgPath).Watchlist) != 0 {
		t.Error("a failed discovery changed the watchlist")
	}
}

// A knowledge base that cannot be read says so and the role words still run;
// with no role words either, there is nothing to search and that is an error.
func TestDiscoverProfileWithoutTheKnowledgeBase(t *testing.T) {
	cfgPath := profileSetup(t, nil)
	fakeStacks(t, nil, errors.New("kb.selections is empty"))
	f := fakeProfileFreehire(t, profileResults)
	o, e := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--json")
	doc := profileDocOf(t, o)
	if !strings.Contains(e, "knowledge base not read, so no skill searches: kb.selections is empty") ||
		doc.KBUsed || doc.KBError != "kb.selections is empty" || !slices.Equal(f.asked(), []string{"Inference", "LLM Engineer"}) {
		t.Errorf("stderr %q doc kb %v %q asked %v", e, doc.KBUsed, doc.KBError, f.asked())
	}
	cfg := watchlistOf(t, cfgPath)
	cfg.TitleSignals = config.TitleSignals{Include: []string{"ML"}}
	writeConfig(t, cfgPath, cfg)
	f = fakeProfileFreehire(t, profileResults)
	_, e = mustCode(t, 1, "watchlist", "discover", "--profile")
	if !strings.Contains(e, "no searches to make") || !strings.Contains(e, "kb.selections is empty") || len(f.asked()) != 0 {
		t.Errorf("no searches: %q", e)
	}
	// The knowledge base read but no skill over the floor.
	fakeStacks(t, []string{"Rust", "Go"}, nil)
	_, e = mustCode(t, 1, "watchlist", "discover", "--profile")
	if !strings.Contains(e, "no skill is listed by 3 or more knowledge-base entries") {
		t.Errorf("below floor: %q", e)
	}
}

// --add adds the top N new boards in rank order through addBoard. A board on a
// host not yet allowed is added only with --allow-board-hosts, which adds that
// host and no other.
func TestDiscoverProfileAdd(t *testing.T) {
	// BadCo is excluded and StaffCo's only title is dropped, so neither is a
	// candidate, let alone added.
	cfgPath := profileSetup(t, profileRules)
	fakeStacks(t, []string{"Rust", "Rust", "Rust"}, nil)
	fakeProfileFreehire(t, profileResults)
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "gamma", "--company", "Gamma")
	hostsBefore := watchlistOf(t, cfgPath).AllowedHosts

	// Ranked: acme (2 searches), the Workday board (2), beta, gamma (watched).
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--add", "2", "--json")
	doc := profileDocOf(t, o)
	if len(doc.Added) != 2 || doc.Added[0].Token != "acme" || doc.Added[1].Token != "beta" {
		t.Fatalf("added %+v", doc.Added)
	}
	if len(doc.NotAdded) != 1 || doc.NotAdded[0].Token != "r1rcm/R1RCM" || !strings.Contains(doc.NotAdded[0].Why, "r1rcm.wd1.myworkdayjobs.com") ||
		!strings.Contains(doc.NotAdded[0].Why, "--allow-board-hosts") {
		t.Errorf("not added %+v", doc.NotAdded)
	}
	if a, _ := profileBoardIn(doc, "gh", "acme"); !a.Added || a.Watched || a.AddCommand != "" {
		t.Errorf("acme %+v", a)
	}
	if g, _ := profileBoardIn(doc, "gh", "gamma"); !g.Watched || g.Added {
		t.Errorf("gamma %+v", g)
	}
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 3 {
		t.Errorf("watchlist has %d boards, want 3", len(cfg.Watchlist))
	}
	if !slices.Equal(cfg.AllowedHosts, append(slices.Clone(hostsBefore), freehire.Host)) {
		t.Errorf("hosts %v -> %v: --add without --allow-board-hosts widened egress", hostsBefore, cfg.AllowedHosts)
	}

	// Again with --allow-board-hosts: the boards already added are skipped and
	// the Workday board joins with its searches and its one host.
	fakeProfileFreehire(t, profileResults)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile", "--add", "5", "--allow-board-hosts", "--json")
	doc = profileDocOf(t, o)
	if len(doc.Added) != 1 || doc.Added[0].Token != "r1rcm/R1RCM" || !slices.Equal(doc.Added[0].Searches, []string{"Inference", "Rust"}) {
		t.Fatalf("added %+v", doc.Added)
	}
	cfg = watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 4 || countOf(cfg.AllowedHosts, "r1rcm.wd1.myworkdayjobs.com") != 1 || len(cfg.AllowedHosts) != len(hostsBefore)+2 {
		t.Errorf("watchlist %d, hosts %v", len(cfg.Watchlist), cfg.AllowedHosts)
	}

	// Nothing left to add: the config is not rewritten and the run still reports.
	fakeProfileFreehire(t, profileResults)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile", "--add", "5", "--allow-board-hosts", "--json")
	if doc = profileDocOf(t, o); len(doc.Added) != 0 || len(doc.Boards) != 4 {
		t.Errorf("third run added %+v", doc.Added)
	}
	if len(watchlistOf(t, cfgPath).Watchlist) != 4 {
		t.Error("third run changed the watchlist")
	}
}

// A role search puts its own word in every title it finds, so a board only
// that word matched is weak: listed, ranked after the corroborated ones, and
// never added by --add. Live, "Agent" found a gym chain's front-desk roles.
func TestDiscoverProfileWeakBoardsAreNotAdded(t *testing.T) {
	cfgPath := profileSetup(t, func(c *config.Config) { c.TitleSignals.Include = []string{"Agent", "Inference"} })
	fakeStacks(t, nil, nil)
	body := fhBody(3,
		fhJob{"greenhouse", "gym:1", "https://boards.greenhouse.io/gym/jobs/1", "Gym", "Agent d'accueil"},
		fhJob{"greenhouse", "gym:2", "https://boards.greenhouse.io/gym/jobs/2", "Gym", "Agent polyvalent"},
		fhJob{"greenhouse", "good:1", "https://boards.greenhouse.io/good/jobs/1", "Good", "Agent Inference Engineer"},
	)
	fakeProfileFreehire(t, func(q string) srctest.Reply {
		if q == "Agent" {
			return srctest.Reply{Status: 200, Body: body}
		}
		return srctest.Reply{Status: 200, Body: fhBody(0)}
	})
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--add", "5", "--json")
	doc := profileDocOf(t, o)
	if len(doc.Added) != 1 || doc.Added[0].Token != "good" || doc.Meta.Weak != 1 {
		t.Fatalf("added %+v, weak %d", doc.Added, doc.Meta.Weak)
	}
	gym, _ := profileBoardIn(doc, "gh", "gym")
	good, _ := profileBoardIn(doc, "gh", "good")
	if gym.Corroborated || gym.Evidence != 0 || gym.Added || gym.AddCommand == "" || !good.Corroborated || good.Evidence != 1 ||
		doc.Boards[0].Token != "good" {
		t.Errorf("gym %+v good %+v first %q", gym, good, doc.Boards[0].Token)
	}
	if n := len(watchlistOf(t, cfgPath).Watchlist); n != 1 {
		t.Errorf("watchlist has %d boards", n)
	}
	fakeProfileFreehire(t, func(q string) srctest.Reply {
		if q == "Agent" {
			return srctest.Reply{Status: 200, Body: body}
		}
		return srctest.Reply{Status: 200, Body: fhBody(0)}
	})
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile")
	if !strings.Contains(o, "gh  gym  Gym  [new, weak]") || !strings.Contains(o, "1 board(s) are weak") {
		t.Errorf("text:\n%s", o)
	}
}

// mirrorBody is one employer on two Greenhouse boards: stone lists three
// postings, jornada two of them under its own ids (live, Stone's second board
// did this on 2026-11-11), and an unrelated board one of the same titles.
func mirrorBody() []byte {
	row := func(token, id, company, title, loc string) map[string]string {
		return map[string]string{"source": "greenhouse", "external_id": token + ":" + id, "url": "https://boards.greenhouse.io/" + token + "/jobs/" + id,
			"company": company, "title": title, "location": loc}
	}
	b, _ := json.Marshal(map[string]any{"meta": map[string]int{"total": 6}, "data": []map[string]string{
		row("stone", "1", "Stone", "LLM Inference Engineer", "Florianopolis"),
		row("stone", "2", "Stone", "Inference Platform Engineer", "Florianopolis"),
		row("stone", "3", "Stone", "Inference SRE", "Florianopolis"),
		row("jornada", "71", "Jornada Stone", "LLM Inference Engineer", "Florianopolis"),
		row("jornada", "72", "Jornada Stone", "inference  platform engineer ", "florianopolis"),
		row("other", "9", "Other", "LLM Inference Engineer", "Florianopolis"),
	}})
	return b
}

// A board that lists only postings a bigger board lists is folded into it:
// shown under it, never added by --add, and a group already watched through
// its mirror is not added again through its keeper.
func TestDiscoverProfileFoldsMirrorBoards(t *testing.T) {
	answer := func(q string) srctest.Reply {
		if q == "Inference" {
			return srctest.Reply{Status: 200, Body: mirrorBody()}
		}
		return srctest.Reply{Status: 200, Body: fhBody(0)}
	}
	cfgPath := profileSetup(t, func(c *config.Config) { c.TitleSignals.Include = []string{"Inference", "LLM"} })
	fakeStacks(t, nil, nil)
	fakeProfileFreehire(t, answer)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--add", "5", "--json")
	doc := profileDocOf(t, o)
	var added []string
	for _, b := range doc.Added {
		added = append(added, b.Token)
	}
	slices.Sort(added)
	if !slices.Equal(added, []string{"other", "stone"}) || doc.Meta.Mirrored != 1 {
		t.Fatalf("added %v, mirrored %d", added, doc.Meta.Mirrored)
	}
	if _, ok := profileBoardIn(doc, "gh", "jornada"); ok {
		t.Error("the mirror is listed as a board of its own")
	}
	st, _ := profileBoardIn(doc, "gh", "stone")
	if len(st.Mirrors) != 1 || st.Mirrors[0].Token != "jornada" || st.Mirrors[0].Company != "Jornada Stone" || st.Mirrors[0].Postings != 2 ||
		st.Mirrors[0].Shared != 2 || st.Mirrors[0].Watched || st.Mirrors[0].AddCommand != "vani watchlist add --ats gh --token jornada --company 'Jornada Stone'" {
		t.Errorf("stone %+v", st)
	}
	if ot, _ := profileBoardIn(doc, "gh", "other"); ot.Mirrors == nil || len(ot.Mirrors) != 0 {
		t.Errorf("other's mirrors %#v, want an empty list", ot.Mirrors)
	}

	// The owner watches the mirror: the keeper is shown watched through it and
	// --add leaves the group alone.
	cfgPath = profileSetup(t, func(c *config.Config) { c.TitleSignals.Include = []string{"Inference", "LLM"} })
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "jornada", "--company", "Jornada Stone")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "other", "--company", "Other")
	fakeProfileFreehire(t, answer)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile", "--allow-host", "--add", "5", "--json")
	doc = profileDocOf(t, o)
	st, _ = profileBoardIn(doc, "gh", "stone")
	if len(doc.Added) != 0 || !st.Watched || st.WatchedAs != "gh/jornada" || st.AddCommand != "" || !st.Mirrors[0].Watched ||
		st.Mirrors[0].AddCommand != "" {
		t.Errorf("added %+v, stone %+v", doc.Added, st)
	}
	if n := len(watchlistOf(t, cfgPath).Watchlist); n != 2 {
		t.Errorf("watchlist has %d boards, want 2", n)
	}
	fakeProfileFreehire(t, answer)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile")
	for _, want := range []string{
		"gh  stone  Stone  [watched as gh/jornada]",
		"      same postings on: gh jornada (Jornada Stone, 2) [watched]",
		"1 board(s) list only postings a bigger board lists and are shown under it",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("text lacks %q:\n%s", want, o)
		}
	}
}

// --max-searches below the skills default narrows the skills to fit; only a
// --max-skills given above --max-searches is refused.
func TestDiscoverProfileFewSearches(t *testing.T) {
	profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--profile", "--dry-run", "--max-searches", "2", "--json")
	doc := profileDocOf(t, o)
	if len(doc.Searches) != 2 || doc.Searches[0].From != searchplan.Skill || doc.Searches[1].Text != "CUDA" {
		t.Errorf("searches %+v", doc.Searches)
	}
	o, _ = mustCode(t, 0, "watchlist", "discover", "--profile", "--dry-run", "--max-searches", "2", "--max-skills", "0", "--json")
	if doc := profileDocOf(t, o); len(doc.Searches) != 2 || doc.Searches[1].Text != "LLM Engineer" {
		t.Errorf("no skills: %+v", doc.Searches)
	}
}

func TestDiscoverProfileUsageErrors(t *testing.T) {
	profileSetup(t, nil)
	fakeStacks(t, profileTestStacks, nil)
	f := fakeProfileFreehire(t, profileResults)
	for _, args := range [][]string{
		{"--profile", "--search", "x"},
		{"--search", "x", "--add", "1"},
		{"--search", "x", "--dry-run"},
		{"--search", "x", "--no-kb"},
		{"--search", "x", "--max-searches", "4"},
		{"--profile", "--add", "51"},
		{"--profile", "--add", "-1"},
		{"--profile", "--add", "1", "--dry-run"},
		{"--profile", "--allow-board-hosts"},
		{"--profile", "--max-searches", "0"},
		{"--profile", "--max-searches", fmt.Sprint(searchplan.MaxSearches + 1)},
		{"--profile", "--max-skills", "-1"},
		{"--profile", "--max-searches", "4", "--max-skills", "5"},
		{"--profile", "--pages", "4"},
		{"--profile", "--country", "USA"},
		{"--profile", "--work-mode", "anywhere"},
	} {
		full := append([]string{"watchlist", "discover"}, args...)
		if code, _, e := do(t, append(full, "--allow-host")...); code != 2 {
			t.Errorf("vani %v: exit %d, want 2 (stderr %q)", full, code, e)
		}
	}
	if n := len(f.asked()); n != 0 {
		t.Errorf("%d requests for refused flags", n)
	}
}
