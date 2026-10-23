package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func excludeOut(t *testing.T, stdout string) excludeReply {
	t.Helper()
	var r excludeReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

func setExcludeInput(t *testing.T, s string) {
	t.Helper()
	old := excludeInput
	excludeInput = strings.NewReader(s)
	t.Cleanup(func() { excludeInput = old })
}

func TestConfigExcludeReadsAndSets(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")

	o, _ := mustCode(t, 0, "config", "exclude")
	r := excludeOut(t, o)
	if r.Error != "" || r.Exclude.Titles == nil || len(r.Exclude.Titles) != 0 || r.MaxEntries != config.MaxExcludeEntries ||
		r.MaxDescriptions != config.MaxExcludeDescriptions || r.MaxEntryBytes != config.MaxExcludeEntryLen {
		t.Fatalf("empty read = %+v (every list is present and empty)", r)
	}

	setExcludeInput(t, `{"titles": [" intern ", "co-op"], "locations": ["India"], "companies": []}`)
	o, _ = mustCode(t, 0, "config", "exclude", "--set")
	if r := excludeOut(t, o); strings.Join(r.Exclude.Titles, ",") != "intern,co-op" || r.Exclude.Locations[0] != "India" || len(r.Exclude.Companies) != 0 {
		t.Fatalf("set reply = %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Exclude.Titles, ",") != "intern,co-op" || cfg.Exclude.Companies != nil {
		t.Errorf("saved %+v", cfg.Exclude)
	}

	// An empty object clears them, and the key leaves the file.
	setExcludeInput(t, `{}`)
	mustCode(t, 0, "config", "exclude", "--set")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"exclude"`) {
		t.Error("cleared exclusions left their key in the config")
	}
}

// The reply carries every list config.Exclude has: the app sends back what
// it was shown, so a list missing from the reply is cleared by its next save.
func TestConfigExcludeReplyCarriesEveryList(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	in := `{"titles": ["intern"], "title_unless": ["internal tools"], "companies": ["Acme"], ` +
		`"locations": ["India"], "only_locations": ["United States", "Canada"], "descriptions": ["security clearance"]}`
	setExcludeInput(t, in)
	mustCode(t, 0, "config", "exclude", "--set")
	o, _ := mustCode(t, 0, "config", "exclude")

	var got struct {
		Exclude map[string][]string `json:"exclude"`
	}
	if err := json.Unmarshal([]byte(o), &got); err != nil {
		t.Fatal(err)
	}
	var want map[string][]string
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	ty := reflect.TypeOf(config.Exclude{})
	for i := range ty.NumField() {
		key, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		if strings.Join(got.Exclude[key], "|") != strings.Join(want[key], "|") {
			t.Errorf("reply %s = %q, want %q", key, got.Exclude[key], want[key])
		}
	}
	if len(got.Exclude) != ty.NumField() {
		t.Errorf("reply has %d lists, config.Exclude %d", len(got.Exclude), ty.NumField())
	}

	// Sending the reply back unchanged keeps the config as it was.
	reply, err := json.Marshal(got.Exclude)
	if err != nil {
		t.Fatal(err)
	}
	setExcludeInput(t, string(reply))
	mustCode(t, 0, "config", "exclude", "--set")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Exclude.OnlyLocations, "|") != "United States|Canada" {
		t.Errorf("a round trip left only_locations %q", cfg.Exclude.OnlyLocations)
	}
}

func TestConfigExcludeRefusesBadInputAndKeepsTheFile(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ in, want string }{
		"unknown key":    {`{"title": ["intern"]}`, "unknown field"},
		"not an object":  {`["intern"]`, "not an exclusions object"},
		"trailing data":  {`{} {}`, "trailing data"},
		"empty entry":    {`{"titles": ["  "]}`, "exclude.titles[0] is empty"},
		"duplicate":      {`{"titles": ["Intern", "intern"]}`, "duplicate"},
		"too long":       {`{"companies": ["` + strings.Repeat("a", config.MaxExcludeEntryLen+1) + `"]}`, "over"},
		"over read cap":  {`{"titles": ["` + strings.Repeat("a", maxExcludeInput) + `"]}`, "larger than"},
		"unless alone":   {`{"title_unless": ["internal"]}`, "needs exclude.titles"},
		"control escape": {`{"titles": ["in\u0000tern"]}`, "control character"},
	} {
		t.Run(name, func(t *testing.T) {
			setExcludeInput(t, c.in)
			o, _ := mustCode(t, exitRuntime, "config", "exclude", "--set")
			r := excludeOut(t, o)
			if !strings.Contains(r.Error, c.want) || len(r.Exclude.Titles) != 0 {
				t.Errorf("reply %+v, want an error containing %q and no lists", r, c.want)
			}
			after, err := os.ReadFile(cfgPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("a refused set changed the file (%v)", err)
			}
		})
	}
	if code, _, _ := do(t, "config", "exclude", "extra"); code != exitUsage {
		t.Errorf("a stray argument exited %d", code)
	}
}

// The exclusions reach every prefilter the run uses, title-only ones
// included (Workday's row prefilter).
func TestExclusionsReachThePrefilters(t *testing.T) {
	cfg := config.Config{Exclude: config.Exclude{Titles: []string{"intern"}, Locations: []string{"India"}}}
	p := dropPolicy(cfg, "d1-ultra")
	drops := titleDrops(p)
	if drops == nil || !drops("SWE Intern") || drops("Software Engineer") {
		t.Fatal("workday title prefilter ignores the title exclusions")
	}
	d := prefilterDropper(p)
	if d == nil || !d(ledger.Record{Status: posting.Live, Posting: posting.Posting{Title: "Engineer", Location: "Pune, India"}}) {
		t.Fatal("advance prefilter ignores the location exclusions")
	}
	if d(ledger.Record{Status: posting.Scored, Posting: posting.Posting{Title: "Intern"}}) {
		t.Error("advance prefilter reports a posting past triage")
	}
	// Editing the config after the policy is built changes nothing.
	cfg.Exclude.Titles[0] = "engineer"
	if !drops("SWE Intern") || drops("Software Engineer") {
		t.Error("the policy shares the config's slices")
	}
}

// Jobs hides an unmarked kept posting the exclusions match, before any run
// records it, and shows a marked one.
func TestJobsListHidesExcluded(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedJobs(t, stateDir,
		seedJob{key: "gh:a:1", title: "Software Engineer Intern", status: posting.Explained},
		seedJob{key: "gh:a:2", title: "Software Engineer", status: posting.Explained},
		seedJob{key: "gh:a:3", title: "Data Intern", status: posting.Digested},
	)
	mustCode(t, 0, "jobs", "mark", "gh:a:3", "saved")
	if got := keys(listJSON(t)); got != "gh:a:1 gh:a:2" && got != "gh:a:2 gh:a:1" {
		t.Fatalf("before: %s", got)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Exclude.Titles = []string{"intern"}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	out := listJSON(t)
	if got := keys(out); got != "gh:a:2" || out.Counts["new"] != 1 {
		t.Fatalf("new view: %s %v", got, out.Counts)
	}
	if got := keys(listJSON(t, "--view", "saved")); got != "gh:a:3" {
		t.Errorf("a saved posting the exclusions match is hidden: %s", got)
	}
}

func TestPrefilterReportsExclusions(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Exclude = config.Exclude{Titles: []string{"intern"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	l := seedJobs(t, stateDir,
		seedJob{key: "gh:a:1", title: "Intern", status: posting.Live},
		seedJob{key: "gh:a:2", title: "Data Intern", status: posting.Scored, fit: "strong_fit"},
		seedJob{key: "gh:a:3", title: "Engineer", status: posting.Explained},
		seedJob{key: "gh:a:4", title: "Platform Engineer", status: posting.Dropped},
	)
	// gh:a:4 was excluded under an earlier exclusion that no longer matches.
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: "gh:a:4", Status: posting.Dropped,
		Annotations: map[string]string{pipeline.AnnExcluded: `excluded: title matches "platform"`}}); err != nil {
		t.Fatal(err)
	}
	out, _ := mustCode(t, 0, "prefilter", "--list")
	for _, want := range []string{
		"1 of 1 live posting(s) would be dropped before triage",
		"1 kept posting(s) would be dropped (marked ones are kept), 1 excluded posting(s) would return to live",
		"gh:a:2\tData Intern\texcluded: title matches \"intern\" (now scored)",
		"gh:a:4\tPlatform Engineer\treturns to live (was excluded: title matches \"platform\")",
		"read strong_fit or partial_fit (false drops): 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A run drops what the exclusions match before advancing anything. The
// advance limit is invalid on purpose so the run stops right after.
func TestRunOnceAppliesExclusionsBeforeAdvancing(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	cfg.AdvancePerRun = -1
	cfg.Exclude = config.Exclude{Titles: []string{"data engineer"}}
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer", "Platform Engineer")
	if err := w.runOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "advance limit") {
		t.Fatalf("err = %v\n%s", err, warn.String())
	}
	for _, r := range w.led.All() {
		want := posting.Live
		if r.Posting.Title == "Data Engineer" {
			want = posting.Dropped
		}
		if r.Status != want {
			t.Errorf("%s %q: %s, want %s", r.Key, r.Posting.Title, r.Status, want)
		}
	}
	if !strings.Contains(warn.String(), "exclusions: dropped 1 posting(s), returned 0 to live") {
		t.Errorf("warnings: %s", warn.String())
	}
}
