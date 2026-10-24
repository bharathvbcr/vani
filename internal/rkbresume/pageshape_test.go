package rkbresume

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Tests of the fixes for the red team's findings (build_redteam_test.go,
// choice_redteam_test.go): what the page keeps, where entries may go, what the
// pool offers, what leaves the package, and how a reply is read.

func figureBuilder(t *testing.T) *builder {
	t.Helper()
	f := newFixture(t)
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return b
}

// rkb reports double_verified false, with no problem, for a claim nothing in the
// probed selection uses (claims.go: P2OK is len(sentences) > 0). That is not
// evidence against the claim, and treating it as one would hide every bullet
// whose figure the base does not use. A claim rkb gives a reason against is
// hidden wherever it is used; a figure rkb has never heard of cannot render.
func TestBadFiguresReadsWhatRkbSaysNotWhatItLeavesUnsaid(t *testing.T) {
	b := figureBuilder(t)
	baseCh := b.base.baseChoice(b.el.Allowed, b.isCont)
	cf := claimsFile{Claims: map[string]struct {
		DoubleVerified bool     `json:"double_verified"`
		Problems       []string `json:"problems"`
	}{
		"clmLat": {DoubleVerified: true},
		"clmThr": {DoubleVerified: false},                                       // used by a2 (base), no reason given
		"clmMem": {DoubleVerified: false},                                       // used by a4 (not in the base), nothing used it: unknown
		"clmUnv": {DoubleVerified: false, Problems: []string{"pass 1 missing"}}, // u1, outside the base, with a reason
	}}
	bad := b.badFigures(cf, baseCh)
	if !bad["clmThr"] {
		t.Errorf("a base figure that is not double-verified was not hidden: %v", bad)
	}
	if bad["clmMem"] {
		t.Errorf("clmMem reads false only because the base does not use it, yet it is hidden: %v", bad)
	}
	if !bad["clmUnv"] {
		t.Errorf("a figure outside the base that rkb gives a reason against was not hidden: %v", bad)
	}
	delete(cf.Claims, "clmLat")
	if bad := b.badFigures(cf, baseCh); !bad["clmLat"] {
		t.Errorf("a figure missing from rkb's export (it cannot render) was not hidden: %v", bad)
	}
}

// A project entry that loses its only bullet to an unverified figure goes with
// it (a bare project heading says nothing), and the section goes when it has no
// entry left; the work history is untouched.
func TestAProjectEntryWithNoVerifiedBulletLeftIsDropped(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	text := "Raised decode throughput {{clmUnv}} with fused kernels"
	editJSON(t, f.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["p2"] = map[string]any{"text": text, "review": reviewFor(text)}
	})
	f.rkb.unverified = map[string][]string{"clmUnv": {"p2"}}
	f.provider.replies = []string{replyWith(secSummary, secExperience,
		`{"title":"Agent Systems","entries":[{"entry":"proj-a","bullets":["p2"]}]}`, secGPU)}
	res := mustBuild(t, f)
	if res.Selection != SelectionTailored || !has(res.UnverifiedDropped, "p2") {
		t.Fatalf("scenario did not drop p2: %+v", res)
	}
	sel := lastSel(t, f)
	if strings.Contains(sel, `"proj-a"`) || has(selTitles(t, sel), "Agent Systems") {
		t.Errorf("a project with no verified bullet is on the page:\n%s", sel)
	}
	if !strings.Contains(sel, `"exp-a"`) || !strings.Contains(sel, `"proj-b"`) {
		t.Errorf("the drop took more than the project:\n%s", sel)
	}
}

// A section with nothing eligible may be left out (or listed empty) without
// forcing the model to borrow entries, and its slot in the order is unused.
func TestASectionWithNothingEligibleMayBeLeftOut(t *testing.T) {
	setup := func() *fixture {
		f := newFixture(t)
		addProjects(t, f)
		editJSON(t, f.bank, func(m map[string]any) {
			bl := m["bullets"].(map[string]any)
			for _, id := range []string{"p3", "p4"} {
				delete(bl[id].(map[string]any), "review")
			}
		})
		return f
	}
	for name, gpu := range map[string][]string{
		"left out":    nil,
		"listed bare": {`{"title":"GPU Systems","entries":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup()
			secs := append([]string{secSummary, secExperience, secAgent}, gpu...)
			f.provider.replies = []string{replyWith(secs...)}
			res := mustBuild(t, f)
			if res.Selection != SelectionTailored {
				t.Fatalf("not tailored: %s", res.Reason)
			}
			if got := strings.Join(selTitles(t, lastSel(t, f)), ","); got != "Summary,Experience,Agent Systems,Skills,Education" {
				t.Errorf("sections = %s", got)
			}
			if user := userText(f.provider.reqs[0]); !strings.Contains(user, `"GPU Systems": nothing eligible`) {
				t.Errorf("the prompt does not tell the model to leave the empty section out")
			}
		})
	}
	// The same section is still required while it has evidence.
	f := newFixture(t)
	addProjects(t, f)
	f.provider.replies = []string{replyWith(secSummary, secExperience, secAgent)}
	if res := mustBuild(t, f); res.Selection != SelectionBase || !strings.Contains(res.Reason, `"GPU Systems" is missing`) {
		t.Errorf("a section with evidence was left out: %+v", res)
	}
}

// The order of the sections that remain is the model's even when one section is
// left out.
func TestLeavingASectionOutKeepsTheModelsOrderOfTheRest(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	editJSON(t, f.bank, func(m map[string]any) { // a third project whose only bullet is ineligible (x1)
		m["entries"].(map[string]any)["proj-c"] = map[string]any{"title": "Third", "org": "Own", "dates": "2023", "bullets": []string{"x1"}}
	})
	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any) // Summary, Experience, Agent, GPU, Skills, Education
		third := map[string]any{"title": "Third Systems", "kind": "entries", "entries": []any{map[string]any{"entry": "proj-c", "bullets": []string{"x1"}}}}
		m["sections"] = append(append(append([]any{}, secs[:4]...), third), secs[4:]...)
	})
	f.provider.replies = []string{replyWith(secSummary, secExperience, secGPU, secAgent)}
	res := mustBuild(t, f)
	if res.Selection != SelectionTailored {
		t.Fatalf("not tailored: %s", res.Reason)
	}
	if got := strings.Join(selTitles(t, lastSel(t, f)), ","); got != "Summary,Experience,GPU Systems,Agent Systems,Skills,Education" {
		t.Errorf("sections = %s: the model's order of the others was lost", got)
	}
}

// Where an entry may go is decided by what it is (kbsync.KindOf) as well as by
// where the owner lists it: a job only in the work history, a project in any
// project section the model may reorder, education nowhere new.
func TestEntriesGoBySectionKindAsWellAsByTheOwnersListing(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	editJSON(t, f.bank, func(m map[string]any) { // a degree title is education (KindOf)
		m["entries"].(map[string]any)["edu"].(map[string]any)["title"] = "Master of Science in Computing"
	})
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	sec := func(title string) int {
		for i, s := range b.base.sections {
			if s.title == title {
				return i
			}
		}
		t.Fatalf("no section %q", title)
		return -1
	}
	exp, agent, gpu, edu := sec("Experience"), sec("Agent Systems"), sec("GPU Systems"), sec("Education")
	for _, c := range []struct {
		entry string
		in    map[int]bool
	}{
		{"exp-a", map[int]bool{exp: true}},
		{"exp-c", map[int]bool{exp: true}}, // a job no base section lists: still the work history's
		{"proj-a", map[int]bool{agent: true, gpu: true}},
		{"proj-b", map[int]bool{agent: true, gpu: true}},
		{"edu", map[int]bool{edu: false}},
		{"no-such-entry", nil},
	} {
		for _, i := range []int{exp, agent, gpu, edu} {
			if got := b.canHold(i, c.entry); got != c.in[i] {
				t.Errorf("canHold(%q, %q) = %v, want %v", b.base.sections[i].title, c.entry, got, c.in[i])
			}
		}
	}
	// The owner's own listing always counts (nothing allowed before is refused):
	// a job the base puts in a project section stays there.
	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any)
		agentSec := secs[2].(map[string]any)
		agentSec["entries"] = append(agentSec["entries"].([]any), map[string]any{"entry": "exp-c", "bullets": []string{"c1"}})
	})
	b2 := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b2.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !b2.canHold(agent, "exp-c") {
		t.Error("an entry the base lists in a section was refused there")
	}
}

// A project may go under a section the owner did not list it in, and the model
// is told to place each by topic.
func TestAProjectMayGoUnderAnotherProjectSection(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	swapped := replyWith(secSummary, secExperience,
		`{"title":"Agent Systems","entries":[{"entry":"proj-b","bullets":["p3","p4"]}]}`,
		`{"title":"GPU Systems","entries":[{"entry":"proj-a","bullets":["p1","p2"]}]}`)
	f.provider.replies = []string{swapped}
	res := mustBuild(t, f)
	if res.Selection != SelectionTailored {
		t.Fatalf("a project under another project section was refused: %s", res.Reason)
	}
	if user := userText(f.provider.reqs[0]); !strings.Contains(user, "a project goes under the section whose title fits its topic") {
		t.Errorf("the prompt does not tell the model to place projects by topic")
	}
}

// The base lists a role bare: the tailored page keeps it in the base's place
// among the entries the model named.
func TestABareRoleKeepsItsPlaceInTheTailoredWorkHistory(t *testing.T) {
	f := newFixture(t)
	editJSON(t, f.bank, func(m map[string]any) {
		m["entries"].(map[string]any)["exp-d"] = map[string]any{"title": "Advisor", "org": "Club", "dates": "2018", "bullets": []string{}}
	})
	editJSON(t, f.base, func(m map[string]any) {
		exp := m["sections"].([]any)[1].(map[string]any)
		ents := exp["entries"].([]any) // exp-a, exp-b
		bare := map[string]any{"entry": "exp-d", "bullets": []string{}}
		exp["entries"] = []any{ents[0], bare, ents[1]}
	})
	f.provider.replies = []string{validReply()}
	res := mustBuild(t, f)
	if res.Selection != SelectionTailored {
		t.Fatalf("not tailored: %s", res.Reason)
	}
	var order []string
	for _, e := range []string{"exp-a", "exp-d", "exp-b", "exp-c"} {
		if i := strings.Index(lastSel(t, f), `"`+e+`"`); i >= 0 {
			order = append(order, e)
		}
	}
	if got := strings.Join(order, ","); got != "exp-a,exp-d,exp-b,exp-c" {
		t.Errorf("entries appear as %s", got)
	}
	// And it is a heading with an explicit empty list, never a missing one (rkb
	// reads a missing list as every bullet the bank has for the entry).
	var sel struct {
		Sections []struct {
			Entries []struct {
				Entry   string    `json:"entry"`
				Bullets *[]string `json:"bullets"`
			} `json:"entries"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(lastSel(t, f)), &sel); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sel.Sections {
		for _, e := range s.Entries {
			if e.Entry == "exp-d" {
				found = true
				if e.Bullets == nil || len(*e.Bullets) != 0 {
					t.Errorf("the bare role's bullets = %v, want an explicit empty list", e.Bullets)
				}
			}
		}
	}
	if !found {
		t.Error("the bare role is not in the selection")
	}
}

// Dropping every bullet of a work-history entry keeps the entry as a heading;
// dropping every bullet of a project entry does not.
func TestWithoutKeepsAHeadingOnlyWhereTheEntryIsMarked(t *testing.T) {
	ch := choice{secs: []secChoice{{entries: []entChoice{
		{id: "job", bullets: []string{"j1"}, heading: true},
		{id: "proj", bullets: []string{"p1"}},
		{id: "bare", keepEmpty: true},
	}}}}
	out, _ := ch.without(map[string]bool{"j1": true, "p1": true}, func(string) bool { return false })
	var ids []string
	for _, e := range out.secs[0].entries {
		ids = append(ids, e.id)
	}
	if got := strings.Join(ids, ","); got != "job,bare" {
		t.Errorf("entries after the drop: %s, want job,bare", got)
	}
}

// ---- hardening: rkb's words never carry the header out

func TestScrubberMasksEveryHeaderStringIgnoringCaseAndSkipsShortOnes(t *testing.T) {
	sc := newScrubber([]string{"Zed Q. Owner", "zed.owner@example.invalid", "US", "  ", "Springfield, IL"})
	got := sc.clean("ERROR: ZED Q. OWNER <zed.owner@example.invalid> of Springfield, IL in the US")
	if strings.Contains(strings.ToLower(got), "zed") || strings.Contains(got, "Springfield") {
		t.Errorf("header strings survive: %q", got)
	}
	if !strings.Contains(got, "in the US") {
		t.Errorf("a two-letter value was masked, mangling ordinary text: %q", got)
	}
	if (scrubber{}).clean("anything") != "anything" {
		t.Error("the zero scrubber changes text")
	}
}

// The base names its header ("header": "default") instead of carrying it: the
// strings come from the bank's header of that name, and every channel from rkb
// is masked: the claims step, lint, fit advice.
func TestANamedBankHeaderIsMaskedInEveryRkbChannel(t *testing.T) {
	named := func(f *fixture) {
		editJSON(t, f.base, func(m map[string]any) { m["header"] = "default" })
	}
	check := func(t *testing.T, what string, err error, f *fixture) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error", what)
		}
		all := err.Error()
		for _, r := range f.provider.reqs {
			all += userText(r)
		}
		if strings.Contains(all, ownerContact) || strings.Contains(all, ownerName) {
			t.Errorf("%s: the header got out: %v", what, err)
		}
	}
	t.Run("claims step", func(t *testing.T) {
		f := newFixture(t)
		named(f)
		f.rkb.runErr = map[string]error{"claims-plain": errors.New("cannot read " + ownerContact + " for " + ownerName)}
		_, err := f.build()
		check(t, "claims", err, f)
	})
	t.Run("lint", func(t *testing.T) {
		f := newFixture(t)
		named(f)
		f.rkb.lint = func(string) (int, string) { return 1, "ERROR header: " + ownerName + " / " + ownerContact + "\n" }
		f.provider.replies = []string{validReply()}
		_, err := f.build()
		check(t, "lint", err, f)
	})
	t.Run("fit advice", func(t *testing.T) {
		f := newFixture(t)
		named(f)
		f.cfg.Provider, f.cfg.NoModel = nil, "off"
		f.rkb.fit = func(string) fitSpec {
			return fitSpec{status: 2, advice: "OVERFLOW for " + ownerName, pages: 3, pdf: testPDF, orphans: []string{ownerContact}}
		}
		_, err := f.build()
		check(t, "fit", err, f)
	})
}

// ---- running out of time

// Only running out of time keeps the soft-fit result; a caller who cancels does
// not get a page after saying stop.
func TestKeepSoftOnTimeoutOnlyKeepsOnATimeout(t *testing.T) {
	soft := &Result{PDF: testPDF, Selection: SelectionTailored, Reason: "kept with a layout note"}
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	if res, _, err := keepSoftOnTimeout(soft, expired, ctxError(expired)); err != nil || res == nil || !strings.Contains(res.Reason, "ran out of time") {
		t.Errorf("a timeout dropped the soft result: %v %v", res, err)
	}
	if res, _, err := keepSoftOnTimeout(nil, expired, ctxError(expired)); res != nil || err == nil || err.Code != CodeTimeout {
		t.Errorf("a timeout with nothing in hand must stay an error: %v %v", res, err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if res, _, err := keepSoftOnTimeout(soft, cancelled, ctxError(cancelled)); res != nil || err == nil || err.Code != CodeCancel {
		t.Errorf("a cancellation kept a result: %v %v", res, err)
	}
	if res, _, err := keepSoftOnTimeout(soft, context.Background(), fail(CodeRender, "x")); res != nil || err == nil {
		t.Errorf("a tool failure kept a result: %v %v", res, err)
	}
}

// ---- reading the reply

func TestTheReplyIsReadStrictlyAtEveryDepth(t *testing.T) {
	_, b, p := rtContBuilder(t)
	kw := mustJSON(testKeywords)
	reply := func(exp string) string {
		return `{"sections":[` + secSummary + `,{"title":"Experience","entries":` + exp + `}],"jd_keywords":` + kw + `}`
	}
	ok := `[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]`
	if _, err := b.parseChoice(reply(ok), p, testJD); err != nil {
		t.Fatalf("control refused: %v", err)
	}
	for name, exp := range map[string]string{
		"a bullets key twice inside an entry": `[{"entry":"exp-a","bullets":["a3"],"bullets":["a1"]},{"entry":"exp-b","bullets":["b1"]}]`,
		"an entry key twice":                  `[{"entry":"exp-a","entry":"exp-b","bullets":["a3"]},{"entry":"exp-b","bullets":["b1"]}]`,
		"Entry in another case":               `[{"Entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]`,
		"Bullets in another case":             `[{"entry":"exp-a","Bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]`,
	} {
		if _, err := b.parseChoice(reply(exp), p, testJD); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	titleCase := strings.Replace(reply(ok), `"title":"Experience"`, `"Title":"Experience"`, 1)
	if _, err := b.parseChoice(titleCase, p, testJD); err == nil {
		t.Errorf(`"Title" in another case: accepted`)
	}
}

func TestKeywordsAreFoldedByASCIIOnlyAndMustBeWords(t *testing.T) {
	jd := "We use Kubernetes with Go, Rust, tracing, latency, batching, observability, Python and ML."
	words := []string{"Go", "Rust", "tracing", "latency", "batching", "observability", "Python", "ML"}
	if _, err := checkKeywords(append(append([]string{}, words...), "KUBERNETES"), jd); err != nil {
		t.Errorf("ASCII case difference refused: %v", err)
	}
	for name, k := range map[string]string{
		"one letter":       "G",
		"punctuation only": "--",
		"non-ASCII fold":   "École", // the posting has no "École", and "é" is not folded to "É"
	} {
		if _, err := checkKeywords(append(append([]string{}, words...), k), jd+" école -- "); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := checkKeywords(words, jd); err != nil {
		t.Errorf("two-letter words are the shortest allowed: %v", err)
	}
}

// ---- summary continuations

func TestAnAlsoBulletIsAContinuationOfTheOneBeforeIt(t *testing.T) {
	for _, s := range []string{
		"**Also trains and evaluates models:** fine-tuning a small model for a calibrated decision model.",
		"Also trains and evaluates models",
		"  also built the evaluation harness",
	} {
		if !isContinuation(s) {
			t.Errorf("not recognised: %q", s)
		}
	}
	for _, s := range []string{
		"**Built an observability pipeline** that is also used by the on-call team.",
		"Alsop's method applied to ranking",
		"Raised throughput 3x, also cutting memory",
	} {
		if isContinuation(s) {
			t.Errorf("false positive: %q", s)
		}
	}
	// In the pool it is offered only directly after its owner-given predecessor.
	texts := map[string]string{"lead": "Research engineer", "also": "**Also trains models:** fine-tuning", "x": "Built tools"}
	sel := kbsync.Selection{Sections: []kbsync.SelSection{{Kind: "bullets", Bullets: []string{"lead", "also", "x"}}}}
	c := buildContinuations([]kbsync.Selection{sel}, textsOf(texts))
	if c.pred("", "also") != "lead" || c.check("", []string{"also"}) == nil || c.check("", []string{"lead", "also"}) != nil {
		t.Errorf("summary continuation rule: pred=%q", c.pred("", "also"))
	}
}
