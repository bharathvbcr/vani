package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// usageLeaves are the leaf commands usageTemplate names: each top-level line's
// lowercase words up to its first flag or placeholder, a|b|c words expanded.
func usageLeaves(t *testing.T) []string {
	t.Helper()
	word := regexp.MustCompile(`^[a-z][a-z-]*(\|[a-z][a-z-]*)*$`)
	var out []string
	for _, line := range strings.Split(usageTemplate, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue // not a command line, or a continuation
		}
		head := strings.TrimSpace(line)
		if i := strings.Index(head, "  "); i >= 0 {
			head = head[:i] // the description starts after a run of spaces
		}
		prefixes := []string{""}
		for _, w := range strings.Fields(head) {
			if !word.MatchString(w) {
				break
			}
			var next []string
			for _, p := range prefixes {
				for _, alt := range strings.Split(w, "|") {
					next = append(next, strings.TrimSpace(p+" "+alt))
				}
			}
			prefixes = next
		}
		out = append(out, prefixes...)
	}
	if len(out) < 50 {
		t.Fatalf("parsed only %d leaves from the usage text: the parser is wrong", len(out))
	}
	return out
}

// jsonLeaves names exactly the leaves the usage text documents, once each.
func TestJSONLeavesMatchUsage(t *testing.T) {
	var table []string
	for _, l := range jsonLeaves {
		table = append(table, l.Command)
	}
	usage := usageLeaves(t)
	slices.Sort(table)
	slices.Sort(usage)
	if dup := slices.Compact(slices.Clone(table)); len(dup) != len(table) {
		t.Errorf("jsonLeaves lists a command twice")
	}
	for _, c := range usage {
		if _, found := slices.BinarySearch(table, c); !found {
			t.Errorf("usage documents %q, which jsonLeaves does not classify", c)
		}
	}
	for _, c := range table {
		if _, found := slices.BinarySearch(usage, c); !found {
			t.Errorf("jsonLeaves classifies %q, which the usage text does not document", c)
		}
	}
	for _, l := range jsonLeaves {
		switch l.Mode {
		case jsonNone:
			if l.Reason == "" {
				t.Errorf("%q has no JSON form and no reason", l.Command)
			}
		case jsonFlag, jsonAlways:
			if l.Reason != "" {
				t.Errorf("%q: only a none entry carries a reason", l.Command)
			}
		default:
			t.Errorf("%q: mode %q", l.Command, l.Mode)
		}
	}
}

// flagListed matches the -json line a FlagSet prints for -h.
var flagListed = regexp.MustCompile(`(?m)^\s+-json\b`)

// A flag leaf declares --json, and a none leaf does not: a command that
// gains the flag must move to flag, or the global --json would refuse it.
// Every flag leaf also takes the flag where the global --json puts it.
func TestJSONLeavesMatchFlags(t *testing.T) {
	x := newIsoEnv(t)
	for _, l := range jsonLeaves {
		words := strings.Fields(l.Command)
		_, stderr := x.mustCodeOneOf(t, []int{exitOK, exitUsage}, append(words, "-h")...)
		has := flagListed.MatchString(stderr)
		switch l.Mode {
		case jsonFlag:
			if !has {
				t.Errorf("%q is classified flag but -h lists no -json:\n%s", l.Command, stderr)
			}
			x.mustCode(t, exitOK, append(append([]string{"--json"}, words...), "-h")...)
		case jsonNone:
			if has {
				t.Errorf("%q declares -json but is classified %s", l.Command, l.Mode)
			}
		}
	}
}

func (x *isoEnv) mustCodeOneOf(t *testing.T, want []int, args ...string) (stdout, stderr string) {
	t.Helper()
	code, o, e := x.do(args...)
	if !slices.Contains(want, code) {
		t.Fatalf("vani %v: exit %d, want one of %v\nstdout: %s\nstderr: %s", args, code, want, o, e)
	}
	return o, e
}

// onlyError decodes stdout as exactly one {"error","code"} object.
func onlyError(t *testing.T, args []string, stdout string) errorReply {
	t.Helper()
	var r errorReply
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || r.Error == "" || r.Code == "" || dec.More() {
		t.Fatalf("vani %v: stdout %q is not one {\"error\",\"code\"} object (%v)", args, stdout, err)
	}
	return r
}

// `vani --json CMD` is `vani CMD --json`, however many times it is said.
func TestGlobalJSONIsTheCommandsOwnFlag(t *testing.T) {
	x := newIsoEnv(t)
	x.mustCode(t, exitOK, "init")
	emptyLedger(t, x.stateDir)
	local, _ := x.mustCode(t, exitOK, "jobs", "list", "--json")
	if !json.Valid([]byte(local)) {
		t.Fatalf("jobs list --json: %q", local)
	}
	for _, args := range [][]string{
		{"--json", "jobs", "list"},
		{"-json", "jobs", "list"},
		{"--json", "--json", "jobs", "list", "--json"},
		{"--json=true", "jobs", "list"},
		{"-json=1", "jobs", "list"},
		{"--json=false", "--json", "jobs", "list"},
	} {
		if got, _ := x.mustCode(t, exitOK, args...); got != local {
			t.Errorf("vani %v:\n got %s\nwant %s", args, got, local)
		}
	}
	// --json=false is no JSON at all, as on a command's own flag.
	text, _ := x.mustCode(t, exitOK, "jobs", "list")
	for _, args := range [][]string{{"--json=false", "jobs", "list"}, {"--json", "--json=0", "jobs", "list"}} {
		if got, _ := x.mustCode(t, exitOK, args...); got != text {
			t.Errorf("vani %v:\n got %s\nwant the text %s", args, got, text)
		}
	}
	folders, _ := x.mustCode(t, exitOK, "--json", "jobs", "folders")
	if alias, _ := x.mustCode(t, exitOK, "--json", "jobs", "categories"); alias != folders {
		t.Errorf("jobs categories under --json:\n got %s\nwant %s", alias, folders)
	}
	v, _ := x.mustCode(t, exitOK, "--json", "version")
	if alias, _ := x.mustCode(t, exitOK, "--json", "--version"); alias != v {
		t.Errorf("--version under --json:\n got %s\nwant %s", alias, v)
	}
}

// emptyLedger gives stateDir a ledger with no events.
func emptyLedger(t *testing.T, stateDir string) {
	t.Helper()
	dir := filepath.Join(stateDir, "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Under the global --json every failure is one envelope on stdout, with the
// exit code it has without it, and a refused command does not run.
func TestGlobalJSONFailuresAreOneEnvelope(t *testing.T) {
	x := newIsoEnv(t)
	for _, tc := range []struct {
		args []string
		exit int
		code string
		has  string
	}{
		{[]string{"--json"}, exitUsage, codeUsage, "expected a command"},
		{[]string{"--json=maybe", "jobs", "list"}, exitUsage, codeUsage, `invalid value "maybe" for --json`},
		{[]string{"--json", "-json=", "status"}, exitUsage, codeUsage, `invalid value "" for --json`},
		{[]string{"--json", "bogus"}, exitUsage, codeUsage, `"bogus" names no command`},
		{[]string{"--json", "jobs"}, exitUsage, codeUsage, "names no command"},
		{[]string{"--json", "jobs", "bogus", "x"}, exitUsage, codeUsage, "names no command"},
		{[]string{"--json", "daemon"}, exitUsage, codeUnsupported, "foreground"},
		{[]string{"--json", "launchd", "plist"}, exitUsage, codeUnsupported, "XML"},
		{[]string{"--json", "label"}, exitUsage, codeUnsupported, "on the terminal"},
		{[]string{"--json", "jobs", "list", "--limit", "0"}, exitUsage, codeUsage, "--limit"},
		{[]string{"--json", "jobs", "list", "--bogus"}, exitUsage, codeUsage, "bogus"},
		{[]string{"--json", "watchlist", "list"}, exitRuntime, codeRuntime, "config"},
	} {
		code, o, _ := x.do(tc.args...)
		if code != tc.exit {
			t.Errorf("vani %v: exit %d, want %d", tc.args, code, tc.exit)
		}
		r := onlyError(t, tc.args, o)
		if r.Code != tc.code || !strings.Contains(r.Error, tc.has) {
			t.Errorf("vani %v: %+v, want code %q and %q in the message", tc.args, r, tc.code, tc.has)
		}
	}
}

// A command's own --json now replies {"error","code"} for a runtime failure it
// did not report itself, and keeps exit 2 with nothing on stdout for a usage
// error (the Mac app reads that silence as an older vani).
func TestCommandJSONFlagFailures(t *testing.T) {
	x := newIsoEnv(t)
	args := []string{"watchlist", "list", "--json"} // no config yet
	o, _ := x.mustCode(t, exitRuntime, args...)
	if r := onlyError(t, args, o); r.Code != codeRuntime {
		t.Errorf("%+v", r)
	}
	for _, args := range [][]string{
		{"jobs", "list", "--json", "--limit", "0"},
		{"config", "motivation", "--bogus"},
		{"answers", "similar", "extra"},
	} {
		if o, _ := x.mustCode(t, exitUsage, args...); o != "" {
			t.Errorf("vani %v: stdout %q on a usage error", args, o)
		}
	}
	// Text output is untouched when no JSON was asked for.
	if o, _ := x.mustCode(t, exitRuntime, "watchlist", "list"); o != "" {
		t.Errorf("watchlist list without --json: stdout %q", o)
	}
}

// A command that reported its own failure is not given a second envelope.
func TestJSONFailureIsReportedOnce(t *testing.T) {
	x := newIsoEnv(t)
	x.mustCode(t, exitOK, "init")
	for _, args := range [][]string{
		{"--json", "jobs", "show", "gh:nobody:1"},
		{"jobs", "show", "--json", "gh:nobody:1"},
	} {
		o, _ := x.mustCode(t, exitRuntime, args...)
		if r := onlyError(t, args, o); r.Code != codeRuntime {
			t.Errorf("vani %v: %+v", args, r)
		}
	}
}

// An always-JSON command is not handed a --json it would refuse.
func TestGlobalJSONLeavesAlwaysCommandsAlone(t *testing.T) {
	x := newIsoEnv(t)
	x.mustCode(t, exitOK, "init")
	o, _ := x.mustCode(t, exitOK, "--json", "config", "motivation")
	var r motivationNotesReply
	if err := json.Unmarshal([]byte(o), &r); err != nil || r.MaxBytes == 0 {
		t.Errorf("--json config motivation: %q (%v)", o, err)
	}
}

// `vani --json help` is the inventory: every leaf and how it answers.
func TestGlobalJSONHelpIsTheInventory(t *testing.T) {
	x := newIsoEnv(t)
	for _, args := range [][]string{{"--json", "help"}, {"--json", "-h"}, {"--json", "--help"}} {
		o, _ := x.mustCode(t, exitOK, args...)
		var inv jsonInventory
		dec := json.NewDecoder(strings.NewReader(o))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&inv); err != nil {
			t.Fatalf("vani %v: %v\n%s", args, err, o)
		}
		if !slices.Equal(inv.Commands, jsonLeaves) {
			t.Errorf("vani %v: inventory differs from jsonLeaves", args)
		}
	}
	if o, _ := x.mustCode(t, exitOK, "help"); o != usageText {
		t.Error("plain help is no longer the usage text")
	}
}

// leafOf takes the longest leaf, reads aliases, and names no leaf for a group.
func TestLeafOf(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		n    int
	}{
		{[]string{"audit"}, "audit", 1},
		{[]string{"audit", "answer", "--key", "k"}, "audit answer", 2},
		{[]string{"audit-sample", "answer"}, "audit answer", 2},
		{[]string{"jobs", "resume", "build", "k"}, "jobs resume build", 3},
		{[]string{"jobs", "categories"}, "jobs folders", 2},
		{[]string{"runs", "show", "list"}, "runs show", 2},
		{[]string{"--version"}, "version", 1},
		{[]string{"jobs"}, "", 0},
		{[]string{"jobs", "resume"}, "", 0},
		{[]string{"categories"}, "", 0}, // an alias only under jobs
		{nil, "", 0},
	} {
		l, n, ok := leafOf(tc.args)
		if l.Command != tc.want || n != tc.n || ok != (tc.want != "") {
			t.Errorf("leafOf(%v) = %q, %d, %v; want %q, %d", tc.args, l.Command, n, ok, tc.want, tc.n)
		}
	}
}

// skillPath is the CLI-Anything skill, from this package's directory.
var skillPath = filepath.Join("..", "..", "skills", "cli-anything-vani", "SKILL.md")

// The skill names itself as CLI-Anything expects and mentions every command
// group, so a new command cannot ship without an agent hearing of it.
func TestSkillCoversEveryCommand(t *testing.T) {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)
	front, _, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(skill, "---\n") || !ok {
		t.Fatal("SKILL.md has no YAML frontmatter")
	}
	if !strings.Contains(front, `name: "cli-anything-vani"`) || !strings.Contains(front, "description: ") {
		t.Errorf("frontmatter lacks name or description:\n%s", front)
	}
	seen := map[string]bool{}
	for _, l := range jsonLeaves {
		group := strings.Fields(l.Command)[0]
		if !seen[group] && !strings.Contains(skill, "vani "+group) {
			t.Errorf("SKILL.md never mentions `vani %s`", group)
		}
		seen[group] = true
		if l.Mode == jsonNone && !strings.Contains(skill, l.Command) {
			t.Errorf("SKILL.md does not say %q has no JSON form", l.Command)
		}
	}
	// The one command whose JSON is a stream of lines, not one value, is named.
	for _, s := range []string{"VANI_NO_KEYCHAIN=1", "vani --json help", `{"error","code"}`, "exit", "jobs retriage --batch --json"} {
		if !strings.Contains(skill, s) {
			t.Errorf("SKILL.md does not mention %q", s)
		}
	}
}
