package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/marks"
)

// helpLine is the usage line that starts with prefix.
func helpLine(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("no help line starting %q in:\n%s", prefix, text)
	return ""
}

// `vani help` lists every word `jobs mark` takes, the stage words included, and
// they are the ones marks accepts, in its order.
func TestHelpListsJobsMarkStageWords(t *testing.T) {
	setup(t)
	out, _ := mustCode(t, 0, "help")
	line := helpLine(t, out, "jobs mark")
	want := "jobs mark [--json] KEY saved|applied|hidden|clear|screening|interview|offer|rejected|withdrawn|unstage"
	if line != want {
		t.Errorf("jobs mark help line\n got %q\nwant %q", line, want)
	}
	for _, s := range marks.Stages {
		if !strings.Contains(line, "|"+string(s)+"|") {
			t.Errorf("stage %q missing from %q", s, line)
		}
	}
	if strings.Contains(out, stagesPlaceholder) {
		t.Errorf("the stage placeholder reached the help text")
	}
}

// The same words are in the usage error `jobs mark` gives, from the same list.
func TestJobsMarkHelpAndUsageErrorShareTheStageList(t *testing.T) {
	setup(t)
	_, stderr := mustCode(t, 2, "jobs", "mark", "gh:a:1")
	for _, s := range marks.Stages {
		if !strings.Contains(stderr, string(s)) {
			t.Errorf("usage error lacks %q: %q", s, stderr)
		}
	}
	if !strings.Contains(usageText, stageList("|")) || !strings.Contains(stderr, stageNames()) {
		t.Errorf("the help and the error do not both read marks.Stages")
	}
	// A usage error prints the help too, with the words filled in.
	if strings.Contains(stderr, stagesPlaceholder) || !strings.Contains(stderr, "clear|screening|") {
		t.Errorf("usage error printed an unfilled or stage-less help: %q", stderr)
	}
}
