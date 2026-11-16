package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// boardKinds is every board kind vani can read: one per API adapter, plus
// careers pages, whose adapter is built per run.
func boardKinds(t *testing.T) []string {
	t.Helper()
	srcs := apiSources(srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }))
	kinds := []string{string(posting.Page)}
	for a := range srcs {
		kinds = append(kinds, string(a))
	}
	slices.Sort(kinds)
	return kinds
}

// `vani help` names every board kind `watchlist add --ats` takes, and only
// those; so does the --ats flag's own help. The help once listed 12 of 30.
func TestHelpListsEveryBoardKind(t *testing.T) {
	setup(t)
	want := boardKinds(t)
	for _, k := range want {
		if err := config.ValidBoard(sources.Board{ATS: posting.ATS(k)}); err != nil && strings.Contains(err.Error(), "is not one of") {
			t.Fatalf("%s has an adapter but the config refuses it: %v", k, err)
		}
	}

	out, _ := mustCode(t, 0, "help")
	line := helpLine(t, out, "CODE:")
	got := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "CODE:")), "|")
	sorted := slices.Clone(got)
	slices.Sort(sorted)
	if !slices.Equal(sorted, want) {
		t.Errorf("help lists board kinds %v\nwant (adapters + pg) %v", sorted, want)
	}
	if add := helpLine(t, out, "watchlist add"); !strings.Contains(add, "--ats CODE") {
		t.Errorf("watchlist add help line does not point at the CODE list: %q", add)
	}

	_, flagHelp := mustCode(t, 0, "watchlist", "add", "-h")
	i := strings.Index(flagHelp, "-ats")
	if i < 0 {
		t.Fatalf("no -ats in watchlist add -h:\n%s", flagHelp)
	}
	atsHelp := flagHelp[i:]
	if j := strings.Index(atsHelp, "\n  -"); j > 0 {
		atsHelp = atsHelp[:j]
	}
	for _, k := range want {
		if !regexp.MustCompile(`\b` + k + `\b`).MatchString(atsHelp) {
			t.Errorf("--ats flag help does not name %s: %q", k, atsHelp)
		}
	}
}

// A hint in vani's own text that names `vani GROUP SUB` names a command the
// help lists. Status and the semantic warning once told the owner to run
// `vani track apply`, which does not exist (it is `track use`).
func TestHintsNameCommandsTheHelpLists(t *testing.T) {
	setup(t)
	out, _ := mustCode(t, 0, "help")
	commands := map[string]bool{} // "group sub"
	groups := map[string]bool{}
	sub := regexp.MustCompile(`^  ([a-z][a-z-]*) ([a-z][a-z-]*)\b`)
	for _, l := range strings.Split(out, "\n") {
		if m := sub.FindStringSubmatch(l); m != nil {
			commands[m[1]+" "+m[2]] = true
			groups[m[1]] = true
		}
	}
	if len(groups) == 0 {
		t.Fatalf("no subcommands read from the help:\n%s", out)
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	mention := regexp.MustCompile(`\bvani ([a-z][a-z-]*) ([a-z][a-z-]*)\b`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mention.FindAllStringSubmatch(string(src), -1) {
			if !groups[m[1]] {
				continue
			}
			checked++
			if !commands[m[1]+" "+m[2]] {
				t.Errorf("%s names %q, which the help does not list", f, m[0])
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no `vani GROUP SUB` mention was checked; the scan read nothing")
	}
}
