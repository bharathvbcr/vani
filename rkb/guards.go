package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Guards for three ways a correct, verified bullet still hurts the page:
//
//  1. It headlines fixing a defect in the person's own work ("Closed a grading
//     loophole…", "Flagged that … memorized a shortcut"). Fixing your own bug
//     is not an achievement.
//  2. Its tail undercuts the result with a disclaimer about something the
//     bullet never claimed (a "no faster", "no longer used" or "not measured"
//     note about a part the bullet does not claim).
//  3. It makes the same point as another bullet on the page in different words
//     (two bullets each crediting the same mechanism for their result). The
//     figure and phrase checks in repeats.go cannot see this, because the words
//     and figures differ.
//
// The guard 1 and 2 patterns are compiled in, not read from config.json, so an
// older config can never switch them off. Guard 3 needs to know which points a
// person's bullets keep making, so its mechanisms come from
// bullet_rules.mechanisms (an explicit per-bullet point works without them).

var (
	// Lead (bold) wording that frames the result as finding or fixing a defect.
	selfFixLeadRe = regexp.MustCompile(`(?i)\b(?:loopholes?|(?:grader|grading|scoring|benchmark|test|security)[- ]holes?|exploits?|exploited|bugs?|flaws?|shortcuts?|leaks?|leaked|unsolved)\b|^\s*(?:caught|flagged|closed|patched)\b`)

	// Tail wording that disclaims or undersells the result. Only the tail (after
	// the bold lead) is checked: a deliberate negative result belongs in the
	// lead, and a scope limit belongs there too, as a narrower claim.
	//
	// Error tier: the kinds reviewers reject outright (no faster, later retired, not
	// profiled) and their close variants — notes about something the bullet
	// never claims.
	undercutRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join([]string{
		`no (?:faster|slower|better|quicker|smaller|cheaper|more accurate)`,
		`(?:was|were|is|are|has been|have been|remains?)\s+(?:not|never)\s+(?:profiled|measured|benchmarked|explained|isolated|reproduced)`,
		`not (?:yet )?(?:profiled|measured|benchmarked)`,
		`un(?:profiled|measured|explained)`,
		`(?:later|since|then|now|was|were|has been|have been) retired`,
		`no longer (?:used|shipped|maintained|in use|supported)`,
		`documented without`,
	}, "|") + `)\b`)
	// Warning tier: status labels and competitor concessions. These can be the
	// scope a claim needs (an unmerged feature's status, a rival that wins one
	// case), so they are flagged for a decision rather than refused. Promote a
	// pattern to the error tier if the owner rules on it.
	undercutWarnRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join([]string{
		`not yet (?:run|qualified|tested|validated|deployed|used|shipped|merged)`,
		`(?:rival|competing|baseline|existing|other)\b[^;:.]{0,40}\bwins?\b`,
		`(?:loses|lose|trails|lags) (?:to|behind)`,
	}, "|") + `)\b`)
)

// readerOwnerRules travels in every reader sheet, so readers apply the owner's
// rules whichever copy of the skill text loaded. Lint catches the detectable
// wordings; readers own the rest (a self-fix lead with no defect word, a
// repeated point outside the known mechanisms).
const readerOwnerRules = `Owner rules, grounds for revise in every round including FINAL CHECK: ` +
	`(d) the bullet presents finding or fixing a defect in the candidate's own code, model or harness (a bug, loophole, shortcut, leak) as an achievement; ` +
	`(e) it makes a point another bullet on page_text already makes, even in different words (e.g. two bullets both crediting one cache or one index): revise the weaker one and name the other bullet's id in the note; ` +
	`(f) its tail undercuts the result with a disclaimer about something the bullet never claims (e.g. "uploads are no faster", "later retired", "the cause was not measured"). ` +
	`A rewrite must fix these by narrowing or replacing the claim, never by appending a caveat.`

// mechanism is a point the person's work makes repeatedly in different words.
// A bullet whose plain text matches every pattern makes that point.
type mechanism struct {
	ID  string
	All []*regexp.Regexp
}

// MechanismSpec is one bullet_rules.mechanisms entry: the point's name and
// the patterns (case-insensitive) a bullet's plain text must all match. Which
// points recur depends on the person's work, so the list lives in config and
// is empty by default; an explicit `point` on a bullet works either way.
type MechanismSpec struct {
	ID  string   `json:"id"`
	All []string `json:"all"`
}

// compileMechanisms validates and compiles the configured mechanisms. An entry
// with no id, no patterns, a blank pattern or one that does not compile is an
// error, never a silently skipped check.
func compileMechanisms(specs []MechanismSpec) ([]mechanism, error) {
	out := make([]mechanism, 0, len(specs))
	for i, s := range specs {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return nil, fmt.Errorf("bullet_rules.mechanisms[%d] has no id", i)
		}
		if len(s.All) == 0 {
			return nil, fmt.Errorf("bullet_rules.mechanisms %q has no patterns", id)
		}
		m := mechanism{ID: id}
		for _, p := range s.All {
			if strings.TrimSpace(p) == "" {
				return nil, fmt.Errorf("bullet_rules.mechanisms %q has a blank pattern", id)
			}
			re, err := regexp.Compile(`(?i)` + p)
			if err != nil {
				return nil, fmt.Errorf("bullet_rules.mechanisms %q pattern %q does not compile: %v", id, p, err)
			}
			m.All = append(m.All, re)
		}
		out = append(out, m)
	}
	return out, nil
}

func mechanisms() []mechanism {
	ms, err := compileMechanisms(cfg.Rules.Mechanisms)
	if err != nil {
		die("config.json: %v", err)
	}
	return ms
}

// bulletPoints returns the points a bullet makes: its explicit point when set,
// otherwise every mechanism class its text matches.
func bulletPoints(b *Bullet, plain string) []string {
	if p := strings.TrimSpace(b.Point); p != "" {
		return []string{p}
	}
	var out []string
	for _, m := range mechanisms() {
		all := true
		for _, re := range m.All {
			if !re.MatchString(plain) {
				all = false
				break
			}
		}
		if all {
			out = append(out, m.ID)
		}
	}
	return out
}

// lintBulletGuards runs guards 1 and 2 on one bullet.
func lintBulletGuards(R *lintResult, b *Bullet, plainLead, plainTail string) {
	if m := selfFixLeadRe.FindString(plainLead); m != "" && strings.TrimSpace(b.SelfFixOK) == "" {
		R.err("%s: the lead reads as fixing a defect in your own work (%q); lead with what was built and its impact instead. Finding or fixing your own bug is not an achievement. If this is someone else's code (an upstream patch), record that in self_fix_ok", b.ID, strings.TrimSpace(m))
	}
	if strings.TrimSpace(b.CaveatOK) != "" {
		return
	}
	if m := undercutRe.FindString(plainTail); m != "" {
		R.err("%s: the tail undercuts the result (%q). Drop the clause: a disclaimer about something the bullet never claims only undersells it. If the claim truly needs that limit, narrow the bold lead to it rather than appending a caveat, or record why the note must stay in caveat_ok", b.ID, m)
	} else if m := undercutWarnRe.FindString(plainTail); m != "" {
		R.warn("%s: the tail may undercut the result (%q). Keep it only if the claim needs that scope (an unmerged feature's status, a rival that wins a case); otherwise drop it. caveat_ok records the decision", b.ID, m)
	}
}

// checkPoints runs guard 3 across the page.
func checkPoints(c *Composed, cf *ClaimsFile, R *lintResult) {
	at := map[string][]string{}
	for _, r := range c.allBullets() {
		for _, p := range bulletPoints(r.B, resolvePlain(r.B.Text, cf.Claims)) {
			at[p] = append(at[p], r.B.ID)
		}
	}
	var keys []string
	for k := range at {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ids := at[k]; len(ids) > 1 {
			R.err("%s make the same point (%s); keep the strongest and give the other spot a different point (set \"point\" on a bullet only if the detection is wrong)", strings.Join(ids, ", "), k)
		}
	}
}
