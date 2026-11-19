package jobinsight

import "testing"

// A period word past a sentence end or line break is only a fallback: used when
// the range is implausible without it, never over a plausible reading, and
// never over a period word in the amount's own sentence.
func TestPayPeriodFallback(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	hour := func(lo, hi float64) []payWant { return []payWant{{"USD", Hour, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		// After the amount.
		{"after: implausible as year, hour past stop", "Pay: $50 - $60 (USD). Per hour", hour(50, 60)},
		{"after: implausible as year, hour on next line", "Salary: $50 - $60\nper hour", hour(50, 60)},
		{"after: comma then line break", "Salary: $50 - $60,\nper hour", hour(50, 60)},
		{"after: plausible as year, hourly past stop ignored", "Salary: $150,000 - $180,000. Hourly staff are paid apart", usd(150000, 180000)},
		{"after: plausible as year, hourly next line ignored", "Salary: $150,000 - $180,000\nHourly staff are paid apart", usd(150000, 180000)},
		{"after: far period does not rescue an implausible range", "Salary: $50 - $60. Annual leave is 25 days", nil},
		{"after: in-sentence period wins over far one", "Salary: $50 - $60 per hour. Annual bonus applies", hour(50, 60)},
		{"after: in-sentence period wins even when implausible", "Salary: $150,000 - $180,000 per hour. Annual", nil},
		{"after: far period beyond the read window is unseen", "Salary: $50 - $60.                                   Per hour", nil},
		// Before the amount.
		{"before: earlier sentence hourly, plausible as year", "Hourly staff are welcome. $150,000 - $180,000", usd(150000, 180000)},
		{"before: heading line hourly, implausible as year", "Hourly rate:\n$45 - $55", hour(45, 55)},
		{"before: earlier sentence hourly rescues implausible", "We pay hourly. Rate: $45 - $55", hour(45, 55)},
		{"before: same sentence hourly wins", "Hourly rate: $45 - $55", hour(45, 55)},
		{"before: earlier sentence hourly does not rescue nothing", "Hourly staff are welcome. Rate: $4 - $5", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

// A not-pay word modifies only a generic range noun in the same phrase.
func TestPayNotPayModifier(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"bonus range", "Bonus Range: $10,000 - $15,000", nil},
		{"equity compensation", "Equity compensation: $150,000 - $180,000", nil},
		{"hyphenated", "bonus-range: $10,000 - $15,000", nil},
		{"nbsp phrase", "Bonus\u00a0Range: $10,000 - $15,000", nil},
		{"bonus between", "Bonus between $10,000 and $15,000", nil},
		{"comma splits phrase", "Bonus, Compensation: $150,000 - $180,000", usd(150000, 180000)},
		{"line break splits phrase", "Equity\nCompensation: $150,000 - $180,000", usd(150000, 180000)},
		{"stop splits phrase", "Equity. Range: $150,000 - $180,000", usd(150000, 180000)},
		{"salary word is never modified", "Bonus Base: $150,000 - $180,000", usd(150000, 180000)},
		{"pay word is never modified", "Equity Pay: $150,000 - $180,000", usd(150000, 180000)},
		{"empty", "", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

// Only an uppercase unknown prefix is a foreign dollar.
func TestPayPlainDollarPrefix(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"lowercase to$", "to$180,000 - $200,000", usd(180000, 200000)},
		{"lowercase up to$", "Salary: up to$200,000 - $240,000", usd(200000, 240000)},
		{"lowercase nt$ stays foreign (listed prefix)", "salary: nt$1,200,000 - nt$1,800,000", nil},
		{"NT$ is foreign", "Salary: NT$150,000 - $180,000", nil},
		{"HK$ is foreign", "Salary: HK$150,000 - HK$180,000", nil},
		{"Hk$ is foreign", "Salary: Hk$150,000 - Hk$180,000", nil},
		{"Is$ is foreign", "Salary: Is$150,000 - Is$180,000", nil},
		{"mixed case in a code prefix", "Salary: Cad$150,000 - Cad$180,000", []payWant{{"CAD", Year, 150000, 180000}}},
		{"known code lowercase", "Salary: cad$150,000 - cad$180,000", []payWant{{"CAD", Year, 150000, 180000}}},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}
