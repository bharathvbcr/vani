package jobinsight

import "testing"

func TestAttack2PayBreaks(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"bonus range is between", "Bonus range is between $10,000 and $20,000", nil},
		{"bonus colon between", "Sign-on bonus: between $10,000 and $20,000", nil},
		{"bonus ranges between chain", "Bonus ranges between $10,000 and $20,000", nil},
		{"bonus target range", "Bonus target range: $50,000 - $60,000", nil},
		{"CR line break before Hourly", "Pay $150,000 - $180,000\rHourly staff earn more", usd(150000, 180000)},
		{"U+2029 before Hourly", "Pay $150,000 - $180,000\u2029Hourly staff", usd(150000, 180000)},
		{"VT before Hourly", "Pay $150,000 - $180,000\vHourly staff", usd(150000, 180000)},
		{"nbsp inside per hour", "Salary $90 - $110\u00a0per\u00a0hour", []payWant{{"USD", Hour, 90, 110}}},
		{"nbsp in per hour", "Salary $90 - $110 per\u00a0hour", []payWant{{"USD", Hour, 90, 110}}},
		{"mxn$ is not USD", "Salary mxn$150,000 - mxn$180,000", nil},
		{"brl$ is not USD", "Salary brl$ 15,000 - 18,000", nil},
		{"ars$ is not USD", "Salary ars$150,000 - ars$180,000", nil},
		{"aed$ is not USD", "Salary aed$ 150,000 - 180,000", nil},
		{"hkd$ is not USD", "Salary hkd$ 150,000 - 180,000", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}
