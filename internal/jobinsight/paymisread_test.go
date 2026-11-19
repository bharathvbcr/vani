package jobinsight

import (
	"strings"
	"testing"
)

// Bonus and equity ranges, non-US dollars and a period word past a sentence
// end are not read as the salary they sit beside (card ft-11d0, items 26-28).
func TestParsePayMisreads(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		// 26: a bonus or equity range is not salary.
		{"bonus range", "Bonus range: $20,000 - $40,000", nil},
		{"annual bonus range", "The annual bonus range is $20,000 - $40,000.", nil},
		{"equity range", "Equity range: $50,000 - $100,000", nil},
		{"equity compensation", "Equity compensation: $50,000 - $100,000 over four years", nil},
		{"signing bonus band", "Signing bonus band $10,000 to $25,000", nil},
		{"salary then bonus range", "Base salary range: $150,000 - $180,000. Bonus range: $20,000 - $40,000.", usd(150000, 180000)},
		{"salary then equity range", "Salary range: $150,000 - $180,000.\nEquity range: $60,000 - $90,000.", usd(150000, 180000)},
		{"bonus word further back still decides", "Bonus: see below. Range: $20,000 - $40,000", usd(20000, 40000)},

		// 27: a prefixed dollar is not USD.
		{"NT$", "Salary: NT$1,200,000 - NT$1,800,000 per year", nil},
		{"HK$", "Salary: HK$600,000 - HK$900,000", nil},
		{"R$", "Salary: R$ 120.000 - R$ 180.000", nil},
		{"MX$", "Salary: MX$ 600,000 - MX$ 900,000", nil},
		{"NT$ lower case", "salary: nt$1,200,000 - nt$1,800,000", nil},
		{"HKD$", "Salary: HKD$600,000 - HKD$900,000", nil},
		{"NT$ one end", "Salary: NT$1,200,000 - $1,800,000", nil},
		{"NT$ single", "The base salary is NT$1,500,000.", nil},
		{"NT$ then USD range", "Taipei: NT$1,200,000 - NT$1,800,000. US: $150,000 - $180,000 base", usd(150000, 180000)},
		{"known C$ still CAD", "Salary: C$120,000 - C$150,000", []payWant{{"CAD", Year, 120000, 150000}}},
		{"known A$ still AUD", "Salary: A$120,000 - A$150,000", []payWant{{"AUD", Year, 120000, 150000}}},
		{"known NZ$ still NZD", "Salary: NZ$120,000 - NZ$150,000", []payWant{{"NZD", Year, 120000, 150000}}},
		{"known S$ still SGD", "Salary: S$120,000 - S$150,000", []payWant{{"SGD", Year, 120000, 150000}}},
		{"known US$ still USD", "Salary: US$120,000 - US$150,000", usd(120000, 150000)},
		{"CAD code with dollar still CAD", "Salary: CAD$120,000 - CAD$150,000", []payWant{{"CAD", Year, 120000, 150000}}},
		{"long word before dollar is no prefix", "Salary: plus $120,000 - $150,000", usd(120000, 150000)},

		// 28: a period word past a sentence end is not this range's.
		{"hourly after full stop", "Salary: $150,000 - $180,000. Hourly contractors are paid separately.", usd(150000, 180000)},
		{"annual after full stop", "Pay rate: $50 - $65. Annual leave is 25 days.", nil},
		{"per hour after question mark", "What is the base salary? $150,000 - $180,000. Is it per hour? No.", usd(150000, 180000)},
		{"hourly on the next line", "Salary: $150,000 - $180,000\nHourly roles are listed apart", usd(150000, 180000)},
		{"period in the same sentence still counts", "Pay rate: $50 - $65 per hour. Equity on top.", []payWant{{"USD", Hour, 50, 65}}},
		{"period after a decimal point still counts", "Pay rate: $40.50 - $52.75 per hour", []payWant{{"USD", Hour, 40.5, 52.75}}},
		{"p.a. still counts", "Salary: £50,000 - £60,000 p.a.", []payWant{{"GBP", Year, 50000, 60000}}},

		// Adversarial.
		{"empty", "", nil},
		{"only a prefix", "NT$", nil},
		{"prefix, no digits", "Salary: NT$ - HK$", nil},
		{"bonus alone", "bonus range", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

// Many prefixed dollars cost no more than any other amounts: the scan stays
// bounded and never yields a USD estimate.
func TestParsePayPrefixedDollarsAreBounded(t *testing.T) {
	text := "Salary: " + strings.Repeat("NT$1,200,000 - NT$1,800,000, ", 5000)
	if got := ParsePay(text); got != nil {
		t.Errorf("got %+v, want none", got)
	}
}
