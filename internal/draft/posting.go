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

// headingKinds classify a normalized heading line. Drop is tried first and
// role before company, so "About the role" is not read as "About <company>".
var headingKinds = []struct {
	kind string
	re   *regexp.Regexp
}{
	// Anchored at both ends: "Salary band design for 40 markets" is a duty, not a heading.
	{kindDrop, regexp.MustCompile(`^((the )?(annual |us |base )?(compensation|salary|pay|base pay|base salary)( and benefits| range| ranges| transparency)?|(our )?benefits( and perks)?|perks( and benefits)?|what we offer|logistics|eeo|equal (employment )?opportunity( employer)?|your safety( matters( to us)?)?|visa( sponsorship)?|locations?|how to apply|application process|accommodations?|privacy( notice| policy)?|deadline( to apply)?)$`)},
	{kindRole, regexp.MustCompile(`^((about|in) (the|this) (role|position|job|opportunity|team)|(the|your) (role|team|opportunity|impact|work)|(role|position|job|team) (overview|summary|description|details)|overview|duties|projects|representative projects|key responsibilities|day to day|a day in the life|what you('ll| will) (do|work on|be doing|build)|skills|about you|who you are|you have|you are|ideal candidate|nice to have|bonus points)$|^(responsibilities|requirements|qualifications|minimum qualifications|basic qualifications|preferred qualifications|you may be a good fit|you might be a good fit|you('ll| will) thrive|strong candidates|what we('re| are) looking for|what you('ll| will)? bring)\b`)},
	{kindCompany, regexp.MustCompile(`^(about (us|the company|our company|[^ ]+( [^ ]+){0,2})|who we are|what we do|our (mission|story|values|culture|company|team|vision)|mission|values|culture|vision|the company|company (overview|description)|how we('re| are) different|why (join|work) .+|why [^ ]+( [^ ]+)?|life at .+|working at .+|come work with us|join us)$`)},
}

// payLine and payShape open a dropped section at a sentence, not a heading:
// boards often print "The annual compensation range for this role is listed
// below." as body text under the last real heading. Both must match, so a duty
// such as "Compensation range benchmarking for new hires" stays role text.
var (
	payLine  = regexp.MustCompile(`^(the )?(annual |expected |estimated |base )?(compensation|salary|pay)( and benefits)? (range|band|bands|scale)\b`)
	payShape = regexp.MustCompile(`for this (role|position|job)|listed below|[$€£]|\busd\b`)
)

var apostrophes = strings.NewReplacer("’", "'", "‘", "'")

// headingKind reports the kind of section line opens, or "" when line is not a
// heading: too long, a sentence, or not one the classifier knows.
func headingKind(line string) string {
	if l := apostrophes.Replace(strings.ToLower(line)); payLine.MatchString(l) && payShape.MatchString(l) {
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
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(out) < maxSections {
			if k := headingKind(line); k != "" {
				out, bodies = append(out, section{kind: k, heading: line}), append(bodies, &strings.Builder{})
				continue
			}
		}
		if len(out) == 0 {
			out, bodies = append(out, section{kind: kindOther}), append(bodies, &strings.Builder{})
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

// posting is the excerpt of a job description a draft is written from.
type posting struct {
	sectioned bool      // false: no heading was recognised and kept holds the first maxRunes
	kept      []section // in posting order, each body clipped to its share; never kindDrop
}

// excerpt picks what of desc is sent, within maxRunes: company sections first,
// then role sections, then the rest; never a dropped one. Each kept section
// first gets up to maxSectionRunes (company sections up to that much between
// them, so several cannot crowd out the role), then leftover budget in the
// same order.
func excerpt(desc string, maxRunes int) posting {
	ss := sections(desc)
	var p posting
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
func (p posting) text(kinds ...string) string {
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

func (p posting) has(kind string) bool {
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
