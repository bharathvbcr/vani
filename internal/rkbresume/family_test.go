package rkbresume

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Entries that share a bullet or an origin are one family, transitively; the
// base may pair two of a family, a tailored page may not add a second.
func TestFamiliesSayEachThingOnce(t *testing.T) {
	bank := kbsync.Bank{Entries: map[string]kbsync.Entry{
		"custom-llm": {Bullets: []string{"x1", "shared"}},
		"gemma":      {Bullets: []string{"shared", "g2"}},
		"tessl-v2":   {Origin: []string{"tessl"}, Bullets: []string{"t9"}},
		"tessl":      {Origin: []string{"tessl"}, Bullets: []string{"t1"}},
		"chain-a":    {Bullets: []string{"c1"}},
		"chain-b":    {Bullets: []string{"c1", "c2"}},
		"chain-c":    {Bullets: []string{"c2"}},
		"alone":      {Bullets: []string{"z"}},
	}}
	f := newFamilies(bank, nil)
	for _, bad := range [][]string{
		{"custom-llm", "alone", "gemma"},
		{"tessl", "tessl-v2"},
		{"chain-a", "chain-c"}, // a and c share nothing directly; b links them
	} {
		if err := f.check(bad); err == nil || !strings.Contains(err.Error(), "same work") {
			t.Errorf("%v: err = %v, want a same-work refusal", bad, err)
		}
	}
	if err := f.check([]string{"custom-llm", "tessl", "chain-b", "alone"}); err != nil {
		t.Errorf("one entry per family must pass: %v", err)
	}
	if err := f.check([]string{"unknown-1", "unknown-2"}); err != nil {
		t.Errorf("entries the bank does not know are their own families: %v", err)
	}
	gs := f.groups([]string{"gemma", "custom-llm", "alone", "tessl", "tessl-v2"})
	if strings.Join(gs, "|") != "custom-llm, gemma|tessl, tessl-v2" {
		t.Errorf("groups = %q", gs)
	}

	// The owner's base shows custom-llm and gemma together: that pair stays allowed.
	base := &baseSel{sections: []baseSection{{entries: []baseEntry{{id: "custom-llm"}, {id: "gemma"}}}}}
	f = newFamilies(bank, base)
	if err := f.check([]string{"custom-llm", "gemma"}); err != nil {
		t.Errorf("a pairing the base itself shows must pass: %v", err)
	}
	if gs := f.groups([]string{"custom-llm", "gemma"}); len(gs) != 0 {
		t.Errorf("a base pairing is not listed as a group to avoid: %q", gs)
	}
	if err := f.check([]string{"tessl", "tessl-v2"}); err == nil {
		t.Error("the base pairing one family must not allow another")
	}
}

// Through Build: a reply that puts two framings of one project on the page is
// refused, the retry tells the model "same work" and names both, and the
// corrected reply is built as the tailored resume.
func TestBuildRefusesTwoFramingsOfOneProjectThenBuildsTheRetry(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	const p5 = "Rebuilt the agent platform's scheduler around a work-stealing queue"
	editJSON(t, f.bank, func(m map[string]any) {
		// proj-c is another framing of proj-a: it lists p1 too.
		m["entries"].(map[string]any)["proj-c"] = map[string]any{"title": "Agent Platform (scheduler)", "bullets": []string{"p1", "p5"}}
		m["bullets"].(map[string]any)["p5"] = map[string]any{"text": p5, "review": reviewFor(p5)}
	})
	editJSON(t, f.other, func(m map[string]any) {
		m["sections"] = append(m["sections"].([]any),
			map[string]any{"title": "Agent Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-c", "bullets": []string{"p5"}}}})
	})
	twoFramings := `{"title":"Agent Systems","entries":[{"entry":"proj-a","bullets":["p1","p2"]},{"entry":"proj-c","bullets":["p5"]}]}`
	f.provider.replies = []string{
		replyWith(secSummary, secExperience, twoFramings, secGPU),
		replyWith(secSummary, secExperience, secAgent, secGPU),
	}
	res := mustBuild(t, f)
	if res.Source != SourceTailored {
		t.Fatalf("result = %+v, want the corrected reply built", res)
	}
	if len(f.provider.reqs) != 2 {
		t.Fatalf("turns = %d, want one retry", len(f.provider.reqs))
	}
	fb := f.provider.reqs[1].Messages[2].Text()
	if !strings.Contains(fb, "same work") || !strings.Contains(fb, `"proj-a"`) || !strings.Contains(fb, `"proj-c"`) {
		t.Errorf("the retry does not say which entries are the same work: %q", fb)
	}
	if sel := lastSel(t, f); strings.Contains(sel, `"proj-c"`) {
		t.Errorf("the second framing reached rkb:\n%s", sel)
	}
	if first := f.provider.reqs[0].Messages[0].Text(); !strings.Contains(first, "describe the same work") || !strings.Contains(first, "proj-a, proj-c") {
		t.Errorf("the first prompt does not warn about the group:\n%s", first)
	}
}

// The refusal parseChoice returns for two framings of one project names both
// entries, so the retry tells the model which to drop.
func TestFamilyRefusalNamesBothEntries(t *testing.T) {
	bank := kbsync.Bank{Entries: map[string]kbsync.Entry{
		"p1": {Bullets: []string{"a", "s"}},
		"p2": {Bullets: []string{"s", "b"}},
	}}
	f := newFamilies(bank, nil)
	err := f.check([]string{"p1", "p2"})
	if err == nil || !strings.Contains(err.Error(), `"p1" and "p2"`) {
		t.Fatalf("err = %v, want the two entries named", err)
	}
}
