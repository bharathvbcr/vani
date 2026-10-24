package rkbresume

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Red-team tests of what reaches the page: structure the model may not move,
// content that silently disappears, budgets and what leaks.

func lastSel(t *testing.T, f *fixture) string {
	t.Helper()
	if len(f.rkb.sels) == 0 {
		t.Fatal("rkb saw no selection")
	}
	return f.rkb.sels[len(f.rkb.sels)-1]
}

// An entry may only appear in a section some selection lists it in. parseChoice
// takes any pool entry in any entries section, so a project can be printed as a
// job and a job as a project.
func TestRedteamEntriesStayInTheSectionTheOwnerPutThemIn(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := b.buildPool(func(string) bool { return true })
	control := replyWith(secSummary, secExperience, secAgent, secGPU)
	if _, err := b.parseChoice(control, p, testJD); err != nil {
		t.Fatalf("control refused: %v", err)
	}
	projectAsJob := replyWith(secSummary,
		`{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"proj-a","bullets":["p1","p2"]}]}`,
		`{"title":"Agent Systems","entries":[{"entry":"exp-c","bullets":["c1"]}]}`, secGPU)
	if _, err := b.parseChoice(projectAsJob, p, testJD); err == nil {
		t.Errorf("proj-a (listed only under Agent Systems) was accepted under Experience, and exp-c (a job, listed only under Experience) under Agent Systems")
	}
}

// A project section whose bullets are all ineligible cannot be filled from its
// own entries, and parseChoice demands an entry in every chosen section: the
// only valid replies take entries from other sections.
func TestRedteamASectionWithNothingEligibleDoesNotForceBorrowedEntries(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	// every GPU Systems bullet the owner lists is ineligible (no review)
	editJSON(t, f.bank, func(m map[string]any) {
		bl := m["bullets"].(map[string]any)
		for _, id := range []string{"p3", "p4"} {
			delete(bl[id].(map[string]any), "review")
		}
	})
	f.provider.replies = []string{replyWith(secSummary, secExperience, secAgent, `{"title":"GPU Systems","entries":[{"entry":"exp-c","bullets":["c1"]}]}`)}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Selection == SelectionTailored {
		t.Errorf("the tailored resume prints exp-c, a job, under the project section GPU Systems because that section had nothing of its own")
	}
}

// The base lists a heading-only entry (a role with no bullets). The base build
// keeps it; the tailored build cannot name it (an entry needs 1 bullet in the
// reply) and drops the role from the work history.
func TestRedteamTailoredBuildKeepsHeadingOnlyEntries(t *testing.T) {
	f := newFixture(t)
	editJSON(t, f.bank, func(m map[string]any) {
		m["entries"].(map[string]any)["exp-d"] = map[string]any{"title": "Advisor", "org": "Club", "dates": "2018", "bullets": []string{}}
	})
	editJSON(t, f.base, func(m map[string]any) {
		exp := m["sections"].([]any)[1].(map[string]any)
		exp["entries"] = append(exp["entries"].([]any), map[string]any{"entry": "exp-d", "bullets": []string{}})
	})
	f.provider.replies = []string{validReply()}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Selection != SelectionTailored {
		t.Fatalf("scenario did not build the tailored choice: %s", res.Reason)
	}
	if !strings.Contains(lastSel(t, f), `"exp-d"`) {
		t.Errorf("the tailored resume dropped exp-d, a role the base lists with no bullets (the base build keeps it)")
	}
}

// The pool is meant to hide bullets that would be dropped for an unverified
// figure ("so the model is not offered bullets that would be dropped"), but the
// unverified set is computed from the base's bullets only. A bullet outside the
// base with an unverified figure is offered.
func TestRedteamPoolDoesNotOfferBulletsWithUnverifiedFigures(t *testing.T) {
	f := newFixture(t)
	makeB2Unverified(t, f)
	f.rkb.unverified = map[string][]string{"clmUnv": nil}
	f.provider.replies = []string{validReply()}
	if _, err := f.build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(f.provider.reqs) == 0 {
		t.Fatal("no model turn")
	}
	if strings.Contains(userText(f.provider.reqs[0]), "b2:") {
		t.Errorf("b2, whose figure rkb reports as not double-verified, is in the prompt")
	}
}

func makeB2Unverified(t *testing.T, f *fixture) {
	t.Helper()
	text := "Replicated a latency benchmark {{clmUnv}} faster on a second machine"
	editJSON(t, f.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["b2"] = map[string]any{"text": text, "review": reviewFor(text)}
	})
}

// The model may pick a bullet outside the base that is then dropped as
// unverified. If it was the only bullet of a work-history entry, the whole
// entry goes: a job disappears from the PDF with only UnverifiedDropped saying so.
func TestRedteamDroppingAnUnverifiedBulletDoesNotDeleteAJob(t *testing.T) {
	f := newFixture(t)
	makeB2Unverified(t, f)
	// The figure is unverified only while b2 is on the page (rkb judges a claim
	// against the sentences it is used in), so the base export cannot see it and
	// the pool still offers b2; the choice's own export finds it. (With "always
	// unverified", the pool hides b2 and the model could never pick it.)
	f.rkb.unverified = map[string][]string{"clmUnv": {"b2"}}
	f.provider.replies = []string{replyWith(secSummary,
		`{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b2"]}]}`)}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !has(res.UnverifiedDropped, "b2") {
		t.Fatalf("scenario did not drop b2: selection=%s dropped=%v", res.Selection, res.UnverifiedDropped)
	}
	if !strings.Contains(lastSel(t, f), `"exp-b"`) {
		t.Errorf("exp-b (in the base's work history) is gone from the PDF after its only bullet was dropped; the base keeps it")
	}
}

// Same on the base path: the owner's own base loses a job when the only bullet
// it lists for it has an unverified figure.
func TestRedteamBaseBuildDoesNotDeleteAJobForAnUnverifiedBullet(t *testing.T) {
	f := newFixture(t)
	text := "Published a study of batching {{clmUnv}} faster in Python inference stacks"
	editJSON(t, f.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["b1"] = map[string]any{"text": text, "review": reviewFor(text)}
	})
	f.rkb.unverified = map[string][]string{"clmUnv": nil}
	f.cfg.Provider = nil
	f.cfg.NoModel = "test"
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !has(res.UnverifiedDropped, "b1") {
		t.Fatalf("scenario did not drop b1: %v", res.UnverifiedDropped)
	}
	if !strings.Contains(lastSel(t, f), `"exp-b"`) {
		t.Errorf("exp-b is gone from the base PDF after its only bullet was dropped, and Result.Notes says nothing: %v", res.Notes)
	}
}

// Only the project sections are reordered; the lines section between two of
// them stays where the base has it.
func TestRedteamSectionsAroundALinesSectionKeepTheirPlace(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	editJSON(t, f.base, func(m map[string]any) {
		s := m["sections"].([]any) // Summary, Experience, Agent, GPU, Skills, Education
		m["sections"] = []any{s[0], s[1], s[2], s[4], s[3], s[5]}
	})
	f.provider.replies = []string{replyWith(secSummary, secExperience, secGPU, secAgent)}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Selection != SelectionTailored {
		t.Fatalf("not tailored: %s", res.Reason)
	}
	got := strings.Join(selTitles(t, lastSel(t, f)), ",")
	if want := "Summary,Experience,Agent Systems,Skills,GPU Systems,Education"; got != want {
		t.Errorf("order = %s, want %s (GPU Systems is not in the run after Experience)", got, want)
	}
}

func TestRedteamNothingToReorderKeepsTheBaseOrder(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{`{"sections":[` + rtExperience(`{"entry":"exp-c","bullets":["c1"]}`) + `,{"title":"Summary","bullets":["s3","s1"]}],"jd_keywords":` + mustJSON(testKeywords) + `}`}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Selection != SelectionTailored {
		t.Fatalf("not tailored: %s", res.Reason)
	}
	got := strings.Join(selTitles(t, lastSel(t, f)), ",")
	if want := "Summary,Experience,Skills,Education"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

// rtRunner runs the fake rkb but holds the second fit until the build's context
// ends: a retry that does not finish.
type rtRunner struct {
	*fakeRKB
	fits int
}

func (r *rtRunner) Run(ctx context.Context, c Command, timeout time.Duration) (Output, error) {
	if len(c.Args) > 0 && c.Args[0] == "fit" {
		r.fits++
		if r.fits == 2 {
			<-ctx.Done()
			return Output{}, ctx.Err()
		}
	}
	return r.fakeRKB.Run(ctx, c, timeout)
}

// The first tailored build is a good PDF with one soft layout flaw (kept as
// the fallback). The retry then runs out of time inside rkb. The build returns
// an error, discarding both the PDF it already holds and the base.
func TestRedteamARetryThatRunsOutOfTimeKeepsTheSoftFitResult(t *testing.T) {
	f := newFixture(t)
	oldM, oldB := minForModel, minForBase
	minForModel, minForBase = 150*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { minForModel, minForBase = oldM, oldB })
	f.cfg.Deadline = 1500 * time.Millisecond
	f.cfg.Runner = &rtRunner{fakeRKB: f.rkb}
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{status: 5, advice: "STRICT: 1 orphan last line(s)", pages: 2, pdf: testPDF, orphans: []string{"a1"}}
	}
	f.provider.replies = []string{validReply()}
	res, err := f.build()
	if err != nil {
		t.Fatalf("a soft-fit tailored PDF was in hand and the retry timed out, but the build failed: %v", err)
	}
	if res.Selection != SelectionTailored || len(res.PDF) == 0 {
		t.Errorf("result = %s, %d bytes", res.Selection, len(res.PDF))
	}
}

// Hardening: rkb's lint lines and stderr are passed on (clipped) to the second
// model prompt and to the caller's error. Real rkb does not echo the header
// (checked: lint.go and bank.go never put a header value in a message), but
// nothing here would stop a message that did.
func TestRedteamRkbOutputIsNotAChannelForTheHeader(t *testing.T) {
	f := newFixture(t)
	f.rkb.lint = func(string) (int, string) {
		return 1, "ERROR header.contact: " + ownerContact + " and " + ownerName + " are malformed\n"
	}
	f.provider.replies = []string{validReply()}
	_, err := f.build()
	if err == nil {
		t.Fatal("expected the base lint failure to be an error")
	}
	if strings.Contains(err.Error(), ownerContact) || strings.Contains(err.Error(), ownerName) {
		t.Errorf("the error returned to the caller carries the header: %v", err)
	}
	for i, r := range f.provider.reqs {
		if i > 0 && (strings.Contains(userText(r), ownerContact) || strings.Contains(userText(r), ownerName)) {
			t.Errorf("model request %d carries the header via rkb's lint line", i)
		}
	}
}
