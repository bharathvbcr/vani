package jobinsight

import "testing"

func TestAttack4Breaks(t *testing.T) {
	usd := []payWant{{"USD", Year, 150000, 180000}}
	for _, b := range []string{
		"Relocation package: expected range $5,000 - $15,000.",
		"Equity grants for this role are in a typical range of $50,000 - $90,000.",
		"Bonus opportunity range: $10,000 - $20,000.",
		"Bonuses typically range from $10,000 - $20,000.",
		"Signing bonus can range between $10,000 and $20,000.",
		"Annual bonus eligibility range is $10,000 - $20,000.",
		"Our bonus plan has a range of $10,000 - $20,000.",
		"Equity compensation, if any, is a range of $50,000 - $90,000.",
	} {
		text := "About the role.\n" + b + " Base salary: $150,000 - $180,000.\nBenefits: health."
		checkPays(t, b, ParsePay(text), usd)
	}
	text := "Bonus and equity: the compensation range for this role is $150,000 - $180,000"
	// A salary sentence: the salary-word precedence rule decides it (a salary
	// word anywhere in the sentence beats the not-pay words beside it).
	checkPays(t, text, ParsePay(text), usd)
	text = "Pay $90 - $110 \U0001F600 per hour"
	for _, p := range ParsePay(text) {
		if p.Period == Hour {
			t.Errorf("%q read hour through an emoji", text)
		}
	}
}
