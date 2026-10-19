package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxTitleSignals bounds each of include and exclude.
	MaxTitleSignals = 64
	// MaxTitleSignalLen bounds one signal, in bytes.
	MaxTitleSignalLen = 100
)

// TitleSignals are title words and phrases for and against the active track,
// copied from its decider by `vani track use`. They order the postings a run
// triages; they never drop one or change its status, so a false positive
// ("Performance Management" for "Performance") only moves a posting earlier.
type TitleSignals struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
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
}

// String lists the signals for `vani track use` and `vani config check`.
func (s TitleSignals) String() string {
	return fmt.Sprintf("include %q, exclude %q", s.Include, s.Exclude)
}
