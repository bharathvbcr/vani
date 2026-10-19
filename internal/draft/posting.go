package draft

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// A posting is split at the headings job boards print ("About Anthropic",
// "About the role", "Responsibilities", "Logistics") so a draft can lead with
// what the posting says about the company and the role, wherever those sit,
// and never spends the excerpt on pay, logistics or legal text. A posting with
// no recognised heading is sent as before: its first MaxDescriptionRunes.
//
// Headings are page text like the rest: they only steer which text is sent and
// are rendered inside the untrusted frame, never as instructions.

// Section kinds.
const (
	kindCompany = "company" // the employer: about us, mission, how we're different
	kindRole    = "role"    // the job: about the role, responsibilities, fit
	kindOther   = "other"   // text before the first heading
	kindDrop    = "drop"    // pay, benefits, logistics, legal: never sent
)

// Bounds. All are ceilings.
const (
	// MaxPostingRunes bounds the posting text that is read for sections. The
	// excerpt sent to the model is at most MaxDescriptionRunes of it.
	MaxPostingRunes = 60_000
	maxSections     = 40
	// maxSectionRunes is each section's first share of the excerpt, so one long
	// section cannot crowd out the rest; budget left over is shared out after.
	maxSectionRunes = 4000
	maxHeadingRunes = 80
	maxHeadingWords = 9
)

type section struct{ kind, heading, body string }

// headingKinds classify a normalized heading line (lower case, straight
// apostrophes, no trailing ":" or "?"). Every pattern is anchored at both
// ends: "Salary band design for 40 markets" is a duty, not a heading. Drop is
// tried first and role before company, so "About the role" is not read as
// "About <company>". The vocabulary is checked against the real Greenhouse,
// Lever, Ashby and Workday postings in internal/sources/*/testdata.
var headingKinds = []struct {
	kind string
	re   *regexp.Regexp
}{
	{kindDrop, oneOf(
		`(the )?(annual |us |base )?(compensation|salary|pay|base pay|base salary)( and benefits| range| ranges| transparency)?`,
		`(our )?benefits( and perks)?`, `perks( and benefits)?`, `what we offer`,
		`how .+ supports? (its |our )?(full-time )?(employees|team members|people|you)`,
		`logistics`, `eeo`, `equal (employment )?opportunity( employer)?`, `your safety( matters( to us)?)?`,
		`visa( sponsorship)?`, `locations?`, `where you('ll| will) (be|work)( based| located| working)?`,
		`how to apply`, `(the )?(interview|hiring|application|recruiting) process`,
		`accommodations?`, `privacy( notice| policy)?`, `deadline( to apply)?`,
	)},
	{kindRole, oneOf(
		`(about|in) (the|this) (role|position|job|opportunity|team)`, `(the|your) (role|team|opportunity|impact|work)`,
		`(job |role |position )?(overview|summary|description|details)`, `why this role( .+)?`,
		`duties`, `(representative )?projects`, `(key )?responsibilities( .+)?`,
		`(minimum |basic |preferred )?(requirements|qualifications)( .+)?`,
		`([^ ]+ ){1,5}(responsibilities|requirements|qualifications)`, // "<job title> Responsibilities"
		`day to day`, `a day in the life( .+)?`, `what you('ll| will) (do|work on|be doing|build|bring)`, `what you bring`,
		`skills`, `about you`, `who you are`, `you (have|are)`, `ideal candidate`, `nice to haves?`, `bonus points( for)?`,
		`you (may|might|could|would) be a (good|great|strong) fit( .+)?`, `you('ll| will) thrive( .+)?`,
		`strong candidates( .+)?`, `what we('re| are) looking for`,
		`reasons you should(n't| not) apply`, `this role (may|might) not be (a|the right) fit( .+)?`,
	)},
	{kindCompany, oneOf(
		`about (us|the company|our company|[^ ]+( [^ ]+){0,2})`, `who we are`, `what we do`,
		`our (mission|story|values|culture|company|team|vision)`, `mission`, `values`, `culture`, `vision`,
		`the company`, `company (overview|description)`, `how we('re| are) different`,
		`why (join|work) .+`, `why [^ ]+( [^ ]+)?`, `why .+ at .+`, `life at .+`, `working at .+`,
		`come work with us`, `join us`,
	)},
}

// oneOf compiles alternatives into one pattern anchored at both ends.
func oneOf(alts ...string) *regexp.Regexp {
	return regexp.MustCompile(`^(` + strings.Join(alts, "|") + `)$`)
}

// payLine and payShape open a dropped section at a sentence, not a heading:
// boards often print "The annual compensation range for this role is listed
// below." as body text under the last real heading. Both must match, so a duty
// such as "Compensation range benchmarking for new hires" stays role text.
// Such a section ends at the first line after it that is not about pay
// (payish), and the text goes back to the section it interrupted.
var (
	payLine  = regexp.MustCompile(`^(the )?(annual |expected |estimated |base )?(compensation|salary|pay)( and benefits)? (range|band|bands|scale)\b`)
	payShape = regexp.MustCompile(`for this (role|position|job)|listed below|[$€£]|\busd\b`)
	payish   = regexp.MustCompile(`[$€£¥]|\b(usd|eur|gbp|ote|salary|salaries|compensation|pay|bonus|equity|commission|on target earnings|range)\b`)
)

var apostrophes = strings.NewReplacer("’", "'", "‘", "'")

func paySentence(line string) bool {
	l := apostrophes.Replace(strings.ToLower(line))
	return payLine.MatchString(l) && payShape.MatchString(l)
}

// headingKind reports the kind of section line opens, or "" when line is not a
// heading: too long, a sentence, or not one the classifier knows.
func headingKind(line string) string {
	if paySentence(line) {
		return kindDrop
	}
	if line == "" || runes(line) > maxHeadingRunes || len(strings.Fields(line)) > maxHeadingWords {
		return ""
	}
	if last, _ := utf8.DecodeLastRuneInString(strings.TrimRight(line, "*_ ")); strings.ContainsRune(".,;", last) {
		return ""
	}
	h := strings.Trim(apostrophes.Replace(strings.ToLower(line)), "#*_ \t:!?")
	h = strings.Join(strings.Fields(h), " ")
	for _, k := range headingKinds {
		if k.re.MatchString(h) {
			return k.kind
		}
	}
	return ""
}

// sections splits desc at recognised heading lines. Text before the first
// heading is one kindOther section. After maxSections headings the rest is
// body of the last section.
func sections(desc string) []section {
	var out []section
	var bodies []*strings.Builder
	open := func(kind, heading string) {
		out, bodies = append(out, section{kind: kind, heading: heading}), append(bodies, &strings.Builder{})
	}
	resume := "" // set while a pay sentence's section is open: the kind it interrupted
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(out) < maxSections {
			if k := headingKind(line); k != "" {
				resume = ""
				if paySentence(line) {
					resume = kindOther
					if len(out) > 0 {
						resume = out[len(out)-1].kind
					}
				}
				open(k, line)
				continue
			}
			if resume != "" && !payish.MatchString(apostrophes.Replace(strings.ToLower(line))) {
				open(resume, "")
				resume = ""
			}
		}
		if len(out) == 0 {
			open(kindOther, "")
		}
		b := bodies[len(bodies)-1]
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	for i := range out {
		out[i].body = bodies[i].String()
	}
	return out
}

// postingText is the excerpt of a job description a draft is written from.
type postingText struct {
	sectioned bool      // false: no heading was recognised and kept holds the first maxRunes
	kept      []section // in posting order, each body clipped to its share; never kindDrop
}

// excerpt picks what of desc is sent, within maxRunes: company sections first,
// then role sections, then the rest; never a dropped one. Each kept section
// first gets up to maxSectionRunes (company sections up to that much between
// them, so several cannot crowd out the role), then leftover budget in the
// same order.
func excerpt(desc string, maxRunes int) postingText {
	ss := sections(desc)
	var p postingText
	for _, s := range ss {
		p.sectioned = p.sectioned || s.heading != ""
	}
	headCost := func(s section) int {
		if s.heading == "" {
			return 0
		}
		return runes(s.heading) + 1
	}
	var order []int
	for _, k := range []string{kindCompany, kindRole, kindOther} {
		for i, s := range ss {
			if s.kind == k && s.body != "" {
				order = append(order, i)
			}
		}
	}
	share, left, companyLeft := make([]int, len(ss)), maxRunes, maxSectionRunes
	for _, i := range order {
		n := min(headCost(ss[i])+runes(ss[i].body), maxSectionRunes, left)
		if ss[i].kind == kindCompany {
			n = min(n, companyLeft)
			companyLeft -= n
		}
		share[i], left = n, left-n
	}
	for _, i := range order {
		n := min(headCost(ss[i])+runes(ss[i].body)-share[i], left)
		share[i], left = share[i]+n, left-n
	}
	for i, s := range ss {
		if n := share[i] - headCost(s); n > 0 {
			s.body = clip(s.body, n)
			p.kept = append(p.kept, s)
		}
	}
	return p
}

// text joins the kept sections of the given kinds (all kept ones when none
// are given), headings included.
func (p postingText) text(kinds ...string) string {
	var b strings.Builder
	for _, s := range p.kept {
		if len(kinds) > 0 && !slices.Contains(kinds, s.kind) {
			continue
		}
		if s.heading != "" {
			b.WriteString(s.heading + "\n")
		}
		b.WriteString(s.body + "\n")
	}
	return b.String()
}

func (p postingText) has(kind string) bool {
	for _, s := range p.kept {
		if s.kind == kind {
			return true
		}
	}
	return false
}

// Motivation reports whether question asks why the seeker wants this job:
// "Why Anthropic?", "Why do you want to work here?", "What excites you about
// this role?". It reads the raw question: the selection stop list drops the
// very words ("why", "want", "work") that mark one.
func Motivation(question string) bool {
	q := apostrophes.Replace(strings.ToLower(strings.TrimSpace(question)))
	if q == "" {
		return false
	}
	if m := whyNameRE.FindStringSubmatch(q); m != nil && !auxiliary[m[1]] {
		return true
	}
	return whyWantRE.MatchString(q) || whatDrawsRE.MatchString(q) || excitedRE.MatchString(q) || strings.Contains(q, "motivat")
}

var (
	// "why <name>?" with one or two words: "Why Anthropic?", "Why this role?".
	whyNameRE   = regexp.MustCompile(`^why\s+([^\s?.!]+)(\s+[^\s?.!]+)?[\s?.!]*$`)
	whyWantRE   = regexp.MustCompile(`\bwhy\b[^.?!]*\b(want|wish|like|interested|interest|apply|applying|join|joining|work|working|choose|chose|excited|pursue|pursuing|considering)\b`)
	whatDrawsRE = regexp.MustCompile(`\bwhat\b[^.?!]*\b(excites|excited|interests|interested|draws|drew|attracts|attracted|appeals|inspires|motivates)\b`)
	excitedRE   = regexp.MustCompile(`\b(excited|passionate|enthusiastic|interested)\s+(about|in|by)\s+(joining|working|this|our|us|the (role|company|team|position|mission))\b`)
	// auxiliary verbs open a "why is X important?" question, not "why <name>?".
	auxiliary = map[string]bool{"is": true, "are": true, "do": true, "does": true, "did": true, "should": true, "would": true,
		"could": true, "can": true, "was": true, "were": true, "not": true, "has": true, "have": true}
)
