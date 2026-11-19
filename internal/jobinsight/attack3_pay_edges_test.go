package jobinsight

import "testing"

func TestAttack3PayEdges(t *testing.T) {
	usd := func(lo, hi float64) []payWant { return []payWant{{"USD", Year, lo, hi}} }
	cases := []struct {
		name, text string
		want       []payWant
	}{
		{"CRLF before Hourly", "Pay $150,000 - $180,000\r\nHourly staff", usd(150000, 180000)},
		{"NEL before Hourly", "Pay $150,000 - $180,000\u0085Hourly staff", usd(150000, 180000)},
		{"FF before Hourly", "Pay $150,000 - $180,000\fHourly staff", usd(150000, 180000)},
		{"U+202F in per hour", "Salary $90 - $110 per\u202fhour", []payWant{{"USD", Hour, 90, 110}}},
		{"ideographic space in per hour", "Salary $90 - $110 per\u3000hour", []payWant{{"USD", Hour, 90, 110}}},
		{"bonus then salary keeps salary", "Bonus target range is below. Base salary range: $150,000 - $180,000", usd(150000, 180000)},
		{"bonus comma range is pay", "Bonus, range: $150,000 - $180,000", usd(150000, 180000)},
		{"bonus line break range is pay", "Bonus\nrange: $150,000 - $180,000", usd(150000, 180000)},
		{"equity compensation range", "Equity compensation range is between $150,000 and $180,000", nil},
		{"bare between is pay", "Between $150,000 and $180,000", usd(150000, 180000)},
		{"rs$ is not USD", "Salary rs$150,000 - rs$180,000", nil},
		{"aus$ is not USD", "Salary aus$150,000 - aus$180,000", nil},
		{"upper-case TO$ is foreign", "Salary TO$150,000 - TO$180,000", nil},
		{"us$ any case", "Salary US$150,000 - us$180,000", usd(150000, 180000)},
		{"up to$ stays USD", "Salary up to$180,000 - $200,000", usd(180000, 200000)},
		{"empty", "", nil},
	}
	for _, c := range cases {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}
