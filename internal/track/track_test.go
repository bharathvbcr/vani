package track

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

func ptr[T any](v T) *T { return &v }

// sample is a valid, non-identifying decider.
func sample() Decider {
	return Decider{
		DeciderVersion: 1,
		ID:             "ml-systems",
		Label:          "ML systems",
		TargetSummary:  "Early-career ML systems roles: inference runtimes, GPU kernels and training throughput.",
		DropRules: []config.DropRule{
			{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
			{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
		},
		Seniority: Seniority{
			Want:    []string{"intern_or_new_grad", "mid"},
			Stretch: []string{"senior"},
			Drop:    []string{"staff_plus", "manager"},
		},
		EmployerPreferences: EmployerPreferences{Prefer: []string{"cap_exempt_likely"}, Use: "badge_and_rank", Drops: ptr(false)},
		Evidence:            []Evidence{{Area: "kernels", KBEntries: []string{"entry-a"}, Strength: "strong"}},
		TitleSignals: TitleSignals{Include: []string{"Inference", "GPU"}, Exclude: []string{"Sales"},
			Read: []config.TitleRead{{Slot: "seniority", Value: "staff_plus", Match: []string{"Staff"}, Unless: []string{"Member of Technical Staff"}}}},
	}
}

func writeTrack(t *testing.T, dir string, name string, v any) {
	t.Helper()
	var data []byte
	switch b := v.(type) {
	case []byte:
		data = b
	case string:
		data = []byte(b)
	default:
		var err error
		if data, err = json.MarshalIndent(v, "", "  "); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSampleIsValid(t *testing.T) {
	if p := Check(sample(), BuiltinWords(), Bank{"entry-a": {}}); len(p) != 0 {
		t.Fatalf("sample invalid: %v", p)
	}
}

func TestLoadRoundTripAndList(t *testing.T) {
	dir := t.TempDir()
	writeTrack(t, dir, "ml-systems.json", sample())
	writeTrack(t, dir, "broken.json", "{not json")
	writeTrack(t, dir, "README.md", "ignored")
	writeTrack(t, dir, WordsFile, "ignored by List")
	got, err := Load(dir, "ml-systems")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, sample())
	}
	es, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].ID != "broken" || es[0].Err == nil || es[1].ID != "ml-systems" || es[1].Err != nil {
		t.Fatalf("list: %+v", es)
	}
}

func TestLoadRefusesTraversalAndBadFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tracks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := sample()
	outside.ID = "outside"
	writeTrack(t, root, "outside.json", outside)
	if err := os.Symlink(filepath.Join(root, "outside.json"), filepath.Join(dir, "outside.json")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside", "..", "/etc/passwd", "a/b", "UPPER", "-lead", "", "a.b", strings.Repeat("a", 65)} {
		if _, err := Load(dir, id); err == nil || !strings.Contains(err.Error(), "not a track id") {
			t.Errorf("Load(%q) = %v, want a refused id", id, err)
		}
	}
	if _, err := Load(dir, "outside"); err == nil {
		t.Error("a symlink leading out of the tracks directory was followed")
	}

	wrongID := sample()
	wrongID.ID = "other"
	writeTrack(t, dir, "wrong-id.json", wrongID)
	writeTrack(t, dir, "unknown-field.json", strings.Replace(mustJSON(t, sample()), `"label"`, `"surprise": 1, "label"`, 1))
	writeTrack(t, dir, "trailing.json", mustJSON(t, sample())+"{}")
	writeTrack(t, dir, "huge.json", `{"id":"huge","gaps":["`+strings.Repeat("x", MaxFileBytes)+`"]}`)
	if err := os.Mkdir(filepath.Join(dir, "adir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"wrong-id": "does not match", "unknown-field": "unknown field", "trailing": "trailing data",
		"huge": "exceeds", "missing": "no such file", "adir": "not a regular file",
	} {
		if _, err := Load(dir, id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%s) = %v, want %q", id, err, want)
		}
	}
	if _, err := List(filepath.Join(root, "nope")); err == nil {
		t.Error("List of a missing directory must fail, not return no tracks")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCheckFindsProblems(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Decider)
		want string
	}{
		{"version", func(d *Decider) { d.DeciderVersion = 2 }, "decider_version"},
		{"empty summary", func(d *Decider) { d.TargetSummary = " \n " }, "target_summary is empty"},
		{"oversized summary", func(d *Decider) { d.TargetSummary = strings.Repeat("word ", 300) }, "max 1024"},
		{"email", func(d *Decider) { d.TargetSummary += " contact me at x@y.z" }, `"@"`},
		{"visa", func(d *Decider) { d.TargetSummary += " Needs H-1B transfer." }, `"h-1b"`},
		{"employer pref in summary", func(d *Decider) { d.TargetSummary += " Prefer cap-exempt universities." }, `"cap-exempt"`},
		{"nonprofit in summary", func(d *Decider) { d.TargetSummary += " At a nonprofit." }, `"nonprofit"`},
		{"unknown slot", func(d *Decider) { d.DropRules[0].Slot = "salary" }, "not a triage slot"},
		{"unknown value", func(d *Decider) { d.DropRules[0].Values = []string{"meh"} }, `"meh" is not a role_fit value`},
		{"empty values", func(d *Decider) { d.DropRules[0].Values = nil }, "values is empty"},
		{"sponsorship rule", func(d *Decider) {
			d.DropRules = append(d.DropRules, config.DropRule{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.9})
		}, "belong in the main config"},
		{"duplicate slot", func(d *Decider) { d.DropRules = append(d.DropRules, d.DropRules[0]) }, "second rule"},
		{"zero openrouter prob", func(d *Decider) { d.DropRules[0].MinOpenRouterProb = 0 }, "min_openrouter_prob"},
		{"openrouter prob over 1", func(d *Decider) { d.DropRules[0].MinOpenRouterProb = 1.5 }, "min_openrouter_prob"},
		{"negative lappi", func(d *Decider) { d.DropRules[0].MinLappiScore = -1 }, "min_lappi_score"},
		{"seniority overlap", func(d *Decider) { d.Seniority.Stretch = []string{"mid"} }, "partition"},
		{"seniority unknown", func(d *Decider) { d.Seniority.Want = []string{"wizard", "mid"} }, `"wizard"`},
		{"seniority drop differs", func(d *Decider) { d.DropRules[1].Values = []string{"manager"} }, "differs"},
		{"unknown preference", func(d *Decider) { d.EmployerPreferences.Prefer = []string{"remote_first"} }, "unknown preference"},
		{"duplicate preference", func(d *Decider) {
			d.EmployerPreferences.Prefer = []string{"nonprofit", "nonprofit"}
		}, "duplicate preference"},
		{"preference drops", func(d *Decider) { d.EmployerPreferences.Drops = ptr(true) }, "never drops"},
		{"preference drops missing", func(d *Decider) { d.EmployerPreferences.Drops = nil }, "never drops"},
		{"preference use", func(d *Decider) { d.EmployerPreferences.Use = "filter" }, "never drops"},
		{"evidence", func(d *Decider) { d.Evidence[0].KBEntries = []string{"ghost"} }, `"ghost"`},
		{"list bound", func(d *Decider) { d.WatchlistSearch = make([]string, maxListLen+1) }, "watchlist_search"},
		{"title read sponsorship", func(d *Decider) {
			d.TitleSignals.Read = append(d.TitleSignals.Read, config.TitleRead{Slot: "sponsorship", Value: "no_sponsorship", Match: []string{"Citizen"}})
		}, "never read from a title"},
		{"title read bad value", func(d *Decider) { d.TitleSignals.Read[0].Value = "lead" }, `"lead" is not a seniority value`},
		{"title read no match", func(d *Decider) { d.TitleSignals.Read[0].Match = nil }, "match is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := sample()
			c.mut(&d)
			probs := Check(d, BuiltinWords(), Bank{"entry-a": {}})
			if !slices.ContainsFunc(probs, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("problems %v, want one containing %q", probs, c.want)
			}
		})
	}
}

func TestWordsMatchWholeWordsOnly(t *testing.T) {
	d := sample()
	d.TargetSummary = "Optimization, leadership and reading code; adoption of GitHubs-like tools is fine."
	// "opt" in Optimization, "ead" in leadership/reading and "github" inside
	// "GitHubs" are not whole words.
	if p := Check(d, BuiltinWords(), nil); len(p) != 0 {
		t.Fatalf("false positives: %v", p)
	}
}

func TestPersonalWordsFile(t *testing.T) {
	dir := t.TempDir()
	w, err := LoadWords(dir)
	if err != nil || w.Personal {
		t.Fatalf("missing file: %v personal=%v, want builtin only", err, w.Personal)
	}
	writeTrack(t, dir, WordsFile, "# my list\n  Example   Corp \n\nzorblax\n")
	if w, err = LoadWords(dir); err != nil || !w.Personal {
		t.Fatalf("LoadWords: %v personal=%v", err, w.Personal)
	}
	d := sample()
	d.TargetSummary = "Roles like the one at example corp, building zorblax."
	probs := Check(d, w, nil)
	if len(probs) != 2 || !strings.Contains(probs[0], "example corp") || !strings.Contains(probs[1], "zorblax") {
		t.Fatalf("problems %v", probs)
	}
	writeTrack(t, dir, WordsFile, strings.Repeat("x", maxWordsBytes+1))
	if _, err := LoadWords(dir); err == nil {
		t.Error("an oversized word list was accepted")
	}
	writeTrack(t, dir, WordsFile, strings.Repeat("y", maxWordBytes+1))
	if _, err := LoadWords(dir); err == nil {
		t.Error("an oversized term was accepted")
	}
}

func TestLoadBank(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bank.json")
	writeTrack(t, dir, "bank.json", `{"entries":{"entry-a":{},"entry-b":{"x":1}}}`)
	b, err := LoadBank(p)
	if err != nil || len(b) != 2 {
		t.Fatalf("LoadBank: %v %v", b, err)
	}
	writeTrack(t, dir, "bank.json", `{"other":{}}`)
	if _, err := LoadBank(p); err == nil {
		t.Error("a bank without entries was accepted")
	}
	writeTrack(t, dir, "bank.json", `{`)
	if _, err := LoadBank(p); err == nil {
		t.Error("a malformed bank was accepted")
	}
}

func TestApplyKeepsTrackIndependentRules(t *testing.T) {
	cfg := config.Default()
	sponsorship := config.DropRule{Slot: "sponsorship", Values: []string{"no_sponsorship", "citizenship_or_clearance_required"}, MinOpenRouterProb: 0.95, MinLappiScore: 0.5}
	workMode := config.DropRule{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.8}
	oldRoleFit := config.DropRule{Slot: "role_fit", Values: []string{"adjacent", "mismatch_domain"}, MinOpenRouterProb: 0.7}
	cfg.DropRules = []config.DropRule{sponsorship, oldRoleFit, workMode}
	cfg.TargetSummary = "old summary"
	before, _ := json.Marshal(cfg)

	out, ch, err := Apply(cfg, sample(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(cfg); string(after) != string(before) {
		t.Fatal("Apply modified its input config")
	}
	want := []config.DropRule{sponsorship, workMode, sample().DropRules[0], sample().DropRules[1]}
	if !reflect.DeepEqual(out.DropRules, want) {
		t.Fatalf("drop rules:\n got %+v\nwant %+v", out.DropRules, want)
	}
	if !reflect.DeepEqual(ch.Removed, []config.DropRule{oldRoleFit}) || len(ch.Kept) != 2 || len(ch.Added) != 2 {
		t.Fatalf("change %+v", ch)
	}
	if out.TargetSummary != sample().TargetSummary || out.Track != "ml-systems" ||
		!reflect.DeepEqual(out.EmployerPreferences, []string{"cap_exempt_likely"}) {
		t.Fatalf("summary/track/prefs: %q %q %v", out.TargetSummary, out.Track, out.EmployerPreferences)
	}
	if !reflect.DeepEqual(out.TitleSignals, sample().TitleSignals) {
		t.Fatalf("title signals %+v", out.TitleSignals)
	}
	// The config gets its own copy of the title readings.
	src := sample()
	own, _, err := Apply(cfg, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	src.TitleSignals.Read[0].Match[0] = "changed"
	if own.TitleSignals.Read[0].Match[0] != "Staff" {
		t.Fatal("Apply shares the track's title readings with the config")
	}

	// Applying another track with no preferences clears them, and the
	// sponsorship rule is still byte-identical.
	d := sample()
	d.ID = "other"
	d.EmployerPreferences.Prefer = nil
	d.TitleSignals = TitleSignals{}
	out2, _, err := Apply(out, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out2.TitleSignals.Include != nil || out2.TitleSignals.Exclude != nil || out2.TitleSignals.Read != nil {
		t.Fatalf("second apply kept the previous track's title signals: %+v", out2.TitleSignals)
	}
	if out2.EmployerPreferences != nil || !reflect.DeepEqual(out2.DropRules[0], sponsorship) {
		t.Fatalf("second apply: prefs %v rules %+v", out2.EmployerPreferences, out2.DropRules)
	}
}

// Switching tracks removes the previous track's rules on slots the new track
// does not cover, but only when they are still exactly what that track set.
func TestApplyRemovesPreviousTrackRules(t *testing.T) {
	sponsorship := config.DropRule{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.95}
	prev := sample()
	prev.ID = "remote-only"
	workMode := config.DropRule{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.9}
	prev.DropRules = append(prev.DropRules, workMode)

	next := sample()
	next.ID = "next"
	next.DropRules = next.DropRules[1:] // seniority only: role_fit and work_mode are not covered

	cfg := config.Default()
	cfg.DropRules = []config.DropRule{sponsorship}
	cfg, _, err := Apply(cfg, prev, nil)
	if err != nil {
		t.Fatal(err)
	}

	out, ch, err := Apply(cfg, next, &prev)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.DropRule{sponsorship, next.DropRules[0]}
	if !reflect.DeepEqual(out.DropRules, want) {
		t.Fatalf("drop rules\n got %+v\nwant %+v", out.DropRules, want)
	}
	if len(ch.Removed) != 3 {
		t.Fatalf("removed %+v, want role_fit, seniority and work_mode", ch.Removed)
	}

	// A rule the owner edited after the previous track set it is kept, and
	// so is everything when the previous track is unknown.
	edited := cfg
	edited.DropRules = slices.Clone(cfg.DropRules)
	for i, r := range edited.DropRules {
		if r.Slot == "work_mode" {
			edited.DropRules[i] = config.DropRule{Slot: "work_mode", Values: []string{"onsite", "hybrid"}, MinOpenRouterProb: 0.9}
		}
	}
	out, _, err = Apply(edited, next, &prev)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(out.DropRules, func(r config.DropRule) bool { return r.Slot == "work_mode" }) {
		t.Fatal("an edited rule was removed as the previous track's")
	}
	out, _, err = Apply(cfg, next, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(out.DropRules, func(r config.DropRule) bool { return r.Slot == "work_mode" }) {
		t.Fatal("with no previous track, work_mode must be kept")
	}
	// The previous track can never take a sponsorship rule with it.
	prev.DropRules = append(prev.DropRules, sponsorship)
	out, _, err = Apply(cfg, next, &prev)
	if err != nil || !reflect.DeepEqual(out.DropRules[0], sponsorship) {
		t.Fatalf("sponsorship rule removed via the previous track: %+v %v", out.DropRules, err)
	}
}

func TestApplyRefusesSponsorshipAndInvalidResult(t *testing.T) {
	d := sample()
	d.DropRules = append(d.DropRules, config.DropRule{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.9})
	if _, _, err := Apply(config.Default(), d, nil); err == nil {
		t.Fatal("a track sponsorship rule was applied")
	}
	d = sample()
	d.EmployerPreferences.Prefer = []string{"remote_first"}
	if _, _, err := Apply(config.Default(), d, nil); err == nil || !strings.Contains(err.Error(), "employer_preferences") {
		t.Fatalf("an unknown preference reached the config: %v", err)
	}
}

func TestDir(t *testing.T) {
	if got := Dir(config.Config{}, "/s"); got != "/s/tracks" {
		t.Fatalf("default dir %q", got)
	}
	if got := Dir(config.Config{TracksDir: "/t"}, "/s"); got != "/t" {
		t.Fatalf("configured dir %q", got)
	}
}
