package smartrecruiters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestATSAndKeyPrefix(t *testing.T) {
	if got := New(nil).ATS(); got != "sr" {
		t.Fatalf("ATS = %q, want sr", got)
	}
	if got := (sources.Board{ATS: posting.SmartRecruiters, Token: " ServiceNow "}).KeyPrefix(); got != "sr:servicenow:" {
		t.Fatalf("KeyPrefix = %q", got)
	}
}

// ServiceNow's real first two rows and the real detail of the first, trimmed.
// The second row's detail is the first's with its own id and URL.
func TestListFromFixture(t *testing.T) {
	const id0, id1 = "744000158170259", "744000158164159"
	var d1 map[string]any
	if err := json.Unmarshal(read(t, "detail.json"), &d1); err != nil {
		t.Fatal(err)
	}
	d1["id"] = id1
	d1["name"] = "Senior Manager, StratOps - Office of the COO/CPO "
	d1["postingUrl"] = "https://jobs.smartrecruiters.com/ServiceNow/" + id1 + "-senior-manager-stratops-office-of-the-coo-cpo-"
	delete(d1, "applyUrl")
	d1["location"] = map[string]any{"city": "Santa Clara", "region": "California", "country": "us", "remote": false, "hybrid": false,
		"fullLocation": "Santa Clara, California, United States"}
	raw1, _ := json.Marshal(d1)
	const b = "https://api.smartrecruiters.com/v1/companies/ServiceNow/postings"
	d := srctest.New(map[string]srctest.Reply{
		b + "?limit=100&offset=0": okReply(string(read(t, "list.json"))),
		b + "/" + id0:             okReply(string(read(t, "detail.json"))),
		b + "/" + id1:             okReply(string(raw1)),
	})
	got, err := src(t, d, nil).List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	p := got[0]
	if p.Key != "sr:servicenow:"+id0 || p.ATS != posting.SmartRecruiters {
		t.Errorf("key/ats = %q/%q", p.Key, p.ATS)
	}
	if p.Company != "ServiceNow" || p.Title != "Senior Reliability Engineer - Veza" {
		t.Errorf("company/title = %q/%q", p.Company, p.Title)
	}
	if p.Location != "Minneapolis, Minnesota, United States" || !p.Remote {
		t.Errorf("location/remote = %q/%v, want the structured place and remote true", p.Location, p.Remote)
	}
	if p.Department != "Engineering, Infrastructure and Operations" {
		t.Errorf("department = %q", p.Department)
	}
	if p.URL != "https://jobs.smartrecruiters.com/ServiceNow/"+id0+"-senior-reliability-engineer-veza" ||
		p.ApplyURL != p.URL+"?oga=true" {
		t.Errorf("url/apply = %q / %q", p.URL, p.ApplyURL)
	}
	if want := time.Date(2026, 10, 27, 23, 38, 56, 725_000_000, time.UTC); !p.PostedAt.Equal(want) {
		t.Errorf("posted = %v, want %v", p.PostedAt, want)
	}
	if !p.SeenAt.Equal(fixedNow) {
		t.Errorf("seen = %v", p.SeenAt)
	}
	if !strings.HasPrefix(p.Description, "Role: full-time\n\n") {
		t.Errorf("description does not open with the role line: %.80q", p.Description)
	}
	jd := strings.Index(p.Description, "Job Description\n")
	co := strings.Index(p.Description, "Company Description\n")
	if jd < 0 || co < 0 || jd > co {
		t.Errorf("sections out of order or missing: job description at %d, company description at %d", jd, co)
	}
	if strings.ContainsAny(p.Description, "<>") || strings.Contains(p.Description, "&#") || strings.Contains(p.Description, "&nbsp") {
		t.Errorf("markup or entities left in the description: %.200q", p.Description)
	}
	if p.Description == "" || len(p.Description) > srcutil.MaxDescription || !utf8.ValidString(p.Description) {
		t.Errorf("description size %d valid=%v", len(p.Description), utf8.ValidString(p.Description))
	}
	if got[1].ApplyURL != "" || got[1].Title != "Senior Manager, StratOps - Office of the COO/CPO" {
		t.Errorf("second row: apply %q title %q", got[1].ApplyURL, got[1].Title)
	}
	if got[1].Remote {
		t.Error("second row is not remote")
	}
}

// Canva's real Madrid posting: an empty region ("Madrid, , Spain"), a hybrid
// role and an empty department.
func TestListCanvaEmptyRegionHybridNoDepartment(t *testing.T) {
	const id0, id1 = "6000000001526540", "6000000001522401"
	var d1 map[string]any
	if err := json.Unmarshal(read(t, "detail_canva.json"), &d1); err != nil {
		t.Fatal(err)
	}
	d1["id"] = id1
	d1["postingUrl"] = "https://jobs.smartrecruiters.com/Canva/" + id1 + "-account-executive"
	d1["applyUrl"] = "https://jobs.smartrecruiters.com/Canva/" + id1 + "-account-executive?oga=true"
	raw1, _ := json.Marshal(d1)
	const b = "https://api.smartrecruiters.com/v1/companies/Canva/postings"
	d := srctest.New(map[string]srctest.Reply{
		b + "?limit=100&offset=0": okReply(string(read(t, "list_canva.json"))),
		b + "/" + id0:             okReply(string(read(t, "detail_canva.json"))),
		b + "/" + id1:             okReply(string(raw1)),
	})
	got, err := src(t, d, nil).List(context.Background(), sources.Board{ATS: posting.SmartRecruiters, Token: "Canva"})
	if err != nil {
		t.Fatal(err)
	}
	p := got[0]
	if p.Location != "Madrid, Spain" {
		t.Errorf("location = %q, want the empty region dropped", p.Location)
	}
	if p.Remote || p.Department != "" {
		t.Errorf("remote %v department %q", p.Remote, p.Department)
	}
	if p.Company != "Canva" {
		t.Errorf("company %q: the API's name stands in when the board names none", p.Company)
	}
	if !strings.HasPrefix(p.Description, "Role: full-time, hybrid\n\n") {
		t.Errorf("description = %.60q, want the hybrid flag on the role line", p.Description)
	}
	if strings.Contains(p.Description, "Qualifications") || strings.Contains(p.Description, "Company Description") {
		t.Errorf("an empty section got a heading: %q", p.Description)
	}
	if !strings.Contains(p.Description, "Job Description\nJoin the team redefining") {
		t.Errorf("job description missing: %.200q", p.Description)
	}
}

func TestPlaceOf(t *testing.T) {
	for name, tc := range map[string]struct {
		l    location
		want string
	}{
		"full":               {location{FullLocation: "Santa Clara, California, United States"}, "Santa Clara, California, United States"},
		"empty part dropped": {location{FullLocation: "Madrid, , Spain"}, "Madrid, Spain"},
		"spaces":             {location{FullLocation: "  Kirkland ,Washington,  United States "}, "Kirkland, Washington, United States"},
		"repeat":             {location{FullLocation: "Singapore, Singapore"}, "Singapore"},
		"no full location":   {location{City: "Berlin", Region: "Berlin", Country: "de"}, "Berlin, DE"},
		"country only":       {location{Country: "ca"}, "CA"},
		"nothing":            {location{}, ""},
	} {
		if got := placeOf(tc.l); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestKeyIsLowerCasedTokenAndOwnId(t *testing.T) {
	a := board(1)
	b := srBoard() // token "ServiceNow": the request keeps its case, the key does not
	got, err := src(t, a.doer(), nil).List(context.Background(), b)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[0].Key != posting.Key("sr:servicenow:"+idOf(0)) {
		t.Fatalf("key %q", got[0].Key)
	}
}

func TestEmptyBoardIsAValidEmptyListing(t *testing.T) {
	a := board(0)
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d postings, %v; an empty board is a valid result", len(got), err)
	}
	if n := len(a.listQueries()); n != 1 {
		t.Fatalf("%d listing requests for an empty board, want 1", n)
	}
	if len(a.fetchedIDs()) != 0 {
		t.Fatal("a detail was asked for on an empty board")
	}
}

func TestRequestsAreWhatTheAPIDocumentsAndPaced(t *testing.T) {
	a := board(3)
	var waits []time.Duration
	s := src(t, a.doer(), &Options{Wait: func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}})
	if _, err := s.List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	qs := a.listQueries()
	if len(qs) != 1 || qs[0].Get("limit") != "100" || qs[0].Get("offset") != "0" || qs[0].Has("country") || len(qs[0]) != 2 {
		t.Fatalf("listing query %v, want limit=100&offset=0 and nothing else", qs)
	}
	// One listing page and three details: the first request waits for nothing,
	// each later one waits paceEvery.
	if len(waits) != 3 {
		t.Fatalf("%d waits for 4 requests, want 3", len(waits))
	}
	for _, w := range waits {
		if w != paceEvery {
			t.Fatalf("waited %v, want %v", w, paceEvery)
		}
	}
}

func TestHostileTokensAreRefusedBeforeAnyRequest(t *testing.T) {
	for _, tok := range []string{"", " ", "..", "a/b", "a b", "a:b", "a?x=1", "a#b", "%2e%2e", "a\\b", "-lead", strings.Repeat("a", 101), "café"} {
		a := board(1)
		d := a.doer()
		_, err := src(t, d, nil).List(context.Background(), sources.Board{ATS: posting.SmartRecruiters, Token: tok})
		if err == nil {
			t.Errorf("token %q: want an error", tok)
		}
		if n := len(d.URLs()); n != 0 {
			t.Errorf("token %q: %d requests sent: %v", tok, n, d.URLs())
		}
	}
}

func TestBoardOptionsThatDoNotApplyAreRefused(t *testing.T) {
	for name, b := range map[string]sources.Board{
		"search":      {Search: "engineer"},
		"searches":    {Searches: []string{"a"}},
		"host":        {Host: "careers.example.com"},
		"region":      {Region: "eu"},
		"maxpostings": {MaxPostings: 10},
		"other ats":   {ATS: posting.Lever},
		"bad filter":  {Filters: map[string][]string{"location": {"x"}}},
	} {
		b.Token = company
		if b.ATS == "" {
			b.ATS = posting.SmartRecruiters
		}
		a := board(1)
		d := a.doer()
		if _, err := src(t, d, nil).List(context.Background(), b); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(d.URLs()) != 0 {
			t.Errorf("%s: requests sent: %v", name, d.URLs())
		}
	}
}

func TestNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(nil, Options{}); err == nil {
		t.Fatal("NewWith without Stored must fail")
	}
	if _, err := NewWith(nil, Options{Stored: func(sources.Board) []posting.Posting { return nil }}); err != nil {
		t.Fatal(err)
	}
}
