package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/discover/freehire"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// discoverFixture is a real freehire.me search page (2026-10-28), reduced to
// the fields Vani reads.
func discoverFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../internal/discover/freehire/testdata/search_ml_engineer.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeFreehire replaces the discover egress client with a fake that answers
// every search with body (status 200) and records the requests.
func fakeFreehire(t *testing.T, status int, body []byte, header http.Header) *srctest.Doer {
	t.Helper()
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != freehire.Host || u.Path != "/api/v1/jobs/search" {
			return srctest.Reply{}, false
		}
		return srctest.Reply{Status: status, Body: body, Header: header}, true
	})
	oldDoer, oldSleep := newDiscoverDoer, discoverSleep
	newDiscoverDoer = func() (egress.Doer, error) { return d, nil }
	discoverSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { newDiscoverDoer, discoverSleep = oldDoer, oldSleep })
	return d
}

func discoverJSONOf(t *testing.T, out string) discoverJSON {
	t.Helper()
	var doc discoverJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("not the discover JSON: %v\n%s", err, out)
	}
	return doc
}

func boardIn(doc discoverJSON, ats, token string) (discoverBoardJSON, bool) {
	for _, b := range doc.Boards {
		if b.ATS == ats && strings.EqualFold(b.Token, token) {
			return b, true
		}
	}
	return discoverBoardJSON{}, false
}

// freehire.me is a third-party host the owner opts into: without it in
// allowed_hosts the command refuses, names the host and the flag, and asks nothing.
func TestDiscoverRefusesWhenTheHostIsNotAllowed(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	d := fakeFreehire(t, 200, discoverFixture(t), nil)
	_, e := mustCode(t, 1, "watchlist", "discover", "--search", "ml engineer")
	if !strings.Contains(e, "freehire.me") || !strings.Contains(e, "allowed_hosts") || !strings.Contains(e, "--allow-host") {
		t.Errorf("refusal %q", e)
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("%d requests before the host was allowed", n)
	}
	if slices.Contains(watchlistOf(t, cfgPath).AllowedHosts, freehire.Host) {
		t.Error("a refused discover added the host")
	}
}

func TestDiscoverAllowHostAddsItTheWayWatchlistAddDoes(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	d := fakeFreehire(t, 200, discoverFixture(t), nil)
	o, e := mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--allow-host")
	if !strings.Contains(e, "added freehire.me to allowed_hosts") || !strings.Contains(o, "read 100 of 1340 matches") {
		t.Errorf("stdout %q stderr %q", o, e)
	}
	cfg := watchlistOf(t, cfgPath)
	if n := countOf(cfg.AllowedHosts, freehire.Host); n != 1 {
		t.Errorf("allowed_hosts %v holds the host %d times", cfg.AllowedHosts, n)
	}
	if len(d.URLs()) != 1 {
		t.Errorf("%d requests, want one", len(d.URLs()))
	}
	// Once allowed it needs no flag, and the flag does not add it twice.
	mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer")
	_, e = mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--allow-host")
	if strings.Contains(e, "added") || countOf(watchlistOf(t, cfgPath).AllowedHosts, freehire.Host) != 1 {
		t.Errorf("second --allow-host: %q", e)
	}
}

func TestDiscoverJSONShapeAndWatchedMarking(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Noetik", "--company", "Noetik", "--allow-host")
	fakeFreehire(t, 200, discoverFixture(t), nil)
	before := watchlistOf(t, cfgPath)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--country", "us", "--work-mode", "remote",
		"--allow-host", "--json")

	// Stdout is the one JSON document, nothing else.
	doc := discoverJSONOf(t, o)
	if doc.Search.Search != "ml engineer" || doc.Search.Country != "us" || doc.Search.WorkMode != "remote" || doc.Search.Pages != 1 {
		t.Errorf("search %+v", doc.Search)
	}
	m := doc.Meta
	if m.Total != 1340 || m.Fetched != 100 || m.Pages != 1 || !m.Capped || m.Skipped != 0 || m.Repeated != 0 || m.Stopped != "" {
		t.Errorf("meta %+v", m)
	}

	// A board already on the watchlist is marked, ignoring case, and has no add command.
	noetik, ok := boardIn(doc, "gh", "noetik")
	if !ok || !noetik.Watched || noetik.AddCommand != "" || noetik.Postings != 1 {
		t.Errorf("noetik %+v", noetik)
	}
	// The rest are new, ranked most postings first, each with the exact command.
	prolific, ok := boardIn(doc, "gh", "prolificacademicltd")
	if !ok || prolific.Watched || prolific.Postings != 21 || doc.Boards[0].Token != "prolificacademicltd" {
		t.Errorf("prolific %+v, first board %+v", prolific, doc.Boards[0])
	}
	if want := "vani watchlist add --ats gh --token prolificacademicltd --company 'Prolific Academic Ltd'"; prolific.AddCommand != want {
		t.Errorf("add command %q, want %q", prolific.AddCommand, want)
	}
	wd, ok := boardIn(doc, "wd", "r1rcm/R1RCM")
	if want := "vani watchlist add --ats wd --token r1rcm/R1RCM --company r1rcm --host r1rcm.wd1.myworkdayjobs.com --search 'ml engineer' --allow-host"; !ok || wd.AddCommand != want || wd.Host == "" || wd.Search != "ml engineer" {
		t.Errorf("workday %+v, want command %q", wd, want)
	}
	ab, ok := boardIn(doc, "ab", "vibrant-planet")
	if want := "vani watchlist add --ats ab --token vibrant-planet --company 'Vibrant Planet' --board-name 'vibrant planet' --allow-host"; !ok || ab.AddCommand != want {
		t.Errorf("ashby %+v, want command %q", ab, want)
	}
	for i := 1; i < len(doc.Boards); i++ {
		if doc.Boards[i-1].Postings < doc.Boards[i].Postings {
			t.Fatalf("boards out of order at %d", i)
		}
	}

	// Results Vani cannot read are listed by name and count, so nothing is dropped.
	total := 0
	for _, b := range doc.Boards {
		total += b.Postings
	}
	for _, u := range doc.Unreadable {
		total += u.Count
		if u.Source == "" || u.Reason == "" {
			t.Errorf("unreadable %+v", u)
		}
	}
	if total != 100 {
		t.Errorf("boards and unreadable account for %d of 100 results", total)
	}
	if !slices.Contains(doc.Unreadable, unreadableJSON{Source: "jazzhr", Reason: freehire.ReasonNoAdapter, Count: 6}) {
		t.Errorf("unreadable %+v", doc.Unreadable)
	}

	// The command only reads: the watchlist is what it was.
	after := watchlistOf(t, cfgPath)
	if len(after.Watchlist) != len(before.Watchlist) || len(after.Watchlist) != 1 {
		t.Errorf("watchlist %d -> %d boards", len(before.Watchlist), len(after.Watchlist))
	}
}

// With nothing to report the arrays are empty lists, not null.
func TestDiscoverJSONOfNothing(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	fakeFreehire(t, 200, []byte(`{"data":[],"meta":{"limit":100,"offset":0,"total":0}}`), nil)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "zzz", "--allow-host", "--json")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(o), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["boards"]) != "[]" || string(raw["unreadable"]) != "[]" {
		t.Errorf("boards %s unreadable %s", raw["boards"], raw["unreadable"])
	}
	for _, k := range []string{"search", "meta", "boards", "unreadable"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("no %q key", k)
		}
	}
	o, _ = mustCode(t, 0, "watchlist", "discover", "--search", "zzz")
	if !strings.Contains(o, "read 0 of 0 matches") || strings.Contains(o, "cannot add a board") {
		t.Errorf("text %q", o)
	}
}

func TestDiscoverTextOutput(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "lv", "--token", "entefy", "--company", "Entefy", "--allow-host")
	fakeFreehire(t, 200, discoverFixture(t), nil)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--allow-host")
	for _, want := range []string{
		`freehire.me: read 100 of 1340 matches for "ml engineer" (1 page)`,
		"not every match was read: --pages N reads more, at most 3",
		"  21  gh  prolificacademicltd  Prolific Academic Ltd  [new]",
		"      vani watchlist add --ats gh --token prolificacademicltd --company 'Prolific Academic Ltd' --allow-host",
		"lv  entefy  Entefy  [watched]",
		"results Vani cannot add a board for",
		"jazzhr",
		freehire.ReasonNoAdapter,
	} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "--token entefy") {
		t.Errorf("a watched board got an add command:\n%s", o)
	}
}

func TestDiscoverSendsTheFlagsAndAsksForOnePageByDefault(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	d := fakeFreehire(t, 200, discoverFixture(t), nil)
	mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--country", "US,CA", "--work-mode", "remote",
		"--seniority", "senior", "--allow-host", "--json")
	urls := d.URLs()
	if len(urls) != 1 {
		t.Fatalf("%d requests, want 1: %v", len(urls), urls)
	}
	u, _ := url.Parse(urls[0])
	want := url.Values{"q": {"ml engineer"}, "countries": {"US,CA"}, "work_mode": {"remote"}, "seniority": {"senior"}, "limit": {"100"}, "offset": {"0"}}
	if u.Query().Encode() != want.Encode() {
		t.Errorf("request %s", urls[0])
	}
	// --pages 3 reads three pages, spaced, and the result says it was capped.
	d2 := fakeFreehire(t, 200, discoverFixture(t), nil)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "ml engineer", "--pages", "3", "--json")
	if n := len(d2.URLs()); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
	// The fake answers every page alike, so pages 2 and 3 are repeats.
	if doc := discoverJSONOf(t, o); doc.Meta.Pages != 3 || doc.Meta.Fetched != 300 || doc.Meta.Repeated != 200 || !doc.Meta.Capped || doc.Search.Pages != 3 {
		t.Errorf("meta %+v search %+v", doc.Meta, doc.Search)
	}
}

func TestDiscoverUsageErrors(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	d := fakeFreehire(t, 200, discoverFixture(t), nil)
	for _, args := range [][]string{
		{"watchlist", "discover"},
		{"watchlist", "discover", "--search", "  "},
		{"watchlist", "discover", "--search", "x", "--pages", "0"},
		{"watchlist", "discover", "--search", "x", "--pages", "4"},
		{"watchlist", "discover", "--search", "x", "--pages", "-1"},
		{"watchlist", "discover", "--search", "x", "--country", "USA"},
		{"watchlist", "discover", "--search", "x", "--work-mode", "anywhere"},
		{"watchlist", "discover", "--search", "x", "--seniority", "boss"},
		{"watchlist", "discover", "--search", "x\x00y"},
		{"watchlist", "discover", "--search", "x", "extra"},
		{"watchlist", "discover", "--nope"},
	} {
		if code, _, e := do(t, append(args, "--allow-host")...); code != 2 {
			t.Errorf("vani %v: exit %d, want 2 (stderr %q)", args, code, e)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("%d requests for refused flags", n)
	}
}

func TestDiscoverFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		header http.Header
		want   string
	}{
		"429":       {429, ``, http.Header{"Retry-After": {"3600"}}, "rate limited"},
		"500":       {500, ``, nil, "HTTP 500"},
		"404":       {404, ``, nil, "search endpoint was not found"},
		"malformed": {200, `<html>`, nil, "not the search envelope"},
		"redirect":  {302, ``, http.Header{"Location": {"https://evil.test/"}}, "HTTP 302"},
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath, _ := setup(t)
			mustCode(t, 0, "init")
			fakeFreehire(t, tc.status, []byte(tc.body), tc.header)
			o, e := mustCode(t, 1, "watchlist", "discover", "--search", "x", "--allow-host", "--json")
			if !strings.Contains(e, tc.want) {
				t.Errorf("stderr %q lacks %q", e, tc.want)
			}
			if o != "" {
				t.Errorf("stdout %q on a failed search", o)
			}
			if len(watchlistOf(t, cfgPath).Watchlist) != 0 {
				t.Error("a failed search changed the watchlist")
			}
		})
	}
}

// A page after the first that fails keeps the first page's boards and says so.
func TestDiscoverReportsAnEarlyStop(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	calls := 0
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		calls++
		if calls == 1 {
			return srctest.Reply{Status: 200, Body: discoverFixture(t)}, true
		}
		return srctest.Reply{Status: 500}, true
	})
	oldDoer, oldSleep := newDiscoverDoer, discoverSleep
	newDiscoverDoer = func() (egress.Doer, error) { return d, nil }
	discoverSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { newDiscoverDoer, discoverSleep = oldDoer, oldSleep })

	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "x", "--pages", "2", "--allow-host")
	if !strings.Contains(o, "stopped early: page 2 was not read") || !strings.Contains(o, "read 100 of 1340") {
		t.Errorf("text %q", o)
	}
	calls = 0
	o, _ = mustCode(t, 0, "watchlist", "discover", "--search", "x", "--pages", "2", "--json")
	if doc := discoverJSONOf(t, o); !strings.HasPrefix(doc.Meta.Stopped, "page 2 was not read") || doc.Meta.Pages != 1 || !doc.Meta.Capped || len(doc.Boards) == 0 {
		t.Errorf("meta %+v", doc.Meta)
	}
}

// The real client may reach freehire.me and nothing else, whatever allowed_hosts holds.
func TestDiscoverDoerReachesOnlyFreehire(t *testing.T) {
	d, err := newDiscoverDoer()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/api/v1/jobs/search", nil)
	if resp, err := d.Do(context.Background(), req); err == nil {
		resp.Body.Close()
		t.Fatal("the discover client reached example.com")
	} else if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("err = %v", err)
	}
}

func TestAddCommandWidensEgressOnlyByWhatTheBoardNeeds(t *testing.T) {
	gh := sources.Board{ATS: "gh", Token: "acme", Company: "Acme"}
	if got, want := addCommand(gh, nil), "vani watchlist add --ats gh --token acme --company Acme --allow-host"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := addCommand(gh, []string{"boards-api.greenhouse.io"}), "vani watchlist add --ats gh --token acme --company Acme"; got != want {
		t.Errorf("host already allowed: got %q, want %q", got, want)
	}
	if got := addCommand(gh, []string{"*.greenhouse.io"}); strings.Contains(got, "--allow-host") {
		t.Errorf("wildcard-allowed host: %q", got)
	}
	ab := sources.Board{ATS: "ab", Token: "acme", Company: "Acme"}
	if got := addCommand(ab, []string{"api.ashbyhq.com"}); !strings.Contains(got, "--allow-host") {
		t.Errorf("ashby also reads jobs.ashbyhq.com: %q", got)
	}
	lv := sources.Board{ATS: "lv", Token: "acme", Company: "Acme", Region: "eu"}
	if got := addCommand(lv, []string{"api.eu.lever.co", "api.lever.co"}); !strings.Contains(got, "--region eu") {
		t.Errorf("region: %q", got)
	}
}

// Company names come from a third-party site and land in a command the owner
// pastes into a shell: whatever they hold, the shell must read them as one word.
func TestShellQuoteKeepsHostileNamesInert(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to check the quoting with")
	}
	for _, s := range []string{
		"Acme", "Acme Corp", "O'Brien & Sons", "it's", "'", "''", `a"b`, "$(touch /tmp/pwned)", "`id`", "${HOME}", "a;b|c&d>e<f",
		"-rf", "~", "*", "a\\b", "a\\'b", "", "Müller 数据", "a#b", "!history", "a=b",
	} {
		quoted := shellQuote(s)
		cmd := exec.Command(sh, "-c", "printf %s "+quoted)
		cmd.Dir = t.TempDir()
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if out.String() != s {
			t.Errorf("%q quoted as %s reads back as %q", s, quoted, out.String())
		}
	}
	for s, want := range map[string]string{"acme": "acme", "tenant/Site": "tenant/Site", "a b": "'a b'", "": "''", "it's": `'it'\''s'`} {
		if got := shellQuote(s); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", s, got, want)
		}
	}
}

// A hostile company name in a result reaches the printed command only as quoted text.
func TestDiscoverQuotesHostileCompanyNames(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	fakeFreehire(t, 200, []byte(`{"meta":{"total":1},"data":[{"source":"lever","external_id":"acme:1","url":"https://jobs.lever.co/acme/1",
		"company":"Acme'; touch /tmp/pwned; echo '$(id)\u001b[2J"}]}`), nil)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--search", "x", "--allow-host")
	if strings.ContainsRune(o, 0x1b) {
		t.Errorf("an escape sequence reached the terminal: %q", o)
	}
	if !strings.Contains(o, `--company 'Acme'\''; touch /tmp/pwned; echo '\''$(id)[2J'`) {
		t.Errorf("command not quoted:\n%s", o)
	}
}

// jobs ingest shares BoardFromURL with discover, but a saved Workable or
// Rippling page has always been a page board (its filters live in the URL), and
// still is.
func TestDeriveBoardLeavesWorkableAndRipplingPagesAsPageBoards(t *testing.T) {
	for _, page := range []string{"https://apply.workable.com/acme/", "https://ats.rippling.com/acme/jobs"} {
		host := strings.Split(strings.TrimPrefix(page, "https://"), "/")[0]
		jobs := []inbox.Job{
			{Title: "Engineer", URL: "https://" + host + "/jobs/1"},
			{Title: "Engineer", URL: "https://" + host + "/jobs/2"},
		}
		b, res := deriveBoard(page, jobs, "Acme", nil)
		if res.Status != "" || b.ATS != posting.Page {
			t.Errorf("%s: board %+v result %+v, want a page board", page, b, res)
		}
	}
}
