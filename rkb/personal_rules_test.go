package main

import (
	"regexp"
	"strings"
	"testing"
)

// Per-user lint lists live in config.json (bullet_rules.lead_jargon,
// tech_terms, extra_banned, dup_phrases, summary_banned; tex_labels), empty in
// the embedded default. Each must add its check when set and add nothing when
// empty or blank. Every test restores the loaded config.

func saveRules(t *testing.T) {
	t.Helper()
	saved := cfg.Rules
	savedLabels := cfg.TexLabels
	t.Cleanup(func() { cfg.Rules, cfg.TexLabels = saved, savedLabels })
}

var personalCF = &ClaimsFile{Claims: map[string]*ClaimView{"clmA": {Value: "40 → 25 ms (-38%)", Kind: "time"}}, Statements: map[string]*StatementView{}}

func pageOf(title, kind, text string) *Composed {
	b := &Bullet{ID: "p1", Need: "n", Text: text}
	s := &Section{Title: title, Kind: kind}
	if kind == "bullets" {
		s.Bullets = []*Bullet{b}
	} else {
		s.Entries = []*Entry{{ID: "e1", Title: "P", Bullets: []*Bullet{b}}}
	}
	return &Composed{Header: Header{Name: "T"}, Sections: []*Section{s}}
}

func errsHave(R *lintResult, s string) bool { return guardHas(R.Errors, s) }

func TestPersonalLeadJargon(t *testing.T) {
	saveRules(t)
	const rule = "bold lead contains low-level technical jargon"
	text := "**Cut Zorblat parse time {{clmA}} versus the old parser:** cached parsed headers across all workers in each nightly run."
	page := pageOf("Projects", "entries", text)
	for _, empty := range [][]string{nil, {}, {" ", ""}} {
		cfg.Rules.LeadJargon = empty
		if R := lint(page, personalCF, false, ""); errsHave(R, rule) {
			t.Fatalf("lead_jargon %q must add nothing: %v", empty, R.Errors)
		}
	}
	cfg.Rules.LeadJargon = []string{"Zorblat"}
	if R := lint(page, personalCF, false, ""); !errsHave(R, rule) {
		t.Fatalf("a configured lead_jargon word must be caught: %v", R.Errors)
	}
	// Whole words, any case, quoted as text (a regexp metacharacter is literal).
	cfg.Rules.LeadJargon = []string{"zorb.at"}
	if R := lint(page, personalCF, false, ""); errsHave(R, rule) {
		t.Fatalf("a lead_jargon entry is literal text, not a pattern: %v", R.Errors)
	}
	// The compiled-in generic list still applies with an empty config.
	cfg.Rules.LeadJargon = nil
	if R := lint(pageOf("Projects", "entries", "**Cut epoll wakeup overhead {{clmA}} versus the old loop:** reused resampler buffers across all worker threads on each host."), personalCF, false, ""); !errsHave(R, rule) {
		t.Fatalf("built-in lead jargon lost: %v", R.Errors)
	}
}

func TestPersonalTechTerms(t *testing.T) {
	saveRules(t)
	cfg.Rules.TechTerms = nil
	if got := jargon("tuned the quokka pass"); len(got) != 0 {
		t.Fatalf("no tech_terms: want no jargon, got %v", got)
	}
	cfg.Rules.TechTerms = []string{" Quokka ", ""}
	if got := jargon("tuned the quokka pass"); len(got) != 1 || got[0] != "quokka" {
		t.Fatalf("configured tech term not counted: %v", got)
	}
	if got := jargon("used simd lanes"); len(got) != 1 {
		t.Fatalf("built-in tech terms lost: %v", got)
	}
}

func TestPersonalExtraBanned(t *testing.T) {
	saveRules(t)
	const rule = "filler or self-assessment phrase"
	page := pageOf("Projects", "entries", "**Cut report build time {{clmA}} versus the nightly batch:** frobnicated the cache deliberately across all report workers.")
	cfg.Rules.ExtraBanned = []string{"", "  "}
	if R := lint(page, personalCF, false, ""); errsHave(R, rule) {
		t.Fatalf("blank extra_banned must add nothing: %v", R.Errors)
	}
	cfg.Rules.ExtraBanned = []string{`\bfrobnicated\b`}
	if R := lint(page, personalCF, false, ""); !errsHave(R, rule+` /\bfrobnicated\b/`) {
		t.Fatalf("configured extra_banned pattern not caught: %v", R.Errors)
	}
	if n := len(bannedPatterns()); n != len(banned)+1 {
		t.Fatalf("bannedPatterns = %d, want built-in %d + 1", n, len(banned))
	}
}

func TestPersonalDupPhrases(t *testing.T) {
	saveRules(t)
	const rule = "duplicate word or awkward token expansion"
	page := pageOf("Projects", "entries", "**Cut checkout time {{clmA}} versus the old flow:** split the form into a 9 steps wizard across every store page.")
	cfg.Rules.DupPhrases = nil
	if R := lint(page, personalCF, false, ""); errsHave(R, rule) {
		t.Fatalf("no dup_phrases: nothing to catch here: %v", R.Errors)
	}
	cfg.Rules.DupPhrases = []string{`an?\s+9\s+steps`}
	if R := lint(page, personalCF, false, ""); !errsHave(R, rule) {
		t.Fatalf("configured dup_phrases pattern not caught: %v", R.Errors)
	}
	if m := dupWordRe().FindString("ran the the job"); m == "" {
		t.Fatalf("built-in doubled words lost")
	}
}

func TestPersonalSummaryBanned(t *testing.T) {
	saveRules(t)
	const rule = "summary must state concrete capabilities"
	page := pageOf("Summary", "bullets", "**Cut checkout time {{clmA}} versus the old flow:** a builder who Loves Spreadsheets and ships them.")
	cfg.Rules.SummaryBanned = []string{" "}
	if R := lint(page, personalCF, false, ""); errsHave(R, rule) {
		t.Fatalf("blank summary_banned must add nothing: %v", R.Errors)
	}
	cfg.Rules.SummaryBanned = []string{"loves spreadsheets", "spreadsheets"}
	R := lint(page, personalCF, false, "")
	n := 0
	for _, e := range R.Errors {
		if strings.Contains(e, rule) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("configured summary phrase must be caught once (any case), got %d: %v", n, R.Errors)
	}
	// Only in the Summary.
	if R := lint(pageOf("Projects", "entries", "**Cut checkout time {{clmA}} versus the old flow:** a builder who Loves Spreadsheets and ships them."), personalCF, false, ""); errsHave(R, rule) {
		t.Fatalf("summary_banned must not apply outside the Summary: %v", R.Errors)
	}
}

func TestTexLabel(t *testing.T) {
	saveRules(t)
	cfg.TexLabels = nil
	for stem, want := range map[string]string{"Resume": "Resume", "Resume_Data": "Data", "Curriculum_Vitae": "Master_CV"} {
		if got := texLabel(stem); got != want {
			t.Errorf("texLabel(%q) = %q, want %q", stem, got, want)
		}
	}
	cfg.TexLabels = map[string]string{"Resume": "General_Resume", "Resume_Data": "  "}
	if got := texLabel("Resume"); got != "General_Resume" {
		t.Errorf("tex_labels not applied: %q", got)
	}
	if got := texLabel("Resume_Data"); got != "Data" {
		t.Errorf("a blank tex_labels entry must fall back: %q", got)
	}
}

func TestParityAndStatNotationGeneric(t *testing.T) {
	saveRules(t)
	for _, empty := range [][]string{nil, {}, {" "}} {
		cfg.Rules.ParityBaselines = empty
		if unjustifiedParityRe() != nil || unjustifiedParity("matched NumPy on every shape") != "" {
			t.Fatalf("parity_baselines %q must turn the check off", empty)
		}
	}
	cfg.Rules.ParityBaselines = []string{"NumPy", "zlib", "a.b"}
	for s, want := range map[string]bool{
		"matched NumPy on every shape":     true,
		"matched multi-thread NumPy speed": true,
		"matching single-threaded zlib":    true,
		"matched half-precision NumPy":     false, // only an N-thread modifier is read
		"matched the spec":                 false,
		"matched axb":                      false, // a name is literal text, not a pattern
	} {
		if got := unjustifiedParityRe().MatchString(s); got != want {
			t.Errorf("parity %q = %v, want %v", s, got, want)
		}
	}
	if m := unjustifiedParity("matched NumPy bit-identical on every shape"); m != "" {
		t.Errorf("a generic differentiator must clear the claim: %q", m)
	}
	if m := unjustifiedParity("matched NumPy as a pure-Go library"); m == "" {
		t.Errorf("no differentiator: the claim must be flagged")
	}
	cfg.Rules.ParityDifferentiators = []string{`pure-Go`}
	if m := unjustifiedParity("matched NumPy as a pure-Go library"); m != "" {
		t.Errorf("a configured differentiator must clear the claim: %q", m)
	}
	nested := regexp.MustCompile(`\([^()]*\([^()]*\)[^()]*\)`)
	if s := statNotationRe.ReplaceAllString("held (an effect, F(2,30) = 9.1)", "STAT"); nested.MatchString(s) {
		t.Errorf("statistic notation must not read as a nested parenthetical: %q", s)
	}
	if !nested.MatchString(statNotationRe.ReplaceAllString("held (an effect (large))", "STAT")) {
		t.Errorf("a real nested parenthetical must still be seen")
	}
}

// bullet_rules.mechanisms: empty compiles to nothing; a malformed entry is an
// error naming it, never a silently dropped check; patterns are case-insensitive.
func TestMechanismsFromConfig(t *testing.T) {
	if ms, err := compileMechanisms(nil); err != nil || len(ms) != 0 {
		t.Fatalf("empty config: %v %v", ms, err)
	}
	bad := []struct {
		spec MechanismSpec
		want string
	}{
		{MechanismSpec{ID: " ", All: []string{`x`}}, "has no id"},
		{MechanismSpec{ID: "p", All: nil}, `"p" has no patterns`},
		{MechanismSpec{ID: "p", All: []string{`x`, "  "}}, `"p" has a blank pattern`},
		{MechanismSpec{ID: "p", All: []string{`(`}}, `"p" pattern "(" does not compile`},
	}
	for _, b := range bad {
		if _, err := compileMechanisms([]MechanismSpec{b.spec}); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%+v: want error %q, got %v", b.spec, b.want, err)
		}
	}
	ms, err := compileMechanisms([]MechanismSpec{{ID: " cache replaced recompute ", All: []string{`\bcache[sd]?\b`, `\brecomput\w*`}}})
	if err != nil || len(ms) != 1 || ms[0].ID != "cache replaced recompute" {
		t.Fatalf("valid entry: %+v %v", ms, err)
	}
	saved := cfg.Rules.Mechanisms
	defer func() { cfg.Rules.Mechanisms = saved }()
	cfg.Rules.Mechanisms = []MechanismSpec{{ID: "cache replaced recompute", All: []string{`\bcache[sd]?\b`, `\brecomput\w*`}}}
	b := &Bullet{ID: "b"}
	if got := bulletPoints(b, "A CACHE now skips RECOMPUTING totals"); len(got) != 1 || got[0] != "cache replaced recompute" {
		t.Errorf("case-insensitive match lost: %v", got)
	}
	if got := bulletPoints(b, "a cache of totals"); len(got) != 0 {
		t.Errorf("every pattern must match: %v", got)
	}
}
