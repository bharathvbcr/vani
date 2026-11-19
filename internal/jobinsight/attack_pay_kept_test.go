package jobinsight

import (
	"strings"
	"testing"
)

// Attack tests for ft-11d0: the fix holds on these.
func TestAttackPayKept(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"line break before heading keeps salary", "Benefits: medical, dental, equity\nSalary range: $150,000 - $180,000", usd(150000, 180000)},
		{"equity then bonus then salary range", "Benefits: equity, bonus, salary range $150,000 - $180,000", usd(150000, 180000)},
		{"stop between equity and heading", "Perks include equity.\nCompensation: $150,000 - $180,000", usd(150000, 180000)},
		{"bonus sentence then salary", "Bonus & equity eligible. Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"stock options then base pay", "Stock options, base pay: $150,000 - $180,000", usd(150000, 180000)},
		{"is$ prefix is no salary", "is$150k - is$180k", nil},
		{"up to$ alone", "up to$200k", nil},
		{"USD$ is USD", "USD$100,000 - USD$120,000", usd(100000, 120000)},
		{"us$ is USD", "us$100,000 - us$120,000", usd(100000, 120000)},
		{"Us$ mixed case", "Us$100,000 - $120,000", usd(100000, 120000)},
		{"CAD$", "CAD$100,000 - CAD$120,000", []payWant{{"CAD", Year, 100000, 120000}}},
		{"c$ lower", "c$100,000 - c$120,000", []payWant{{"CAD", Year, 100000, 120000}}},
		{"a$ lower", "a$100,000 - a$120,000", []payWant{{"AUD", Year, 100000, 120000}}},
		{"unknown three-letter prefix conflicts", "Salary: ABC$100,000 - $120,000", nil},
		{"digit before us$", "Salary 5us$100,000 - $120,000", usd(100000, 120000)},
		{"underscore before us$", "Salary: _us$100,000 - $120,000", usd(100000, 120000)},
		{"unicode letter before us$", "Salary: \u00e9us$100,000 - $120,000", usd(100000, 120000)},
		{"trailing us$ without digits", "Salary: $100,000 - $120,000 per year; pay at us$", usd(100000, 120000)},
		{"approx in parens then year", "Salary: $150k - $180k (approx.) per year", usd(150000, 180000)},
		{"p.a.", "Salary: $150,000 - $180,000 p.a.", usd(150000, 180000)},
		{"slash hr with stop", "Pay: $45 - $55 /hr.", []payWant{{"USD", Hour, 45, 55}}},
		{"exp. then per year", "Pay: $100k-$120k, depending on exp. per year", usd(100000, 120000)},
		{"e.g. then per year", "Pay: $100k-$120k e.g. per year", usd(100000, 120000)},
		{"i.e. annually", "Pay: $100k-$120k i.e. annually", usd(100000, 120000)},
		{"european thousands", "Salary: $150.000 - $180.000 per year", usd(150000, 180000)},
		{"earlier sentence per hour does not attach to later salary", "Per hour rates vary. Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"slash year", "Salary: $100,000 - $120,000 / year", usd(100000, 120000)},
		{"semicolon before period word is a real stop (unchanged by fix? baseline attached; now defaulted to year)", "Salary: $100,000 - $120,000; per year", usd(100000, 120000)},
		{"empty", "", nil},
		{"only dollar", "$", nil},
		{"only prefixed dollar", "xy$", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

// Oversized, repeated and unicode-heavy input stay bounded and never become USD from a prefix.
func TestAttackPayHostile(t *testing.T) {
	for _, s := range []string{
		strings.Repeat("ab$", 200000),
		strings.Repeat("us$", 100000),
		strings.Repeat("\u00e9\u4e2d$1,000 - ", 50000),
		"Salary: " + strings.Repeat("HK$1 - ", 100000),
		strings.Repeat("Bonus range: $1 - $2.\n", 50000),
		strings.Repeat("\n", 1<<20) + "Salary: $150,000 - $180,000",
	} {
		_ = ParsePay(s)
	}
	if got := ParsePay("Salary: HK$" + strings.Repeat("9", 5000) + " - HK$1"); got != nil {
		t.Errorf("got %+v", got)
	}
}
