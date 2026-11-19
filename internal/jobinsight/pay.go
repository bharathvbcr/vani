package jobinsight

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/posting"
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
	// About USD 10k–2M a year at 2026 rates, like the rest; each holds every
	// nonzero AMD range in it (2026-10-20: TWD 430k–5.35M, MYR 37.8k–445k,
	// CNY 156k–1.24M, KRW 80.6M–198M, RON 85k–320k, RSD 2.98M–7.87M, SAR
	// 317k–543k). The Armenian dram is left out: its code is AMD, the
	// company's name.
	"TWD": {200_000, 64_000_000, 500_000},
	"MYR": {20_000, 9_000_000, 50_000},
	"CNY": {40_000, 15_000_000, 100_000},
	"KRW": {10_000_000, 3_000_000_000, 10_000_000},
	"RON": {20_000, 9_000_000, 25_000},
	"RSD": {500_000, 216_000_000, 500_000},
	"SAR": {20_000, 7_500_000, 50_000},
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

const codes = `USD|CAD|AUD|NZD|SGD|GBP|EUR|CHF|INR|JPY|SEK|NOK|DKK|PLN|TWD|MYR|CNY|KRW|RON|RSD|SAR`

// moneyRE matches one amount: an optional currency before it (a symbol, a
// code, or a code then a symbol, "CAD $"; a letter-prefixed dollar the symbols
// table does not name, "NT$", "HK$", "R$", "MX$", reads as a conflict, never as
// USD), the number (thousands separated by
// , . or a non-breaking space; at most two decimals), an optional k, and an
// optional currency after it. It matches bare numbers too; an amount counts
// only with a currency on it or on its range partner.
var moneyRE = regexp.MustCompile(`(?i)((?:\b(?:` + codes + `)[ \x{00a0}\x{202f}]?)?(?:US\$|CA\$|C\$|AU\$|A\$|NZ\$|SG\$|S\$|\b[a-z]{1,3}\$|\$|£|€|₹|¥)|\b(?:` + codes + `)\b)?[ \x{00a0}\x{202f}]?` +
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
// and _ read as spaces). The earliest match wins. A match in the amount's own
// sentence on its own line is the amount's period; one past a sentence end or
// a line break ("$50,000 - $60,000. Hourly staff ...") is only a fallback for
// a range that is not plausible without it (see parsePay).
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

// genericPay are the payWords that are range nouns or connectives, not salary
// words: a not-pay word right before one in the same phrase modifies it ("bonus
// range", "equity compensation", "bonus between"). The rest of payWords
// (salary, base, pay, wage, earn, ote, hourly, annual and their forms) name
// pay itself, so "Equity Salary: $150,000" is still a salary.
var genericPay = map[string]bool{
	"compensation": true, "comp": true, "rate": true, "rates": true, "earnings": true,
	"range": true, "ranges": true, "band": true, "bands": true, "starting": true, "between": true,
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
func ParsePay(text string) []Pay { return parsePay(text, "") }

// PostingPay is the pay p's description states, read as ParsePay reads it
// except that a bare "$" is the dollar of the one country p's location names
// (Canada, Australia, New Zealand, Singapore) instead of USD. A code on the
// amount or its range partner still decides. Every caller with a posting
// reads its pay here, so the digest, the Jobs list and its filters agree.
func PostingPay(p posting.Posting) []Pay {
	return parsePay(p.Description, dollarOf(location.Parse(p.Location, p.Remote)))
}

// dollars maps a country, as the last part of a place's region or a remote
// scope names it, to its dollar. The US is listed so a location naming it
// and another country gets no hint.
var dollars = map[string]string{
	"canada": "CAD", "australia": "AUD", "new zealand": "NZD", "singapore": "SGD",
	"us": "USD", "usa": "USD", "u s": "USD", "u s a": "USD", "united states": "USD", "united states of america": "USD",
}

// dollarOf is the currency a bare "$" means at l: a non-US dollar when every
// place and the remote scope name the same such country, else "" (USD).
// A place's country is read only where the board wrote it ("Markham,
// Ontario, Canada", "Singapore"); a province alone ("Markham, ON") or a bare
// city names none, and a location with any such place gets no hint.
func dollarOf(l location.Location) string {
	cur := ""
	same := func(name string) bool {
		c := dollars[strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
			return !unicode.IsLetter(r)
		}), " ")]
		if c == "" || (cur != "" && c != cur) {
			return false
		}
		cur = c
		return true
	}
	for _, p := range l.Places {
		switch {
		case p.State != "":
			return "" // a US state
		case p.Region != "":
			parts := strings.Split(p.Region, ",")
			if !same(parts[len(parts)-1]) {
				return ""
			}
		case !same(p.Area):
			return ""
		}
	}
	if l.Remote {
		if l.RemoteScope == "" && len(l.Places) == 0 {
			return ""
		}
		for _, s := range strings.Split(l.RemoteScope, ",") {
			if strings.TrimSpace(s) != "" && !same(s) {
				return ""
			}
		}
	}
	if cur == "USD" {
		return ""
	}
	return cur
}

func parsePay(text, dollar string) []Pay {
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
	if dollar != "" {
		for i := range toks {
			// Only a bare "$" (US$ and C$ are not symbol-only); combine still
			// prefers a range partner's code.
			if toks[i].symbolOnly && toks[i].cur == "USD" {
				toks[i].cur = dollar
			}
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
		period, pend, far := readPeriod(text, last.end)
		if period == "" {
			var farBefore Period
			period, farBefore = beforeWord(text[max(0, a.start-before):a.start])
			if farBefore != "" {
				far = append(far, periodAt{farBefore, pend})
			}
		}
		if period == "" && !plausible(info, lo, hi, Year) {
			// A period word past a sentence end or line break names this
			// range only when the range is not plausible without it
			// ("Pay: $50 - $60 (USD). Per hour"; "$150,000 - $180,000.
			// Hourly staff ..." stays per year).
			for _, f := range far {
				if plausible(info, lo, hi, f.p) {
					period, pend = f.p, f.end
					break
				}
			}
		}
		next := text[pend:min(len(text), pend+after)]
		// The words after: in the same sentence, before the next amount.
		if k := strings.IndexAny(next, ".;"+lineBreaks+"•|$£€₹¥0123456789"); k >= 0 {
			next = next[:k]
		}
		nextWords := firstWords(next, 2)
		if anyIn(nextWords, notPayAfter) {
			continue
		}
		// The nearest pay or not-pay word before decides, unless a pay word
		// right after names it ("equity plus $150,000 - $180,000 base").
		win := text[max(0, a.start-before):a.start]
		decided := nearestWord(win)
		if decided == "" && earlierPay(win) {
			// A finished sentence before can still name the amounts after
			// it as pay ("The base salary is below.\n$150,000"), but a
			// not-pay word there no longer vetoes them.
			decided = "pay"
		}
		if anyIn(nextWords, payAfter) {
			decided = "pay"
		}
		if decided == "not" || (!isRange && decided != "pay") {
			continue
		}
		if period == "" {
			period = Year
		}
		if !plausible(info, lo, hi, period) {
			continue
		}
		amin, amax := lo*perYear[period], hi*perYear[period]
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

// plausible reports whether lo..hi per period is a believable range of pay in
// the currency info describes.
func plausible(info currencyInfo, lo, hi float64, period Period) bool {
	amin, amax := lo*perYear[period], hi*perYear[period]
	return lo > 0 && lo <= hi && hi <= lo*maxRatio && amin >= info.lo/2 && amax <= info.hi && amax >= info.lo
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

// letterRun is the length of the ASCII letters s starts with.
func letterRun(s string) int {
	n := 0
	for n < len(s) && isLetterByte(s[n]) {
		n++
	}
	return n
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
	if m[2] >= 0 && plainDollarPrefix(text[m[2]:m[3]]) {
		// "to$180,000": an all-lowercase unknown prefix is ordinary text and
		// the amount is a plain "$" one.
		pre = "$"
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

// glued are the short English words that land before a "$" with the space
// lost ("up to$180,000", "is$150k"): such a prefix is no currency and the
// amount is a plain "$" one.
var glued = map[string]bool{
	"to": true, "is": true, "up": true, "at": true, "of": true, "or": true, "in": true, "on": true, "by": true,
	"as": true, "an": true, "be": true, "per": true, "and": true, "was": true, "for": true, "are": true,
	"any": true, "the": true, "pay": true, "has": true, "can": true, "get": true, "via": true,
}

// plainDollarPrefix reports whether raw, the text moneyRE read before an
// amount, is a glued English word (see glued) written in lowercase on a dollar
// sign. Any other letter prefix is a foreign dollar or peso ("NT$", "mxn$",
// "ars$", "rs$", "aus$"), which resolve drops; known codes and symbols
// ("us$", "cad$") keep their meaning in any case.
func plainDollarPrefix(raw string) bool {
	n := letterRun(raw)
	if n == 0 || n >= len(raw) || raw[n:] != "$" {
		return false
	}
	return glued[raw[:n]]
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
		if n := letterRun(s); n > 0 && n < len(s) {
			// Not a symbol, and letters then more: moneyRE matched a code
			// then a symbol ("cad $", "cad$"), or a letter-prefixed dollar the
			// symbols table does not know ("nt$", "hk$", "r$"; a dollar that
			// is not the US one). The code names the currency unless the
			// symbol names another one ("usd c$"); an unknown prefix names
			// no currency we read, so the amount conflicts and is dropped.
			c := strings.ToUpper(s[:n])
			if _, known := currencies[c]; !known {
				return "!", false
			}
			sym := strings.TrimLeftFunc(s[n:], unicode.IsSpace)
			if sc, ok := symbols[sym]; !ok || (!ambiguous[sym] && sc != c) {
				return "!", false
			}
			return c, false
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

// periodAt is a period word read, and the offset past it.
type periodAt struct {
	p   Period
	end int
}

// readPeriod finds the period stated just after end, and returns the offset
// past it (end when none). A period word in the amount's own sentence and line
// is the period (earliest wins). One past a sentence end (".", "!", "?", ";")
// or a line break comes back in far, earliest first, for the caller to use
// only when the range is implausible without it: a line break alone does not
// end a sentence, but a period word on the next line is as often the next
// line's own subject ("Hourly roles are listed apart").
func readPeriod(text string, end int) (near Period, nearEnd int, far []periodAt) {
	win := text[end:min(len(text), end+after)]
	low, at := foldPeriodWindow(win)
	nearAt, farAt := len(low), len(low)
	nearEnd = end
	var farBest periodAt
	for _, pw := range periodWords {
		i := strings.Index(low, pw.w)
		if i < 0 || !fillerOnly(low[:i]) {
			continue
		}
		stop := strings.ContainsAny(low[:i], ".!?;"+lineBreaks)
		pend := end + min(at[i+len(strings.TrimRight(pw.w, " "))], len(win))
		switch {
		case !stop && i < nearAt:
			near, nearAt, nearEnd = pw.p, i, pend
		case stop && i < farAt:
			farAt, farBest = i, periodAt{pw.p, pend}
		}
	}
	if farBest.p != "" {
		far = []periodAt{farBest}
	}
	return near, nearEnd, far
}

// lineBreaks are the characters that end a line: a line break of any kind, not
// only "\n" (CR, VT, FF, NEL, and the Unicode line and paragraph separators).
// Every split of text into lines or sentences here uses this one set.
const lineBreaks = "\n\r\v\f\u0085\u2028\u2029"

// foldPeriodWindow lowers win's ASCII letters, reads "-" and "_" as spaces and
// folds every Unicode space that is not a line break (a non-breaking space,
// U+202F, an ideographic space) to one ASCII space, so "per\u00a0hour" matches
// "per hour". The period words are ASCII. It appends a space, and returns the
// folded text with, for each byte of it (and one past the end), the byte
// offset in win it came from.
func foldPeriodWindow(win string) (string, []int) {
	var b strings.Builder
	at := make([]int, 0, len(win)+2)
	for i := 0; i < len(win); {
		r, w := utf8.DecodeRuneInString(win[i:])
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteByte(byte(r) + 'a' - 'A')
			at = append(at, i)
		case r == '-' || r == '_' || (unicode.IsSpace(r) && !strings.ContainsRune(lineBreaks, r)):
			b.WriteByte(' ')
			at = append(at, i)
		default:
			b.WriteString(win[i : i+w])
			for k := 0; k < w; k++ {
				at = append(at, i+k)
			}
		}
		i += w
	}
	b.WriteByte(' ')
	return b.String(), append(at, len(win), len(win))
}

// fillerOnly: the period word may follow a currency code or a word like
// "base", but no other words ("$100 to spend each year" is not per year).
func fillerOnly(s string) bool {
	for _, w := range strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		if unicode.IsSymbol(r) {
			return '#' // an emoji or sign between: not a filler
		}
		return ' '
	}, s)) {
		if _, ok := currencies[strings.ToUpper(w)]; !ok && w != "base" && w != "salary" && w != "gross" {
			return false
		}
	}
	return true
}

type wordSpan struct {
	w          string // lowercased
	start, end int    // byte offsets of the word in the (valid UTF-8) text
}

// wordSpans returns s's letter words, lowercased, with their offsets in s.
func wordSpans(s string) []wordSpan {
	var out []wordSpan
	start := -1
	for i, r := range s {
		if unicode.IsLetter(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, wordSpan{strings.ToLower(s[start:i]), start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, wordSpan{strings.ToLower(s[start:]), start, len(s)})
	}
	return out
}

// words returns s's letter words, lowercased.
func words(s string) []string {
	spans := wordSpans(strings.ToValidUTF8(s, " "))
	out := make([]string, len(spans))
	for i, sp := range spans {
		out[i] = sp.w
	}
	return out
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

// earlierPay reports whether the nearest salary or not-pay word anywhere in s
// is a salary word.
func earlierPay(s string) bool {
	w := wordSpans(strings.ToValidUTF8(s, " "))
	for i := len(w) - 1; i >= 0; i-- {
		switch {
		case notPay[w[i].w]:
			return false
		case payWords[w[i].w] && !genericPay[w[i].w]:
			return true
		}
	}
	return false
}

// sentenceTail is s after the last sentence end in it: a ".", "!", "?" or ";".
// A comma, a colon and a line break do not end one here (a heading line with
// no stop, "Signing bonus\n$20,000 - $30,000", still says what its amount is).
func sentenceTail(s string) string {
	if i := strings.LastIndexAny(s, ".!?;"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// nearestWord is "pay" or "not" for what the words of the last sentence of s
// say about the amount after them: the nearest salary word (payWords other
// than genericPay) makes it "pay", the nearest notPay word "not". Every other
// word, genericPay ones included, is transparent ("Bonus opportunity range",
// "Equity grants are in a typical range"). A sentence with neither but a
// generic pay word ("range", "between") reads "pay"; one with none reads "".
//
// A phrase break between a not-pay word and the generic words that end the
// sentence makes those a label of their own, so the amount is "pay": a line
// break, an unclosed "(", and a comma, dash (but not one right after a not-pay word), "and" or "or" with only generic
// words after it ("Bonus, Compensation: $150,000", "Equity and compensation
// range: $150,000"). A comma with other words after it ("Equity compensation,
// if any, is a range of $50,000") does not break it.
func nearestWord(s string) string {
	t := strings.ToValidUTF8(sentenceTail(s), " ")
	w := wordSpans(t)
	generic, allGeneric := false, true // allGeneric: every word after i is generic
	for i := len(w) - 1; i >= 0; i-- {
		if generic && phraseBreak(t, w, i, allGeneric) {
			return "pay"
		}
		switch {
		case notPay[w[i].w]:
			return "not"
		case genericPay[w[i].w]:
			generic = true
		case payWords[w[i].w]:
			return "pay"
		}
		allGeneric = allGeneric && genericPay[w[i].w]
	}
	if generic {
		return "pay"
	}
	return ""
}

// phraseBreak reports whether a phrase break stands between w[i] and the words
// after it in t, or w[i] is "and" / "or" (see nearestWord).
func phraseBreak(t string, w []wordSpan, i int, allGeneric bool) bool {
	if allGeneric && (w[i].w == "and" || w[i].w == "or") {
		return true
	}
	end := len(t)
	if i+1 < len(w) {
		end = w[i+1].start
	}
	gap := t[w[i].end:end]
	if strings.ContainsAny(gap, lineBreaks) {
		return true
	}
	if k := strings.LastIndex(gap, "("); k >= 0 && !strings.Contains(t[w[i].end+k:], ")") {
		return true
	}
	if !allGeneric {
		return false
	}
	// A dash right after a not-pay word joins them ("bonus-range", "Bonus -
	// Compensation"); after any other word it starts a new phrase.
	return strings.Contains(gap, ",") || (strings.ContainsAny(gap, "-–—") && !notPay[w[i].w])
}

// beforeWord finds a period word just before an amount: near when it is in the
// amount's own sentence and line, far when only past a sentence end or line
// break ("Hourly staff are welcome. $150,000"), which the caller uses only as
// a fallback, as with readPeriod.
func beforeWord(s string) (near, far Period) {
	last := func(s string) Period {
		w := words(s)
		for i := len(w) - 1; i >= 0 && i >= len(w)-4; i-- {
			if p, ok := beforePeriod[w[i]]; ok {
				return p
			}
		}
		return ""
	}
	start, found := 0, false
	if i := strings.LastIndexAny(s, ".!?;"+lineBreaks); i >= 0 {
		_, w := utf8.DecodeRuneInString(s[i:])
		start, found = i+w, true
	}
	if near = last(s[start:]); near != "" || !found {
		return near, ""
	}
	return "", last(s)
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
