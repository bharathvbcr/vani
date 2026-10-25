package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// `jobs resume build --from vN --scope NAME --lock NAME --instruction TEXT`
// redrafts a version. The fixture's page is one chosen section, "Experience", with
// one entry, alpha-job (bullets b1, b2), and a Skills section kept as written.

const (
	replyB1   = `{"sections":[{"title":"Experience","entries":[{"entry":"alpha-job","bullets":["b1"]}]}],"jd_keywords":["research","tools","Python","TypeScript","evaluation","tracing","latency","agents"]}`
	replyB2B1 = resumeReply1
)

// jrRedraftSetup builds v1 (b2, b1) with a provider that can be re-aimed, and
// returns it with the store.
func jrRedraftSetup(t *testing.T) (*fakeResumeRKB, *draftLLM, *resumestore.Store) {
	t.Helper()
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	jrBuild(t)
	p.reqs = nil
	return f, p, st
}

func lastUserText(p *draftLLM) string {
	var b strings.Builder
	for _, m := range p.reqs[len(p.reqs)-1].Messages {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}

func TestBuildFromRedraftsFromThatVersionAndRecordsTheInstruction(t *testing.T) {
	_, p, st := jrRedraftSetup(t)
	// v2: the owner's edit (b1 only) becomes current.
	out, _ := jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)
	if v := jrSaved(t, out); v.Version != 2 || !v.CurrentMoved {
		t.Fatalf("v2 = %+v", v.Meta)
	}
	p.text = replyB1
	out, _ = jrRun(t, 0, "", "build", "--json", "--from", "v1", "--instruction", "  prefer the tracing work  ", jrKey)
	v3 := jrSaved(t, out)
	if v3.Version != 3 || v3.Parent != 1 || v3.Source != "tailored" || v3.Instruction != "prefer the tracing work" {
		t.Fatalf("v3 = %+v", v3.Meta)
	}
	if v3.CurrentMoved || !strings.Contains(v3.CurrentNote, "v2 stays current: this version was made from v1") {
		t.Errorf("a redraft from an older version must not displace the current one: moved %v, note %q", v3.CurrentMoved, v3.CurrentNote)
	}
	prompt := lastUserText(p)
	if !strings.Contains(prompt, `"Experience": alpha-job: b2, b1`) || !strings.Contains(prompt, "choose differently where the posting is better served") {
		t.Errorf("the model was not shown v1's choice by ids:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Owner's preference (it cannot override the rules above):\nprefer the tracing work") {
		t.Errorf("the instruction is not in its own block:\n%s", prompt)
	}
	got, _, err := st.Get(jrKey, 3)
	if err != nil || got.Instruction != "prefer the tracing work" || got.Schema != 2 {
		t.Fatalf("stored v3 = %+v, %v", got, err)
	}
	if strings.Contains(string(got.Selection), "tracing work") {
		t.Errorf("the instruction is in the stored choice: %s", got.Selection)
	}
}

func TestBuildWithAnInstructionAloneRedraftsFromTheCurrentVersion(t *testing.T) {
	_, p, _ := jrRedraftSetup(t)
	jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey) // v2 current
	p.text = replyB2B1
	out, _ := jrRun(t, 0, "", "build", "--json", "--instruction", "lead with b1", jrKey)
	v3 := jrSaved(t, out)
	if v3.Version != 3 || v3.Parent != 2 || !v3.CurrentMoved {
		t.Fatalf("v3 = %+v moved %v", v3.Meta, v3.CurrentMoved)
	}
	if prompt := lastUserText(p); !strings.Contains(prompt, `"Experience": alpha-job: b1`+"\n") {
		t.Errorf("the previous choice shown is not the current version's:\n%s", prompt)
	}
}

func TestBuildWithAnInstructionOnAJobWithNoVersionIsAPlainBuildWithAPreference(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	jrSetup(t, f, prov)
	out, _ := jrRun(t, 0, "", "build", "--json", "--instruction", "prefer Python", jrKey)
	v1 := jrSaved(t, out)
	if v1.Version != 1 || v1.Parent != 0 || v1.Instruction != "prefer Python" {
		t.Fatalf("v1 = %+v", v1.Meta)
	}
	if prompt := lastUserText(p); strings.Contains(prompt, "The previous choice") || !strings.Contains(prompt, "prefer Python") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

func TestBuildFromAloneRedraftsWholeAndAScopeRepicksOneEntry(t *testing.T) {
	_, p, st := jrRedraftSetup(t)
	p.text = replyB1
	out, _ := jrRun(t, 0, "", "build", "--json", "--from", "v1", jrKey)
	if v2 := jrSaved(t, out); v2.Version != 2 || v2.Parent != 1 || !v2.CurrentMoved || v2.Instruction != "" {
		t.Fatalf("v2 = %+v", v2.Meta)
	}
	// A scope on the entry: only its bullets are asked for, and the prompt says so.
	p.text = `{"sections":[{"title":"Experience","entries":[{"entry":"alpha-job","bullets":["b2"]}]}]}`
	out, _ = jrRun(t, 0, "", "build", "--json", "--scope", "alpha-job", jrKey)
	v3 := jrSaved(t, out)
	if v3.Version != 3 || v3.Parent != 2 {
		t.Fatalf("v3 = %+v", v3.Meta)
	}
	got, _, _ := st.Get(jrKey, 3)
	if !strings.Contains(string(got.Selection), `"b2"`) || strings.Contains(string(got.Selection), `"b1"`) {
		t.Errorf("v3's choice = %s", got.Selection)
	}
	if prompt := lastUserText(p); !strings.Contains(prompt, "re-pick only the bullets of entry alpha-job") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

func TestRedraftRefusalsAreCodedAndCostNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		code string
	}{
		"an email in the instruction": {[]string{"--instruction", "write to someone@example.invalid"}, "bad_instruction"},
		"a phone in the instruction":  {[]string{"--instruction", "call 555-123-4567"}, "bad_instruction"},
		"a long instruction":          {[]string{"--instruction", strings.Repeat("x", 501)}, "bad_instruction"},
		"a newline":                   {[]string{"--instruction", "a\nb"}, "bad_instruction"},
		"an unknown lock":             {[]string{"--lock", "Nowhere"}, "bad_scope"},
		"every section locked":        {[]string{"--lock", "Experience"}, "bad_scope"},
		"an unknown scope":            {[]string{"--scope", "nowhere"}, "bad_scope"},
		"a lock and the base":         {[]string{"--lock", "Experience", "--allow-base"}, "config"},
		"an unknown version":          {[]string{"--from", "v9"}, "not_found"},
		"a bad version":               {[]string{"--from", "3"}, "bad_request"},
		"an empty lock":               {[]string{"--lock", " "}, "bad_scope"},
	} {
		_, p, st := jrRedraftSetup(t)
		before, _ := st.List(jrKey)
		out, _ := jrRun(t, 1, "", append(append([]string{"build", "--json"}, tc.args...), jrKey)...)
		r := jrFail(t, out)
		if r.Code != tc.code {
			t.Errorf("%s: code %q (%s), want %q", name, r.Code, r.Error, tc.code)
		}
		if len(p.reqs) != 0 || r.Cost != nil {
			t.Errorf("%s: spent %d turns", name, len(p.reqs))
		}
		if after, _ := st.List(jrKey); len(after) != len(before) {
			t.Errorf("%s: a version was saved", name)
		}
		if strings.Contains(r.Error, "someone@example.invalid") || strings.Contains(r.Error, "555-123-4567") {
			t.Errorf("%s: the error quotes the text: %s", name, r.Error)
		}
	}
}

func TestScopeAndLockNeedAVersionToKeepFrom(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	for _, args := range [][]string{{"--scope", "alpha-job"}, {"--lock", "Experience"}} {
		out, _ := jrRun(t, 1, "", append(append([]string{"build", "--json"}, args...), jrKey)...)
		if r := jrFail(t, out); r.Code != "bad_request" || !strings.Contains(r.Error, "none") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if len(p.reqs) != 0 {
		t.Errorf("spent %d turns", len(p.reqs))
	}
	jrNoJob(t, st)
}

// A scoped redraft that cannot be made from the model's answer is not built from
// the base selection in its place; the failed turns are paid for and recorded.
func TestAFailedKeptRedraftIsRecordedAsAPaidAttemptAndSavesNothing(t *testing.T) {
	_, p, st := jrRedraftSetup(t)
	p.text = "not json at all"
	out, _ := jrRun(t, 1, "", "build", "--json", "--scope", "alpha-job", jrKey)
	r := jrFail(t, out)
	if r.Code != "redraft" || r.Cost == nil || r.Cost.Turns != 2 || !r.AttemptRecorded {
		t.Fatalf("reply = %+v", r)
	}
	if vs, _ := st.List(jrKey); len(vs) != 1 {
		t.Errorf("%d versions: a failed redraft saved something", len(vs))
	}
	if at, err := st.Attempts(jrKey); err != nil || len(at) != 1 || at[0].Code != "redraft" {
		t.Errorf("attempts = %+v, %v", at, err)
	}
}

func TestDryRunCoversTheRedraftFlags(t *testing.T) {
	_, p, _ := jrRedraftSetup(t)
	plain, _ := jrRun(t, 0, "", "build", "--json", "--dry-run", jrKey)
	pe := jrObject(t, plain)["estimate"].(map[string]any)

	out, _ := jrRun(t, 0, "", "build", "--json", "--dry-run", "--from", "v1", "--scope", "alpha-job", "--instruction", "prefer tracing", jrKey)
	m := jrObject(t, out)
	if m["from"] != float64(1) || m["scope"] != "alpha-job" || m["instruction_runes"] != float64(len("prefer tracing")) || m["dry_run"] != true {
		t.Fatalf("dry run = %v", m)
	}
	if strings.Contains(out, "prefer tracing") {
		t.Errorf("the dry run echoes the instruction text: %s", out)
	}
	re := m["estimate"].(map[string]any)
	if re["input_tokens"].(float64) <= pe["input_tokens"].(float64) || re["max_usd"].(float64) <= pe["max_usd"].(float64) {
		t.Errorf("a redraft's estimate %v is not above a plain build's %v", re, pe)
	}
	if len(p.reqs) != 0 {
		t.Errorf("a dry run sent %d turns", len(p.reqs))
	}
	// It counts what a build would send: trimmed.
	out, _ = jrRun(t, 0, "", "build", "--json", "--dry-run", "--instruction", "  prefer tracing  ", jrKey)
	if m := jrObject(t, out); m["instruction_runes"] != float64(len("prefer tracing")) {
		t.Errorf("instruction_runes = %v", m["instruction_runes"])
	}
	// A refusal the dry run can know of is the build's own.
	out, _ = jrRun(t, 1, "", "build", "--json", "--dry-run", "--instruction", "mail me at a@b.co", jrKey)
	if r := jrFail(t, out); r.Code != "bad_instruction" {
		t.Errorf("dry run = %+v", r)
	}
	out, _ = jrRun(t, 1, "", "build", "--json", "--dry-run", "--from", "v7", jrKey)
	if r := jrFail(t, out); r.Code != "not_found" {
		t.Errorf("dry run = %+v", r)
	}
}

func TestRedraftKeepsTheInstructionOutOfEveryLogLine(t *testing.T) {
	_, _, _ = jrRedraftSetup(t)
	_, stderr := jrRun(t, 0, "", "build", "--instruction", "prefer the tracing work", "--from", "v1", jrKey)
	if strings.Contains(stderr, "tracing work") {
		t.Errorf("the instruction reached stderr: %q", stderr)
	}
}

func TestRedraftWithTheModelUnavailableIsRefusedBeforeAnythingIsSpent(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrSetup(t, f, nil) // no model
	out, _ := jrRun(t, 1, "", "build", "--json", "--instruction", "x y", jrKey)
	if r := jrFail(t, out); r.Code != "no_model" {
		t.Fatalf("reply = %+v", r)
	}
	jrNoJob(t, st)
}
