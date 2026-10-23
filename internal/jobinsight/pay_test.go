package jobinsight

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type payWant struct {
	cur      string
	period   Period
	min, max float64
}

func checkPays(t *testing.T, name string, got []Pay, want []payWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %+v, want %+v", name, got, want)
		return
	}
	for i, w := range want {
		g := got[i]
		if g.Currency != w.cur || g.Period != w.period || g.Min != w.min || g.Max != w.max {
			t.Errorf("%s: pay %d = %+v, want %+v", name, i, g, w)
		}
		if g.AnnualMin != w.min*perYear[w.period] || g.AnnualMax != w.max*perYear[w.period] {
			t.Errorf("%s: annual %v-%v, want %v-%v", name, g.AnnualMin, g.AnnualMax, w.min*perYear[w.period], w.max*perYear[w.period])
		}
		if g.Text == "" {
			t.Errorf("%s: no text", name)
		}
	}
}

func TestParsePay(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		// Verbatim from the Lever fixture's additionalPlain.
		{"lever en dash, no spaces", "The United States base range for this position is $92,126–$131,609, plus equity.", usd(92126, 131609)},
		{"lever spaced dash and code", "The United States base range for this position is $74,000 – $106,000 USD, plus equity.", usd(74000, 106000)},
		// Verbatim from the Ashby fixture: a monthly budget is not pay.
		{"ashby education budget", " - $100/month education budget with more expensive items (like conferences) covered with manager approval.", nil},
		{"k suffix both ends", "Salary: $150K - $200K", usd(150000, 200000)},
		{"k suffix on the top only", "Base pay $150-200k per year.", usd(150000, 200000)},
		{"to", "Compensation: $120,000 to $150,000 annually", usd(120000, 150000)},
		{"between and", "We pay between $120,000 and $150,000.", usd(120000, 150000)},
		{"and without between is no range", "We have $120,000 and $150,000 in grants.", nil},
		{"code before", "Salary range: USD 140,000 - 180,000", usd(140000, 180000)},
		{"single with pay word", "The base salary for this role is $150,000.", usd(150000, 150000)},
		{"single without pay word", "Our customers saved $150,000 last year.", nil},
		{"hourly", "Pay rate: $50–$65/hr", []payWant{{"USD", Hour, 50, 65}}},
		{"hourly before", "Hourly rate: $45 - $55", []payWant{{"USD", Hour, 45, 55}}},
		{"per hour words", "The range is $40.50 - $52.75 per hour.", []payWant{{"USD", Hour, 40.5, 52.75}}},
		{"monthly", "Salary: €4.000 - €5.500 per month", []payWant{{"EUR", Month, 4000, 5500}}},
		{"euro suffix thousands dots", "Gehalt: 65.000 – 80.000 € brutto", []payWant{{"EUR", Year, 65000, 80000}}},
		{"pound", "Salary £70,000 - £90,000", []payWant{{"GBP", Year, 70000, 90000}}},
		{"cad symbol", "Base salary range: CA$130,000–CA$160,000", []payWant{{"CAD", Year, 130000, 160000}}},
		{"dollar with cad code", "The range is $120,000 - $150,000 CAD.", []payWant{{"CAD", Year, 120000, 150000}}},
		{"conflicting codes", "Range: USD 100,000 - 120,000 EUR", nil},
		{"funding round", "We raised $50M - $60M in our Series B.", nil},
		{"billion", "a $2 billion - $3 billion market and salary", nil},
		{"percent", "Salary bump of $10 - $20% yearly", nil},
		{"signing bonus after", "Offer range $10,000 - $25,000 signing bonus.", nil},
		{"bonus before", "Annual bonus: $20,000 - $30,000", nil},
		{"equity after is fine", "Salary $150,000 - $180,000 plus equity", usd(150000, 180000)},
		{"implausible ratio", "Salary: $10,000 - $900,000", nil},
		{"inverted", "Salary: $200,000 - $150,000", nil},
		{"too small for a year", "Salary: $1,000 - $2,000", nil},
		{"too large", "Salary: $5,000,000 - $6,000,000", nil},
		{"bare numbers", "Experience: 3 - 5 years, team of 10 - 20", nil},
		{"years are not money", "Founded 2015 - 2024, salary competitive", nil},
		{"two location bands merge", "Salary: SF $180,000 - $220,000; NYC $170,000 - $210,000; remote $150,000 - $190,000.", usd(150000, 220000)},
		{"two currencies in order", "US base salary $150,000 - $180,000. UK salary £90,000 - £110,000.",
			[]payWant{{"USD", Year, 150000, 180000}, {"GBP", Year, 90000, 110000}}},
		{"mixed periods merge per year", "Pay: $50 - $60 per hour, or salary $120,000 - $130,000.", []payWant{{"USD", Year, 104000, 130000}}},
		{"decimals", "Salary: $150,000.00 - $175,000.00", usd(150000, 175000)},
		{"decimal k", "Salary: $150.5k - $175.25k", usd(150500, 175250)},
		{"partly read digit run is no amount", "Salary: $150.0001 - $175.0001", nil},
		{"three decimals read as thousands", "Salary: $52.125 - $60.5", nil},
		// The lines the Lever and Ashby adapters write (srcutil.PayLine, and
		// Ashby's summaries verbatim from the live board).
		{"pay line", "Pay range: USD 120000 – 150000 per year\n\nBody.", usd(120000, 150000)},
		{"pay line hourly", "Pay range: USD 40 – 52.5 per hour", []payWant{{"USD", Hour, 40, 52.5}}},
		{"pay line single", "Pay range: EUR 70000", []payWant{{"EUR", Year, 70000, 70000}}},
		{"ashby salary summary", "Compensation: €110K - €195.5K", []payWant{{"EUR", Year, 110000, 195500}}},
		{"ashby tier summary", "Compensation: $210,000 – $280,313 • Offers Equity • Offers Commission • Multiple Ranges", usd(210000, 280313)},
		// Shapes real US postings use around pay.
		{"401k is a plan", "Competitive salary, health insurance and $401k match", nil},
		{"401k without match", "Salary is competitive; we offer a $401k plan.", nil},
		{"401k contributions", "Salary and benefits include $401k contributions.", nil},
		{"near bands still merge", "Salary: Tier 1 $160,000 - $200,000; Tier 2 $140,000 - $180,000.", usd(140000, 200000)},
		{"variable range is not base", "Base $120,000 - $150,000, $20,000 - $40,000 variable", usd(120000, 150000)},
		{"far range is not merged", "Salary: $120,000 - $150,000. Salary for interns: $20,000 - $30,000.", usd(120000, 150000)},
		{"pay word after overrides equity before", "Compensation: equity plus $150,000 - $180,000 base", usd(150000, 180000)},
		{"insurance cap", "Health insurance up to $20,000 per year", nil},
		{"empty", "", nil},
		{"no money", "We build compilers. Salary is competitive.", nil},
		{"invalid utf-8", "Salary \xff\xfe $150,000 - $180,000 \xff", usd(150000, 180000)},
		// Verbatim from Google's results page as the source adapter renders it
		// (2026-10-20): "education" ends the sentence before, not this line.
		{"google, after a finished sentence", "The US base salary range for this full-time position is $188,000-$274,000 + bonus + equity + benefits. Our salary ranges are determined by role, level, and location. Within the range, individual pay is determined by work location and additional factors, including job-related skills, experience, and relevant education or training.\nUS: $188000 - $274000 (USD) + 20% bonus target + equity + benefits\nLearn more about benefits at Google", usd(188000, 274000)},
		{"google line alone", "relevant education or training.\nUS: $126000 - $180000 (USD) + 15% bonus target + equity + benefits", usd(126000, 180000)},
		{"heading line without a stop still counts", "Base salary\n$150,000", usd(150000, 150000)},
		{"not-pay heading without a stop still counts", "Signing bonus\n$20,000 - $30,000", nil},
		{"not-pay word on the same line", "We pay well.\nRelocation package: $20,000 - $30,000", nil},
		{"pay word in a finished sentence still names it", "The base salary is listed below.\n$150,000", usd(150000, 150000)},
		{"not-pay tail beats an earlier pay word", "The salary is great.\nSigning bonus $20,000 - $30,000", nil},
		{"nearest earlier word decides only when it is a pay word", "Base salary.\nNo equity.\n$150,000", nil},
		{"stop then spaces before the break", "Skills and education. \t\r\n$120,000 - $150,000", usd(120000, 150000)},
		{"break with no stop before it", "education\n$120,000 - $150,000", nil},
		{"only breaks", "\n\n\n$120,000 - $150,000", usd(120000, 150000)},
		// Verbatim from Microsoft's Montreal posting 1970393557023166 (Eightfold, 2026-10-20).
		{"code before a symbol", "Software Engineering IC3 - The typical base pay range for this role across Canada is CAD $114,400 - CAD $203,900 per year.", []payWant{{"CAD", Year, 114400, 203900}}},
		{"code glued to a symbol", "Salary: CAD$100,000 - CAD$120,000", []payWant{{"CAD", Year, 100000, 120000}}},
		{"code, no-break space, symbol", "Salary: AUD $150,000 - AUD $180,000", []payWant{{"AUD", Year, 150000, 180000}}},
		{"code before a symbol, lowercase", "salary: sgd $90,000 - sgd $120,000", []payWant{{"SGD", Year, 90000, 120000}}},
		{"code before a matching symbol", "Salary: GBP £60,000 - GBP £80,000", []payWant{{"GBP", Year, 60000, 80000}}},
		{"code before another currency's symbol", "Salary: USD £60,000 - USD £80,000", nil},
		{"code before a symbol on one end only", "Salary: CAD $100,000 - $120,000", []payWant{{"CAD", Year, 100000, 120000}}},
		{"code inside a word is not a code", "AutoCAD $150,000 - $180,000 salary", usd(150000, 180000)},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

func TestParsePayBounds(t *testing.T) {
	// Oversized text: only the first maxPayScan bytes are read, and a range
	// past them is not found.
	pad := strings.Repeat("filler 12, 34. ", maxPayScan/15+1)
	if got := ParsePay(pad + "Salary: $150,000 - $180,000"); got != nil {
		t.Errorf("range past the scan bound found: %+v", got)
	}
	if got := ParsePay("Salary: $150,000 - $180,000 " + pad); len(got) != 1 {
		t.Errorf("range before padding: %+v", got)
	}
	// Many currencies: at most MaxPays, first-seen first.
	var b strings.Builder
	for _, c := range []string{"USD", "EUR", "GBP", "CAD", "AUD", "CHF"} {
		b.WriteString("Salary " + c + " 100,000 - 120,000. ")
	}
	got := ParsePay(b.String())
	if len(got) != MaxPays || got[0].Currency != "USD" || got[3].Currency != "CAD" {
		t.Errorf("many currencies: %+v", got)
	}
	// Currency-dense text: a range at the start is read; one after more than
	// maxPayWindowBytes of windows is not.
	dense := strings.Repeat("Budget $100, $200 or $300 each. ", (2*maxPayWindowBytes)/32)
	if got := ParsePay("Salary: $150,000 - $180,000. " + dense); len(got) != 1 || got[0].AnnualMax != 180000 {
		t.Errorf("range before dense text: %+v", got)
	}
	if got := ParsePay(dense + "Salary: $150,000 - $180,000."); got != nil {
		t.Errorf("range past the window budget found: %+v", got)
	}
	// A huge digit run is not a number.
	if got := ParsePay("Salary $" + strings.Repeat("9", 40)); got != nil {
		t.Errorf("digit run: %+v", got)
	}
	// Text is one cleaned line, cut.
	long := "Salary: $150,000 - $180,000"
	if got := ParsePay(long); len(got) != 1 || strings.ContainsAny(got[0].Text, "\n\t") {
		t.Errorf("text: %+v", got)
	}
}

// TestParsePayIsLinear guards the regexp against a pathological input: a
// long run of separators and digits must not take quadratic time.
func TestParsePayIsLinear(t *testing.T) {
	s := strings.Repeat("$1,000,", 15000)
	start := time.Now()
	ParsePay(s)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("ParsePay took %v on %d bytes", d, len(s))
	}
}

// TestParsePayFixtures runs the parser over the real board payloads the
// source adapters are tested with: the Lever ranges are read, and nothing in
// the Ashby or Greenhouse texts (benefit budgets, equity plans) reads as pay.
func TestParsePayFixtures(t *testing.T) {
	read := func(path string, v any) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	var lv []map[string]any
	read("../sources/lever/testdata/board.json", &lv)
	wantLever := map[string][]payWant{
		"Talent Relations Manager":                              {{"USD", Year, 92126, 131609}},
		"Associate – Audiobook Licensing & Author Partnerships": {{"USD", Year, 74000, 106000}},
	}
	for _, p := range lv {
		title, _ := p["text"].(string)
		desc, _ := p["descriptionPlain"].(string)
		add, _ := p["additionalPlain"].(string)
		checkPays(t, "lever "+title, ParsePay(desc+"\n\n"+add), wantLever[title])
	}
	var ab struct{ Jobs []map[string]any }
	read("../sources/ashby/testdata/board.json", &ab)
	for _, p := range ab.Jobs {
		s, _ := p["descriptionPlain"].(string)
		title, _ := p["title"].(string)
		checkPays(t, "ashby "+title, ParsePay(s), nil)
	}
	var gh struct{ Jobs []map[string]any }
	read("../sources/greenhouse/testdata/board.json", &gh)
	for _, p := range gh.Jobs {
		s, _ := p["content"].(string)
		title, _ := p["title"].(string)
		checkPays(t, "greenhouse "+title, ParsePay(s), nil)
	}
}

func TestPayInAndCurrencies(t *testing.T) {
	pays := ParsePay("US base salary $150,000 - $180,000. UK salary £90,000 - £110,000.")
	if p, ok := PayIn(pays, "GBP"); !ok || p.Max != 110000 {
		t.Errorf("PayIn GBP = %+v %v", p, ok)
	}
	if _, ok := PayIn(pays, "EUR"); ok {
		t.Error("PayIn EUR found")
	}
	for _, c := range PayCurrencies() {
		if PayStep(c) <= 0 {
			t.Errorf("%s has no step", c)
		}
	}
	if PayStep("XXX") != 0 {
		t.Error("unknown currency has a step")
	}
}

// BenchmarkParsePay is one worst-size description (the 64 KiB adapter cap) of
// prose with numbers, and one pay range at the end.
func BenchmarkParsePay(b *testing.B) {
	text := strings.Repeat("We serve 10,000 customers in 40 countries and ship 3 releases a week. ", (64<<10)/70) +
		"Salary: $150,000 - $180,000 per year."
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		if len(ParsePay(text)) != 1 {
			b.Fatal("range not found")
		}
	}
}

func TestPayShort(t *testing.T) {
	for _, c := range []struct {
		p    Pay
		want string
	}{
		{Pay{Currency: "USD", Period: Year, Min: 120000, Max: 160000}, "USD 120k–160k/yr"},
		{Pay{Currency: "USD", Period: Hour, Min: 70, Max: 90}, "USD 70–90/hr"},
		{Pay{Currency: "USD", Period: Hour, Min: 72.5, Max: 72.5}, "USD 72.5/hr"},
		{Pay{Currency: "EUR", Period: Year, Min: 153200, Max: 180000}, "EUR 153.2k–180k/yr"},
		{Pay{Currency: "INR", Period: Year, Min: 2500000, Max: 4000000}, "INR 2.5M–4M/yr"},
		{Pay{Currency: "GBP", Period: Month, Min: 5000, Max: 6000}, "GBP 5k–6k/mo"},
		{Pay{Currency: "SEK", Period: Week, Min: 9000, Max: 9000}, "SEK 9k/wk"},
		{Pay{Currency: "CAD", Period: Day, Min: 400, Max: 600}, "CAD 400–600/day"},
	} {
		if got := c.p.Short(); got != c.want {
			t.Errorf("%+v.Short() = %q, want %q", c.p, got, c.want)
		}
	}
	// Every range ParsePay returns has a period Short can name.
	pays := ParsePay("Salary: $50 - $70 per hour. Base salary €90.000 – €110.000 per year.")
	if len(pays) != 2 {
		t.Fatalf("pays = %+v, want two", pays)
	}
	for _, p := range pays {
		if s := p.Short(); !strings.Contains(s, "/") {
			t.Errorf("%+v.Short() = %q has no period", p, s)
		}
	}
}

// BenchmarkParsePayDense is the other extreme: a currency marker every few
// bytes, so the windows cover the whole text.
func BenchmarkParsePayDense(b *testing.B) {
	text := strings.Repeat("Budget $100, $200 or $300 each. ", (64<<10)/32)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		if ParsePay(text) != nil {
			b.Fatal("budgets read as pay")
		}
	}
}
