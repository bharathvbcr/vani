package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestExcludeMatchTitles(t *testing.T) {
	x := Exclude{Titles: []string{"intern", "Internship", "co-op", "coop"}, TitleUnless: []string{"internal tools"}}
	for title, want := range map[string]string{
		"Software Engineer Intern":               `title matches "intern"`,
		"SWE INTERN (Summer 2027)":               `title matches "intern"`,
		"Machine Learning Internship":            `title matches "Internship"`,
		"Co-Op, Firmware":                        `title matches "co-op"`,
		"Engineering Coop":                       `title matches "coop"`,
		"Intern,  Internal Tools":                "", // unless lifts the title exclusion
		"Internal Platform Engineer":             "", // whole words: "intern" is not "Internal"
		"Internationalization Engineer":          "",
		"Senior Engineer, Co-Operative Programs": "", // "co-op" is not "co-operative"
		"":                                       "",
		"   ":                                    "",
	} {
		why, ok := x.Match(posting.Posting{Title: title})
		if why != want || ok != (want != "") {
			t.Errorf("%q: (%q, %v), want %q", title, why, ok, want)
		}
	}
}

func TestExcludeMatchCompanies(t *testing.T) {
	x := Exclude{Companies: []string{"Acme  Corp"}}
	for company, want := range map[string]bool{"acme corp": true, " ACME Corp ": true, "Acme": false, "Acme Corp Labs": false, "": false} {
		if _, ok := x.Match(posting.Posting{Company: company}); ok != want {
			t.Errorf("%q: %v, want %v", company, ok, want)
		}
	}
}

// A posting is excluded by location only when every option it gives (each
// place, and remote when it is remote) is excluded.
func TestExcludeMatchLocations(t *testing.T) {
	x := Exclude{Locations: []string{"India", "London", "TX", "Washington"}}
	for _, c := range []struct {
		loc    string
		remote bool
		want   string
	}{
		{"Bangalore, India", false, "India"},
		{"Bengaluru, Karnataka, India", false, "India"},
		{"London, United Kingdom", false, "London"},
		{"Austin, TX", false, "TX"},
		{"Austin, Texas, United States", false, "TX"},
		{"Bangalore, India; London, UK", false, "India"},
		{"Bangalore, India; San Francisco, CA", false, ""}, // San Francisco is acceptable
		{"Bangalore, India", true, "India"},                // board-flagged remote with no scope: remote within the place
		{"Remote (India)", false, "India"},
		{"India - Remote", false, "India"},
		{"Remote - US", false, ""},
		{"Mountain View, CA / Remote (Continental US + Hawaii + Canada Only)", false, ""},
		{"Seattle, WA", false, "Washington"},
		{"Washington, DC", false, ""},       // the state entry is not the District
		{"New London, CT", false, "London"}, // entries match whole words of the area
		{"Londonderry, NH", false, ""},      // ...never part of a word
		{"Remote - Texas", false, ""},       // the remote option says US, which is acceptable
		{"", false, ""},
		{"TBD", false, ""},
	} {
		why, ok := x.Match(posting.Posting{Location: c.loc, Remote: c.remote})
		want := ""
		if c.want != "" {
			want = `location matches "` + c.want + `"`
		}
		if why != want || ok != (want != "") {
			t.Errorf("%q remote=%v: (%q, %v), want %q", c.loc, c.remote, why, ok, want)
		}
	}
}

func TestExcludeMatchRemoteAndCountry(t *testing.T) {
	remote := Exclude{Locations: []string{"Remote"}}
	if _, ok := remote.Match(posting.Posting{Location: "Remote - US"}); !ok {
		t.Error(`"remote" kept a remote-only role`)
	}
	if _, ok := remote.Match(posting.Posting{Location: "New York, NY; Remote"}); ok {
		t.Error(`"remote" dropped a role that also lists New York`)
	}
	if _, ok := remote.Match(posting.Posting{Location: "New York, NY"}); ok {
		t.Error(`"remote" dropped an on-site role`)
	}
	if _, ok := remote.Match(posting.Posting{Location: "Remote"}); !ok {
		t.Error(`"remote" kept a bare remote role`)
	}
	us := Exclude{Locations: []string{"United States"}}
	for _, loc := range []string{"Austin, TX", "Remote (US)"} {
		if _, ok := us.Match(posting.Posting{Location: loc}); !ok {
			t.Errorf(`"United States" kept %q`, loc)
		}
	}
	// A scope of several places needs each excluded.
	if _, ok := us.Match(posting.Posting{Location: "Remote (US, Canada)"}); ok {
		t.Error(`"United States" dropped a role also open in Canada`)
	}
	both := Exclude{Locations: []string{"USA", "Canada"}}
	if _, ok := both.Match(posting.Posting{Location: "Remote (US, Canada)"}); !ok {
		t.Error(`"USA" and "Canada" kept "Remote (US, Canada)"`)
	}
	if _, ok := us.Match(posting.Posting{Location: "Toronto, Ontario, Canada"}); ok {
		t.Error(`"United States" dropped a Canadian role`)
	}
}

// only_locations keeps a posting when any option it lists is in the list,
// and keeps what it cannot read.
func TestExcludeOnlyLocations(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"United States", "Canada"}}
	for _, c := range []struct {
		loc    string
		remote bool
		keep   bool
	}{
		{"Austin, TX", false, true},
		{"Toronto, Ontario, Canada", false, true},
		{"London, United Kingdom", false, false},
		{"Bangalore, India; Seattle, WA", false, true}, // Seattle is in the list
		{"Bangalore, India; London, UK", false, false},
		{"Remote (US)", false, true},
		{"Remote (US, Canada)", false, true},
		{"Remote (Europe)", false, false},
		{"Remote - India", false, false}, // remote within India
		{"Bangalore, India", true, false},
		{"Remote", false, true}, // no place, no scope: unknown, kept
		{"", false, true},
		{"TBD", false, true},
	} {
		why, ok := x.Match(posting.Posting{Location: c.loc, Remote: c.remote})
		if ok == c.keep || (ok && why != "location is outside only_locations") {
			t.Errorf("%q remote=%v: (%q, %v), want keep=%v", c.loc, c.remote, why, ok, c.keep)
		}
	}
	// "remote" keeps a role whose remote option stands on its own.
	r := Exclude{OnlyLocations: []string{"TX", "remote"}}
	for loc, keep := range map[string]bool{"Remote (Europe)": true, "Austin, TX": true, "Denver, CO": false, "Denver, CO; Remote (US)": true} {
		if _, ok := r.Match(posting.Posting{Location: loc}); ok == keep {
			t.Errorf("%q: dropped=%v, want keep=%v", loc, ok, keep)
		}
	}
	// With locations, it narrows further: the US, but not Texas.
	both := Exclude{OnlyLocations: []string{"US"}, Locations: []string{"Texas"}}
	if _, ok := both.Match(posting.Posting{Location: "Austin, TX"}); !ok {
		t.Error("an excluded state inside only_locations was kept")
	}
	if _, ok := both.Match(posting.Posting{Location: "Denver, CO"}); ok {
		t.Error("a place in only_locations was dropped")
	}
	if (Exclude{OnlyLocations: []string{"US"}}).Empty() {
		t.Error("only_locations alone is not empty")
	}
}

// The ledger never learns which description phrase matched.
func TestExcludeMatchDescriptionHidesThePhrase(t *testing.T) {
	x := Exclude{Descriptions: []string{"security clearance"}}
	why, ok := x.Match(posting.Posting{Description: "Requires an active\nSecurity   Clearance."})
	if !ok || strings.Contains(strings.ToLower(why), "clearance") {
		t.Fatalf("(%q, %v)", why, ok)
	}
	if _, ok := x.Match(posting.Posting{Description: "No clearance needed"}); ok {
		t.Error("a partial phrase matched")
	}
}

// Title is checked first, so the reason names the title.
func TestExcludeMatchOrder(t *testing.T) {
	x := Exclude{Titles: []string{"intern"}, Companies: []string{"Acme"}}
	if why, _ := x.Match(posting.Posting{Title: "Intern", Company: "Acme"}); why != `title matches "intern"` {
		t.Errorf("reason %q", why)
	}
	if !(Exclude{TitleUnless: []string{"x"}}).Empty() || (Exclude{Locations: []string{"x"}}).Empty() {
		t.Error("Empty")
	}
}

func TestExcludeValidate(t *testing.T) {
	long := strings.Repeat("a", MaxExcludeEntryLen+1)
	many := make([]string, MaxExcludeEntries+1)
	for i := range many {
		many[i] = "w" + strings.Repeat("x", i)
	}
	for name, c := range map[string]struct {
		x    Exclude
		want string
	}{
		"empty entry":        {Exclude{Titles: []string{" "}}, "exclude.titles[0] is empty"},
		"punctuation place":  {Exclude{Locations: []string{"--"}}, "exclude.locations[0] is empty"},
		"too long":           {Exclude{Companies: []string{long}}, "exclude.companies[0] is over"},
		"control character":  {Exclude{Titles: []string{"in\x00tern"}}, "control character"},
		"bad utf-8":          {Exclude{Titles: []string{"\xff"}}, "not valid UTF-8"},
		"duplicate folded":   {Exclude{Titles: []string{"Intern", " intern"}}, `duplicate " intern"`},
		"too many":           {Exclude{Titles: many}, "exclude.titles has 129 entries"},
		"too many desc":      {Exclude{Descriptions: many[:MaxExcludeDescriptions+1]}, "exclude.descriptions has 33 entries"},
		"unless alone":       {Exclude{TitleUnless: []string{"internal"}}, "exclude.title_unless needs exclude.titles"},
		"duplicate location": {Exclude{Locations: []string{"New York", "new-york"}}, "duplicate"},
	} {
		c := c
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Exclude = c.x
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	cfg := Default()
	cfg.Exclude = Exclude{Titles: []string{"intern", "co-op"}, TitleUnless: []string{"internal"}, Companies: []string{"Acme"},
		Locations: []string{"India", "remote"}, Descriptions: []string{"clearance"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("good exclusions refused: %v", err)
	}
}

// Empty lists leave the key out of the file; a full set round-trips; an
// unknown key inside exclude is refused.
func TestExcludeSaveAndParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Exclude = Exclude{Titles: []string{}, Locations: []string{}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"exclude"`) {
		t.Errorf("empty exclusions written:\n%s", data)
	}
	cfg.Exclude = Exclude{Titles: []string{"intern"}, Locations: []string{"India"}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Exclude.Titles) != 1 || got.Exclude.Locations[0] != "India" {
		t.Errorf("round trip: %+v", got.Exclude)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["exclude"] = map[string]any{"title": []string{"intern"}}
	bad, _ := json.Marshal(doc)
	if _, err := Parse(bad); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("unknown exclude key: %v", err)
	}
}

func TestExcludeClone(t *testing.T) {
	x := Exclude{Titles: []string{"intern"}, TitleUnless: []string{"a"}, Companies: []string{"b"}, Locations: []string{"c"}, Descriptions: []string{"d"}}
	y := x.Clone()
	y.Titles[0], y.TitleUnless[0], y.Companies[0], y.Locations[0], y.Descriptions[0] = "z", "z", "z", "z", "z"
	if x.Titles[0] != "intern" || x.TitleUnless[0] != "a" || x.Companies[0] != "b" || x.Locations[0] != "c" || x.Descriptions[0] != "d" {
		t.Errorf("clone shares a slice: %+v", x)
	}
}
