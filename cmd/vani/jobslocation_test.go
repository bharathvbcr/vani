package main

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type locSeed struct {
	key, where string
	remote     bool
}

var locSeeds = []locSeed{
	{key: "gh:a:ep", where: "El Paso, TX"},
	{key: "gh:a:lc", where: "Las Cruces, New Mexico; El Paso, TX"},
	{key: "gh:b:rus", where: "Remote - US"},
	{key: "gh:b:flag", where: "Austin, TX", remote: true},
	{key: "gh:c:ldn", where: "London, United Kingdom"},
	{key: "gh:c:none", where: "2 Locations"},
}

func seedLocations(t *testing.T, stateDir string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for i, s := range locSeeds {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: "Acme", Title: "Engineer " + s.key,
			Location: s.where, Remote: s.remote}
		e := ledger.Event{V: 1, At: jobsT0.Add(-time.Duration(i+1) * time.Hour), Key: p.Key, Status: posting.Explained, Posting: &p}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJobsPlacesAndRemoteListed(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedLocations(t, state)
	l := listJSON(t, "--view", "all")
	ep := findJob(t, l, "gh:a:ep")
	if len(ep.Places) != 1 || ep.Places[0] != (jobview.Place{Area: "El Paso", State: "TX"}) || ep.RemoteListed {
		t.Errorf("El Paso: %+v remote %v", ep.Places, ep.RemoteListed)
	}
	if r := findJob(t, l, "gh:b:rus"); !r.RemoteListed || r.RemoteScope != "US" || len(r.Places) != 0 {
		t.Errorf("Remote - US: %+v", r)
	}
	// The board's flag counts as listed remote even when the text names a city.
	if f := findJob(t, l, "gh:b:flag"); !f.RemoteListed || f.RemoteScope != "" || f.Places[0].State != "TX" {
		t.Errorf("flagged remote: %+v", f)
	}
	if n := findJob(t, l, "gh:c:none"); len(n.Places) != 0 || n.Location != "2 Locations" {
		t.Errorf("placeholder location: %+v", n)
	}
	// jobs show carries the same fields.
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:a:lc")
	var j jobview.Job
	if err := json.Unmarshal([]byte(o), &j); err != nil || len(j.Places) != 2 || j.Places[0].State != "NM" {
		t.Errorf("show %q: %v", o, err)
	}
}

func TestJobsLocationFilters(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedLocations(t, state)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--state", "TX"}, "gh:a:ep gh:a:lc gh:b:flag"},
		{[]string{"--state", "tx"}, "gh:a:ep gh:a:lc gh:b:flag"},
		{[]string{"--state", "new mexico"}, "gh:a:lc"},
		{[]string{"--state", "NM,texas"}, "gh:a:ep gh:a:lc gh:b:flag"},
		{[]string{"--state", "none"}, "gh:b:rus gh:c:ldn gh:c:none"},
		{[]string{"--area", "el paso"}, "gh:a:ep gh:a:lc"},
		{[]string{"--area", "London,Austin"}, "gh:b:flag gh:c:ldn"},
		{[]string{"--area", "none"}, "gh:b:rus gh:c:none"},
		{[]string{"--remote"}, "gh:b:rus gh:b:flag"},
		{[]string{"--remote", "--state", "TX"}, "gh:b:flag"},
		{[]string{"--state", "TX", "--area", "Austin"}, "gh:b:flag"},
		{[]string{"--state", "CA"}, ""},
		// --or-remote: in these places, or listed as remote anywhere.
		{[]string{"--state", "NM", "--or-remote"}, "gh:a:lc gh:b:rus gh:b:flag"},
		{[]string{"--state", "CA", "--or-remote"}, "gh:b:rus gh:b:flag"},
		{[]string{"--area", "London", "--or-remote"}, "gh:b:rus gh:b:flag gh:c:ldn"},
		{[]string{"--state", "TX", "--area", "El Paso", "--or-remote"}, "gh:a:ep gh:a:lc gh:b:rus gh:b:flag"},
		{[]string{"--state", "none", "--or-remote"}, "gh:b:rus gh:b:flag gh:c:ldn gh:c:none"},
	} {
		got := keys(listJSON(t, append([]string{"--view", "all"}, c.args...)...))
		if !sameKeys(got, c.want) {
			t.Errorf("%q: %q, want %q", c.args, got, c.want)
		}
	}
	for _, args := range [][]string{
		{"jobs", "list", "--state", "XX"},
		{"jobs", "list", "--state", "TX,"},
		{"jobs", "list", "--state", "Ontario"},
		{"jobs", "list", "--area", "El Paso,,Austin"},
		{"jobs", "list", "--area", manyAreas(maxAreas + 1)},
		{"jobs", "list", "--state", strings.Repeat("T", 5000)},
		{"jobs", "stats", "--state", "\xff"},
		{"jobs", "list", "--or-remote"},
		{"jobs", "stats", "--or-remote"},
		{"jobs", "list", "--or-remote", "--remote", "--state", "TX"},
	} {
		mustCode(t, 2, args...)
	}
	mustCode(t, 0, "jobs", "list", "--area", manyAreas(maxAreas)) // the cap itself is allowed
}

func TestJobsLocationStats(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedLocations(t, state)
	o, _ := mustCode(t, 0, "jobs", "stats", "--json", "--view", "all")
	var s jobview.Stats
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatalf("stats %q: %v", o, err)
	}
	// gh:a:lc names El Paso and Las Cruces: one TX, one NM; each job counts a
	// state once.
	if s.State["TX"] != 3 || s.State["NM"] != 1 || s.State["none"] != 3 {
		t.Errorf("state %+v", s.State)
	}
	// El Paso, Las Cruces, Remote - US and the flagged Austin role are the
	// United States; London is the United Kingdom; "2 Locations" names none.
	if s.Country["United States"] != 4 || s.Country["United Kingdom"] != 1 || s.Country["none"] != 1 {
		t.Errorf("country %+v", s.Country)
	}
	if s.RemoteListed != 2 || s.RemoteScope["US"] != 1 || s.RemoteScope["unstated"] != 1 {
		t.Errorf("remote %d %+v", s.RemoteListed, s.RemoteScope)
	}
	if len(s.Area) == 0 || s.Area[0] != (jobview.AreaStat{Area: "El Paso", State: "TX", N: 2}) {
		t.Errorf("area %+v", s.Area)
	}
	o, _ = mustCode(t, 0, "jobs", "stats", "--json", "--remote")
	if err := json.Unmarshal([]byte(o), &s); err != nil || s.Total != 2 || s.RemoteListed != 2 {
		t.Errorf("filtered stats %q", o)
	}
	o, _ = mustCode(t, 0, "jobs", "stats", "--view", "all")
	if !strings.Contains(o, "where: remote 2, TX 3, NM 1, none 3") || !strings.Contains(o, "country: United States 4, United Kingdom 1, none 1") {
		t.Errorf("text stats %q", o)
	}
}

func TestJobsCountryFilter(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:tx", title: "TX", status: posting.Explained, location: "Austin, TX"},
		seedJob{key: "gh:a:de", title: "DE", status: posting.Explained, location: "Berlin, Germany"},
		seedJob{key: "gh:a:in", title: "IN", status: posting.Explained, location: "Bengaluru, IN"},
		seedJob{key: "gh:a:uk", title: "UK", status: posting.Explained, location: "London, United Kingdom"},
		seedJob{key: "gh:a:rus", title: "RUS", status: posting.Explained, location: "Remote - US"},
		seedJob{key: "gh:a:eu", title: "EU", status: posting.Explained, location: "Remote - Europe"},
		seedJob{key: "gh:a:none", title: "NONE", status: posting.Explained, location: "2 Locations"},
		seedJob{key: "gh:a:both", title: "BOTH", status: posting.Explained, location: "Berlin, DE; Austin, TX"},
	)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--country", "Germany"}, "gh:a:both gh:a:de"},
		{[]string{"--country", "germany,DE"}, "gh:a:both gh:a:de"},
		{[]string{"--country", "US"}, "gh:a:both gh:a:rus gh:a:tx"},
		{[]string{"--country", "United States,United Kingdom"}, "gh:a:both gh:a:rus gh:a:tx gh:a:uk"},
		{[]string{"--country", "India"}, "gh:a:in"},
		{[]string{"--country", "none"}, "gh:a:eu gh:a:none"},
		{[]string{"--country", "Germany", "--or-remote"}, "gh:a:both gh:a:de gh:a:eu gh:a:rus"},
		{[]string{"--country", "Germany", "--remote"}, ""},
		{[]string{"--country", "US", "--remote"}, "gh:a:rus"},
		{[]string{"--country", "Germany", "--state", "TX"}, "gh:a:both"},
		{[]string{"--country", "France"}, ""},
	} {
		got := keys(listJSON(t, append([]string{"--view", "all"}, c.args...)...))
		if !sameKeys(got, c.want) {
			t.Errorf("%q: %q, want %q", c.args, got, c.want)
		}
	}
	for _, args := range [][]string{
		{"jobs", "list", "--country", "Texas"},
		{"jobs", "list", "--country", "XX"},
		{"jobs", "list", "--country", "Germany,"},
		{"jobs", "list", "--country", "Europe"},
		{"jobs", "export", "--country", "Notaland"},
		{"jobs", "list", "--country", strings.Join(tooManyCountries(), ",")},
	} {
		mustCode(t, 2, args...)
	}
	mustCode(t, 0, "jobs", "list", "--view", "all", "--country", strings.Join(tooManyCountries()[:maxCountries], ","))
	out := filepath.Join(t.TempDir(), "jobs.xlsx")
	o, _ := mustCode(t, 0, "jobs", "export", "--json", "--dry-run", "--view", "all", "--country", "Germany", "--top", "10", "--out", out)
	var reply struct {
		Total  int  `json:"total"`
		Rows   int  `json:"rows"`
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(o), &reply); err != nil || !reply.DryRun || reply.Total != 2 || reply.Rows != 2 {
		t.Fatalf("export %q: %+v %v", o, reply, err)
	}
	// The place menu counts the search before --country, --state and --remote,
	// so ticking Germany still offers the United States.
	wantCountries := map[string]int{"United States": 3, "Germany": 2, "India": 1, "United Kingdom": 1, "none": 2}
	all := listJSON(t, "--view", "all")
	if all.Where == nil || !maps.Equal(all.Where.Country, wantCountries) {
		t.Fatalf("where %+v, want %+v", all.Where, wantCountries)
	}
	for _, args := range [][]string{
		{"--country", "Germany"},
		{"--state", "TX"},
		{"--remote"},
	} {
		got := listJSON(t, append([]string{"--view", "all"}, args...)...)
		if got.Where == nil || !maps.Equal(got.Where.Country, wantCountries) {
			t.Errorf("%q where %+v, want the unfiltered countries", args, got.Where)
		}
	}
	if de := listJSON(t, "--view", "all", "--country", "Germany"); de.Total != 2 {
		t.Errorf("Germany still filters the list: total %d", de.Total)
	}
}

// tooManyCountries is one more distinct country than --country allows.
func tooManyCountries() []string {
	return []string{
		"US", "UK", "CA", "MX", "DE", "FR", "IN", "IE", "NL", "BE", "ES", "IT", "PT", "CH", "AT",
		"SE", "NO", "DK", "FI", "PL", "CZ", "RO", "HU", "GR", "UA", "TR", "AE", "SG", "JP", "KR",
		"CN", "TW", "HK", "AU", "NZ", "BR", "CL", "PH", "VN", "TH", "MY", "ZA", "NG", "KE", "EG",
		"CR", "LU", "IS", "SK", "SI", "HR",
	}
}

func sameKeys(got, want string) bool {
	g, w := strings.Fields(got), strings.Fields(want)
	if len(g) != len(w) {
		return false
	}
	seen := map[string]int{}
	for _, k := range g {
		seen[k]++
	}
	for _, k := range w {
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}

func manyAreas(n int) string {
	var a []string
	for i := range n {
		a = append(a, "Town"+strconv.Itoa(i))
	}
	return strings.Join(a, ",")
}

func TestJobsTextListShowsWhere(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedLocations(t, state)
	o, _ := mustCode(t, 0, "jobs", "list", "--view", "all")
	for _, want := range []string{
		"Engineer gh:a:lc · Las Cruces, NM; El Paso, TX  [gh:a:lc]",
		"Engineer gh:b:rus · Remote (US)  [gh:b:rus]",
		"Engineer gh:b:flag · Austin, TX · Remote  [gh:b:flag]",
		"Engineer gh:c:none · 2 Locations  [gh:c:none]",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("text list missing %q:\n%s", want, o)
		}
	}
	if got := whereText(jobview.Job{}); got != "" {
		t.Errorf("empty location: %q", got)
	}
	// A record with no places (read before parsing ran) that only says remote.
	if got := whereText(jobview.Job{Location: "Remote - US", Remote: true}); got != " · Remote" {
		t.Errorf("no places, remote text: %q", got)
	}
}
