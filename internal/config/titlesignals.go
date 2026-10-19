package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
)

const (
	// MaxTitleSignals bounds each of include and exclude, the read entries,
	// and each entry's match and unless lists.
	MaxTitleSignals = 64
	// MaxTitleSignalLen bounds one signal, in bytes.
	MaxTitleSignalLen = 100
)

// TitleSignals are title words and phrases for and against the active track,
// copied from its decider by `vani track use`. Include and Exclude order the
// postings a run triages; they never drop one or change its status, so a
// false positive ("Performance Management" for "Performance") only moves a
// posting earlier. Read entries read triage slot values from the title alone;
// a reading drops a posting only through a drop rule for that slot and value.
type TitleSignals struct {
	Include []string    `json:"include,omitempty"`
	Exclude []string    `json:"exclude,omitempty"`
	Read    []TitleRead `json:"read,omitempty"`
}

// TitleRead reads Slot as Value from a title that contains any Match phrase
// and no Unless phrase. Unless is an abstention, never another value: a title
// it matches is left for the decision model to read.
type TitleRead struct {
	Slot   string   `json:"slot"`
	Value  string   `json:"value"`
	Match  []string `json:"match"`
	Unless []string `json:"unless,omitempty"`
}

// TitleReading is one slot value read from a title, with the phrase that
// matched.
type TitleReading struct {
	Slot, Value, Matched string
}

// titleReadSlotRefused is never read from a title: it is the slot that
// encodes the owner's work authorization, and it stays a main-config,
// model-read slot.
const titleReadSlotRefused = "sponsorship"

// Reads returns the slot values the Read entries find in title, at most one
// per slot: the first entry, in order, that reads a slot wins it. Matching is
// Rank's: case and whitespace runs are ignored, and phrases match whole words.
// An empty title reads nothing.
func (s TitleSignals) Reads(title string) []TitleReading {
	t := fold(title)
	if t == "" {
		return nil
	}
	var out []TitleReading
	for _, r := range s.Read {
		if slices.ContainsFunc(out, func(x TitleReading) bool { return x.Slot == r.Slot }) {
			continue
		}
		m := firstWord(t, r.Match)
		if m == "" || firstWord(t, r.Unless) != "" {
			continue
		}
		out = append(out, TitleReading{Slot: r.Slot, Value: r.Value, Matched: m})
	}
	return out
}

// firstWord returns the first phrase that occurs in the folded title t as
// whole words, or "".
func firstWord(t string, phrases []string) string {
	for _, p := range phrases {
		if containsWord(t, fold(p)) {
			return p
		}
	}
	return ""
}

// Rank scores a title for triage order: -1 when any exclude signal matches,
// else the number of distinct include signals that match. Matching ignores
// case and runs of whitespace, and a signal matches only as whole words: a
// letter or digit on either side of it is no match, so "Agent" does not match
// "Agents" and "Recruit" does not match "Recruiter".
func (s TitleSignals) Rank(title string) int {
	t := fold(title)
	if t == "" {
		return 0
	}
	for _, x := range s.Exclude {
		if containsWord(t, fold(x)) {
			return -1
		}
	}
	n := 0
	seen := map[string]bool{}
	for _, in := range s.Include {
		w := fold(in)
		if !seen[w] && containsWord(t, w) {
			n++
		}
		seen[w] = true
	}
	return n
}

// fold lower-cases s and collapses each run of whitespace to one space.
func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// containsWord reports whether w occurs in t with no letter or digit
// immediately before or after it. Both are folded.
func containsWord(t, w string) bool {
	if w == "" {
		return false
	}
	for from := 0; from <= len(t)-len(w); {
		i := strings.Index(t[from:], w)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(w)
		before, _ := utf8.DecodeLastRuneInString(t[:i])
		after, _ := utf8.DecodeRuneInString(t[end:])
		if (i == 0 || !wordRune(before)) && (end == len(t) || !wordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(t[i:])
		from = i + size
	}
	return false
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// validate reports each bad list or entry through add.
func (s TitleSignals) validate(add func(string, ...any)) {
	for name, l := range map[string][]string{"title_signals.include": s.Include, "title_signals.exclude": s.Exclude} {
		validPhrases(name, l, add)
	}
	for _, p := range s.ReadProblems() {
		add("%s", p)
	}
}

// validPhrases checks one phrase list's length and entries.
func validPhrases(name string, l []string, add func(string, ...any)) {
	if len(l) > MaxTitleSignals {
		add("%s has %d entries; at most %d", name, len(l), MaxTitleSignals)
	}
	for i, v := range l {
		switch {
		case strings.TrimSpace(v) == "":
			add("%s[%d] is empty", name, i)
		case len(v) > MaxTitleSignalLen:
			add("%s[%d] is over %d bytes", name, i, MaxTitleSignalLen)
		case !utf8.ValidString(v):
			add("%s[%d] is not valid UTF-8", name, i)
		}
	}
}

// ReadProblems checks the Read entries: each names a triage slot other than
// sponsorship and one of that slot's values, and has at least one match
// phrase; the phrase lists have the include/exclude bounds. The config and
// `vani track check` both use it.
func (s TitleSignals) ReadProblems() []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	if len(s.Read) > MaxTitleSignals {
		add("title_signals.read has %d entries; at most %d", len(s.Read), MaxTitleSignals)
	}
	for i, r := range s.Read {
		name := fmt.Sprintf("title_signals.read[%d]", i)
		legal, ok := triageSlotValues(r.Slot)
		switch {
		case r.Slot == titleReadSlotRefused:
			add("%s: %s is never read from a title", name, r.Slot)
		case !ok:
			add("%s: slot %q is not a triage slot", name, r.Slot)
		case !slices.Contains(legal, r.Value):
			add("%s: %q is not a %s value", name, r.Value, r.Slot)
		}
		if len(r.Match) == 0 {
			add("%s: match is empty", name)
		}
		validPhrases(name+".match", r.Match, add)
		validPhrases(name+".unless", r.Unless, add)
	}
	return probs
}

// triageSlotValues returns the legal values of a career.jd_triage slot.
func triageSlotValues(slot string) ([]string, bool) {
	for _, s := range jdtriage.Slots {
		if s.Name == slot {
			vs := make([]string, len(s.Options))
			for i, o := range s.Options {
				vs[i] = o.Value
			}
			return vs, true
		}
	}
	return nil, false
}

// Clone returns a deep copy, so a copied config shares no slice with s.
func (s TitleSignals) Clone() TitleSignals {
	out := TitleSignals{Include: slices.Clone(s.Include), Exclude: slices.Clone(s.Exclude)}
	if s.Read != nil {
		out.Read = make([]TitleRead, len(s.Read))
		for i, r := range s.Read {
			out.Read[i] = TitleRead{Slot: r.Slot, Value: r.Value, Match: slices.Clone(r.Match), Unless: slices.Clone(r.Unless)}
		}
	}
	return out
}

// String lists the signals for `vani track use` and `vani config check`.
func (s TitleSignals) String() string {
	reads := make([]string, len(s.Read))
	for i, r := range s.Read {
		reads[i] = fmt.Sprintf("%s=%s on %q unless %q", r.Slot, r.Value, r.Match, r.Unless)
	}
	return fmt.Sprintf("include %q, exclude %q, read %v", s.Include, s.Exclude, reads)
}
