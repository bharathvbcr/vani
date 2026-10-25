package rkbresume

import (
	"strings"
	"testing"
)

// The validator has two rule sets: checkSafe binds every choice (the model's
// and the owner's), checkSteering only the model's. parseChoice is both.

func mustReply(t *testing.T, text string) reply {
	t.Helper()
	r, err := decodeReply(text)
	if err != nil {
		t.Fatalf("decodeReply: %v", err)
	}
	return r
}

// What the model is refused for its shape, the owner may do: each case passes
// checkSafe and is refused by parseChoice.
func TestOwnerEditsAreNotHeldToTheModelsShape(t *testing.T) {
	_, b, p := rtContBuilder(t)
	kw := mustJSON(testKeywords)
	reply := func(summary, exp, keywords string) string {
		return `{"sections":[{"title":"Summary","bullets":` + summary + `},{"title":"Experience","entries":` + exp + `}],"jd_keywords":` + keywords + `}`
	}
	okExp := `[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]`
	cases := map[string]string{
		"a job left out of the work history": reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]}]`, kw),
		"seven bullets under one entry":      reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a1","a2","a3","a4","a5","a6","a7"]},{"entry":"exp-b","bullets":["b1"]}]`, kw),
		"no summary bullets":                 reply(`[]`, okExp, kw),
		"a job kept as a heading":            reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-b","bullets":[]}]`, kw),
		"no keywords":                        reply(`["s3"]`, okExp, `[]`),
		"a summary section left out":         `{"sections":[{"title":"Experience","entries":` + okExp + `}],"jd_keywords":` + kw + `}`,
	}
	for name, text := range cases {
		if _, err := b.checkSafe(mustReply(t, text), p, testJD); err != nil {
			t.Errorf("%s: checkSafe refused the owner's edit: %v", name, err)
		}
		if _, err := b.parseChoice(text, p, testJD); err == nil {
			t.Errorf("%s: the model's reply was accepted; checkSteering must refuse it", name)
		}
	}
}

// What cannot go on a page is refused whoever chose it: each case is refused
// by checkSafe itself.
func TestSafetyRulesBindTheOwnerToo(t *testing.T) {
	_, b, p := rtContBuilder(t)
	kw := mustJSON(testKeywords)
	reply := func(summary, exp string) string {
		return `{"sections":[{"title":"Summary","bullets":` + summary + `},{"title":"Experience","entries":` + exp + `}],"jd_keywords":` + kw + `}`
	}
	cases := map[string]struct{ text, want string }{
		"ineligible bullet":               {reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-c","bullets":["x1"]}]`), "not available"},
		"bullet under another entry":      {reply(`["s3"]`, `[{"entry":"exp-b","bullets":["a3"]}]`), "not available under entry"},
		"entry bullet in the summary":     {reply(`["a1"]`, `[{"entry":"exp-a","bullets":["a3"]}]`), "not available for section"},
		"bullet twice":                    {reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3","a3"]}]`), "used twice"},
		"entry twice":                     {reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-a","bullets":["a1"]}]`), "used twice"},
		"education entry in a chosen sec": {reply(`["s3"]`, `[{"entry":"edu","bullets":["a1"]}]`), `"edu"`},
		"continuation without its lead":   {reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a8"]}]`), "a8"},
		"unknown section":                 {`{"sections":[{"title":"Hobbies","bullets":["s3"]}],"jd_keywords":` + kw + `}`, "not one to fill"},
		"section twice":                   {`{"sections":[{"title":"Summary","bullets":["s3"]},{"title":"Summary","bullets":["s1"]}],"jd_keywords":` + kw + `}`, "appears twice"},
		"keyword not in the posting":      {`{"sections":[{"title":"Summary","bullets":["s3"]}],"jd_keywords":["Fortran"]}`, `keyword "Fortran"`},
	}
	for name, c := range cases {
		_, err := b.checkSafe(mustReply(t, c.text), p, testJD)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: checkSafe = %v, want a refusal containing %q", name, err, c.want)
		}
	}
}

// A project the owner lists with no bullets is not a heading it may keep: a
// project heading with no evidence says nothing (keepsHeading).
func TestAnEmptyProjectIsRefusedEvenForTheOwner(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatalf("load: %v", err)
	}
	p := b.buildPool(func(string) bool { return true })
	text := replyWith(secSummary, secExperience, `{"title":"Agent Systems","entries":[{"entry":"proj-a","bullets":[]}]}`)
	_, err := b.checkSafe(mustReply(t, text), p, testJD)
	if err == nil || !strings.Contains(err.Error(), "has no bullets") {
		t.Fatalf("checkSafe = %v, want an empty project refused", err)
	}
}

// The owner's choice keeps the base's keywords when it names none, and keeps a
// heading-only job in the work history.
func TestOwnerChoiceKeepsBaseKeywordsAndJobHeadings(t *testing.T) {
	_, b, p := rtContBuilder(t)
	text := `{"sections":[{"title":"Summary","bullets":["s3"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-b","bullets":[]}]}],"jd_keywords":[]}`
	ch, err := b.checkSafe(mustReply(t, text), p, testJD)
	if err != nil {
		t.Fatalf("checkSafe: %v", err)
	}
	if ch.keywords != nil {
		t.Errorf("keywords = %q, want nil (the base selection's own)", ch.keywords)
	}
	exp := ch.secs[b.base.expIdx].entries
	if len(exp) != 2 || exp[1].id != "exp-b" || !exp[1].keeps() || len(exp[1].bullets) != 0 {
		t.Errorf("experience = %+v, want exp-b kept as a heading", exp)
	}
}

// The summary cap is a steering rule. The fixture offers three summary
// bullets, so it is shown on a reply that already passed checkSafe in shape.
func TestSteeringCapsTheSummary(t *testing.T) {
	_, b, p := rtContBuilder(t)
	r := mustReply(t, `{"sections":[{"title":"Summary","bullets":["s1","s2","s3","s4","s5"]}],"jd_keywords":[]}`)
	err := b.checkSteering(r, choice{secs: make([]secChoice, len(b.base.sections))}, p)
	if err == nil || !strings.Contains(err.Error(), "needs 1 to 4 bullets") {
		t.Fatalf("checkSteering = %v, want the summary cap", err)
	}
}
