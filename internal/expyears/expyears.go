// Package expyears reads how many years of experience a job description asks
// for, and owns the one rule built on it: a staff-level reading that the
// description's own figures contradict. Some companies put "Staff" in every
// title (Perplexity's "Member of Data Staff", any "Member of Technical Staff"),
// and some "Staff" postings ask for 5 years, so neither a title nor a model's
// reading of one is trusted over the years the posting states.
//
// The text is untrusted posting content: it is only scanned, bounded at
// MaxScanBytes, with RE2 (linear time).
package expyears

import (
	"regexp"
	"strconv"
)

const (
	// MaxScanBytes bounds how much of a description is scanned. Requirements
	// sit well inside it; a longer body is cut, never rejected.
	MaxScanBytes = 64 << 10

	// maxYears is the largest figure read as a requirement; past it a figure
	// is history ("50 years of research"), not an ask.
	maxYears = 40

	// StaffMinYears is the fewest stated years a staff-level role asks for.
	// A staff reading on a posting whose largest figure is below it is read at
	// the level its years imply (seniority's own scale: senior is "roughly 5
	// or more years", mid "roughly 2 to 5"; internal/decide/jdtriage).
	StaffMinYears  = 7
	seniorMinYears = 5

	// Seniority values, as the career.jd_triage seniority slot names them.
	ValueStaff  = "staff_plus"
	ValueSenior = "senior"
	ValueMid    = "mid"
)

// figure is "N years", "N+ years", "N-M years", "N to M+ yrs", "N plus years".
// The digits must not be glued to a letter or digit before them, and three
// digits are no figure. Group 1 is N, the lower bound.
var figure = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(\d{1,2})\s*(?:\+|plus\b)?\s*(?:(?:-|–|—|\bto\b)\s*\d{1,2}\s*\+?\s*)?(?:years?|yrs?)\b`)

// before and after mark a figure that is a date or an age, not an ask.
var (
	before = regexp.MustCompile(`(?i)\b(?:within|past|last|next|over the|in the|for the)\s*$`)
	after  = regexp.MustCompile(`(?i)^\s+(?:ago|old)\b`)
)

// Required returns the most years of experience text asks for: the largest
// lower bound among its year figures ("2 to 10+ years" asks for 2). ok is
// false when it states none.
func Required(text string) (years int, ok bool) {
	if len(text) > MaxScanBytes {
		text = text[:MaxScanBytes]
	}
	for _, m := range figure.FindAllStringSubmatchIndex(text, -1) {
		n, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || n <= 0 || n > maxYears {
			continue
		}
		if before.MatchString(text[max(0, m[2]-16):m[2]]) {
			continue
		}
		if after.MatchString(text[m[1]:]) {
			continue
		}
		if !ok || n > years {
			years, ok = n, true
		}
	}
	return years, ok
}

// BelowStaff reports the seniority text's stated years imply when they are
// below StaffMinYears: senior from 5 years, mid under that (a stated figure
// never makes a role a new-grad one). ok is false when the text states no
// figure, or one at staff level: the staff reading then stands.
func BelowStaff(text string) (level string, ok bool) {
	y, stated := Required(text)
	if !stated || y >= StaffMinYears {
		return "", false
	}
	if y >= seniorMinYears {
		return ValueSenior, true
	}
	return ValueMid, true
}
