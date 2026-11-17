package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// w5bRead is what the Mac pane reads when it opens: the lists and the revision.
func w5bRead(t *testing.T) excludeReply {
	t.Helper()
	o, _ := mustCode(t, 0, "config", "exclude")
	r := excludeOut(t, o)
	if r.Revision == "" {
		t.Fatalf("a read replied without a revision: %s", o)
	}
	return r
}

func w5bPatch(t *testing.T, want int, in string) excludeReply {
	t.Helper()
	setExcludeInput(t, in)
	o, _ := mustCode(t, want, "config", "exclude", "--patch")
	return excludeOut(t, o)
}

func w5bConfigBytes(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// [72] The pane read the lists, the CLI then added an exclusion, and the pane
// saved its copy with the revision it read: refused as stale, the CLI's entry
// kept, the file untouched.
func TestW5bStaleSaveIsRefusedNotApplied(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, 0, "config", "exclude", "--set")

	pane := w5bRead(t)
	w5bPatch(t, 0, `{"add": {"companies": ["Acme"]}}`) // the CLI, the extension or another window
	before := w5bConfigBytes(t, cfgPath)

	setExcludeInput(t, `{"titles": ["intern", "co-op"]}`)
	o, _ := mustCode(t, exitRuntime, "config", "exclude", "--set", "--revision", pane.Revision)
	r := excludeOut(t, o)
	if r.Code != codeStale || !strings.Contains(r.Error, "changed since") || len(r.Exclude.Titles) != 0 || r.Revision != "" {
		t.Fatalf("stale save reply = %+v, want code %q, the message, no lists, no revision", r, codeStale)
	}
	if w5bConfigBytes(t, cfgPath) != before {
		t.Fatal("a stale save changed the config file")
	}
	if now := w5bRead(t); strings.Join(now.Exclude.Companies, ",") != "Acme" || now.Revision == pane.Revision {
		t.Errorf("after the refusal the config reads %+v", now)
	}
}

// [72] A save on the revision just read is applied, and its reply's revision
// is the one the next read gives, so a pane can chain saves.
func TestW5bCurrentRevisionSaves(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	pane := w5bRead(t)

	setExcludeInput(t, `{"titles": ["intern", "co-op"]}`)
	o, _ := mustCode(t, 0, "config", "exclude", "--set", "--revision", pane.Revision)
	saved := excludeOut(t, o)
	if strings.Join(saved.Exclude.Titles, ",") != "intern,co-op" {
		t.Fatalf("saved = %+v", saved)
	}
	if now := w5bRead(t); now.Revision != saved.Revision {
		t.Errorf("save replied revision %s, the next read %s", saved.Revision, now.Revision)
	}

	// The old revision is stale now.
	setExcludeInput(t, `{}`)
	mustCode(t, exitRuntime, "config", "exclude", "--set", "--revision", pane.Revision)
}

// [72] The pane's add/remove intent merges with what another writer added.
func TestW5bPatchMergesWithAConcurrentAdd(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern", "trainee"], "locations": ["India"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	w5bRead(t) // the pane's copy

	w5bPatch(t, 0, `{"add": {"companies": ["Acme"]}}`) // somewhere else
	r := w5bPatch(t, 0, `{"add": {"titles": ["co-op"], "descriptions": ["security clearance"]}, "remove": {"titles": ["  INTERN "]}}`)
	if strings.Join(r.Exclude.Titles, ",") != "trainee,co-op" || strings.Join(r.Exclude.Companies, ",") != "Acme" ||
		strings.Join(r.Exclude.Locations, ",") != "India" || strings.Join(r.Exclude.Descriptions, ",") != "security clearance" {
		t.Fatalf("patch reply = %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Exclude; strings.Join(got.Titles, ",") != "trainee,co-op" || strings.Join(got.Companies, ",") != "Acme" {
		t.Errorf("saved %+v", got)
	}
	if now := w5bRead(t); now.Revision != r.Revision {
		t.Errorf("patch replied revision %s, the next read %s", r.Revision, now.Revision)
	}
}

// The full-list form without a revision still works for callers that predate
// revisions: it replaces everything, so an exclusion added since is erased.
// That is the documented last-writer-wins, and why the pane no longer uses it.
func TestW5bLegacySetWithoutRevisionIsLastWriterWins(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	w5bPatch(t, 0, `{"add": {"companies": ["Acme"]}}`)

	setExcludeInput(t, `{"titles": ["intern", "co-op"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Exclude.Titles, ",") != "intern,co-op" || cfg.Exclude.Companies != nil {
		t.Errorf("legacy set left %+v", cfg.Exclude)
	}
}

func TestW5bPatchEdgeCases(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"], "locations": ["New York"]}`)
	mustCode(t, 0, "config", "exclude", "--set")

	// Nothing to do, duplicates, absent removals and case or whitespace
	// variants are no-ops, not errors.
	base := w5bRead(t)
	for name, in := range map[string]string{
		"empty object":     `{}`,
		"empty lists":      `{"add": {}, "remove": {"titles": []}}`,
		"add present":      `{"add": {"titles": ["INTERN", " intern "]}}`,
		"remove absent":    `{"remove": {"titles": ["nobody"], "companies": ["Acme"]}}`,
		"add blank":        `{"add": {"titles": ["   "]}}`,
		"location variant": `{"add": {"locations": ["new  york"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if r := w5bPatch(t, 0, in); r.Revision != base.Revision {
				t.Errorf("patch %s changed the lists: %+v", in, r.Exclude)
			}
		})
	}

	// Two adds of one new entry in one patch land once.
	if r := w5bPatch(t, 0, `{"add": {"companies": ["Acme", "acme"]}}`); len(r.Exclude.Companies) != 1 {
		t.Errorf("a repeated add landed %q", r.Exclude.Companies)
	}

	// Refusals leave the file as it was.
	before := w5bConfigBytes(t, cfgPath)
	over := make([]string, config.MaxExcludeEntries+1)
	for i := range over {
		over[i] = `"t` + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	for name, c := range map[string]struct{ in, want string }{
		"unknown key":   {`{"adds": {}}`, "unknown field"},
		"unknown list":  {`{"add": {"title": ["x"]}}`, "unknown field"},
		"not an object": {`["x"]`, `not an {"add","remove"} object`},
		"trailing data": {`{} {}`, "trailing data"},
		"over the cap":  {`{"add": {"titles": [` + strings.Join(over, ",") + `]}}`, "at most"},
		"too long":      {`{"add": {"companies": ["` + strings.Repeat("a", config.MaxExcludeEntryLen+1) + `"]}}`, "over"},
		"control":       {`{"add": {"titles": ["in\u0000tern"]}}`, "control character"},
		"over read cap": {`{"add": {"titles": ["` + strings.Repeat("a", maxExcludeInput) + `"]}}`, "larger than"},
		"unless alone":  {`{"remove": {"titles": ["intern"]}, "add": {"title_unless": ["internal"]}}`, "needs exclude.titles"},
	} {
		t.Run(name, func(t *testing.T) {
			r := w5bPatch(t, exitRuntime, c.in)
			if !strings.Contains(r.Error, c.want) {
				t.Errorf("reply %+v, want an error containing %q", r, c.want)
			}
			if w5bConfigBytes(t, cfgPath) != before {
				t.Fatal("a refused patch changed the file")
			}
		})
	}
}

func TestW5bFlagMisuseIsAUsageError(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, args := range [][]string{
		{"config", "exclude", "--set", "--patch"},
		{"config", "exclude", "--revision", "abc"},
		{"config", "exclude", "--patch", "--revision", "abc"},
	} {
		setExcludeInput(t, `{}`)
		if code, _, _ := do(t, args...); code != exitUsage {
			t.Errorf("vani %v exited %d, want %d", args, code, exitUsage)
		}
	}
	// A revision no read ever gave refuses, rather than saving.
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, exitRuntime, "config", "exclude", "--set", "--revision", "not-a-revision")
	if len(w5bRead(t).Exclude.Titles) != 0 {
		t.Error("a bogus revision saved")
	}
}
