package jobinsight

import "testing"

func TestAttackPayBreaks(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"bonus on previous line then Compensation", "401k match, bonus\nCompensation: $150,000 - $180,000", usd(150000, 180000)},
		{"Equity Salary heading run-in", "Equity Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"Bonus, Salary list punctuation", "Bonus, Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"annual bonus sentence then Salary", "We pay annual bonus. Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"period word on next line", "Salary: $50 - $60\nper hour", []payWant{{"USD", Hour, 50, 60}}},
		{"period word after (USD). sentence stop", "Pay: $50 - $60 (USD). Per hour", []payWant{{"USD", Hour, 50, 60}}},
		{"to$ typo missing space", "to$180,000 - $200,000", usd(180000, 200000)},
		{"before-word: earlier sentence hourly staff", "Hourly staff are welcome. $150,000 - $180,000", usd(150000, 180000)},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}
