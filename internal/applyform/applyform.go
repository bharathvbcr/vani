// Package applyform turns an application form's questions into the bounded
// summary a posting carries (posting.ApplyForm): which required questions can
// knock a candidate out, how many essays and extra uploads it asks for, and
// how long the form is.
//
// It reads only the form's own structure, the question labels, their answer
// type and their offered options, never the job description, and it never
// sees an answer. An ATS adapter maps its form into Questions; this package
// owns what counts as a knock-out, so the rule is one for every ATS.
// Classification is by keyword on the employer's labels: a label it does not
// recognise is counted but never flagged, so a miss shows less, never more.
package applyform

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Type is the answer type of a question.
type Type int

const (
	// Other is any type this package does not classify (text, number, date,
	// phone, a free-form location box).
	Other Type = iota
	// LongText is a free-text essay box.
	LongText
	// YesNo is a Boolean question.
	YesNo
	// Choice is a pick from offered options, one or several.
	Choice
	// File is a file upload.
	File
)

// Question is one question of a form as an adapter reads it.
type Question struct {
	Label    string
	Type     Type
	Required bool
	// Basic marks the applicant basics every form has (name, e-mail, resume):
	// they are left out of the counts and are never a gate.
	Basic bool
	// Options are the offered answers of a Choice, archived ones already left
	// out; OptionsTotal is how many there are when the adapter kept fewer.
	Options      []string
	OptionsTotal int
}

var (
	sponsorRE = regexp.MustCompile(`(?i)sponsor|\bvisas?\b|work permit|immigration`)
	// authRE is a question about the right to work, or about living where the
	// role is based.
	authRE = regexp.MustCompile(`(?i)authori[sz](?:ed|ation)\b.{0,30}\bwork|\bwork\b.{0,20}authori[sz]ation|(?:eligible|eligibility|able|entitled|permitted|permission|right)\b.{0,12}\bto work|legally .{0,20}\bwork`)
	// withoutRE words the authorization question as "…without sponsorship",
	// whose No is the only way to say sponsorship is needed.
	withoutRE = regexp.MustCompile(`(?i)\bwithout\b|\bnot (?:require|need)|\bno (?:need|sponsor)|(?:do not|don'?t) (?:require|need)`)
	placeRE   = regexp.MustCompile(`(?i)\b(?:countr(?:y|ies)|states?|provinces?|regions?|locations?|cit(?:y|ies)|reside|residen(?:ce|cy|t)|based|located|work from)\b`)
	degreeRE  = regexp.MustCompile(`(?i)\bdegree\b|bachelor|master'?s|\bph\.?d\b|doctorate|(?:level of|highest) education|education level`)
	yearsRE   = regexp.MustCompile(`(?i)\byears?\b.{0,40}\bexperience|\bexperience\b.{0,40}\byears?\b|how many years|\byoe\b|years of`)
	// negRE finds an option that says the opposite of needing sponsorship.
	negRE  = regexp.MustCompile(`(?i)\b(?:no|not|never|without|don'?t|do not|doesn'?t|does not)\s+(?:\w+\s+){0,2}(?:sponsor|visas?|require|need)`)
	needRE = regexp.MustCompile(`(?i)\b(?:sponsor|visas?|work permit|h-?1b|opt|cpt|tn)\b|\b(?:require|need)s?\b`)
)

// maxPlaceOptions is the longest option list still read as "you must be in one
// of these places"; a longer one (every country) restricts nobody.
const maxPlaceOptions = 40

// untitledUpload stands for a required upload whose label is blank.
const untitledUpload = "File upload"

// Summarize builds the summary of a form's questions. incomplete says the
// adapter could not read every entry or stopped at its own scan bound. It
// returns nil when qs is empty: a form with no questions at all was not read,
// it is not a form with no knock-outs (every real form asks at least for a
// name).
func Summarize(qs []Question, incomplete bool) *posting.ApplyForm {
	if len(qs) == 0 {
		return nil
	}
	f := &posting.ApplyForm{V: posting.ApplyFormVersion, Incomplete: incomplete}
	for _, q := range qs {
		if q.Basic {
			continue
		}
		f.Questions++
		if !q.Required {
			continue
		}
		f.Required++
		switch q.Type {
		case File:
			l := clean(q.Label, posting.MaxFormOptionRunes)
			switch {
			case len(f.Uploads) >= posting.MaxFormUploads:
				f.Omitted++
			case l == "":
				f.Uploads = append(f.Uploads, untitledUpload)
			default:
				f.Uploads = append(f.Uploads, l)
			}
		case LongText:
			f.Essays++
		case YesNo, Choice:
			g, ok := gateOf(q)
			switch {
			case !ok:
			case len(f.Gates) >= posting.MaxFormGates:
				f.Omitted++
			default:
				f.Gates = append(f.Gates, g)
			}
		}
	}
	return f
}

// gateOf classifies one required yes/no or choice question.
func gateOf(q Question) (posting.FormGate, bool) {
	label := q.Label
	g := posting.FormGate{Text: clean(label, posting.MaxFormLabelRunes), YesNo: q.Type == YesNo}
	if g.Text == "" {
		return g, false
	}
	yesNo := q.Type == YesNo || (q.Type == Choice && onlyYesNo(q.Options))
	switch {
	case sponsorRE.MatchString(label):
		g.Kind = posting.GateSponsorship
		authorized := withoutRE.MatchString(label)
		switch {
		case yesNo && authorized:
			g.Flag = posting.FlagAuthorizedYesNo
		case q.Type == Choice && len(q.Options) > 0 && !offersNeed(q.Options, authorized):
			g.Flag = posting.FlagNoSponsorAnswer
		}
	case authRE.MatchString(label):
		g.Kind = posting.GateWorkAuth
		if yesNo {
			g.Flag = posting.FlagEligibilityYesNo
		}
	case degreeRE.MatchString(label):
		g.Kind = posting.GateDegree
	case yearsRE.MatchString(label):
		g.Kind = posting.GateExperience
	case q.Type == Choice && len(q.Options) >= 2 && placeRE.MatchString(label):
		if max(q.OptionsTotal, len(q.Options)) > maxPlaceOptions {
			return g, false
		}
		g.Kind, g.Flag = posting.GatePlace, posting.FlagPlaceList
	default:
		return g, false
	}
	if q.Type == Choice {
		g.Options, g.OptionsTotal = optionsOf(q)
	}
	return g, true
}

// offersNeed reports whether some option lets the candidate say they need
// sponsorship. For a question worded as "authorized without sponsorship"
// (authorized) a plain No is that answer; otherwise a plain Yes is.
func offersNeed(opts []string, authorized bool) bool {
	for _, o := range opts {
		l := strings.ToLower(strings.TrimSpace(o))
		switch {
		case negRE.MatchString(l):
		case needRE.MatchString(l):
			return true
		case authorized && startsWord(l, "no"), !authorized && startsWord(l, "yes"):
			return true
		}
	}
	return false
}

// startsWord reports whether s is word, or begins with it as a whole word
// ("yes, now"; not "yesterday" or "nope").
func startsWord(s, word string) bool {
	rest, ok := strings.CutPrefix(s, word)
	if !ok {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !(unicode.IsLetter(r) || unicode.IsDigit(r))
}

func onlyYesNo(opts []string) bool {
	if len(opts) == 0 {
		return false
	}
	for _, o := range opts {
		switch strings.ToLower(strings.TrimSpace(o)) {
		case "yes", "no":
		default:
			return false
		}
	}
	return true
}

// optionsOf is q's options cut to the bounds, and how many there are when that
// is more than is kept.
func optionsOf(q Question) (kept []string, total int) {
	total = max(q.OptionsTotal, len(q.Options))
	for _, o := range q.Options {
		if len(kept) == posting.MaxFormOptions {
			break
		}
		if c := clean(o, posting.MaxFormOptionRunes); c != "" {
			kept = append(kept, c)
		}
	}
	if total == len(kept) {
		total = 0
	}
	return kept, total
}

// clean makes employer text one short plain line: invalid UTF-8, control and
// format characters (bidirectional overrides, zero-width marks) are removed or
// become spaces, runs of space collapse, and the result is cut at limit runes
// with an ellipsis.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	f := strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(f) <= limit {
		return f
	}
	return string([]rune(f)[:limit-1]) + "…"
}
