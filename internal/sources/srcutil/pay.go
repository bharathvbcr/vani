package srcutil

import (
	"math"
	"strconv"
	"strings"
)

// Adapters put lines of their own making ahead of a board's description text.
// Each starts with one of these prefixes, and SourceText removes them.
const (
	// PayLinePrefix starts every line PayLine writes.
	PayLinePrefix = "Pay range: "
	// CompensationPrefix starts a line carrying a board's free-text pay
	// summary (Ashby's salary or tier summary), written by CompensationLine.
	CompensationPrefix = "Compensation: "
)

// CompensationLine renders a board's free-text pay summary as one line, or ""
// for a blank summary.
func CompensationLine(summary string) string {
	if s := strings.Join(strings.Fields(summary), " "); s != "" {
		return CompensationPrefix + s
	}
	return ""
}

// SourceText is desc without the lines an adapter synthesized ahead of the
// board's own text: the leading run of PayLine and CompensationLine lines and
// the blank lines among them. Only that leading run is removed; pay-shaped
// text anywhere after the first board line is the board's own and stays. It
// is what change detection compares, so a posting whose board text is
// unchanged is not re-judged when Vani starts, stops or changes rendering
// pay. A board whose own text opens with such a line loses that line from the
// comparison too: an edit confined to it is read as a re-render.
func SourceText(desc string) string {
	rest := desc
	for rest != "" {
		line, after, _ := strings.Cut(rest, "\n")
		l := strings.TrimSpace(line)
		if l != "" && !strings.HasPrefix(l, PayLinePrefix) && !strings.HasPrefix(l, CompensationPrefix) {
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
