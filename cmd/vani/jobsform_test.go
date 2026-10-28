package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func flaggedForm() *posting.ApplyForm {
	return &posting.ApplyForm{V: posting.ApplyFormVersion, Questions: 6, Required: 4, Essays: 1, Uploads: []string{"Cover letter"},
		Gates: []posting.FormGate{
			{Kind: posting.GateSponsorship, Text: "Are you authorized to work in the US without sponsorship?", YesNo: true, Flag: posting.FlagAuthorizedYesNo},
			{Kind: posting.GatePlace, Text: "Which country are you in?", Options: []string{"Portugal", "Spain"}, Flag: posting.FlagPlaceList},
		}}
}

func cleanForm() *posting.ApplyForm {
	return &posting.ApplyForm{V: posting.ApplyFormVersion, Questions: 2, Required: 1}
}

// seedForms writes Ashby postings whose forms were read (flagged and clean),
// not read, and saved by a newer Vani, beside a Greenhouse posting, and leaves
// the ledger open as seedJobs does.
func seedForms(t *testing.T, stateDir string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, s := range []struct {
		key  string
		form *posting.ApplyForm
		url  string
	}{
		{"ab:acme:flagged", flaggedForm(), "https://jobs.ashbyhq.com/acme/flagged"},
		{"ab:acme:clean", cleanForm(), "https://jobs.ashbyhq.com/acme/clean"},
		{"ab:acme:unread", nil, "https://jobs.ashbyhq.com/acme/unread"},
		{"ab:acme:newer", &posting.ApplyForm{V: posting.ApplyFormVersion + 1, Questions: 9}, "https://jobs.ashbyhq.com/acme/newer"},
		{"gh:acme:1", nil, "https://boards.greenhouse.io/acme/jobs/1"},
	} {
		p := posting.Posting{Key: posting.Key(s.key), ATS: posting.Key(s.key).ATS(), Company: "Acme", Title: "Role " + s.key, URL: s.url,
			ApplyURL: s.url + "/application", Description: "Build things.", Form: s.form}
		ann := map[string]string{
			ledger.AnnRank: "50", "triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered",
			"triage.sponsorship": "no_sponsorship", "triage.sponsorship.reading": "answered",
		}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func showForm(t *testing.T, key string) jobview.Job {
	t.Helper()
	o, _ := mustCode(t, 0, "jobs", "show", "--json", key)
	var j jobview.Job
	if err := json.Unmarshal([]byte(o), &j); err != nil {
		t.Fatalf("%q: %v", o, err)
	}
	return j
}

func TestJobsShowJSONCarriesTheForm(t *testing.T) {
	_, state := setup(t)
	seedForms(t, state)

	j := showForm(t, "ab:acme:flagged")
	if j.ApplyForm == nil || len(j.ApplyForm.Gates) != 2 || j.ApplyForm.Gates[0].Flag != posting.FlagAuthorizedYesNo {
		t.Fatalf("apply_form %+v", j.ApplyForm)
	}
	if !strings.HasPrefix(j.ApplyFormNote, "Application form: 4 of 6 questions required; sponsorship: only a yes/no on being authorized without it; you must be in one of: Portugal, Spain;") ||
		!strings.Contains(j.ApplyFormNote, "1 required essay") || !strings.Contains(j.ApplyFormNote, "Cover letter") {
		t.Fatalf("note %q", j.ApplyFormNote)
	}
	// Rule 7: the form only annotates. Triage's reading is untouched and the
	// posting keeps its status.
	if j.Triage["sponsorship"] != "no_sponsorship" || j.Status != posting.Digested || strings.Join(j.ApplyFlags, ",") != "sponsorship,places" {
		t.Fatalf("triage %v status %s flags %v", j.Triage, j.Status, j.ApplyFlags)
	}

	if c := showForm(t, "ab:acme:clean"); c.ApplyForm == nil || c.ApplyFormNote != "Application form: 1 of 2 questions required." || len(c.ApplyFlags) != 0 {
		t.Fatalf("clean: %+v %q", c.ApplyForm, c.ApplyFormNote)
	}
	// Not read is said in words and is not a clean form.
	for _, key := range []string{"ab:acme:unread", "ab:acme:newer"} {
		u := showForm(t, key)
		if u.ApplyForm != nil || u.ApplyFormNote != "Application form not read." || len(u.ApplyFlags) != 0 {
			t.Fatalf("%s: %+v %q", key, u.ApplyForm, u.ApplyFormNote)
		}
	}
	// An ATS whose form Vani cannot read says nothing about it.
	if g := showForm(t, "gh:acme:1"); g.ApplyForm != nil || g.ApplyFormNote != "" {
		t.Fatalf("greenhouse: %+v %q", g.ApplyForm, g.ApplyFormNote)
	}
}

func TestJobsShowTextNamesTheFormBesideTriage(t *testing.T) {
	_, state := setup(t)
	seedForms(t, state)
	o, _ := mustCode(t, 0, "jobs", "show", "ab:acme:flagged")
	want := "Role ab:acme:flagged at Acme\nhttps://jobs.ashbyhq.com/acme/flagged\n" +
		"Application form: 4 of 6 questions required; sponsorship: only a yes/no on being authorized without it; you must be in one of: Portugal, Spain; 1 required essay; required uploads beyond the resume: Cover letter.\n" +
		"Sponsorship: triage reads No sponsorship; only a yes/no on being authorized without it.\n\nBuild things.\n"
	if o != want {
		t.Fatalf("got:\n%s\nwant:\n%s", o, want)
	}

	o, _ = mustCode(t, 0, "jobs", "show", "ab:acme:unread")
	if !strings.Contains(o, "Application form not read.\n") || !strings.Contains(o, "Sponsorship: triage reads No sponsorship; not read.\n") {
		t.Fatalf("unread: %s", o)
	}
	o, _ = mustCode(t, 0, "jobs", "show", "ab:acme:clean")
	if !strings.Contains(o, "Sponsorship: triage reads No sponsorship; the form does not ask.\n") {
		t.Fatalf("clean: %s", o)
	}
	// Greenhouse's text is what it was: no form lines.
	o, _ = mustCode(t, 0, "jobs", "show", "gh:acme:1")
	if o != "Role gh:acme:1 at Acme\nhttps://boards.greenhouse.io/acme/jobs/1\n\nBuild things.\n" {
		t.Fatalf("greenhouse: %q", o)
	}
}

func TestJobsListCarriesTheNoteAndAFlagNotTheForm(t *testing.T) {
	_, state := setup(t)
	seedForms(t, state)
	l := listJSON(t, "--view", "all")
	got := map[posting.Key]jobview.Job{}
	for _, j := range l.Jobs {
		got[j.Key] = j
	}
	if len(got) != 5 {
		t.Fatalf("%d jobs listed: nothing may be dropped for its form", len(got))
	}
	if j := got["ab:acme:flagged"]; j.ApplyForm != nil || j.ApplyFormNote == "" || strings.Join(j.ApplyFlags, ",") != "sponsorship,places" {
		t.Fatalf("flagged row %+v", j)
	}
	if j := got["ab:acme:unread"]; j.ApplyFormNote != "Application form not read." || j.ApplyForm != nil {
		t.Fatalf("unread row %q", j.ApplyFormNote)
	}
	if j := got["gh:acme:1"]; j.ApplyFormNote != "" {
		t.Fatalf("greenhouse row %q", j.ApplyFormNote)
	}

	o, _ := mustCode(t, 0, "jobs", "list", "--view", "all")
	lines := map[string]string{}
	for _, ln := range strings.Split(o, "\n") {
		if i := strings.LastIndex(ln, "["); i >= 0 {
			lines[strings.Trim(ln[i:], "[]")] = ln
		}
	}
	if !strings.Contains(lines["ab:acme:flagged"], "  (form: sponsorship, places)  [ab:acme:flagged]") {
		t.Fatalf("flagged line %q", lines["ab:acme:flagged"])
	}
	for _, k := range []string{"ab:acme:clean", "ab:acme:unread", "ab:acme:newer", "gh:acme:1"} {
		if strings.Contains(lines[k], "(form:") {
			t.Fatalf("%s: %q", k, lines[k])
		}
	}
}

func TestJobsFindSendsTheNoteOnlyForAFlaggedForm(t *testing.T) {
	_, state := setup(t)
	seedForms(t, state)
	for url, want := range map[string]bool{
		"https://jobs.ashbyhq.com/acme/flagged/application": true,
		"https://jobs.ashbyhq.com/acme/flagged":             true,
		"https://jobs.ashbyhq.com/acme/clean/application":   false,
		"https://jobs.ashbyhq.com/acme/unread/application":  false,
		"https://jobs.ashbyhq.com/acme/newer/application":   false,
	} {
		out := findJSON(t, url)
		if out.Job == nil {
			t.Fatalf("%s: no job", url)
		}
		if got := out.Job.ApplyFormNote != ""; got != want {
			t.Fatalf("%s: note %q, want present=%v", url, out.Job.ApplyFormNote, want)
		}
	}
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", "https://jobs.ashbyhq.com/acme/flagged/application")
	if !strings.Contains(o, `"apply_form_note":"Application form: 4 of 6 questions required;`) {
		t.Fatalf("%s", o)
	}
	o, _ = mustCode(t, 0, "jobs", "find", "--json", "--url", "https://jobs.ashbyhq.com/acme/unread/application")
	if strings.Contains(o, "apply_form") {
		t.Fatalf("an unread form must add nothing to the extension reply: %s", o)
	}
}
