package rkbresume

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Tests of the page's shape: continuations stay behind what they continue, and
// the project sections can be put in the order the posting calls for.

func editJSON(t *testing.T, path string, mutate func(m map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	mutate(m)
	writeJSON(t, path, m)
}

// addProjects gives the fixture two project sections after Experience:
// "Agent Systems" (proj-a: p1, p2) and "GPU Systems" (proj-b: p3, p4).
func addProjects(t *testing.T, f *fixture) {
	t.Helper()
	editJSON(t, f.bank, func(m map[string]any) {
		ents := m["entries"].(map[string]any)
		// No org or dates: kbsync.KindOf reads these as projects, not work.
		ents["proj-a"] = map[string]any{"title": "Agent Platform", "bullets": []string{"p1", "p2"}}
		ents["proj-b"] = map[string]any{"title": "GPU Runtime", "bullets": []string{"p3", "p4"}}
	})
	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any)
		agent := map[string]any{"title": "Agent Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-a", "bullets": []string{"p1"}}}}
		gpu := map[string]any{"title": "GPU Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-b", "bullets": []string{"p3"}}}}
		m["sections"] = append(append(append([]any{}, secs[:2]...), agent, gpu), secs[2:]...)
	})
	editJSON(t, f.other, func(m map[string]any) {
		m["sections"] = append(m["sections"].([]any),
			map[string]any{"title": "Agent Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-a", "bullets": []string{"p1", "p2"}}}},
			map[string]any{"title": "GPU Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-b", "bullets": []string{"p3", "p4"}}}})
	})
}

func selTitles(t *testing.T, sel string) []string {
	t.Helper()
	var s struct {
		Sections []struct {
			Title string `json:"title"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(sel), &s); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sec := range s.Sections {
		out = append(out, sec.Title)
	}
	return out
}

// replyWith builds a reply from section JSON fragments, in the order given.
func replyWith(sections ...string) string {
	return `{"sections":[` + strings.Join(sections, ",") + `],"jd_keywords":` + mustJSON(testKeywords) + `}`
}

const (
	secSummary    = `{"title":"Summary","bullets":["s3","s1"]}`
	secExperience = `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]}`
	secAgent      = `{"title":"Agent Systems","entries":[{"entry":"proj-a","bullets":["p1","p2"]}]}`
	secGPU        = `{"title":"GPU Systems","entries":[{"entry":"proj-b","bullets":["p3","p4"]}]}`
)

func TestTheModelMayReorderOnlyTheProjectSections(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	f.provider.replies = []string{replyWith(secSummary, secExperience, secGPU, secAgent)}
	res := mustBuild(t, f)
	if res.Source != SourceTailored {
		t.Fatalf("result = %+v", res)
	}
	got := strings.Join(selTitles(t, f.rkb.sels[len(f.rkb.sels)-1]), ",")
	if got != "Summary,Experience,GPU Systems,Agent Systems,Skills,Education" {
		t.Errorf("section order = %s", got)
	}
}

func TestTheOrderOfEverySectionButTheProjectOnesIsTheBases(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	// Experience listed before Summary, projects swapped: only the projects move.
	f.provider.replies = []string{replyWith(secExperience, secSummary, secGPU, secAgent)}
	mustBuild(t, f)
	got := strings.Join(selTitles(t, f.rkb.sels[len(f.rkb.sels)-1]), ",")
	if got != "Summary,Experience,GPU Systems,Agent Systems,Skills,Education" {
		t.Errorf("section order = %s", got)
	}
	// Listed in the base order, they stay in it.
	f2 := newFixture(t)
	addProjects(t, f2)
	f2.provider.replies = []string{replyWith(secSummary, secExperience, secAgent, secGPU)}
	mustBuild(t, f2)
	if got := strings.Join(selTitles(t, f2.rkb.sels[len(f2.rkb.sels)-1]), ","); got != "Summary,Experience,Agent Systems,GPU Systems,Skills,Education" {
		t.Errorf("section order = %s", got)
	}
}

func TestTheBasePathKeepsTheBasesSectionOrder(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	mustBuild(t, f)
	if got := strings.Join(selTitles(t, f.rkb.sels[len(f.rkb.sels)-1]), ","); got != "Summary,Experience,Agent Systems,GPU Systems,Skills,Education" {
		t.Errorf("section order = %s", got)
	}
}

func TestReorderedSectionsMustBeAPermutation(t *testing.T) {
	cases := map[string]struct{ reply, why string }{
		"a project section omitted":   {replyWith(secSummary, secExperience, secGPU), `"Agent Systems" is missing`},
		"a project section twice":     {replyWith(secSummary, secExperience, secGPU, secGPU, secAgent), `"GPU Systems" appears twice`},
		"a made-up section":           {replyWith(secSummary, secExperience, secGPU, secAgent, `{"title":"Hobbies","bullets":["s1"]}`), `"Hobbies" is not one to fill`},
		"a fixed section listed":      {replyWith(secSummary, secExperience, secGPU, secAgent, `{"title":"Skills","bullets":["s1"]}`), `"Skills" is not one to fill`},
		"experience omitted":          {replyWith(secSummary, secGPU, secAgent), `"Experience" is missing`},
		"summary omitted":             {replyWith(secExperience, secGPU, secAgent), `"Summary" is missing`},
		"an experience entry missing": {replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3"]}]}`, secGPU, secAgent), `entry "exp-b" of section "Experience" is missing`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			addProjects(t, f)
			f.provider.replies = []string{c.reply}
			res := mustBuild(t, f)
			if res.Source != SourceBase || !strings.Contains(res.Reason, c.why) {
				t.Fatalf("result = %+v, want a refusal containing %q", res, c.why)
			}
			if got := strings.Join(selTitles(t, f.rkb.sels[len(f.rkb.sels)-1]), ","); got != "Summary,Experience,Agent Systems,GPU Systems,Skills,Education" {
				t.Errorf("the base was not built in its own order: %s", got)
			}
		})
	}
}

func TestReorderableSectionsAreTheEntriesRunAfterTheFirstEntriesSection(t *testing.T) {
	mk := func(kind string, choosable bool) baseSection {
		return baseSection{kind: kind, choosable: choosable}
	}
	cases := []struct {
		name string
		secs []baseSection
		exp  int
		run  []int
	}{
		{"summary, experience, three projects, skills", []baseSection{mk("bullets", true), mk("entries", true), mk("entries", true), mk("entries", true), mk("entries", true), mk("lines", false), mk("entries", false)}, 1, []int{2, 3, 4}},
		{"a lines section ends the run", []baseSection{mk("entries", true), mk("entries", true), mk("lines", false), mk("entries", true)}, 0, []int{1}},
		{"a fixed entries section ends the run", []baseSection{mk("entries", true), mk("entries", true), mk("entries", false), mk("entries", true)}, 0, []int{1}},
		{"a summary after experience ends the run", []baseSection{mk("entries", true), mk("bullets", true), mk("entries", true)}, 0, nil},
		{"experience alone", []baseSection{mk("bullets", true), mk("entries", true)}, 1, nil},
		{"no entries section", []baseSection{mk("bullets", true), mk("lines", false)}, -1, nil},
	}
	for _, c := range cases {
		exp, run := reorderableSections(c.secs)
		if exp != c.exp || len(run) != len(c.run) {
			t.Errorf("%s: exp %d run %v, want %d %v", c.name, exp, run, c.exp, c.run)
			continue
		}
		for i := range run {
			if run[i] != c.run[i] {
				t.Errorf("%s: run %v, want %v", c.name, run, c.run)
			}
		}
	}
}

func TestThePromptNamesTheOrderTheWorkHistoryAndRelevance(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	f.provider.replies = []string{replyWith(secSummary, secExperience, secGPU, secAgent)}
	mustBuild(t, f)
	user := f.provider.reqs[0].Messages[0].Text()
	for _, want := range []string{
		`Section order: the order you list these sections in is their order on the page`,
		`"Agent Systems", "GPU Systems"`,
		`Keep every entry of "Experience"`,
		"Relevance to the posting decides everything",
		"Leave out evidence that does not bear on this posting",
		"All other sections keep their place",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// With no project sections there is no order to offer.
	g := newFixture(t)
	g.provider.replies = []string{validReply()}
	mustBuild(t, g)
	if strings.Contains(g.provider.reqs[0].Messages[0].Text(), "Section order:") {
		t.Error("a section order was offered with nothing to reorder")
	}
}

func TestContinuationsAreMarkedAndOnlyOfferedWithTheirPredecessor(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	mustBuild(t, f)
	user := f.provider.reqs[0].Messages[0].Text()
	for _, want := range []string{
		"a8 [must directly follow a7]: Operated the serving systems beneath those features",
		"c3 [must directly follow u1]: Brought the same lessons to the whole fleet",
		"must directly follow X",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// c2 is a continuation that came first in its list: nothing to follow, not offered.
	if strings.Contains(user, "c2:") || strings.Contains(user, "c2 [") || strings.Contains(user, "second GPU vendor") {
		t.Error("a continuation with no predecessor was offered")
	}
	// A plain bullet carries no marker.
	if strings.Contains(user, "a3 [must") || !strings.Contains(user, "a3: Traced slow requests") {
		t.Error("a plain bullet was marked")
	}
}

func replyExpA(ids string) string {
	return replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":`+ids+`},{"entry":"exp-b","bullets":["b1"]}]}`)
}

func TestAContinuationDirectlyAfterItsPredecessorIsBuilt(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{replyExpA(`["a1","a7","a8"]`)}
	res := mustBuild(t, f)
	if res.Source != SourceTailored {
		t.Fatalf("result = %+v", res)
	}
	ids := selBullets(t, f.rkb.sels[len(f.rkb.sels)-1])
	for i, id := range ids {
		if id == "a8" && ids[i-1] != "a7" {
			t.Errorf("a8 is not after a7: %v", ids)
		}
	}
	if !has(ids, "a8") {
		t.Errorf("a8 was not built: %v", ids)
	}
}

func TestAContinuationApartFromItsPredecessorIsRefusedAndTheRetryExplains(t *testing.T) {
	apart := map[string]string{
		"first under its entry":   `["a8","a7"]`,
		"after the wrong bullet":  `["a7","a1","a8"]`,
		"without its predecessor": `["a1","a8"]`,
		"predecessor after it":    `["a8","a1","a7"]`,
	}
	for name, ids := range apart {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.provider.replies = []string{replyExpA(ids), replyExpA(`["a1","a7","a8"]`)}
			res := mustBuild(t, f)
			if res.Source != SourceTailored || res.Reason != "after one retry" {
				t.Fatalf("result = %+v", res)
			}
			fb := f.provider.reqs[1].Messages[2].Text()
			if !strings.Contains(fb, `"a8" continues the bullet before it`) || !strings.Contains(fb, `must come directly after "a7"`) {
				t.Errorf("the retry does not say what is wrong: %q", fb)
			}
		})
	}
	t.Run("twice refused falls back to the base", func(t *testing.T) {
		f := newFixture(t)
		f.provider.replies = []string{replyExpA(`["a1","a8"]`)}
		res := mustBuild(t, f)
		if res.Source != SourceBase || !strings.Contains(res.Reason, `"a8" continues the bullet before it`) {
			t.Fatalf("result = %+v", res)
		}
		for _, sel := range f.rkb.sels {
			if has(selBullets(t, sel), "a8") {
				t.Error("a dangling continuation reached rkb")
			}
		}
	})
}

func TestAContinuationWithNoKnownPredecessorIsRefused(t *testing.T) {
	f := newFixture(t)
	reply := replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a1"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c2","c1"]}]}`)
	f.provider.replies = []string{reply}
	res := mustBuild(t, f)
	if res.Source != SourceBase || !strings.Contains(res.Reason, `"c2" is not available`) {
		t.Fatalf("result = %+v", res)
	}
}

func TestAContinuationWhosePredecessorIsIneligibleIsNotOffered(t *testing.T) {
	f := newFixture(t)
	editJSON(t, f.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["a7"].(map[string]any)["text"] = "Automated capacity planning (reworded after review)"
	})
	f.provider.replies = []string{validReply()}
	mustBuild(t, f)
	user := f.provider.reqs[0].Messages[0].Text()
	if strings.Contains(user, "a7:") || strings.Contains(user, "a8 [") {
		t.Errorf("a7 is ineligible, so neither it nor its continuation is offered:\n%s", user)
	}
}

func TestDroppingAnUnverifiedBulletDropsTheContinuationAfterIt(t *testing.T) {
	f := newFixture(t)
	reply := replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a1"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1","u1","c3"]}]}`)
	f.provider.replies = []string{reply}
	f.rkb.unverified = map[string][]string{"clmUnv": {"u1"}}
	res := mustBuild(t, f)
	if res.Source != SourceTailored || strings.Join(res.UnverifiedDropped, ",") != "c3,u1" {
		t.Fatalf("result = %+v", res)
	}
	for _, sel := range f.rkb.sels[len(f.rkb.sels)-3:] {
		if ids := selBullets(t, sel); has(ids, "u1") || has(ids, "c3") {
			t.Errorf("ids %v: c3 refers to u1, which is off the page", ids)
		}
	}
}

func baseListing(t *testing.T, f *fixture, ids ...string) {
	t.Helper()
	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any)
		secs[1].(map[string]any)["entries"] = []any{map[string]any{"entry": "exp-a", "bullets": ids}}
	})
}

func TestACoherentBaseIsUnchangedAndAnIncoherentOneLosesTheDanglingBullet(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	baseListing(t, f, "a7", "a8")
	mustBuild(t, f)
	if ids := selBullets(t, f.rkb.sels[len(f.rkb.sels)-1]); strings.Join(ids, ",") != "s1,s2,a7,a8" {
		t.Errorf("a coherent base changed: %v", ids)
	}
	g := newFixture(t)
	g.cfg.Provider, g.cfg.NoModel = nil, "off"
	baseListing(t, g, "a7", "a8")
	editJSON(t, g.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["a7"].(map[string]any)["text"] = "Automated capacity planning (reworded after review)"
	})
	// a7 stays listed by the base and the other selection, but no longer passes the readers.
	mustBuild(t, g)
	ids := selBullets(t, g.rkb.sels[len(g.rkb.sels)-1])
	if has(ids, "a8") || has(ids, "a7") {
		t.Errorf("ids %v: a8 refers to a7, which is off the page", ids)
	}
	// A continuation first in the base's own list has nothing to follow.
	h := newFixture(t)
	h.cfg.Provider, h.cfg.NoModel = nil, "off"
	baseListing(t, h, "a8", "a7")
	mustBuild(t, h)
	if ids := selBullets(t, h.rkb.sels[len(h.rkb.sels)-1]); has(ids, "a8") {
		t.Errorf("ids %v: a8 came first", ids)
	}
}
