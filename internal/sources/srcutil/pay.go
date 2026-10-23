package srcutil

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Adapters put lines of their own making ahead of a board's description text,
// written by Lead: pay first, then the role line. Each starts with one of
// these prefixes, and SourceText removes them.
const (
	// PayLinePrefix starts every line PayLine writes.
	PayLinePrefix = "Pay range: "
	// CompensationPrefix starts a line carrying a board's free-text pay
	// summary (Ashby's salary or tier summary), written by CompensationLine.
	CompensationPrefix = "Compensation: "
	// RolePrefix starts the line RoleLine writes: the board's structured
	// role facts (employment type, schedule, work site, clearance).
	RolePrefix = "Role: "
)

// Bounds on a role line: a board's flags are a handful of short values.
const (
	maxRoleFlags    = 8
	maxRoleFlagLen  = 80
	maxLeadLinesLen = 1 << 10
)

// RoleLine renders a board's structured role facts as one line, "Role:
// full-time, security clearance", or "" when none is left. Each flag is
// trimmed with its whitespace collapsed; empty, over-long and repeated
// (case-insensitively) flags are dropped, and at most maxRoleFlags are kept.
// The flags are untrusted board text, kept as plain text.
//
// SourceText removes the line, as it does pay lines: the decision model
// still reads it, but starting to write it, or writing it differently, is a
// re-render, not a re-triage. The cost is that a board changing only a role
// field (full-time to temporary) re-renders a stored posting rather than
// re-triaging it; a real change of terms almost always edits the text too.
// The line was in the board text for Eightfold and Amazon until 2026-10-20,
// when the ledger held no postings, so moving it cost no re-triage.
func RoleLine(flags ...string) string {
	var out []string
	for _, f := range flags {
		f = strings.Join(strings.Fields(f), " ")
		if f == "" || len(f) > maxRoleFlagLen || len(out) == maxRoleFlags ||
			slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, f) }) {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return ""
	}
	return RolePrefix + strings.Join(out, ", ")
}

// EmploymentType is a board's employment type in one wording across boards
// ("full-time", "part-time", "temporary", "contract", "internship"), so the
// decision model and the Jobs list see the same word whichever board said
// it. Seen live on 2026-10-20: Jibe's FULL_TIME, PART_TIME, TEMPORARY and
// CONTRACTOR, Workday's "Full time" and "Part time", Eightfold's "Full-Time"
// and Amazon's "full-time". Anything else is returned trimmed, as written.
func EmploymentType(raw string) string {
	k := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, raw)
	switch k {
	case "fulltime":
		return "full-time"
	case "parttime":
		return "part-time"
	case "temporary", "temp":
		return "temporary"
	case "contractor", "contract":
		return "contract"
	case "intern", "internship":
		return "internship"
	}
	return strings.Join(strings.Fields(raw), " ")
}

// Lead is the synthesized lines that open a description (pay lines first,
// then the role line; empty ones skipped), each on its own line and the run
// ended by a blank line, and room, what is left of MaxDescription for the
// board's text after them. The lines together are capped at
// maxLeadLinesLen bytes, dropping whole lines from the end, so the board's
// text always keeps most of its room and the cap never cuts a lead line.
func Lead(lines ...string) (lead string, room int) {
	var kept []string
	n := 0
	for _, l := range lines {
		if l = strings.TrimSpace(l); l == "" || strings.Contains(l, "\n") {
			continue
		}
		if n+len(l)+1 > maxLeadLinesLen {
			break
		}
		kept = append(kept, l)
		n += len(l) + 1
	}
	if len(kept) == 0 {
		return "", MaxDescription
	}
	lead = strings.Join(kept, "\n") + "\n\n"
	return lead, MaxDescription - len(lead)
}

// CompensationLine renders a board's free-text pay summary as one line, or ""
// for a blank summary.
func CompensationLine(summary string) string {
	if s := strings.Join(strings.Fields(summary), " "); s != "" {
		return CompensationPrefix + s
	}
	return ""
}

// SourceText is desc without the lines an adapter synthesized ahead of the
// board's own text: the leading run of PayLine, CompensationLine and RoleLine
// lines, in any order, and the blank lines among them. Only that leading run
// is removed; such text anywhere after the first board line is the board's
// own and stays. It is what change detection compares, so a posting whose
// board text is unchanged is not re-judged when Vani starts, stops or changes
// rendering pay or role facts. A board whose own text opens with such a line
// loses that line from the comparison too: an edit confined to it is read as
// a re-render.
func SourceText(desc string) string {
	rest := desc
	for rest != "" {
		line, after, _ := strings.Cut(rest, "\n")
		l := strings.TrimSpace(line)
		if l != "" && !strings.HasPrefix(l, PayLinePrefix) && !strings.HasPrefix(l, CompensationPrefix) && !strings.HasPrefix(l, RolePrefix) {
			break
		}
		rest = after
	}
	return rest
}

// PayLine renders a board's structured pay range (Lever's salaryRange,
// Ashby's salary component) as one plain line, "Pay range: USD 120000 –
// 150000 per year", that jobinsight.ParsePay reads back. Adapters put it at
// the top of the description, so the description cap never cuts it. It is
// empty when the range is unusable: no three-letter currency, no positive
// finite amount, or a bottom above the top. interval is the board's own
// word; one that names no known period is left out (ParsePay then reads a
// year-sized amount as per year).
func PayLine(currency string, lo, hi *float64, interval string) string {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if len(cur) != 3 || strings.IndexFunc(cur, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return ""
	}
	ok := func(v *float64) bool { return v != nil && *v > 0 && !math.IsInf(*v, 0) && !math.IsNaN(*v) }
	switch {
	case !ok(lo) && !ok(hi):
		return ""
	case !ok(lo):
		lo = hi
	case !ok(hi):
		hi = lo
	}
	if *lo > *hi {
		return ""
	}
	num := func(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }
	line := PayLinePrefix + cur + " " + num(*lo)
	if *hi != *lo {
		line += " – " + num(*hi)
	}
	if p := payPeriod(interval); p != "" {
		line += " per " + p
	}
	return line
}

func payPeriod(interval string) string {
	s := strings.ToLower(interval)
	switch {
	case strings.Contains(s, "hour"):
		return "hour"
	case strings.Contains(s, "year"), strings.Contains(s, "annual"):
		return "year"
	case strings.Contains(s, "month"):
		return "month"
	case strings.Contains(s, "week"):
		return "week"
	case strings.Contains(s, "day"), strings.Contains(s, "daily"):
		return "day"
	}
	return ""
}
