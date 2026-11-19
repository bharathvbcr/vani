package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// `track use --allow-unchecked` and the run gate (checkPromptSummary) agree:
// when the gate would refuse the track's summary, use refuses up front with the
// gate's reason, saves nothing and says so in its JSON reply; when the gate
// passes, the saved config passes it.
func TestTrackAllowUncheckedAgreesWithRunGate(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	stateDir := filepath.Dir(dir)
	before, _ := os.ReadFile(cfgPath)

	for _, words := range []string{"", "# only a comment\n", "\ufeff# only a comment behind a BOM\n"} {
		if words != "" {
			if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte(words), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{
			{"track", "use", "--allow-unchecked", "ml-systems"},
			{"track", "use", "--allow-unchecked", "--dry-run", "ml-systems"},
		} {
			o, e := mustCode(t, 1, args...)
			if !strings.Contains(e, "not applied") || !strings.Contains(e, "config target_summary") || !strings.Contains(e, trackWordsFile()) ||
				strings.Contains(o, "saved") || strings.Contains(o, "target_summary replaced") {
				t.Errorf("words %q %v: stdout %q stderr %q", words, args, o, e)
			}
		}
		o, _ := mustCode(t, 1, "--json", "track", "use", "--allow-unchecked", "ml-systems")
		var r trackUseReply
		decodeOne(t, o, &r)
		if r.Saved || !strings.Contains(r.Error, "not applied") || !strings.Contains(r.Error, "config target_summary") {
			t.Errorf("words %q: json reply %+v", words, r)
		}
		if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
			t.Fatalf("words %q: a refused use changed the config", words)
		}
	}

	// A word that cannot be read is also the gate's refusal, never a save.
	if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte(strings.Repeat("x", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 1, "track", "use", "--allow-unchecked", "ml-systems")

	// With a term the check can run; the saved config passes the gate.
	trackWords(t, dir)
	mustCode(t, 0, "track", "use", "--allow-unchecked", "ml-systems")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Track != "ml-systems" {
		t.Fatalf("track %q was not saved", cfg.Track)
	}
	if err := checkPromptSummary(cfg, stateDir, false); err != nil {
		t.Fatalf("saved a summary the run gate refuses: %v", err)
	}
}

func TestTrackAllowUncheckedFlagHelp(t *testing.T) {
	_, e := mustCode(t, 0, "track", "use", "--help")
	if !strings.Contains(e, "evidence check") || !strings.Contains(e, trackWordsFile()) || strings.Contains(e, "enters every triage prompt unchecked") {
		t.Fatalf("help %q", e)
	}
}
