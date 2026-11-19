package jobinsight

import (
	"fmt"
	"testing"
)

// A salary word anywhere in the amount's sentence or heading makes the amount
// pay, whatever bonus or equity word shares it ("Salary and Bonus Range").
// Every case here read right before nearest-word-wins and was dropped by it,
// or was dropped by both.
func TestAttack6PayBreaks(t *testing.T) {
	for _, c := range [][3]string{
		{"salary-and-bonus-range-heading", "Salary and Bonus Range\n$150,000 - $180,000", "150000-180000"},
		{"pay-and-bonus-range-heading", "Pay and Bonus Range\n$150,000 - $180,000", "150000-180000"},
		{"base-salary-and-bonus-range", "Base salary and bonus range: $150,000 - $180,000", "150000-180000"},
		{"salary-comma-bonus-range", "Salary, bonus range: $150,000 - $180,000", "150000-180000"},
		{"salary-and-bonus-band", "Salary and bonus band: $150,000 - $180,000", "150000-180000"},
		{"salary-and-bonus-range-from", "The salary and bonus for this position range from $150,000 to $180,000.", "150000-180000"},
		{"compensation-and-equity-range", "Compensation and equity range\n$150,000 - $180,000", "150000-180000"},
		{"salary-range-bonus-eligible-paren", "Salary Range (bonus eligible): $150,000 - $180,000", "150000-180000"},
		{"base-salary-range-which-is-bonus-eligible", "The base salary range for this role, which is bonus eligible, is $150,000 - $180,000", "150000-180000"},
		{"annual-salary-bonus-eligible-line", "Annual Salary\nBonus Eligible\n$150,000 - $180,000", "150000-180000"},
		{"salary-range-dash-bonus-eligible", "Salary Range - Bonus Eligible\n$150,000 - $180,000", "150000-180000"},
		{"compensation-includes-bonus", "Compensation (includes bonus)\n$150,000 - $180,000", "150000-180000"},
		{"bonus-eligible-role-sentence", "The salary range for this bonus-eligible role is $150,000 - $180,000", "150000-180000"},
		{"salary-range-and-bonus-eligibility", "Salary range and bonus eligibility: $150,000 - $180,000", "150000-180000"},
		{"salary-range-excluding-bonus-and-equity", "Salary Range (excluding bonus and equity) $150,000 - $180,000", "150000-180000"},
		{"salary-and-bonus", "Salary and bonus: $150,000 - $180,000", "150000-180000"},
		{"salary-plus-bonus", "Salary plus bonus $150,000 - $180,000", "150000-180000"},
		{"salary-range-with-bonus", "Salary range with bonus: $150,000 - $180,000", "150000-180000"},
		{"pay-range-plus-bonus", "Pay range plus bonus: $150,000 - $180,000", "150000-180000"},
		{"base-salary-bonus-equity", "Base salary, bonus, equity: $150,000 - $180,000", "150000-180000"},
		{"salary-range-bonus-percent-line", "Salary Range\nBonus: 10%\n$150,000 - $180,000", "150000-180000"},
		{"compensation-includes-salary-bonus-equity", "Compensation\nIncludes salary, bonus and equity\n$150,000 - $180,000", "150000-180000"},
		{"total-target-compensation-base-bonus", "Total target compensation (base + bonus): $200,000 - $240,000", "200000-240000"},
		{"pay-range-nyc-then-bonus-range", "The pay range for NYC is $150,000 - $180,000 and bonus up to $20,000 - $30,000.", "150000-180000"},
	} {
		ps := ParsePay(c[1])
		got := ""
		if len(ps) > 0 {
			got = fmt.Sprintf("%.0f-%.0f", ps[0].AnnualMin, ps[0].AnnualMax)
		}
		if got != c[2] {
			t.Errorf("%s: got %q want %q", c[0], got, c[2])
		}
	}
}
