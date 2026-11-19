package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A refused use (dry run, JSON, flag or not) leaves the config byte-identical
// and never echoes the summary or the owner's words in its error.
func TestAttackTrackUseRefusalLeaksNothingAndKeepsConfig(t *testing.T) {
	cfgPath, dir := trackSetup(t)
	before, _ := os.ReadFile(cfgPath)
	const secret = "zorblax"
	for _, words := range []string{"", "# c\n", "\ufeff# c\n", "\ufeff\n"} {
		if words != "" {
			if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte(words), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{
			{"track", "use", "ml-systems"},
			{"track", "use", "--dry-run", "ml-systems"},
			{"track", "use", "--allow-unchecked", "ml-systems"},
			{"track", "use", "--allow-unchecked", "--dry-run", "ml-systems"},
			{"--json", "track", "use", "--allow-unchecked", "--dry-run", "ml-systems"},
			{"--json", "track", "use", "--allow-unchecked", "ml-systems"},
		} {
			o, e := mustCode(t, 1, args...)
			for _, out := range []string{o, e} {
				if strings.Contains(out, "inference runtimes") || strings.Contains(out, secret) {
					t.Errorf("%v leaked text: %q", args, out)
				}
			}
			if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
				t.Fatalf("%v changed the config", args)
			}
		}
	}
	// A summary naming an owner word is refused even with the flag.
	if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte("inference runtimes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 1, "track", "use", "--allow-unchecked", "ml-systems")
	mustCode(t, 1, "--json", "track", "use", "--allow-unchecked", "--dry-run", "ml-systems")
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("config changed")
	}
}
