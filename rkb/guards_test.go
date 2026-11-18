package main

import (
	"strings"
	"testing"
)

// Fixtures keep the shapes of wordings a reviewer rejected (a self-fix lead,
// an undercutting tail, a repeated point), so each guard is pinned to a
// realistic failure. The projects and figures in them are invented.

func guardHas(xs []string, s string) bool {
	for _, x := range xs {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func onePage(bs ...*Bullet) *Composed {
	return &Composed{Header: Header{Name: "T"}, Sections: []*Section{{Title: "Projects", Kind: "entries",
		Entries: []*Entry{{ID: "e1", Title: "P", Bullets: bs}}}}}
}

var guardClaims = &ClaimsFile{Claims: map[string]*ClaimView{
	"clmA": {Value: "35%", Kind: "percent"},
	"clmB": {Value: "2.7×", Kind: "mult"},
	"clmC": {Value: "120–900×", Kind: "mult"},
	"clmD": {Value: "61%", Kind: "percent"},
	"clmE": {Value: "52 → 30 (−42%)", Kind: "percent"},
	"clmF": {Value: "−71%", Kind: "percent"},
}, Statements: map[string]*StatementView{}}

func TestGuardSelfFixLead(t *testing.T) {
	const rule = "fixing a defect in your own work"
	bad := []string{
		"**Closed a grading loophole in a typing-test app where pasted text scored {{clmA}} of drills unsolved:** the app had trusted the browser's own timer; a server-side timer now scores each drill.",
		"**Patched a benchmark hole where a cached reply passed {{clmA}} of the latency suite unsolved:** the timer had started after the cache lookup; the harness now times the full request.",
		"**Found a stale-token path that let {{clmA}} of quiz attempts through unsolved:** the quiz app checked the token only on load; every answer is now checked on submit.",
		"**Flagged that a recipe tagger had memorized a shortcut, since dropping one cuisine from its training set lowered its precision {{clmA}}:** three of four cuisines failed, pointing at how the tags were collected.",
	}
	for _, txt := range bad {
		b := &Bullet{ID: "x", Need: "n", Text: txt}
		if R := lint(onePage(b), guardClaims, false, ""); !guardHas(R.Errors, rule) {
			t.Errorf("self-fix lead not caught: %s", txt)
		}
		b.SelfFixOK = "upstream patch to someone else's project"
		if R := lint(onePage(b), guardClaims, false, ""); guardHas(R.Errors, rule) {
			t.Errorf("self_fix_ok must clear it: %s", txt)
		}
	}
	good := []string{
		"**Decreased the weekly invoice export time {{clmB}}:** a shared connection pool now serves all export workers.",
		"**Built a recipe search app that answers {{clmA}} of queries from a local cache:** the cache warms itself from the most-searched dishes each morning.",
		"**Designed a seat-booking widget used for {{clmA}} of a small theater's sales:** it holds a seat for ten minutes while the buyer pays.",
	}
	for _, txt := range good {
		if R := lint(onePage(&Bullet{ID: "g", Need: "n", Text: txt}), guardClaims, false, ""); guardHas(R.Errors, rule) {
			t.Errorf("false positive self-fix: %s", txt)
		}
	}
}

func TestGuardUndercutTail(t *testing.T) {
	const rule = "undercuts the result"
	bad := []string{
		"**Shrank the photo archive's thumbnails {{clmA}} on a sample album:** thumbnails are now stored as WebP; uploads are no faster.",
		"**Sped up the garden planner's weekly plan {{clmB}} by caching soil lookups:** one lookup per bed replaced one per plant; the planner was later retired.",
		"**Made the tide-chart renderer draw {{clmB}} faster than the charting library it replaced:** timed over a month of charts on an office desktop; the cause was not measured.",
		"**Sped up the bird-call classifier's audio trimming {{clmB}} over the old script:** trims run in one pass per clip; timings documented without a saved log.",
	}
	for _, txt := range bad {
		b := &Bullet{ID: "x", Need: "n", Text: txt}
		if R := lint(onePage(b), guardClaims, false, ""); !guardHas(R.Errors, rule) {
			t.Errorf("undercutting tail not caught: %s", txt)
		}
		b.CaveatOK = "owner's call recorded in Claims.tex"
		if R := lint(onePage(b), guardClaims, false, ""); guardHas(R.Errors, rule) {
			t.Errorf("caveat_ok must clear it: %s", txt)
		}
	}
	good := []string{
		// Fixed versions of the bad ones, and a negative in the lead (a deliberate negative result).
		"**Shrank the photo archive's thumbnails {{clmA}} on a sample album:** thumbnails are now stored as WebP.",
		"**Cut the recipe app's page load {{clmA}}, so the shopping list opens before the photos finish:** photos now load after the list.",
		"**Showed that a second print queue left label printing no faster, despite {{clmA}} shorter queues:** the printer itself set the pace in every test.",
		"**Built a seed catalog search answering {{clmB}} faster than the shop's old search page:** results stay warm between visits.",
	}
	for _, txt := range good {
		if R := lint(onePage(&Bullet{ID: "g", Need: "n", Text: txt}), guardClaims, false, ""); guardHas(R.Errors, rule) {
			t.Errorf("false positive undercut: %s", txt)
		}
	}
	// Status labels and competitor concessions can be scope the claim needs
	// (unmerged work is labelled with its status): they warn, never fail.
	warnOnly := []string{
		"**Built the hiking-map tile server, serving tiles {{clmB}} faster than the city's public map:** tiles are prerendered at night; the rival TrailView wins on very old maps.",
		"**Built a plant-watering reminder that times {{clmA}} of waterings by soil moisture:** the sensor reader and the app share one message format; not yet deployed in the greenhouse.",
	}
	for _, txt := range warnOnly {
		b := &Bullet{ID: "w", Need: "n", Text: txt}
		R := lint(onePage(b), guardClaims, false, "")
		if guardHas(R.Errors, "undercut") || !guardHas(R.Warns, "may undercut") {
			t.Errorf("status/concession tail must warn, not fail: %s\nerrors=%v", txt, R.Errors)
		}
		b.CaveatOK = "status label required for unmerged work"
		if R := lint(onePage(b), guardClaims, false, ""); guardHas(R.Warns, "may undercut") {
			t.Errorf("caveat_ok must clear the warning too: %s", txt)
		}
	}
}

func TestGuardDuplicatePoint(t *testing.T) {
	const rule = "make the same point"
	store := &Bullet{ID: "catalog-loans", Need: "n", Text: "**Sped up the library catalog's loan lookups at 300 branches {{clmA}}:** a borrower index replaced full-table reads.", AllowLiterals: []string{"300"}}
	counts := &Bullet{ID: "catalog-overdue-counts", Need: "n", Text: "**Trimmed the catalog's overdue counts on a 7,500-book branch to {{clmC}} fewer database reads:** the overdue query now filters on an indexed due-date column.", AllowLiterals: []string{"7,500-book"}}
	search := &Bullet{ID: "catalog-title-search", Need: "n", Text: "**Cut the catalog's title-search time on a 40,000-title shelf list by {{clmD}}:** titles are looked up in a word index instead of reading every row.", AllowLiterals: []string{"40,000-title"}}
	procs := &Bullet{ID: "weather-panel-requests", Need: "n", Text: "**Streamlined the weather widget's forecast panel, cutting its API requests {{clmE}}:** on a 9-city sample, each panel's forecasts arrive in one batched call.", AllowLiterals: []string{"9-city"}}
	blast := &Bullet{ID: "weather-api-roundtrips", Need: "n", Text: "**Cut API round trips {{clmF}} in the weather widget:** one batched request serves every city lookup."}

	// The recurring points are the person's own, so they come from config.
	saved := cfg.Rules.Mechanisms
	defer func() { cfg.Rules.Mechanisms = saved }()
	cfg.Rules.Mechanisms = nil
	if R := lint(onePage(store, counts, search, procs, blast), guardClaims, false, ""); guardHas(R.Errors, rule) {
		t.Fatalf("no configured mechanisms and no explicit points: nothing can collide: %v", R.Errors)
	}
	cfg.Rules.Mechanisms = []MechanismSpec{
		{ID: "an index replaced reading every row", All: []string{`\bindex(?:ed|es)?\b`, `\b(?:rows?|tables?|database|reads?)\b`}},
		{ID: "fewer API round trips", All: []string{`\bAPI\b`, `\b(?:requests?|round[- ]trips?|calls?)\b`}},
	}

	R := lint(onePage(store, counts, search, procs, blast), guardClaims, false, "")
	for _, pair := range [][2]string{{"catalog-loans", "catalog-overdue-counts"}, {"catalog-overdue-counts", "catalog-title-search"}, {"weather-api-roundtrips", "weather-panel-requests"}} {
		found := false
		for _, e := range R.Errors {
			if strings.Contains(e, rule) && strings.Contains(e, pair[0]) && strings.Contains(e, pair[1]) {
				found = true
			}
		}
		if !found {
			t.Errorf("duplicate point %s / %s not caught; errors: %v", pair[0], pair[1], R.Errors)
		}
	}
	// Two bullets about different mechanisms must not collide.
	if R := lint(onePage(counts, blast), guardClaims, false, ""); guardHas(R.Errors, rule) {
		t.Errorf("distinct points flagged as duplicates: %v", R.Errors)
	}
	// An explicit point replaces the detected classes: distinct points clear it,
	// and two bullets tagged with one point collide even without the keywords.
	procs.Point, blast.Point = "panel-batched-call", "widget-shared-request"
	if R := lint(onePage(procs, blast), guardClaims, false, ""); guardHas(R.Errors, rule) {
		t.Errorf("explicit distinct points must clear the collision: %v", R.Errors)
	}
	procs.Point = "widget-shared-request"
	if R := lint(onePage(procs, blast), guardClaims, false, ""); !guardHas(R.Errors, rule) {
		t.Errorf("an explicit shared point must collide")
	}
}

func TestGuardRefutedSentence(t *testing.T) {
	const rule = "a blind check refuted this wording"
	txt := "**Cut the planning step in the garden planner {{clmB}} versus looking soil up for each of 60 plants:** one lookup per bed now serves every plant in it."
	b := &Bullet{ID: "planner-soil-lookups", Need: "n", Text: txt, AllowLiterals: []string{"60"}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{
		"clmB": {Value: "6.5×", Kind: "mult", RefutedSentences: map[string]string{sentenceHash(txt, "clmB"): "SCOPE: lookup phase only"}},
	}, Statements: map[string]*StatementView{}}
	if R := lint(onePage(b), cf, false, ""); !guardHas(R.Errors, rule) {
		t.Errorf("a refuted sentence must fail lint even in draft: %v", R.Errors)
	}
	b.Text = "**Cut the lookup phase in the garden planner {{clmB}} versus looking soil up for each of 60 plants:** one lookup per bed now serves every plant in it."
	if R := lint(onePage(b), cf, false, ""); guardHas(R.Errors, rule) {
		t.Errorf("new wording has no verdict yet and must not inherit the refutation")
	}
	// A statement check that found the wording UNSUPPORTED blocks the same way,
	// but only for the exact wording it judged.
	cf.Statements["planner-soil-lookups"] = &StatementView{TextHash: textHash(b.Text), Refuted: "UNSUPPORTED: [whole planning step]"}
	if R := lint(onePage(b), cf, false, ""); !guardHas(R.Errors, "a blind check refuted this wording") {
		t.Errorf("an UNSUPPORTED statement check must fail lint in draft")
	}
	cf.Statements["planner-soil-lookups"].TextHash = "older-wording"
	if R := lint(onePage(b), cf, false, ""); guardHas(R.Errors, "a blind check refuted this wording") {
		t.Errorf("a refutation of older wording must not block the new wording")
	}
}

// An export from an older rkb has no refuted-wording record: that check could
// not run, so it must not read as clean (warn in drafts, fail the final gate).
func TestLintOldClaimsExportCannotPassRefutedCheck(t *testing.T) {
	b := &Bullet{ID: "x", Need: "n", Text: "**Cut the step {{clmB}} versus the old lookup:** one lookup per bed now serves every plant."}
	const gate = "carries no refuted-wording record"
	old := &ClaimsFile{Generated: "2026-11-01T00:00:00Z", Claims: guardClaims.Claims, Statements: map[string]*StatementView{}}
	if R := lint(onePage(b), old, false, ""); !guardHas(R.Warns, gate) {
		t.Errorf("draft lint on an old export must warn")
	}
	if R := lint(onePage(b), old, true, ""); !guardHas(R.Errors, gate) {
		t.Errorf("final lint on an old export must fail")
	}
	old.Guards = 1
	if R := lint(onePage(b), old, false, ""); guardHas(R.Warns, gate) || guardHas(R.Errors, gate) {
		t.Errorf("a current export must not trip the gate")
	}
}

// 2026-11-13: two verifiers re-saved slimmed answer files without the hash
// fields; ingest recorded CONFIRMED entries that matched no sentence.
func TestIngestRefusesAnswersWithoutHashes(t *testing.T) {
	ok := &Worksheet{Items: []*WorkItem{
		{Kind: "figure", Claim: "clmB", SentenceHash: "abc"},
		{Kind: "statement", Key: "x", TextHash: "def"},
	}}
	if err := checkAnswerHashes(ok); err != nil {
		t.Errorf("complete answers refused: %v", err)
	}
	for _, bad := range []*WorkItem{{Kind: "figure", Claim: "clmB"}, {Kind: "statement", Key: "x"}} {
		if err := checkAnswerHashes(&Worksheet{Items: []*WorkItem{bad}}); err == nil {
			t.Errorf("an answer without its hash must be refused: %+v", bad)
		}
	}
}

func TestReaderSheetCarriesOwnerRules(t *testing.T) {
	for _, s := range []string{"(d)", "(e)", "(f)", "own code", "another bullet", "undercuts"} {
		if !strings.Contains(readerOwnerRules, s) {
			t.Errorf("reader instructions lost %q", s)
		}
	}
}

// The Assess() side: a SCOPE or MISMATCH blind pass is recorded as a
// refutation of that sentence; a missing or unanswered pass is not.
func TestEvaluateRecordsRefutations(t *testing.T) {
	s := "**Cut the step {{clmB}} versus the old loop:** grouped the lookups into one pass."
	h := sentenceHash(s, "clmB")
	cl := &Claim{ID: "clmB", Value: "6.5×"}
	for verdict, want := range map[string]bool{"SCOPE": true, "MISMATCH": true, "NO_SCOPE": false, "NOT_FOUND": false, "QUOTE_NOT_FOUND": false} {
		log := VerifyLog{"clmB": {{Value: "6.5×", Pass: 2, SentenceHash: h, Verdict: Verdict(verdict), Evidence: "phase only"}}}
		r := Assess(cl, log, nil, []string{s})
		if got := r.Refuted[h] != ""; got != want {
			t.Errorf("verdict %s: refuted=%v, want %v", verdict, got, want)
		}
	}
}
