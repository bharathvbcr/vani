package oraclehcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const jpmcHost = "jpmc.fa.oraclecloud.com"

func jpmc() sources.Board {
	return sources.Board{ATS: posting.OracleHCM, Token: "jpmc/CX_1001", Company: "JPMorgan Chase", Host: jpmcHost}
}

func jpmcWith(terms ...string) sources.Board {
	b := jpmc()
	b.Searches = terms
	return b
}

func ok(body []byte) srctest.Reply { return srctest.Reply{Status: 200, Body: body} }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var (
	keywordRE = regexp.MustCompile(`keyword="([^"]*)"`)
	offsetRE  = regexp.MustCompile(`offset=([0-9]+)`)
	idParamRE = regexp.MustCompile(`Id="([0-9A-Za-z_]*)"`)
)

// fake is a board of ids, newest first. It answers the listing and the detail
// like the live host (testdata/ holds real answers; these are shaped like
// them) and counts what it was asked.
type fake struct {
	ids    []string
	byTerm map[string][]string
	// total overrides the count a page reports (default: the term's rows).
	total func(term string, offset int) int
	// page overrides the rows a page serves (default: the term's rows from offset).
	page func(term string, offset int, rows []string) []string
	// detail overrides a detail's reply; ok=false serves the default one.
	detail func(id string) (srctest.Reply, bool)

	listings, details int
	offsets           []int
	urlsAsked         []string // every detail URL
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(210000000 + i)
	}
	return out
}

func rowJSON(id string) map[string]any {
	return map[string]any{
		"Id": id, "Title": "Role " + id, "PostedDate": "2026-10-20", "PrimaryLocation": "Plano, TX, United States",
		"WorkplaceTypeCode": nil, "secondaryLocations": []any{},
	}
}

func detailJSON(id string) []byte {
	raw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"Id": id, "Title": "Role " + id, "ExternalDescriptionStr": "<p>Build things " + id + ".</p>",
		"ExternalPostedStartDate": "2026-10-21T08:36:47+00:00", "PrimaryLocation": "Plano, TX, United States",
		"JobSchedule": "Full time", "WorkplaceType": "", "WorkplaceTypeCode": nil,
	}}})
	return raw
}

func (f *fake) doer() *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != jpmcHost || r.Method != http.MethodGet {
			return srctest.Reply{}, false
		}
		finder := u.Query().Get("finder")
		switch u.Path {
		case listPath:
			f.listings++
			term := ""
			if m := keywordRE.FindStringSubmatch(finder); m != nil {
				term = m[1]
			}
			offset, _ := strconv.Atoi(offsetRE.FindStringSubmatch(finder)[1])
			f.offsets = append(f.offsets, offset)
			all := f.ids
			if rows, hit := f.byTerm[term]; hit {
				all = rows
			}
			var rows []string
			if offset < len(all) {
				rows = all[offset:min(len(all), offset+pageSize)]
			}
			if f.page != nil {
				rows = f.page(term, offset, rows)
			}
			total := len(all)
			if f.total != nil {
				total = f.total(term, offset)
			}
			list := []any{}
			for _, id := range rows {
				list = append(list, rowJSON(id))
			}
			raw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"TotalJobsCount": total, "requisitionList": list}}, "count": 1, "hasMore": false, "limit": 200, "offset": 0})
			return ok(raw), true
		case detailPath:
			f.details++
			f.urlsAsked = append(f.urlsAsked, r.URL)
			id := idParamRE.FindStringSubmatch(finder)[1]
			if f.detail != nil {
				if rep, hit := f.detail(id); hit {
					return rep, true
				}
			}
			return ok(detailJSON(id)), true
		}
		return srctest.Reply{}, false
	})
}

func goneBody() []byte {
	return []byte(`{"items":[],"count":0,"hasMore":false,"limit":499,"offset":0}`)
}

func list(t *testing.T, s sources.Source, b sources.Board) ([]posting.Posting, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.List(ctx, b)
}

func storedAll(ids []string) func(sources.Board) []posting.Posting {
	return func(sources.Board) []posting.Posting {
		var out []posting.Posting
		for _, id := range ids {
			k, _ := posting.OracleHCMKey("jpmc", "CX_1001", id)
			out = append(out, posting.Posting{Key: k, ATS: posting.OracleHCM, Title: "stored " + id})
		}
		return out
	}
}

// storedNow is the clock of a source from withStored, so which stored
// postings are due a re-read (srcutil.RefreshTurn) is the same on every run.
var storedNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func withStored(t *testing.T, d egress.Doer, stored func(sources.Board) []posting.Posting, report func(sources.Board, Stats)) sources.Source {
	t.Helper()
	s, err := NewWith(d, Options{Stored: stored, Report: report})
	if err != nil {
		t.Fatal(err)
	}
	s.(*source).now = func() time.Time { return storedNow }
	return s
}

// dueIDs splits ids by whether their re-read turn is storedNow's day.
func dueIDs(ids []string) (due, notDue []string) {
	day := srcutil.Day(storedNow)
	for _, id := range ids {
		if srcutil.RefreshTurn(id, day) {
			due = append(due, id)
		} else {
			notDue = append(notDue, id)
		}
	}
	return due, notDue
}

// ---- real answers ----

// fixtureDoer serves the recorded jpmc answers: the listing's two rows (its
// count, 1600 for the whole search, set to the two rows held) and their details.
func fixtureDoer(t *testing.T) *srctest.Doer {
	t.Helper()
	var whole map[string]any
	if err := json.Unmarshal(readFixture(t, "list.json"), &whole); err != nil {
		t.Fatal(err)
	}
	whole["items"].([]any)[0].(map[string]any)["TotalJobsCount"] = 2
	listing, _ := json.Marshal(whole)
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		switch u.Path {
		case listPath:
			return ok(listing), true
		case detailPath:
			id := idParamRE.FindStringSubmatch(u.Query().Get("finder"))[1]
			if raw, err := os.ReadFile(filepath.Join("testdata", "detail-"+id+".json")); err == nil {
				return ok(raw), true
			}
			return ok(readFixture(t, "detail-gone.json")), true
		}
		return srctest.Reply{}, false
	})
}

func TestListFromRecordedAnswers(t *testing.T) {
	d := fixtureDoer(t)
	b := jpmc()
	b.Search = "software engineer"
	got, err := list(t, New(d), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d postings, want 2", len(got))
	}
	p := got[0]
	if p.Key != "oc:jpmc:cx_1001:210804083" || p.ATS != posting.OracleHCM {
		t.Errorf("key %q ats %q", p.Key, p.ATS)
	}
	if p.Company != "JPMorgan Chase" || p.Title != "Lead Software Engineer Python GenAI" {
		t.Errorf("company %q title %q", p.Company, p.Title)
	}
	if want := "Wilmington, DE, United States; Houston, TX, United States; Plano, TX, United States"; p.Location != want {
		t.Errorf("location %q, want %q", p.Location, want)
	}
	if want := "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/job/210804083"; p.URL != want {
		t.Errorf("url %q, want %q", p.URL, want)
	}
	if want := time.Date(2026, 10, 27, 12, 35, 20, 0, time.UTC); !p.PostedAt.Equal(want) {
		t.Errorf("posted %v, want %v", p.PostedAt, want)
	}
	if !strings.HasPrefix(p.Description, "Role: full-time") || !strings.Contains(p.Description, "As an Lead Software Engineer Python GenAI at JPMorgan Chase") {
		t.Errorf("description starts %.120q", p.Description)
	}
	if strings.Contains(p.Description, "<p>") || strings.Contains(p.Description, "&nbsp;") {
		t.Errorf("description keeps markup: %.200q", p.Description)
	}
	if strings.Contains(p.Description, "equal opportunity employer") {
		t.Error("the corporate blurb is not part of a posting's description")
	}
	if got[1].Key != "oc:jpmc:cx_1001:210801417" || got[1].Location != "GLASGOW, LANARKSHIRE, United Kingdom" {
		t.Errorf("second posting %q at %q", got[1].Key, got[1].Location)
	}
	if p.Remote || got[1].Remote {
		t.Error("an on-site posting read as remote")
	}
	if p.SeenAt.IsZero() {
		t.Error("SeenAt unset")
	}

	// What was asked: one listing request, then one detail per row.
	reqs := d.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3: %v", len(reqs), d.URLs())
	}
	lu, _ := url.Parse(reqs[0].URL)
	wantFinder := `findReqs;siteNumber=CX_1001,keyword="software engineer",limit=200,offset=0,sortBy=POSTING_DATES_DESC,facetsList=""`
	if lu.Query().Get("finder") != wantFinder {
		t.Errorf("listing finder %q, want %q", lu.Query().Get("finder"), wantFinder)
	}
	if lu.Query().Get("expand") != "requisitionList.secondaryLocations" || lu.Query().Get("onlyData") != "true" {
		t.Errorf("listing query %q", lu.RawQuery)
	}
	du, _ := url.Parse(reqs[1].URL)
	if want := `ById;Id="210804083",siteNumber=CX_1001`; du.Query().Get("finder") != want {
		t.Errorf("detail finder %q, want %q", du.Query().Get("finder"), want)
	}
	fields := du.Query().Get("fields")
	for _, banned := range []string{"HiringManager", "ExternalContact", "CorporateDescription", "OrganizationDescription"} {
		if strings.Contains(fields, banned) {
			t.Errorf("detail fields %q ask for %s", fields, banned)
		}
	}
	if !strings.Contains(fields, "ExternalDescriptionStr") {
		t.Errorf("detail fields %q do not ask for the description", fields)
	}
}

func TestEmptySearchListsNothingWithoutError(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.Contains(r.URL, listPath) {
			return ok(readFixture(t, "list-empty.json")), true
		}
		return srctest.Reply{}, false // a detail for a posting that does not exist
	})
	got, err := list(t, New(d), jpmcWith("zzzxq"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d postings, err %v; want none, nil", len(got), err)
	}
	if n := len(d.Requests()); n != 1 {
		t.Errorf("%d requests, want the one listing", n)
	}
}

// ---- paging ----

func TestPagingReadsEveryPageToTheTotal(t *testing.T) {
	f := &fake{ids: ids(450)}
	var st Stats
	s := withStored(t, f.doer(), storedAll(f.ids), func(_ sources.Board, s Stats) { st = s })
	got, err := list(t, s, jpmcWith("engineer"))
	if err != nil {
		t.Fatal(err)
	}
	// Every row is stored, so the only details are the day's re-reads.
	due, _ := dueIDs(f.ids)
	if len(due) == 0 || len(due) > 450/10 {
		t.Fatalf("%d of 450 ids due on storedNow; pick a day with a few", len(due))
	}
	if len(got) != 450 || f.details != len(due) {
		t.Fatalf("%d postings and %d details, want 450 and %d (all stored, the day's turn re-read)", len(got), f.details, len(due))
	}
	if fmt.Sprint(f.offsets) != "[0 200 400]" {
		t.Errorf("offsets %v, want [0 200 400]", f.offsets)
	}
	if st.Listed != 450 || st.Requests != 3 || st.Reused != 450-len(due) || st.Refreshed != len(due) {
		t.Errorf("stats %+v", st)
	}
	for i, p := range got {
		if p.Key != posting.Key("oc:jpmc:cx_1001:"+f.ids[i]) {
			t.Fatalf("posting %d is %q, want listing order", i, p.Key)
		}
	}
}

func TestExactMultipleOfThePageSizeStopsAtTheTotal(t *testing.T) {
	f := &fake{ids: ids(400)}
	got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), nil), jpmc())
	if err != nil || len(got) != 400 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	if fmt.Sprint(f.offsets) != "[0 200]" {
		t.Errorf("offsets %v, want [0 200]: a page at offset 400 is past the total", f.offsets)
	}
}

// A page one row short is the host dropping a counted requisition, not the end
// of the listing (seen live at offset 300 of a 1,600-job search).
func TestShortPageMidWalkDoesNotEndTheListing(t *testing.T) {
	f := &fake{ids: ids(450)}
	f.page = func(_ string, offset int, rows []string) []string {
		if offset == 200 {
			return rows[:len(rows)-1]
		}
		return rows
	}
	got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), nil), jpmc())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 449 || fmt.Sprint(f.offsets) != "[0 200 400]" {
		t.Fatalf("%d postings, offsets %v; want 449 and the third page read", len(got), f.offsets)
	}
}

func TestTotalDriftAfterTheFirstPageIsIgnored(t *testing.T) {
	for name, later := range map[string]int{"shrinks": 449, "grows": 460, "zero": 0} {
		t.Run(name, func(t *testing.T) {
			f := &fake{ids: ids(450)}
			f.total = func(_ string, offset int) int {
				if offset == 0 {
					return 450
				}
				return later
			}
			got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), nil), jpmc())
			if err != nil || len(got) != 450 {
				t.Fatalf("%d postings, err %v; want 450, nil (the first page's count rules)", len(got), err)
			}
			if fmt.Sprint(f.offsets) != "[0 200 400]" {
				t.Errorf("offsets %v: a later count must not stop or extend the walk", f.offsets)
			}
		})
	}
}

func TestShortfallBound(t *testing.T) {
	for _, tc := range []struct {
		n, lost int
		fail    bool
	}{
		{450, 4, false}, // a hundredth of 450 is 4
		{450, 5, true},
		{50, 1, false}, // at least one
		{50, 2, true},
	} {
		t.Run(fmt.Sprintf("n=%d/lost=%d", tc.n, tc.lost), func(t *testing.T) {
			f := &fake{ids: ids(tc.n)}
			f.page = func(_ string, offset int, rows []string) []string {
				if offset == 0 {
					return rows[:len(rows)-tc.lost]
				}
				return rows
			}
			got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), nil), jpmc())
			if tc.fail {
				if !errors.Is(err, errIncomplete) {
					t.Fatalf("got %d postings, err %v; want errIncomplete", len(got), err)
				}
				return
			}
			if err != nil || len(got) != tc.n-tc.lost {
				t.Fatalf("%d postings, err %v; want %d, nil", len(got), err, tc.n-tc.lost)
			}
		})
	}
}

// An empty page before the total is a listing that stopped, not the end: its
// rows are missing, and the board fails rather than age them.
func TestEmptyPageBeforeTheTotalFailsTheBoard(t *testing.T) {
	f := &fake{ids: ids(450)}
	f.page = func(_ string, offset int, rows []string) []string {
		if offset == 200 {
			return nil
		}
		return rows
	}
	got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), nil), jpmc())
	if !errors.Is(err, errIncomplete) {
		t.Fatalf("got %d postings, err %v; want errIncomplete", len(got), err)
	}
}

func TestStuckCursorFailsTheBoard(t *testing.T) {
	f := &fake{ids: ids(450)}
	f.page = func(_ string, _ int, rows []string) []string { return ids(200) } // every page is page 1
	_, err := list(t, withStored(t, f.doer(), storedAll(ids(200)), nil), jpmc())
	if !errors.Is(err, errIncomplete) || !strings.Contains(err.Error(), "repeated a page") {
		t.Fatalf("err %v, want a repeated-page errIncomplete", err)
	}
	if f.listings > 3 {
		t.Errorf("%d listing requests: the walk must stop at the first repeated page", f.listings)
	}
}

func TestCountedJobsButNoRowsFailsTheBoard(t *testing.T) {
	for _, total := range []int{1, 2, 100} {
		f := &fake{ids: nil}
		f.total = func(string, int) int { return total }
		got, err := list(t, New(f.doer()), jpmc())
		if !errors.Is(err, errIncomplete) {
			t.Errorf("total %d, no rows: got %d postings, err %v; want errIncomplete", total, len(got), err)
		}
	}
}

func TestDuplicateRowsAreOnePosting(t *testing.T) {
	f := &fake{ids: ids(3)}
	f.page = func(_ string, _ int, rows []string) []string {
		return []string{rows[0], rows[1], rows[1], rows[2], rows[0]}
	}
	f.total = func(string, int) int { return 3 }
	got, err := list(t, New(f.doer()), jpmc())
	if err != nil || len(got) != 3 || f.details != 3 {
		t.Fatalf("%d postings, %d details, err %v; want 3, 3, nil", len(got), f.details, err)
	}
}

// ---- caps ----

func TestOverTheCapFailsBeforePagingOrDetails(t *testing.T) {
	f := &fake{ids: ids(5)}
	f.total = func(string, int) int { return MaxPostings + 1 }
	_, err := list(t, New(f.doer()), jpmc())
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
	if f.listings != 1 || f.details != 0 {
		t.Errorf("%d listing and %d detail requests, want 1 and 0", f.listings, f.details)
	}
}

func TestMaxPostingsRaisesAndLowersTheCap(t *testing.T) {
	f := &fake{ids: ids(10)}
	b := jpmc()
	b.MaxPostings = 9
	if _, err := list(t, New(f.doer()), b); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("max_postings 9 over a 10-job board: err %v, want ErrTooManyPostings", err)
	}
	b.MaxPostings = 10
	if got, err := list(t, New(f.doer()), b); err != nil || len(got) != 10 {
		t.Fatalf("max_postings 10: %d postings, err %v", len(got), err)
	}
	for _, bad := range []int{-1, sources.MaxPostingsCeiling + 1} {
		b.MaxPostings = bad
		if _, err := list(t, New(f.doer()), b); err == nil {
			t.Errorf("max_postings %d accepted", bad)
		}
	}
}

func TestTermsMergeByIDAndTheCapAppliesToTheUnion(t *testing.T) {
	f := &fake{byTerm: map[string][]string{"alpha": ids(3), "beta": append(ids(2), "210000050", "210000051")}}
	got, err := list(t, New(f.doer()), jpmcWith("alpha", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || f.details != 5 {
		t.Fatalf("%d postings, %d details; want the union of 5, each fetched once", len(got), f.details)
	}
	b := jpmcWith("alpha", "beta")
	b.MaxPostings = 4
	f2 := &fake{byTerm: f.byTerm}
	if _, err := list(t, New(f2.doer()), b); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("a union of 5 under max_postings 4: err %v, want ErrTooManyPostings", err)
	}
}

func TestSearchTermsAreQuotedAndCarried(t *testing.T) {
	f := &fake{ids: ids(1)}
	d := f.doer()
	if _, err := list(t, New(d), jpmcWith("data, engineer", "c++ ; x=y")); err != nil {
		t.Fatal(err)
	}
	var finders []string
	for _, u := range d.URLs() {
		pu, _ := url.Parse(u)
		if pu.Path == listPath {
			finders = append(finders, pu.Query().Get("finder"))
		}
	}
	want := []string{
		`findReqs;siteNumber=CX_1001,keyword="data, engineer",limit=200,offset=0,sortBy=POSTING_DATES_DESC,facetsList=""`,
		`findReqs;siteNumber=CX_1001,keyword="c++ ; x=y",limit=200,offset=0,sortBy=POSTING_DATES_DESC,facetsList=""`,
	}
	if fmt.Sprint(finders) != fmt.Sprint(want) {
		t.Errorf("finders\n%q\nwant\n%q", finders, want)
	}
}

func TestSearchTermWithQuoteBackslashOrControlIsRefused(t *testing.T) {
	for _, term := range []string{`a"b`, `x",limit=5`, `a\b`, `a\"b`, "a\nb", "a\x00b", "\t\"", `"`} {
		d := srctest.New(nil)
		_, err := list(t, New(d), jpmcWith(term))
		if err == nil {
			t.Errorf("term %q accepted", term)
		}
		if n := len(d.Requests()); n != 0 {
			t.Errorf("term %q: %d requests went out before it was refused", term, n)
		}
	}
}

// ---- hostile boards ----

func TestHostileTokenAndHostRefusedBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct{ name, token, host string }{
		{"dotdot", "../CX_1001", jpmcHost},
		{"dotdot site", "jpmc/..", jpmcHost},
		{"two slashes", "jpmc/CX_1001/x", jpmcHost},
		{"no slash", "jpmc", jpmcHost},
		{"empty tenant", "/CX_1001", jpmcHost},
		{"empty site", "jpmc/", jpmcHost},
		{"colon", "jpmc:x/CX_1001", jpmcHost},
		{"space", "jpmc/CX 1001", jpmcHost},
		{"query", "jpmc/CX_1001?x=1", jpmcHost},
		{"finder comma", "jpmc/CX_1001,limit=1", jpmcHost},
		{"finder semicolon", "jpmc/CX_1001;x", jpmcHost},
		{"finder quote", `jpmc/CX_1001"`, jpmcHost},
		{"fragment", "jpmc/CX#1", jpmcHost},
		{"percent", "jpmc/CX%2e", jpmcHost},
		{"unicode", "jpmc/CX_١٠٠١", jpmcHost},
		{"empty", "", jpmcHost},
		{"empty host", "jpmc/CX_1001", ""},
		{"other tenant", "jpmc/CX_1001", "chase.fa.oraclecloud.com"},
		{"suffix trick", "jpmc/CX_1001", "jpmc.fa.oraclecloud.com.evil.test"},
		{"prefix trick", "jpmc/CX_1001", "evil.jpmc.fa.oraclecloud.com"},
		{"other domain", "jpmc/CX_1001", "jpmc.fa.example.com"},
		{"no fa label", "jpmc/CX_1001", "jpmc.oraclecloud.com"},
		{"fa first", "jpmc/CX_1001", "fa.jpmc.oraclecloud.com"},
		{"bad region", "jpmc/CX_1001", "jpmc.fa.us-2.oraclecloud.com"},
		{"two regions", "jpmc/CX_1001", "jpmc.fa.us2.em2.oraclecloud.com"},
		{"port", "jpmc/CX_1001", jpmcHost + ":8443"},
		{"userinfo", "jpmc/CX_1001", "x@" + jpmcHost},
		{"path", "jpmc/CX_1001", jpmcHost + "/x"},
		{"workday host", "jpmc/CX_1001", "jpmc.wd5.myworkdayjobs.com"},
		{"ip", "jpmc/CX_1001", "10.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := srctest.New(nil)
			b := sources.Board{ATS: posting.OracleHCM, Token: tc.token, Host: tc.host}
			if got, err := list(t, New(d), b); err == nil {
				t.Fatalf("accepted: %d postings", len(got))
			}
			if n := len(d.Requests()); n != 0 {
				t.Errorf("%d requests went out", n)
			}
			if err := CheckSize(context.Background(), d, b); err == nil {
				t.Error("CheckSize accepted it")
			}
		})
	}
}

func TestValidHostsAndTokens(t *testing.T) {
	for _, tc := range []struct{ token, host string }{
		{"jpmc/CX_1001", "jpmc.fa.oraclecloud.com"},
		{"JPMC/CX_1001", "JPMC.FA.ORACLECLOUD.COM"},
		{"eeho/CX_45001", "eeho.fa.us2.oraclecloud.com"}, // verified live
		{"acme/jobsearch", "acme.fa.em2.oraclecloud.com"},
		{"acme-corp/Site_1", "acme-corp.fa.ap1.oraclecloud.com"},
	} {
		tenant, _, err := parseToken(tc.token)
		if err != nil {
			t.Errorf("token %q: %v", tc.token, err)
			continue
		}
		if _, err := checkHost(tc.host, tenant); err != nil {
			t.Errorf("host %q: %v", tc.host, err)
		}
	}
}

func TestWrongATSAndFiltersRefused(t *testing.T) {
	d := srctest.New(nil)
	b := jpmc()
	b.ATS = posting.Workday
	if _, err := list(t, New(d), b); err == nil {
		t.Error("a workday board accepted")
	}
	b = jpmc()
	b.Filters = map[string][]string{"location": {"US"}}
	if _, err := list(t, New(d), b); err == nil {
		t.Error("filters accepted")
	}
	if n := len(d.Requests()); n != 0 {
		t.Errorf("%d requests went out", n)
	}
}

// ---- the host misbehaves ----

func TestListingShapesThatAreNeverAnEmptyBoard(t *testing.T) {
	item := func(body string) []byte { return []byte(`{"items":[` + body + `]}`) }
	for name, body := range map[string][]byte{
		"no items":               []byte(`{"items":[]}`),
		"items missing":          []byte(`{}`),
		"two items":              item(`{"TotalJobsCount":0,"requisitionList":[]},{"TotalJobsCount":0,"requisitionList":[]}`),
		"null list":              item(`{"TotalJobsCount":0,"requisitionList":null}`),
		"list missing":           item(`{"TotalJobsCount":0}`),
		"count missing":          item(`{"requisitionList":[]}`),
		"count null":             item(`{"TotalJobsCount":null,"requisitionList":[]}`),
		"count negative":         item(`{"TotalJobsCount":-1,"requisitionList":[]}`),
		"count zero beside rows": item(`{"TotalJobsCount":0,"requisitionList":[{"Id":"210000001","Title":"T"},{"Id":"210000002","Title":"T"}]}`),
		"count under rows":       item(`{"TotalJobsCount":1,"requisitionList":[{"Id":"210000001","Title":"T"},{"Id":"210000002","Title":"T"}]}`),
		"count string":           item(`{"TotalJobsCount":"many","requisitionList":[]}`),
		"id not a number":        item(`{"TotalJobsCount":1,"requisitionList":[{"Id":"12a","Title":"T"}]}`),
		"id missing":             item(`{"TotalJobsCount":1,"requisitionList":[{"Title":"T"}]}`),
		"id path":                item(`{"TotalJobsCount":1,"requisitionList":[{"Id":"1\"/../x","Title":"T"}]}`),
		"id exponent":            item(`{"TotalJobsCount":1,"requisitionList":[{"Id":1e3,"Title":"T"}]}`),
		"id negative":            item(`{"TotalJobsCount":1,"requisitionList":[{"Id":-5,"Title":"T"}]}`),
		"malformed":              []byte(`{"items":[{"TotalJobsCount":`),
		"html error page":        []byte(`<html><body>W4S-402: Blocked by WAF4SaaS</body></html>`),
		"empty body":             nil,
		"rows over a page":       nil, // built below
		"json null":              []byte(`null`),
		"json array":             []byte(`[]`),
		"items not a list":       []byte(`{"items":"none"}`),
		"requisitionList=1":      item(`{"TotalJobsCount":1,"requisitionList":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "rows over a page" {
				var rows []string
				for _, id := range ids(pageSize + 1) {
					rows = append(rows, `{"Id":"`+id+`","Title":"T"}`)
				}
				body = item(fmt.Sprintf(`{"TotalJobsCount":%d,"requisitionList":[%s]}`, pageSize+1, strings.Join(rows, ",")))
			}
			d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return ok(body), true })
			got, err := list(t, New(d), jpmc())
			if err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
			if n := len(d.Requests()); n != 1 {
				t.Errorf("%d requests, want just the listing (no detail for a row that cannot be read)", n)
			}
		})
	}
}

func TestListingFailuresAreErrorsNotEmptyBoards(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"404":       {Status: 404, Body: []byte(`{}`)},
		"403 waf":   {Status: 403, Body: []byte(`<html>W4S-402: Blocked by WAF4SaaS</html>`)},
		"500":       {Status: 500, Body: []byte(`{}`)},
		"502":       {Status: 502},
		"429":       {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
		"redirect":  {Status: 302, Header: http.Header{"Location": {"https://evil.test/"}}},
		"oversized": ok([]byte(strings.Repeat("x", maxListBody+1))),
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return rep, true })
			got, err := list(t, New(d), jpmc())
			if err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
			if name == "404" && !errors.Is(err, ErrBoardNotFound) {
				t.Errorf("err %v, want ErrBoardNotFound", err)
			}
			if name == "oversized" && !errors.Is(err, srcutil.ErrBodyTooLarge) {
				t.Errorf("err %v, want ErrBodyTooLarge", err)
			}
		})
	}
}

// errDoer is a host that cannot be reached.
type errDoer struct{ err error }

func (e errDoer) Do(context.Context, *http.Request) (*http.Response, error) { return nil, e.err }

func TestDependencyDownAndTimeoutAreErrors(t *testing.T) {
	for name, cause := range map[string]error{
		"dns":     errors.New("lookup jpmc.fa.oraclecloud.com: no such host"),
		"timeout": context.DeadlineExceeded,
		"reset":   errors.New("connection reset by peer"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := list(t, New(errDoer{cause}), jpmc())
			if err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
			if !errors.Is(err, cause) {
				t.Errorf("err %v does not wrap %v", err, cause)
			}
			// the same host going down after the listing answered, at a detail
			f := &fake{ids: ids(3)}
			down := dropAfter{inner: f.doer(), after: 1, err: cause}
			if got, err := list(t, New(&down), jpmc()); err == nil || !errors.Is(err, cause) {
				t.Errorf("host down at the details: %d postings, err %v", len(got), err)
			}
		})
	}
}

// dropAfter answers its first `after` requests, then fails every one.
type dropAfter struct {
	inner egress.Doer
	after int
	n     int
	err   error
}

func (d *dropAfter) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	d.n++
	if d.n > d.after {
		return nil, d.err
	}
	return d.inner.Do(ctx, r)
}

// cancelDoer cancels the caller's context as the first request goes out.
type cancelDoer struct {
	inner  egress.Doer
	cancel func()
}

func (c cancelDoer) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	c.cancel()
	return c.inner.Do(ctx, r)
}

func TestCanceledContextStopsTheListingAndTheDetails(t *testing.T) {
	f := &fake{ids: ids(450)}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := New(cancelDoer{f.doer(), cancel}).List(ctx, jpmc()); !errors.Is(err, context.Canceled) {
		t.Fatalf("listing: err %v, want context.Canceled", err)
	}
	if f.listings != 1 || f.details != 0 {
		t.Errorf("%d listing and %d detail requests after the cancel", f.listings, f.details)
	}

	// Canceled between details: the walk over the fetches stops.
	f = &fake{ids: ids(5)}
	ctx, cancel = context.WithCancel(context.Background())
	f.detail = func(id string) (srctest.Reply, bool) {
		if id == f.ids[1] {
			cancel()
		}
		return srctest.Reply{}, false
	}
	if _, err := New(f.doer()).List(ctx, jpmc()); !errors.Is(err, context.Canceled) {
		t.Fatalf("details: err %v, want context.Canceled", err)
	}
	if f.details != 2 {
		t.Errorf("%d detail requests, want 2: none after the cancel", f.details)
	}
}

// ---- details ----

func TestGoneDetailIsLeftOut(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"200 no items": ok(goneBody()),
		"404":          {Status: 404, Body: []byte(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{ids: ids(10)}
			f.detail = func(id string) (srctest.Reply, bool) { return rep, id == f.ids[3] }
			var st Stats
			s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s })
			got, err := list(t, s, jpmc())
			if err != nil || len(got) != 9 {
				t.Fatalf("%d postings, err %v; want 9, nil", len(got), err)
			}
			for _, p := range got {
				if strings.HasSuffix(string(p.Key), f.ids[3]) {
					t.Errorf("the gone posting %s is listed", p.Key)
				}
			}
			if st.Gone != 1 || st.Fetched != 9 || st.Listed != 10 {
				t.Errorf("stats %+v, want listed 10, fetched 9, gone 1", st)
			}
		})
	}
}

func TestGoneDetailsBeyondTheBoundFailTheBoard(t *testing.T) {
	for _, tc := range []struct {
		n, gone int
		fail    bool
	}{
		{20, 2, false}, // a tenth of 20
		{20, 3, true},
		{5, 1, false}, // at least one
		{5, 2, true},
		{1, 1, true}, // every posting gone
		{2, 2, true},
	} {
		t.Run(fmt.Sprintf("n=%d/gone=%d", tc.n, tc.gone), func(t *testing.T) {
			f := &fake{ids: ids(tc.n)}
			gone := map[string]bool{}
			for _, id := range f.ids[:tc.gone] {
				gone[id] = true
			}
			f.detail = func(id string) (srctest.Reply, bool) { return ok(goneBody()), gone[id] }
			got, err := list(t, New(f.doer()), jpmc())
			if tc.fail {
				if err == nil {
					t.Fatalf("got %d postings, want the board to fail", len(got))
				}
				if errors.Is(err, srcutil.ErrBoardNotFound) {
					t.Errorf("detail gones read as board-not-found: %v", err)
				}
				return
			}
			if err != nil || len(got) != tc.n-tc.gone {
				t.Fatalf("%d postings, err %v; want %d, nil", len(got), err, tc.n-tc.gone)
			}
		})
	}
}

func TestDetailFailuresFailTheBoardAndAreNotGone(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"500":       {Status: 500, Body: []byte(`{}`)},
		"403":       {Status: 403, Body: []byte(`<html>W4S-402</html>`)},
		"410":       {Status: 410, Body: []byte(`{}`)},
		"429":       {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
		"malformed": ok([]byte(`{"items":[{"Id":`)),
		"html":      ok([]byte(`<html><body>not found</body></html>`)),
		"oversized": ok([]byte(strings.Repeat("x", maxDetailBody+1))),
		"two items": ok([]byte(`{"items":[{"Id":"210000005","Title":"A"},{"Id":"210000006","Title":"B"}]}`)),
		"wrong id":  ok([]byte(`{"items":[{"Id":"210000099","Title":"Another role"}]}`)),
		"no id":     ok([]byte(`{"items":[{"Title":"No id"}]}`)),
		"no title":  ok([]byte(`{"items":[{"Id":"210000005","Title":""}]}`)),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{ids: ids(20)}
			f.detail = func(id string) (srctest.Reply, bool) { return rep, id == "210000005" }
			var st Stats
			s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s })
			got, err := list(t, s, jpmc())
			if name == "no title" {
				// The row's own title stands in for an empty one; this reply has
				// both a title (from the row) and no description, which is a posting.
				if err != nil || len(got) != 20 {
					t.Fatalf("%d postings, err %v; want the row's title to stand in", len(got), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
			if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") || st.Gone != 0 {
				t.Errorf("read as gone: %v (gone %d)", err, st.Gone)
			}
			if name == "oversized" && !errors.Is(err, srcutil.ErrBodyTooLarge) {
				t.Errorf("err %v, want ErrBodyTooLarge", err)
			}
		})
	}
}

func TestEmptyTitleEverywhereFailsTheBoard(t *testing.T) {
	blank := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.Contains(r.URL, detailPath) {
			return ok([]byte(`{"items":[{"Id":"210000000","Title":" "}]}`)), true
		}
		return ok([]byte(`{"items":[{"TotalJobsCount":1,"requisitionList":[{"Id":"210000000","Title":""}]}]}`)), true
	})
	if got, err := list(t, New(blank), jpmc()); err == nil {
		t.Fatalf("a posting with no title anywhere was listed: %d", len(got))
	}
}

// ---- the bounded detail step ----

func TestNewWithReusesStoredAndSpendsNoRequestOnThem(t *testing.T) {
	_, notDue := dueIDs(ids(400))
	f := &fake{ids: notDue[:5]}
	var st Stats
	s := withStored(t, f.doer(), storedAll(f.ids[:3]), func(_ sources.Board, s Stats) { st = s })
	got, err := list(t, s, jpmc())
	if err != nil || len(got) != 5 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	if f.details != 2 || st.Reused != 3 || st.Fetched != 2 {
		t.Errorf("%d detail requests, stats %+v; want 2 fetched, 3 reused", f.details, st)
	}
	if got[0].Title != "stored "+f.ids[0] || got[4].Title != "Role "+f.ids[4] {
		t.Errorf("titles %q, %q: a stored row keeps its stored text, a new one is read", got[0].Title, got[4].Title)
	}
	if got[0].SeenAt.IsZero() {
		t.Error("a reused posting must be marked seen")
	}
}

// A stored posting is re-read on its turn, so an edited description is seen
// within srcutil.RefreshDays; before, a stored posting kept its first text for
// as long as it was listed.
func TestStoredPostingOnItsTurnIsReread(t *testing.T) {
	due, notDue := dueIDs(ids(400))
	f := &fake{ids: []string{due[0], notDue[0]}}
	var st Stats
	got, err := list(t, withStored(t, f.doer(), storedAll(f.ids), func(_ sources.Board, s Stats) { st = s }), jpmc())
	if err != nil || len(got) != 2 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	if f.details != 1 || st.Refreshed != 1 || st.Reused != 1 || st.Fetched != 0 {
		t.Errorf("%d detail requests, stats %+v; want the due one re-read, the other reused", f.details, st)
	}
	if got[0].Title != "Role "+due[0] || got[1].Title != "stored "+notDue[0] {
		t.Errorf("titles %q, %q: the re-read one takes the detail's text, the other keeps its stored text", got[0].Title, got[1].Title)
	}
}

func TestStoredPostingsOfAnotherSiteAreNeverReused(t *testing.T) {
	f := &fake{ids: ids(2)}
	other := func(sources.Board) []posting.Posting {
		k1, _ := posting.OracleHCMKey("jpmc", "CX_9999", f.ids[0])
		k2, _ := posting.OracleHCMKey("chase", "CX_1001", f.ids[1])
		return []posting.Posting{{Key: k1, Title: "other site"}, {Key: k2, Title: "other tenant"}}
	}
	got, err := list(t, withStored(t, f.doer(), other, nil), jpmc())
	if err != nil || len(got) != 2 || f.details != 2 {
		t.Fatalf("%d postings, %d details, err %v; want 2, 2, nil", len(got), f.details, err)
	}
	for _, p := range got {
		if strings.HasPrefix(p.Title, "other") {
			t.Errorf("reused %q", p.Title)
		}
	}
}

func TestNewWithDefersNewRowsPastMaxDetails(t *testing.T) {
	n := MaxDetails + 5
	f := &fake{ids: ids(n)}
	var st Stats
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s })
	got, err := list(t, s, jpmc())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails || f.details != MaxDetails || st.Deferred != 5 || st.Listed != n {
		t.Errorf("%d postings, %d details, stats %+v; want %d, %d, deferred 5", len(got), f.details, st, MaxDetails, MaxDetails)
	}
}

func TestNewFailsABoardNeedingMoreThanMaxDetails(t *testing.T) {
	f := &fake{ids: ids(MaxDetails + 1)}
	_, err := list(t, New(f.doer()), jpmc())
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
	if f.details != 0 {
		t.Errorf("%d detail requests before refusing", f.details)
	}
	f2 := &fake{ids: ids(MaxDetails)}
	if got, err := list(t, New(f2.doer()), jpmc()); err != nil || len(got) != MaxDetails {
		t.Fatalf("exactly MaxDetails: %d postings, err %v", len(got), err)
	}
}

func TestNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(srctest.New(nil), Options{}); err == nil {
		t.Fatal("NewWith without Stored accepted")
	}
}

func TestReportSeesAFailedBoardToo(t *testing.T) {
	f := &fake{ids: ids(20)}
	f.detail = func(id string) (srctest.Reply, bool) { return srctest.Reply{Status: 500}, id == f.ids[7] }
	var calls []Stats
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { calls = append(calls, s) })
	if _, err := list(t, s, jpmc()); err == nil {
		t.Fatal("want an error")
	}
	if len(calls) != 1 || calls[0].Listed != 20 || calls[0].Fetched != 7 {
		t.Errorf("Report calls %+v, want one with listed 20, fetched 7", calls)
	}
	// a board refused before listing is not reported
	calls = nil
	b := jpmc()
	b.Token = "../x"
	if _, err := list(t, s, b); err == nil || len(calls) != 0 {
		t.Errorf("refused board: err %v, %d reports", err, len(calls))
	}
}

// ---- fields ----

func TestRemoteReadsTheWorkplaceType(t *testing.T) {
	for _, tc := range []struct {
		text  string
		codes []string
		want  bool
	}{
		{"", []string{""}, false}, // every posting seen live
		{"", []string{"ORA_ON_SITE"}, false},
		{"", []string{"ORA_HYBRID", "ORA_ON_SITE"}, false},
		{"Remote", nil, true},
		{" remote ", nil, true},
		{"", []string{"ORA_REMOTE"}, true},
		{"", []string{"", "ora_remote"}, true},
		{"Remote-friendly office", nil, false},
	} {
		if got := remote(tc.text, tc.codes...); got != tc.want {
			t.Errorf("remote(%q, %q) = %v, want %v", tc.text, tc.codes, got, tc.want)
		}
	}
}

func TestPostedAtPrefersTheDetailInstantThenTheRowDay(t *testing.T) {
	inst := time.Date(2026, 10, 21, 8, 36, 47, 0, time.UTC)
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		start, day string
		want       time.Time
	}{
		{"2026-10-21T08:36:47+00:00", "2026-10-20", inst},
		{"", "2026-10-20", day},
		{"garbage", "2026-10-20", day},
		{"2026-10-21", "2026-10-20", day}, // not an instant: the row's day
		{"", "", time.Time{}},
		{"x", "y", time.Time{}},
		{"", "20/10/2026", time.Time{}},
	} {
		if got := postedAt(tc.start, tc.day); !got.Equal(tc.want) {
			t.Errorf("postedAt(%q, %q) = %v, want %v", tc.start, tc.day, got, tc.want)
		}
	}
}

func TestDescriptionJoinsTheThreeTextsAndStripsMarkup(t *testing.T) {
	got := description(detailItem{
		JobSchedule:                 "Part time",
		ExternalDescriptionStr:      `<p>About the role.</p><script>alert(1)</script>`,
		ExternalResponsibilitiesStr: `<ul><li>Own the pipeline</li></ul>`,
		ExternalQualificationsStr:   `<p>5 years of <b>Go</b></p>`,
	})
	for _, want := range []string{"Role: part-time", "About the role.", "Own the pipeline", "5 years of Go"} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q lacks %q", got, want)
		}
	}
	for _, bad := range []string{"<", "alert(1)", "&lt;"} {
		if strings.Contains(got, bad) {
			t.Errorf("description %q keeps %q", got, bad)
		}
	}
	if i, j := strings.Index(got, "About the role."), strings.Index(got, "5 years"); i > j {
		t.Error("qualifications come before the description")
	}
	if got := description(detailItem{}); got != "" {
		t.Errorf("an empty detail's description is %q", got)
	}
}

func TestDescriptionIsBounded(t *testing.T) {
	huge := "<p>" + strings.Repeat("word ", srcutil.MaxDescription) + "</p>"
	if got := description(detailItem{ExternalDescriptionStr: huge, JobSchedule: "Full time"}); len(got) > srcutil.MaxDescription {
		t.Errorf("description is %d bytes, over %d", len(got), srcutil.MaxDescription)
	}
}

func TestLocationsJoinDedupeAndCap(t *testing.T) {
	if got := joinLocations("  Plano,  TX ", []string{"plano, tx", "Palo Alto, CA", "", " "}); got != "Plano, TX; Palo Alto, CA" {
		t.Errorf("got %q", got)
	}
	var many []string
	for i := 0; i < 50; i++ {
		many = append(many, fmt.Sprintf("City %d", i))
	}
	if got := strings.Count(joinLocations("Home", many), ";") + 1; got != maxLocations {
		t.Errorf("%d locations joined, want %d", got, maxLocations)
	}
	if got := joinLocations("", nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestCompanyFallsBackToTheTenant(t *testing.T) {
	f := &fake{ids: ids(1)}
	b := jpmc()
	b.Company = " "
	got, err := list(t, New(f.doer()), b)
	if err != nil || len(got) != 1 || got[0].Company != "jpmc" {
		t.Fatalf("%v err %v, want company jpmc", got, err)
	}
}

func TestPostingURLComesFromTheBoardNotTheAnswer(t *testing.T) {
	f := &fake{ids: ids(1)}
	f.detail = func(id string) (srctest.Reply, bool) {
		return ok([]byte(`{"items":[{"Id":"` + id + `","Title":"T","ExternalDescriptionStr":"<a href='https://evil.test/'>x</a>","ExternalApplyUrl":"https://evil.test/"}]}`)), true
	}
	got, err := list(t, New(f.doer()), jpmc())
	if err != nil || len(got) != 1 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	if want := "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/job/" + f.ids[0]; got[0].URL != want {
		t.Errorf("url %q, want %q", got[0].URL, want)
	}
}

func TestKeyIsStableAcrossRunsAndLocalesOfTheSameRole(t *testing.T) {
	f := &fake{ids: ids(2)}
	a, err := list(t, New(f.doer()), jpmc())
	if err != nil {
		t.Fatal(err)
	}
	f2 := &fake{ids: []string{f.ids[1], f.ids[0]}} // the listing's order changed
	b, err := list(t, New(f2.doer()), jpmc())
	if err != nil {
		t.Fatal(err)
	}
	if a[0].Key != b[1].Key || a[1].Key != b[0].Key {
		t.Errorf("keys %q %q vs %q %q: a requisition keeps its key whatever its place in the listing", a[0].Key, a[1].Key, b[0].Key, b[1].Key)
	}
}

func TestATS(t *testing.T) {
	if New(srctest.New(nil)).ATS() != posting.OracleHCM {
		t.Fatal("wrong ATS")
	}
}
