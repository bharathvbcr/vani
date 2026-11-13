package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedLocRows writes postings with a status, a location and annotations.
func seedLocRows(t *testing.T, stateDir string, rows []locRow) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, r := range rows {
		p := posting.Posting{Key: posting.Key(r.key), ATS: "gh", Company: "Acme", Title: "Engineer " + r.key, Location: r.loc, Description: "jd"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: r.st, Posting: &p, Annotations: r.ann}); err != nil {
			t.Fatal(err)
		}
	}
}

type locRow struct {
	key, loc string
	st       posting.Status
	ann      map[string]string
}

func usOnly(t *testing.T, cfgPath string) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Exclude = config.Exclude{OnlyLocations: []string{"United States"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

// Under a US-only list, `vani prefilter` separates the postings it drops on a
// place the tables do not know from the ones it drops on a known place abroad,
// names the locations (most common first), lists the postings with --list and
// says where a US town is added. It counts the postings already dropped too,
// whose stored reason predates the cause.
func TestPrefilterListsUnrecognisedLocations(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	usOnly(t, cfgPath)
	seedLocRows(t, state, []locRow{
		{"gh:a:1", "Zzyzxville", posting.Live, nil},
		{"gh:a:2", "Quillmont Heights", posting.Live, nil},
		{"gh:a:3", "Quillmont Heights", posting.Live, nil},
		{"gh:a:4", "Paris, France", posting.Live, nil},
		{"gh:a:5", "Austin, TX", posting.Live, nil},
		{"gh:a:6", "Quillmont Heights", posting.Digested, nil}, // kept: the next run drops it
		{"gh:a:7", "Zzyzxville", posting.Dropped, map[string]string{ // dropped by an earlier run, before the cause was named
			"triage.backend": "exclude", "exclude.matched": "location is outside only_locations"}},
	})
	o, _ := mustCode(t, 0, "prefilter", "--json", "--list")
	var out struct {
		Report struct {
			Dropped      int `json:"dropped"`
			Unrecognised int `json:"unrecognised_location"`
			Places       []struct {
				Location string `json:"location"`
				Postings int    `json:"postings"`
			} `json:"unrecognised_places"`
			Rows []struct {
				Key      string `json:"key"`
				Location string `json:"location"`
				Stage    string `json:"stage"`
			} `json:"unrecognised"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("%q: %v", o, err)
	}
	r := out.Report
	if r.Dropped != 4 { // a:1 a:2 a:3 a:4 are live and dropped; the unrecognised ones are among them
		t.Errorf("dropped %d, want 4", r.Dropped)
	}
	if r.Unrecognised != 5 || len(r.Rows) != 5 {
		t.Fatalf("unrecognised %d rows %d, want 5: %s", r.Unrecognised, len(r.Rows), o)
	}
	if len(r.Places) != 2 || r.Places[0].Location != "Quillmont Heights" || r.Places[0].Postings != 3 || r.Places[1].Location != "Zzyzxville" || r.Places[1].Postings != 2 {
		t.Errorf("places = %+v", r.Places)
	}
	for _, row := range r.Rows {
		if row.Key == "gh:a:4" || row.Key == "gh:a:5" {
			t.Errorf("%s (a known place) is listed as unrecognised", row.Key)
		}
	}
	// Text: the count, the places and the pointer.
	txt, _ := mustCode(t, 0, "prefilter")
	for _, want := range []string{"unrecognised locations: 5 posting(s)", "    3  Quillmont Heights", "    2  Zzyzxville", "internal/location/us.go (usCityNames)", "exclude:location_unrecognised"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	// Without --list the postings are not listed, the counts are.
	o, _ = mustCode(t, 0, "prefilter", "--json")
	if strings.Contains(o, `"unrecognised":[`) || !strings.Contains(o, `"unrecognised_location":5`) {
		t.Errorf("json without --list: %s", o)
	}
	// With nothing unrecognised the section is absent.
	seedLocRows(t, state, []locRow{{"gh:a:1", "Austin, TX", posting.Live, nil}, {"gh:a:2", "Paris, France", posting.Live, nil},
		{"gh:a:3", "Paris, France", posting.Live, nil}, {"gh:a:6", "Paris, France", posting.Digested, nil}, {"gh:a:7", "Paris, France", posting.Dropped, nil}})
	txt, _ = mustCode(t, 0, "prefilter")
	if strings.Contains(txt, "unrecognised locations") {
		t.Errorf("a ledger with no unrecognised place still reports some:\n%s", txt)
	}
}

// The exclusion stage counts the drops that rest on an unrecognised place, in
// the run record, apart from the rest.
func TestExclusionSummaryCountsUnrecognisedLocations(t *testing.T) {
	x := config.Exclude{OnlyLocations: []string{"United States"}}
	pol := pipeline.DropPolicy{Exclude: x.Match}
	mk := func(k, loc string) ledger.Record {
		return ledger.Record{Key: posting.Key(k), Status: posting.Live, Posting: posting.Posting{Key: posting.Key(k), Title: "T", Location: loc}}
	}
	ch := pipeline.ExclusionChanges([]ledger.Record{
		mk("gh:a:1", "Zzyzxville"), mk("gh:a:2", "Quillmont Heights"), mk("gh:a:3", "Paris, France"), mk("gh:a:4", "Austin, TX"),
	}, pol, nil)
	s := summarizeExclusions(ch)
	if s.Dropped != 3 || s.UnrecognisedLocation != 2 {
		t.Errorf("summary %+v, want 3 dropped of which 2 unrecognised", s)
	}
	b, err := json.Marshal(s)
	if err != nil || !strings.Contains(string(b), `"unrecognised_location":2`) {
		t.Errorf("run record JSON %s %v", b, err)
	}
	if b, _ := json.Marshal(summarizeExclusions(pipeline.ExclusionChange{})); strings.Contains(string(b), "unrecognised") {
		t.Errorf("an empty summary names the count: %s", b)
	}
}

// A drop on an unrecognised place is its own audit class, so a wrong one has a
// stratum to surface in; one on a known place stays "exclude:location".
func TestAuditDrawsUnrecognisedLocationDropsAsTheirOwnClass(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedLocRows(t, state, []locRow{
		{"gh:u:1", "Zzyzxville", posting.Dropped, map[string]string{"triage.backend": "exclude", "exclude.matched": "location is outside only_locations (unrecognised location)"}},
		{"gh:u:2", "Quillmont Heights", posting.Dropped, map[string]string{"triage.backend": "exclude", "exclude.matched": "excluded: location is outside only_locations (unrecognised location)"}},
		{"gh:k:1", "Paris, France", posting.Dropped, map[string]string{"triage.backend": "exclude", "exclude.matched": "location is outside only_locations"}},
	})
	o, _ := mustCode(t, 0, "audit", "--list", "--seed", "w1", "--dropped", "100", "--scored", "0", "--closed", "0")
	var unrec, plain int
	for _, ln := range strings.Split(o, "\n") {
		switch {
		case strings.HasPrefix(ln, "dropped:exclude:location_unrecognised\t"):
			unrec++
		case strings.HasPrefix(ln, "dropped:exclude:location\t"):
			plain++
		}
	}
	if unrec != 2 || plain != 1 {
		t.Errorf("unrecognised %d plain %d, want 2 and 1:\n%s", unrec, plain, o)
	}
}
