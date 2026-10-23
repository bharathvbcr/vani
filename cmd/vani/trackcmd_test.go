package main

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

// testTrack is a valid, non-identifying decider file.
const testTrack = `{
  "decider_version": 1,
  "id": "ml-systems",
  "label": "ML systems",
  "target_summary": "Early-career ML systems roles: inference runtimes and GPU kernels.",
  "drop_rules": [
    {"slot": "role_fit", "values": ["mismatch_domain"], "min_jev_prob": 0.9, "min_lappi_score": 0},
    {"slot": "seniority", "values": ["staff_plus", "manager"], "min_jev_prob": 0.9, "min_lappi_score": 0}
  ],
  "seniority": {"want": ["intern_or_new_grad", "mid"], "stretch": ["senior"], "drop": ["staff_plus", "manager"]},
  "employer_preferences": {"prefer": ["cap_exempt_likely"], "use": "badge_and_rank", "drops": false},
  "devprism_persona": null
}`

// trackSetup writes a config with a sponsorship and a work-mode rule, and a
// tracks directory holding testTrack.
func trackSetup(t *testing.T) (cfgPath, tracksDir string) {
	t.Helper()
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	tracksDir = filepath.Join(stateDir, "tracks")
	if err := os.MkdirAll(tracksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracksDir, "ml-systems.json"), []byte(testTrack), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DropRules = []config.DropRule{
		{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.95},
		{Slot: "role_fit", Values: []string{"adjacent"}, MinOpenRouterProb: 0.5},
		{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.9},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return cfgPath, tracksDir
}

func TestTrackListShowUse(t *testing.T) {
	cfgPath, _ := trackSetup(t)
	o, _ := mustCode(t, 0, "track", "list")
	if !strings.Contains(o, "ml-systems") || !strings.Contains(o, "ML systems") || strings.Contains(o, "*") {
		t.Fatalf("list: %q", o)
	}
	o, e := mustCode(t, 1, "track", "show", "ml-systems")
	if !strings.Contains(o, "check: incomplete") || !strings.Contains(e, "personal names were NOT checked") || !strings.Contains(e, "evidence check skipped") {
		t.Fatalf("show: %q / %q", o, e)
	}

	before, _ := os.ReadFile(cfgPath)
	o, _ = mustCode(t, 0, "track", "use", "--allow-unchecked", "--dry-run", "ml-systems")
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || !strings.Contains(o, "dry run") {
		t.Fatalf("dry run changed the config or said nothing: %q", o)
	}

	_, e = mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	if !strings.Contains(e, "no employer lists are imported") {
		t.Errorf("missing no-index warning: %q", e)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.DropRule{
		{Slot: "sponsorship", Values: []string{"no_sponsorship"}, MinOpenRouterProb: 0.95},
		{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.9},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
		{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
	}
	if !reflect.DeepEqual(cfg.DropRules, want) {
		t.Fatalf("drop rules\n got %+v\nwant %+v", cfg.DropRules, want)
	}
	if cfg.Track != "ml-systems" || !reflect.DeepEqual(cfg.EmployerPreferences, []string{"cap_exempt_likely"}) ||
		!strings.HasPrefix(cfg.TargetSummary, "Early-career ML systems") {
		t.Fatalf("config %+v", cfg)
	}
	if strings.Contains(strings.ToLower(cfg.TargetSummary), "cap") {
		t.Fatal("an employer preference reached target_summary")
	}
	o, _ = mustCode(t, 0, "track", "list")
	if !strings.Contains(o, "* ml-systems") {
		t.Fatalf("active track not marked: %q", o)
	}
	mustCode(t, 0, "config", "check")
}

func TestTrackUseRefusesBadTracksAndLeavesConfig(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	before, _ := os.ReadFile(cfgPath)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pref.json", strings.NewReplacer(`"ml-systems"`, `"pref"`, `"cap_exempt_likely"`, `"remote_first"`).Replace(testTrack))
	write("sponsor.json", strings.NewReplacer(`"ml-systems"`, `"sponsor"`, `"drop_rules": [`,
		`"drop_rules": [{"slot": "sponsorship", "values": ["no_sponsorship"], "min_jev_prob": 0.9, "min_lappi_score": 0},`).Replace(testTrack))
	write("ident.json", strings.NewReplacer(`"ml-systems"`, `"ident"`, "GPU kernels.", "GPU kernels at zorblax corp.").Replace(testTrack))
	write(trackWordsFile(), "zorblax corp\n")

	for id, want := range map[string]string{
		"pref":    "unknown preference",
		"sponsor": "belong in the main config",
		"ident":   `"zorblax corp"`,
	} {
		o, e := mustCode(t, 1, "track", "use", id)
		if !strings.Contains(o, want) || !strings.Contains(e, "config is unchanged") {
			t.Errorf("use %s: stdout %q stderr %q, want %q", id, o, e, want)
		}
	}
	for _, id := range []string{"../ml-systems", "a/b", "UPPER", "..", "x.json"} {
		mustCode(t, 2, "track", "use", id)
		mustCode(t, 2, "track", "show", id)
	}
	mustCode(t, 1, "track", "use", "missing")
	mustCode(t, 2, "track", "use")
	mustCode(t, 2, "track", "use", "a", "b")
	mustCode(t, 2, "track")
	mustCode(t, 2, "track", "wat")
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("a refused track changed the config")
	}
	// An unreadable file is listed and makes list exit 1.
	write("broken.json", "{")
	o, _ := mustCode(t, 1, "track", "list")
	if !strings.Contains(o, "broken") || !strings.Contains(o, "unreadable") {
		t.Fatalf("list: %q", o)
	}
}

func trackWordsFile() string { return "identifying-words.txt" }

func TestTrackSwitchRemovesPreviousTrackRulesAndShowsOldSummary(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	cfg, _ := config.Load(cfgPath)
	cfg.TargetSummary = "Hand-written summary of target roles."
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	hybridOff := `{"slot": "work_mode", "values": ["hybrid"], "min_jev_prob": 0.8, "min_lappi_score": 0},`
	remote := strings.NewReplacer(`"ml-systems"`, `"remote"`, `"drop_rules": [`, `"drop_rules": [`+hybridOff,
		"Early-career ML systems", "Remote ML systems").Replace(testTrack)
	if err := os.WriteFile(filepath.Join(dir, "remote.json"), []byte(remote), 0o600); err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "track", "use", "--allow-unchecked", "remote")
	if !strings.Contains(o, "written by hand") || !strings.Contains(o, "Hand-written summary of target roles.") {
		t.Fatalf("hand-written summary not shown when replaced: %q", o)
	}
	o, _ = mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	if !strings.Contains(o, "set by track remote") || !strings.Contains(o, "Remote ML systems") {
		t.Fatalf("previous track's summary not shown: %q", o)
	}
	cfg, _ = config.Load(cfgPath)
	for _, r := range cfg.DropRules {
		if r.Slot == "work_mode" {
			t.Fatalf("the remote track's work_mode rule survived the switch: %+v", cfg.DropRules)
		}
	}
	if cfg.DropRules[0].Slot != "sponsorship" || len(cfg.DropRules) != 3 {
		t.Fatalf("rules %+v", cfg.DropRules)
	}

	// An unreadable previous track is reported, and its rules are kept.
	mustCode(t, 0, "track", "use", "--allow-unchecked", "remote")
	if err := os.Remove(filepath.Join(dir, "remote.json")); err != nil {
		t.Fatal(err)
	}
	_, e := mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	if !strings.Contains(e, "previous track remote unreadable") {
		t.Fatalf("stderr %q", e)
	}
	cfg, _ = config.Load(cfgPath)
	if !slices.ContainsFunc(cfg.DropRules, func(r config.DropRule) bool { return r.Slot == "work_mode" }) {
		t.Fatal("a rule from an unreadable previous track was removed")
	}
}

func TestTrackEvidenceCheckedAgainstBank(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	body := strings.Replace(testTrack, `"devprism_persona": null`,
		`"evidence": [{"area": "kernels", "kb_entries": ["entry-a", "ghost"], "strength": "strong"}], "devprism_persona": null`, 1)
	if err := os.WriteFile(filepath.Join(dir, "ml-systems.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	bank := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(bank, []byte(`{"entries": {"entry-a": {}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	cfg.KB.Bank = bank
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	o, e := mustCode(t, 1, "track", "show", "ml-systems")
	if !strings.Contains(o, `"ghost"`) || strings.Contains(e, "evidence check skipped") {
		t.Fatalf("show: %q / %q", o, e)
	}
}

func TestTracksDirFromConfig(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "ml-systems.json"), []byte(testTrack), 0o600); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(cfgPath)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["tracks_dir"] = other
	data, _ = json.Marshal(raw)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "track", "list")
	if !strings.Contains(o, "ml-systems") {
		t.Fatalf("list from tracks_dir: %q", o)
	}
	// A relative tracks_dir is refused by config validation.
	raw["tracks_dir"] = "relative/dir"
	data, _ = json.Marshal(raw)
	_ = os.WriteFile(cfgPath, data, 0o600)
	mustCode(t, 1, "track", "list")
}

// A check that did not run must not look like a pass (B1-3): with no words
// file and no bank, show says incomplete and exits non-zero, and use refuses
// and leaves the config alone unless --allow-unchecked.
func TestTrackUncheckedIsNotOK(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	before, _ := os.ReadFile(cfgPath)

	o, e := mustCode(t, 1, "track", "show", "ml-systems")
	if strings.Contains(o, "check: ok") || !strings.Contains(o, "check: incomplete (not run: ") ||
		!strings.Contains(o, "personal-name") || !strings.Contains(o, "evidence") {
		t.Fatalf("show stdout %q", o)
	}
	if !strings.Contains(e, "personal names were NOT checked") {
		t.Fatalf("show stderr %q", e)
	}

	o, e = mustCode(t, 1, "track", "use", "ml-systems")
	if !strings.Contains(e, "not applied") || !strings.Contains(e, "--allow-unchecked") || strings.Contains(o, "saved") {
		t.Fatalf("use stdout %q stderr %q", o, e)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("a refused use changed the config")
	}

	// With the flag the config is written, and stderr still names what did not run.
	o, e = mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	if !strings.Contains(o, "saved") || !strings.Contains(e, "personal names were NOT checked") || !strings.Contains(e, "evidence check skipped") {
		t.Fatalf("use --allow-unchecked stdout %q stderr %q", o, e)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) == string(before) {
		t.Fatal("--allow-unchecked did not save")
	}

	// With both checks able to run, show prints ok and use needs no flag.
	if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte("zorblax corp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bank := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(bank, []byte(`{"entries": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	cfg.KB.Bank = bank
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "track", "show", "ml-systems")
	if !strings.Contains(o, "check: ok") {
		t.Fatalf("fully checked show: %q", o)
	}
	mustCode(t, 0, "track", "use", "ml-systems")
}
