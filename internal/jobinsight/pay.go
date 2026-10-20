package jobinsight

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pay is a pay range a posting's text states, as `vani jobs` shows, filters
// and sorts by it. It is read from untrusted text by fixed rules, so it is a
// display aid like the keywords: no engine rule reads it.
type Pay struct {
	// Currency is an ISO 4217 code. A bare "$" reads as USD unless a code
	// next to the amount says otherwise.
	Currency string `json:"currency"`
	// Period is what Min and Max are paid per.
	Period Period  `json:"period"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	// AnnualMin and AnnualMax are Min and Max over a year (2080 hours, 260
	// days, 52 weeks, 12 months).
	AnnualMin float64 `json:"annual_min"`
	AnnualMax float64 `json:"annual_max"`
	// Text is the span the first range was read from, cleaned and cut.
	Text string `json:"text"`
}

// Period is the unit a pay amount is stated per.
type Period string

const (
	Hour  Period = "hour"
	Day   Period = "day"
	Week  Period = "week"
	Month Period = "month"
	Year  Period = "year"
)

var perYear = map[Period]float64{Hour: 2080, Day: 260, Week: 52, Month: 12, Year: 1}

// currencyInfo bounds what a year's pay can plausibly be in a currency (so a
// $100/month stipend or a $50M round is never read as a salary) and gives the
// width of a stats bucket.
type currencyInfo struct{ lo, hi, step float64 }

var currencies = map[string]currencyInfo{
	"USD": {10_000, 2_000_000, 50_000},
	"CAD": {10_000, 2_000_000, 50_000},
	"AUD": {10_000, 2_000_000, 50_000},
	"NZD": {10_000, 2_000_000, 50_000},
	"SGD": {10_000, 2_000_000, 50_000},
	"GBP": {8_000, 1_500_000, 25_000},
	"EUR": {8_000, 1_500_000, 25_000},
	"CHF": {10_000, 2_000_000, 50_000},
	"INR": {100_000, 100_000_000, 1_000_000},
	"JPY": {1_000_000, 200_000_000, 2_000_000},
	"SEK": {80_000, 20_000_000, 200_000},
	"NOK": {80_000, 20_000_000, 200_000},
	"DKK": {80_000, 20_000_000, 200_000},
	"PLN": {20_000, 3_000_000, 50_000},
}

// PayCurrencies lists the currency codes ParsePay reads, sorted.
func PayCurrencies() []string {
	out := make([]string, 0, len(currencies))
	for c := range currencies {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// PayStep is the width of one stats bucket of annual pay in cur, or 0 for a
// currency ParsePay does not read.
func PayStep(cur string) float64 { return currencies[cur].step }

const (
	// maxPayScan bounds the text ParsePay reads; a description is capped at
	// 64 KiB by the source adapters.
	maxPayScan = 128 << 10
	// maxPayTokens bounds the numbers looked at, and maxPayWindowBytes the
	// text around currency signs the pattern runs on. A real posting states
	// pay in a few hundred bytes.
	maxPayTokens      = 4000
	maxPayWindowBytes = 8 << 10
	// MaxPays is the most ranges ParsePay returns: one per currency.
	MaxPays  = 4
	payText  = 80 // runes of Text
	before   = 64 // bytes of context read before an amount
	after    = 32 // bytes read after it for the period
	maxRatio = 6  // a range whose top is more than this times its bottom is not one
)

const codes = `USD|CAD|AUD|NZD|SGD|GBP|EUR|CHF|INR|JPY|SEK|NOK|DKK|PLN`

// moneyRE matches one amount: an optional currency before it, the number
// (thousands separated by , . or a non-breaking space; at most two decimals),
// an optional k, and an optional currency after it. It matches bare numbers
// too; an amount counts only with a currency on it or on its range partner.
var moneyRE = regexp.MustCompile(`(?i)(US\$|CA\$|C\$|AU\$|A\$|NZ\$|SG\$|S\$|\$|£|€|₹|¥|\b(?:` + codes + `)\b)?[ \x{00a0}\x{202f}]?` +
	`(\d{1,3}(?:(?:,|\.|\x{00a0}|\x{202f})\d{3})+|\d+)(?:[.,](\d{1,2}))?` +
	`(?:[ \x{00a0}]?(k)\b)?` +
	`(?:[ \x{00a0}\x{202f}]?(\b(?:` + codes + `)\b|€|£))?`)

// rangeGap is what may stand between the two ends of a range.
var rangeGap = regexp.MustCompile(`(?i)^[ \x{00a0}\x{202f}]*(?:-|–|—|~|to|and|through)[ \x{00a0}\x{202f}]*$`)

var symbols = map[string]string{
	"us$": "USD", "ca$": "CAD", "c$": "CAD", "au$": "AUD", "a$": "AUD", "nz$": "NZD", "sg$": "SGD", "s$": "SGD",
	"$": "USD", "£": "GBP", "€": "EUR", "₹": "INR", "¥": "JPY",
}

// ambiguous symbols name a family, not a currency: a code beside them wins.
var ambiguous = map[string]bool{"$": true, "¥": true}

// periodWords, matched in the text just after an amount (lowercased, with -
// and _ read as spaces). The earliest match wins.
var periodWords = []struct {
	w string
	p Period
}{
	{"/hr", Hour}, {"/hour", Hour}, {"per hour", Hour}, {"an hour", Hour}, {"hourly", Hour}, {"/h ", Hour},
	{"/yr", Year}, {"/year", Year}, {"per year", Year}, {"a year", Year}, {"annually", Year}, {"annual", Year},
	{"per annum", Year}, {"/annum", Year}, {"p.a", Year}, {"yearly", Year}, {"/y ", Year},
	{"/mo", Month}, {"/month", Month}, {"per month", Month}, {"a month", Month}, {"monthly", Month},
	{"/wk", Week}, {"/week", Week}, {"per week", Week}, {"a week", Week}, {"weekly", Week},
	{"/day", Day}, {"per day", Day}, {"a day", Day}, {"daily", Day},
}

// beforePeriod: a period word just before the amount ("Hourly rate: $50").
var beforePeriod = map[string]Period{"hourly": Hour, "annual": Year, "annually": Year, "yearly": Year, "monthly": Month, "weekly": Week, "daily": Day}

// payWords say the amount after them is pay; notPay words say it is not. The
// nearest one before an amount decides.
var payWords = map[string]bool{
	"salary": true, "salaries": true, "base": true, "pay": true, "compensation": true, "comp": true,
	"wage": true, "wages": true, "rate": true, "rates": true, "earn": true, "earnings": true, "ote": true,
	"range": true, "ranges": true, "band": true, "bands": true, "hourly": true, "annual": true, "annually": true,
	"starting": true, "between": true,
}

var notPay = map[string]bool{
	"budget": true, "budgets": true, "stipend": true, "stipends": true, "allowance": true, "bonus": true, "bonuses": true,
	"signing": true, "sign": true, "relocation": true, "raised": true, "raise": true, "funding": true, "funded": true,
	"valuation": true, "revenue": true, "arr": true, "credit": true, "credits": true, "match": true, "matching": true,
	"equity": true, "gift": true, "learning": true, "education": true, "wellness": true, "coworking": true,
	"referral": true, "award": true, "prize": true, "grant": true, "investment": true, "invested": true,
	"series": true, "spend": true, "spent": true, "savings": true, "discount": true, "insurance": true,
	"coverage": true, "reimbursement": true, "reimbursed": true, "reimburse": true, "fee": true, "fees": true,
	"customers": true, "users": true, "deductible": true, "commission": true,
}

// notPayAfter words, right after an amount, say it is not pay ("$10,000
// signing bonus"). Narrower than notPay: "$150k, plus equity" is pay.
var notPayAfter = map[string]bool{
	"bonus": true, "bonuses": true, "signing": true, "sign": true, "relocation": true, "stipend": true, "stipends": true,
	"budget": true, "budgets": true, "allowance": true, "credit": true, "credits": true, "reimbursement": true,
	"grant": true, "referral": true, "award": true, "prize": true, "raised": true, "funding": true,
	"valuation": true, "revenue": true, "arr": true, "commission": true, "variable": true, "incentive": true,
	"match": true, "matching": true, "plan": true, "contribution": true, "contributions": true, "retirement": true,
}

// payAfter words right after an amount say it is pay.
var payAfter = map[string]bool{"base": true, "salary": true, "pay": true, "wage": true, "wages": true, "compensation": true}

// magnitude words after an amount make it a large sum, not pay.
var magnitude = map[string]bool{"m": true, "mm": true, "mn": true, "mil": true, "million": true, "millions": true, "b": true, "bn": true, "billion": true, "billions": true, "t": true, "trillion": true}

type moneyTok struct {
	start, end int // the amount, from its currency (or number) to its end
	value      float64
	k          bool
	cur        string // resolved code, "" for none, "!" for a conflict
	symbolOnly bool   // cur came from an ambiguous symbol alone
	big        bool   // followed by a magnitude word or %
}

// ParsePay reads the pay ranges text states: at most one per currency (the
// envelope of every range stated in it, so a role listing several location
// bands keeps all of them), in order of first appearance, at most MaxPays. It
// reads fixed patterns only and leaves out what it cannot read: an amount
// needs a currency, a single amount needs a pay word before it, an amount
// after a word like budget, bonus or raised is not pay, and a year's pay must
// fall in the currency's plausible bounds. No period and a year-sized amount
// reads as per year.
func ParsePay(text string) []Pay {
	if len(text) > maxPayScan {
		text = text[:maxPayScan]
	}
	// The regexp runs only in windows around a currency marker: every amount
	// that counts has one on it or on its range partner within the window.
	// At most maxPayWindowBytes of windows are read, so a posting dense with
	// currency signs costs a bounded time.
	var toks []moneyTok
	read := 0
	for _, w := range payWindows(text) {
		if read >= maxPayWindowBytes {
			break
		}
		w[1] = min(w[1], w[0]+maxPayWindowBytes-read)
		read += w[1] - w[0]
		for _, m := range moneyRE.FindAllStringSubmatchIndex(text[w[0]:w[1]], maxPayTokens-len(toks)) {
			for k := range m {
				if m[k] >= 0 {
					m[k] += w[0]
				}
			}
			if t, ok := readToken(text, m); ok {
				toks = append(toks, t)
			}
		}
		if len(toks) >= maxPayTokens {
			break
		}
	}
	type acc struct {
		Pay
		periods map[Period]bool
		first   int
	}
	byCur := map[string]*acc{}
	for i := 0; i < len(toks); i++ {
		a := toks[i]
		lo, hi, last, isRange := a.value, a.value, a, false
		cur := a.cur
		if i+1 < len(toks) {
			b := toks[i+1]
			if gap := text[a.end:b.start]; rangeGap.MatchString(gap) && (!strings.Contains(strings.ToLower(gap), "and") || endsWithWord(text[max(0, a.start-before):a.start], "between")) {
				c, ok := combine(a, b)
				if !ok {
					// A range whose ends disagree on the currency: neither end
					// is read alone either.
					i++
					continue
				}
				// "$150-200k": a k on one end applies to a small other end.
				lv, hv := a.value, b.value
				if a.k != b.k {
					if !a.k && lv < 1000 {
						lv *= 1000
					}
					if !b.k && hv < 1000 {
						hv *= 1000
					}
				}
				lo, hi, last, isRange, cur = lv, hv, b, true, c
				i++
			}
		}
		if cur == "" || cur == "!" || a.big || last.big {
			continue
		}
		info, known := currencies[cur]
		if !known {
			continue
		}
		period, pend := readPeriod(text, last.end)
		if period == "" {
			period = beforeWord(text[max(0, a.start-before):a.start])
		}
		next := text[pend:min(len(text), pend+after)]
		// The words after: in the same sentence, before the next amount.
		if k := strings.IndexAny(next, ".;\n•|$£€₹¥0123456789"); k >= 0 {
			next = next[:k]
		}
		nextWords := firstWords(next, 2)
		if anyIn(nextWords, notPayAfter) {
			continue
		}
		// The nearest pay or not-pay word before decides, unless a pay word
		// right after names it ("equity plus $150,000 - $180,000 base").
		decided := nearestWord(text[max(0, a.start-before):a.start])
		if anyIn(nextWords, payAfter) {
			decided = "pay"
		}
		if decided == "not" || (!isRange && decided != "pay") {
			continue
		}
		if period == "" {
			if hi < info.lo {
				continue
			}
			period = Year
		}
		amin, amax := lo*perYear[period], hi*perYear[period]
		if lo <= 0 || lo > hi || hi > lo*maxRatio || amin < info.lo/2 || amax > info.hi || amax < info.lo {
			continue
		}
		p := byCur[cur]
		if p == nil {
			if len(byCur) >= MaxPays {
				continue
			}
			p = &acc{Pay: Pay{Currency: cur, Min: lo, Max: hi, AnnualMin: amin, AnnualMax: amax,
				Text: cleanSpan(text[a.start:pend])}, periods: map[Period]bool{}, first: a.start}
			byCur[cur] = p
		} else {
			// Another range in the currency widens the envelope (location
			// bands) only while the whole stays a plausible range; one far
			// off (an intern band, a variable-pay range) leaves the first.
			nmin, nmax := math.Min(p.AnnualMin, amin), math.Max(p.AnnualMax, amax)
			if nmax > nmin*maxRatio/2 {
				continue
			}
			p.Min, p.Max = math.Min(p.Min, lo), math.Max(p.Max, hi)
			p.AnnualMin, p.AnnualMax = nmin, nmax
		}
		p.periods[period] = true
		p.Period = period
	}
	out := make([]Pay, 0, len(byCur))
	for _, p := range byCur {
		if len(p.periods) > 1 {
			// Ranges in different units: the envelope is only meaningful per year.
			p.Period, p.Min, p.Max = Year, p.AnnualMin, p.AnnualMax
		}
		out = append(out, p.Pay)
	}
	sort.Slice(out, func(i, j int) bool { return byCur[out[i].Currency].first < byCur[out[j].Currency].first })
	if len(out) == 0 {
		return nil
	}
	return out
}

// payWindow is how far around a currency marker amounts are looked for: far
// enough for "150.000 – 180.000 €" before one and "$150,000 - 180,000" after.
const payWindow = 48

// payWindows returns the merged [start, end) byte ranges around each currency
// marker ($, £, ¥, €, ₹, or a currency code as a word), widened so no number
// is cut at an edge. A byte scan, so text without a marker costs one pass.
func payWindows(text string) [][2]int {
	var out [][2]int
	for i := 0; i < len(text); i++ {
		c := text[i]
		hit := false
		switch {
		case c == '$':
			hit = true
		case c == 0xC2 && i+1 < len(text) && (text[i+1] == 0xA3 || text[i+1] == 0xA5): // £ ¥
			hit = true
		case c == 0xE2 && i+2 < len(text) && text[i+1] == 0x82 && (text[i+2] == 0xAC || text[i+2] == 0xB9): // € ₹
			hit = true
		case isLetterByte(c) && i+3 <= len(text) && (i == 0 || !isWordByte(text[i-1])) && (i+3 == len(text) || !isWordByte(text[i+3])):
			code := [3]byte{c &^ 0x20, text[i+1] &^ 0x20, text[i+2] &^ 0x20}
			_, hit = currencies[string(code[:])]
		}
		if !hit {
			continue
		}
		lo, hi := max(0, i-payWindow), min(len(text), i+payWindow)
		for lo > 0 && isNumByte(text[lo-1]) {
			lo--
		}
		for hi < len(text) && (isNumByte(text[hi]) || isLetterByte(text[hi])) {
			hi++
		}
		if n := len(out); n > 0 && lo <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], hi)
		} else {
			out = append(out, [2]int{lo, hi})
		}
	}
	return out
}

func isLetterByte(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isWordByte(c byte) bool   { return isLetterByte(c) || (c >= '0' && c <= '9') || c == '_' }
func isNumByte(c byte) bool    { return (c >= '0' && c <= '9') || c == ',' || c == '.' }

var periodSuffix = map[Period]string{Hour: "/hr", Day: "/day", Week: "/wk", Month: "/mo", Year: "/yr"}

// Short is p in one short line for text output: "USD 150k–180k/yr", or
// "USD 70/hr" when the range is one amount.
func (p Pay) Short() string {
	amount := ShortMoney(p.Min)
	if p.Max != p.Min {
		amount += "–" + ShortMoney(p.Max)
	}
	return p.Currency + " " + amount + periodSuffix[p.Period]
}

// ShortMoney writes 150000 as 150k and 2500000 as 2.5M.
func ShortMoney(v float64) string {
	switch {
	case v >= 1e6:
		return strconv.FormatFloat(math.Round(v/1e5)/10, 'f', -1, 64) + "M"
	case v >= 1e3:
		return strconv.FormatFloat(math.Round(v/1e2)/10, 'f', -1, 64) + "k"
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// PayIn returns the range pays states in cur.
func PayIn(pays []Pay, cur string) (Pay, bool) {
	for _, p := range pays {
		if p.Currency == cur {
			return p, true
		}
	}
	return Pay{}, false
}

func readToken(text string, m []int) (moneyTok, bool) {
	// m: 0,1 whole; 2,3 prefix; 4,5 number; 6,7 decimals; 8,9 k; 10,11 suffix.
	numS, numE := m[4], m[5]
	t := moneyTok{start: numS, end: m[1]}
	if m[2] >= 0 {
		t.start = m[2]
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, text[numS:numE])
	if len(digits) > 12 || (numE < len(text) && text[numE] >= '0' && text[numE] <= '9') {
		// Too long, or a digit run the thousands pattern only partly read
		// ("12.3456").
		return moneyTok{}, false
	}
	v := 0.0
	for _, d := range digits {
		v = v*10 + float64(d-'0')
	}
	if m[6] >= 0 {
		if m[7] < len(text) && text[m[7]] >= '0' && text[m[7]] <= '9' {
			return moneyTok{}, false // "12.3456": not an amount with two decimals
		}
		dec := text[m[6]:m[7]]
		f := 0.0
		for i, d := range dec {
			f += float64(d-'0') / math.Pow(10, float64(i+1))
		}
		v += f
	}
	t.value = v
	t.k = m[8] >= 0
	if t.k {
		t.value *= 1000
	}
	var pre, suf string
	if m[2] >= 0 {
		pre = strings.ToLower(text[m[2]:m[3]])
	}
	if m[10] >= 0 {
		suf = strings.ToLower(text[m[10]:m[11]])
	}
	t.cur, t.symbolOnly = resolve(pre, suf)
	// A word right after the amount ("$50M", "$2 billion") or a % makes it
	// a sum or a share, not pay.
	rest := strings.TrimPrefix(text[m[1]:min(len(text), m[1]+16)], " ")
	n := 0
	for n < len(rest) && ((rest[n] >= 'a' && rest[n] <= 'z') || (rest[n] >= 'A' && rest[n] <= 'Z')) {
		n++
	}
	t.big = strings.HasPrefix(rest, "%") || (n > 0 && magnitude[strings.ToLower(rest[:n])])
	return t, true
}

// resolve picks a moneyTok's currency from what stands before and after it.
func resolve(pre, suf string) (cur string, symbolOnly bool) {
	code := func(s string) (string, bool) {
		if s == "" {
			return "", false
		}
		if c, ok := symbols[s]; ok {
			return c, ambiguous[s]
		}
		return strings.ToUpper(s), false
	}
	a, aAmb := code(pre)
	b, bAmb := code(suf)
	switch {
	case a == "":
		return b, bAmb
	case b == "":
		return a, aAmb
	case a == b:
		return a, aAmb && bAmb
	case aAmb && !bAmb:
		return b, false
	case bAmb && !aAmb:
		return a, false
	}
	return "!", false
}

// combine picks a range's currency from its two ends, by resolve's rule; at
// least one end must carry one.
func combine(a, b moneyTok) (string, bool) {
	switch {
	case a.cur == "!" || b.cur == "!":
		return "", false
	case a.cur == "":
		return b.cur, b.cur != ""
	case b.cur == "" || a.cur == b.cur:
		return a.cur, true
	case a.symbolOnly && !b.symbolOnly:
		return b.cur, true
	case b.symbolOnly && !a.symbolOnly:
		return a.cur, true
	}
	return "", false
}

// readPeriod finds the period stated just after end, and returns the offset
// past it (end when none).
func readPeriod(text string, end int) (Period, int) {
	win := text[end:min(len(text), end+after)]
	// ASCII-only lowering keeps byte offsets; the period words are ASCII.
	b := []byte(win)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + 'a' - 'A'
		case c == '-' || c == '_':
			b[i] = ' '
		}
	}
	low := string(b) + " "
	best, bestAt, bestEnd := Period(""), len(low), end
	for _, pw := range periodWords {
		i := strings.Index(low, pw.w)
		if i < 0 || i >= bestAt || !fillerOnly(low[:i]) {
			continue
		}
		best, bestAt, bestEnd = pw.p, i, end+min(i+len(strings.TrimRight(pw.w, " ")), len(win))
	}
	return best, bestEnd
}

// fillerOnly: the period word may follow a currency code or a word like
// "base", but no other words ("$100 to spend each year" is not per year).
func fillerOnly(s string) bool {
	for _, w := range strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return ' '
	}, s)) {
		if _, ok := currencies[strings.ToUpper(w)]; !ok && w != "base" && w != "salary" && w != "gross" {
			return false
		}
	}
	return true
}

// words returns s's letter words, lowercased.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.ToValidUTF8(s, " ")), func(r rune) bool { return !unicode.IsLetter(r) })
}

func firstWords(s string, n int) []string {
	w := words(s)
	if len(w) > n {
		w = w[:n]
	}
	return w
}

func anyIn(ws []string, set map[string]bool) bool {
	for _, w := range ws {
		if set[w] {
			return true
		}
	}
	return false
}

// nearestWord is "pay" or "not" for the pay or not-pay word nearest the end
// of s, or "" for neither.
func nearestWord(s string) string {
	w := words(s)
	for i := len(w) - 1; i >= 0; i-- {
		switch {
		case notPay[w[i]]:
			return "not"
		case payWords[w[i]]:
			return "pay"
		}
	}
	return ""
}

func beforeWord(s string) Period {
	w := words(s)
	for i := len(w) - 1; i >= 0 && i >= len(w)-4; i-- {
		if p, ok := beforePeriod[w[i]]; ok {
			return p
		}
	}
	return ""
}

func endsWithWord(s, word string) bool {
	w := words(s)
	return len(w) > 0 && w[len(w)-1] == word
}

// cleanSpan makes a span of untrusted text one short plain line.
func cleanSpan(s string) string {
	s = strings.Join(strings.FieldsFunc(strings.ToValidUTF8(s, "�"), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if utf8.RuneCountInString(s) > payText {
		r := []rune(s)
		s = string(r[:payText]) + "…"
	}
	return strings.TrimRight(s, " ,;(")
}
