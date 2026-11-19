package jobinsight

import (
	"strings"
	"testing"
)

func TestAttack5SentenceScope(t *testing.T) {
	usd := []payWant{{"USD", Year, 150000, 180000}}
	for _, c := range []struct {
		name, text string
		want       []payWant
	}{
		{"empty", "", nil},
		{"bonus verb phrase then salary", "A bonus is paid in March, and equity vests monthly. Salary is $150,000 - $180,000", usd},
		{"bonus sentence is not the salary", "The bonus has a range of $10,000 - $20,000. Base salary: $150,000 - $180,000", usd},
		{"salary after not-pay word in sentence", "Equity and a competitive salary of $150,000 - $180,000", usd},
		{"closed paren does not break", "Bonus (optional) range: $10,000 - $20,000", nil},
		{"unclosed paren breaks", "Bonus eligible (the expected range for this role is $150,000 - $180,000)", usd},
		{"oversized sentence", strings.Repeat("word ", 5000) + "range $150,000 - $180,000", usd},
		{"invalid utf8", "Bonus \xff range: $10,000 - $20,000", nil},
	} {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
}

func TestAttack5FillerSymbols(t *testing.T) {
	for _, text := range []string{
		"Pay $90 - $110 \U0001F600 per hour",
		"Pay $90 - $110 + per hour",
		"Pay $90 - $110 ~ per hour",
	} {
		for _, p := range ParsePay(text) {
			if p.Period == Hour {
				t.Errorf("%q read hour through a symbol", text)
			}
		}
	}
	checkPays(t, "plain", ParsePay("Pay $90 - $110 (USD) per hour"), []payWant{{"USD", Hour, 90, 110}})
}
