package eval

import (
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// LabelMode is one reading of the owner's would_apply labels for the
// sensitivity check: whether a stage-0 verdict survives when the doubtful
// labels are read another way. The owner's "maybe" has proved mixed (some are
// engineering roles at a level the rubric calls wrong, some are sales roles),
// so a verdict that holds in only one mode is not one to act on.
type LabelMode string

const (
	// ModeYesOrMaybe is the default: wanted is yes or maybe.
	ModeYesOrMaybe LabelMode = "yes_or_maybe"
	// ModeYesOnly counts a maybe as unwanted.
	ModeYesOnly LabelMode = "yes_only"
	// ModeMaybesExcluded leaves maybes out: yes against no.
	ModeMaybesExcluded LabelMode = "maybes_excluded"
	// ModeEngineering keeps engineering postings only (TitleIsEngineering),
	// wanted yes or maybe.
	ModeEngineering LabelMode = "engineering_only"
)

// LabelModes are the modes in report order.
var LabelModes = []LabelMode{ModeYesOrMaybe, ModeYesOnly, ModeMaybesExcluded, ModeEngineering}

// Remap is the verdict a judged label (yes, maybe or no) counts as under m,
// and whether it counts at all. engineering is TitleIsEngineering of the
// posting, read only by ModeEngineering. An unjudged verdict (skip) never
// counts.
func (m LabelMode) Remap(verdict string, engineering bool) (string, bool) {
	if _, judged := wanted(verdict); !judged {
		return "", false
	}
	switch m {
	case ModeYesOnly:
		if verdict == "maybe" {
			return "no", true
		}
	case ModeMaybesExcluded:
		if verdict == "maybe" {
			return "", false
		}
	case ModeEngineering:
		if !engineering {
			return "", false
		}
	case ModeYesOrMaybe:
	default:
		return "", false
	}
	return verdict, true
}

// ViewLabels is labels as m reads them: each would_apply label remapped, the
// ones m leaves out removed, every other label untouched. engineering reports
// TitleIsEngineering for a key (false when the posting is unknown). The input
// is not modified.
func ViewLabels(labels map[posting.Key]Entry, m LabelMode, engineering func(posting.Key) bool) map[posting.Key]Entry {
	out := make(map[posting.Key]Entry, len(labels))
	for k, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			out[k] = e
			continue
		}
		eng := m == ModeEngineering && engineering != nil && engineering(k)
		nv, keep := m.Remap(v, eng)
		if !keep {
			continue
		}
		c := e
		c.Labels = make(map[string]string, len(e.Labels))
		for s, x := range e.Labels {
			c.Labels[s] = x
		}
		c.Labels[WouldApply] = nv
		out[k] = c
	}
	return out
}

// The title heuristic behind ModeEngineering. It is a report filter, not a
// classifier the engine acts on: a word list, so it is transparent and free,
// and wrong on purpose in the conservative direction (a non-engineering word
// anywhere wins).
var (
	engineeringWords = []string{"engineer", "engineering", "developer", "swe", "programmer", "member of technical staff", "mts"}
	// nonEngineeringWords name functions that share the word "engineer" or
	// "developer" but are not software engineering for this report.
	nonEngineeringWords = []string{
		"sales", "account", "solutions", "gtm", "go-to-market", "marketing", "campaign", "finance", "recruit",
		"people", "talent", "legal", "audit", "media", "customer", "support", "mechanical", "electrical",
		"hardware", "asic", "manager", "head of", "director", "salesforce",
	}
)

// TitleIsEngineering reports whether a job title reads as a software
// engineering role: an engineering word and no non-engineering word, matched
// on word boundaries, ignoring case. "Lead Salesforce Developer" and "GTM
// Engineer" are not; "Member of Technical Staff (Backend Platform)" is.
func TitleIsEngineering(title string) bool {
	t := " " + normaliseTitle(title) + " "
	has := func(words []string) bool {
		for _, w := range words {
			if strings.Contains(t, " "+w+" ") {
				return true
			}
		}
		return false
	}
	return has(engineeringWords) && !has(nonEngineeringWords)
}

// normaliseTitle lowercases a title and turns every run of characters other
// than letters, digits and '-' into one space; a title is bounded first.
func normaliseTitle(s string) string {
	if len(s) > 512 {
		s = s[:512]
	}
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
