package srcutil

import (
	"math"
	"strconv"
	"strings"
)

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
	line := "Pay range: " + cur + " " + num(*lo)
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
