package jobinsight

import (
	"strings"
	"testing"
	"time"
)

// Round-2 attack tests for ft-11d0: the three-rule fix holds on these.
func TestAttack2PayKept(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		// rule 1
		{"bonus range", "Bonus range: $10,000 - $20,000", nil},
		{"sign-on bonus between", "Sign-on bonus between $10,000 and $20,000", nil},
		{"salary between", "Salary between $100,000 and $150,000", usd(100000, 150000)},
		{"equity range next line", "Equity range\n$100,000 - $150,000", nil},
		{"equity range CR", "Equity range\r$100,000 - $150,000", nil},
		{"equity range CRLF", "Equity range\r\n$100,000 - $150,000", nil},
		{"bonus nbsp range", "Bonus\u00a0range: $10,000 - $20,000", nil},
		{"stipend tab range", "Stipend\trange: $10,000 - $20,000", nil},
		{"equity comp", "Equity comp: $100,000 - $150,000", nil},
		{"bonus dash compensation", "Bonus - Compensation: $100,000 - $150,000", nil},
		{"equity salary run-in", "Equity Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"bonus comma salary", "Bonus, Salary: $150,000 - $180,000", usd(150000, 180000)},
		{"allowance and base salary", "Relocation allowance and base salary: $100,000 - $150,000", usd(100000, 150000)},
		{"bonus range then base salary", "Relocation range: $100,000 - $150,000. Base salary: $120,000 - $150,000", usd(120000, 150000)},
		// rule 2
		{"year-sized range, hourly on next line stays year", "$50,000-$60,000\nper hour", usd(50000, 60000)},
		{"hourly staff after sentence end stays year", "Pay: $150,000 - $180,000\n\nHourly staff", usd(150000, 180000)},
		{"CRLF period word is a line break", "Pay $80 - $120\r\nper hour", []payWant{{"USD", Hour, 80, 120}}},
		{"hourly before sentence end applies to implausible year range", "Hourly. $80 - $120", []payWant{{"USD", Hour, 80, 120}}},
		{"hourly then another range", "$50,000 - $60,000. Hourly. $20 - $30", usd(41600, 62400)},
		{"U+2028 before amount does not attach earlier hourly", "Hourly staff welcome.\u2028Pay $150,000 - $180,000", usd(150000, 180000)},
		{"nbsp before /hr", "Salary $90 - $110\u00a0/hr", []payWant{{"USD", Hour, 90, 110}}},
		{"narrow nbsp before per hour", "Salary $90 - $110\u202fper hour", []payWant{{"USD", Hour, 90, 110}}},
		// rule 3
		{"ar$ drops", "Salary ar$150,000 - ar$180,000", nil},
		{"cl$ drops", "Salary cl$150,000 - cl$180,000", nil},
		{"Rd$ drops", "Salary Rd$150,000 - Rd$180,000", nil},
		{"r$ Brazilian drops", "Salary r$ 5.000,00 - r$ 6.000,00 per month", nil},
		{"R$ drops", "Salary R$ 15.000,00 - R$ 18.000,00", nil},
		{"Nt$ mixed case drops", "Salary Nt$150,000 - Nt$180,000", nil},
		{"Mxn$ mixed case drops", "Salary Mxn$ 150,000 - 180,000", nil},
		{"aud$ code", "Salary aud$150,000 - aud$180,000", []payWant{{"AUD", Year, 150000, 180000}}},
		{"eur$ code", "Salary eur$ 150,000 - 180,000", []payWant{{"EUR", Year, 150000, 180000}}},
		{"per$ plain dollar", "Salary per$100,000 - per$150,000", usd(100000, 150000)},
		{"currency-conflict via symbol", "Salary \u00a5 nt$150,000 - $180,000", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

// ParsePay stays bounded on 1 MiB of dense ranges, sentence ends and breaks.
func TestAttack2PayLinear(t *testing.T) {
	units := []string{"Salary: $100,000 - $150,000. Hourly staff. ", "Pay $50 - $60\nper hour. ", "$80 - $120. ", "Hourly. $80 $90 ", ".\n.\n$80 - $120.", "$80-$120 " + strings.Repeat(".", 30), "Bonus range\n$1 - $2 ", "$ "}
	for _, u := range units {
		s := strings.Repeat(u, (1<<20)/len(u))
		st := time.Now()
		_ = ParsePay(s)
		if d := time.Since(st); d > 2*time.Second {
			t.Errorf("%q: %v for 1 MiB", u, d)
		}
	}
}
