package jobinsight

import (
	"fmt"
	"testing"
)

// Real-posting compensation sections (headings, bullets, transparency
// boilerplate, location ranges) that the sentence-scoped rule reads right.
func TestAttack6PaySections(t *testing.T) {
	for _, c := range [][3]string{
		{"comp-section-order", "Compensation\nSalary Range\n$150,000 - $180,000\nBonus\nUp to 15%\nEquity\nStock options", "150000-180000"},
		{"heading-base-salary", "Base Salary\n$150,000 - $180,000", "150000-180000"},
		{"heading-bonus", "Bonus\n$10,000 - $20,000", ""},
		{"bonus-then-salary-lines", "Compensation\nBonus: $10,000 - $20,000\nSalary: $150,000 - $180,000", "150000-180000"},
		{"ca-transparency", "The base salary range for this position in California is $150,000 - $180,000 per year.", "150000-180000"},
		{"annual-base-bonus-elig", "Annual base salary range: $150,000 - $180,000. Bonus eligible. Equity.", "150000-180000"},
		{"pay-range-then-signing", "Pay Range\n$120,000 - $150,000 USD\nSigning bonus\n$20,000 - $30,000", "120000-150000"},
		{"401k-then-salary", "Benefits\n401(k) match up to 4%\nSalary\n$150,000 - $180,000", "150000-180000"},
		{"equity-bonus-base-salary-sentence", "Compensation: Equity and bonus eligible. Base salary $150,000 - $180,000", "150000-180000"},
		{"two-locations", "Location: NYC\nSalary range $160,000 - $190,000\nLocation: Remote\nSalary range $140,000 - $170,000", "140000-190000"},
		{"bonus-and-salary-heading", "Bonus and Salary\n$150,000 - $180,000", "150000-180000"},
		{"wellness-then-salary", "Benefits: Wellness stipend of $1,000. Salary: $150,000 - $180,000", "150000-180000"},
		{"learning-budget-lines", "Learning budget\n$2,500\nSalary\n$150,000-$180,000", "150000-180000"},
		{"equity-compensation-range", "Equity compensation range: $100,000 - $200,000", ""},
		{"equity-heading", "Equity (RSUs)\n$150,000 - $180,000", ""},
		{"equity-grant-valued", "Equity grant valued at $150,000 - $180,000", ""},
		{"all-three-lines", "Base Salary: $150,000 - $180,000\nBonus: $10,000 - $20,000\nEquity: $100,000 - $200,000", "150000-180000"},
		{"bonus-target-then-base-range", "Annual Bonus Target\nBase Salary Range\n$150,000-$180,000", "150000-180000"},
		{"signing-bonus-then-base", "Compensation Overview\nSigning Bonus\nBase Salary\n$150,000 - $180,000", "150000-180000"},
		{"levels-bullets", "Salary:\n- Level 1: $100,000 - $120,000\n- Level 2: $130,000 - $150,000", "100000-150000"},
		{"total-comp-includes", "Total Compensation Range for this role: $200,000 – $260,000, which includes base salary, bonus and equity", "200000-260000"},
		{"between-and", "The pay range for this role is between $150,000 and $180,000 per year, plus equity.", "150000-180000"},
		{"equity-bonus-comp-lines", "Equity\nBonus\nCompensation\n$150,000 - $180,000", "150000-180000"},
		{"equity-salary-label", "Equity\nSalary: $150,000 - $180,000", "150000-180000"},
		{"bonus-structure", "Bonus structure: sales commission. Pay: $150,000 - $180,000 OTE", "150000-180000"},
		{"bonus-up-to-then-base", "Bonus: Up to 10%. Base salary: $120,000 - $150,000.", "120000-150000"},
		{"salary-section-empty-then-bonus-amount", "Salary Range\n\n$150,000 - $180,000\n\nBonus\n\nUp to $20,000", "150000-180000"},
		{"eligible-for-bonus-salary-range", "Eligible for bonus. Salary range: $150,000 - $180,000", "150000-180000"},
		{"bonus-hyphen-eligible-salary", "Bonus-eligible position. The expected salary is $150,000 - $180,000.", "150000-180000"},
		{"comp-range-bonus-bullet-before", "- Bonus eligible\n- Equity\n- Salary range: $150,000 - $180,000", "150000-180000"},
		{"stock-comp-salary-after", "Stock options and salary range: $150,000 - $180,000", "150000-180000"},
		{"hourly-bonus", "Hourly: $50 - $60\nBonus\n$5,000 - $8,000", "104000-124800"},
		{"equity-and-salary-range", "Equity and salary range: $150,000 - $180,000", "150000-180000"},
		{"bonus-plan-salary-range", "Bonus plan\nBase salary range: $150,000 - $180,000", "150000-180000"},
		{"bonus-colon-then-salary-range-lowercase", "bonus: 10% salary range $150,000 - $180,000", "150000-180000"},
		{"period-emoji", "Salary: $150,000 - $180,000 💰 per year", "150000-180000"},
		{"period-currency-sign", "Pay: $50 - $60 € per hour", "104000-124800"},
		{"hourly-usd-per-hour", "Hourly rate: $50 - $60 USD per hour", "104000-124800"},
		{"hourly-newline-per-hour", "Pay Range\n$50 - $60\nPer Hour", "104000-124800"},
		{"salary-trailing-dash-per-year", "Salary: $150,000 - $180,000 - per year", "150000-180000"},
		{"salary-paren-usd", "Salary Range: ($150,000 - $180,000 USD)", "150000-180000"},
		{"salary-range-paren-open-unclosed", "Salary Range (USD: $150,000 - $180,000", "150000-180000"},
		{"bonus-paren-closed-salary-eq", "Bonus (10%) Salary Range\n$150,000 - $180,000", "150000-180000"},
		{"salary-range-bonus-sentence-break", "Salary range for bonus-eligible roles.\n$150,000 - $180,000", "150000-180000"},
		{"compensation-colon-bonus-sentence", "Compensation: bonus. $150,000 - $180,000", "150000-180000"},
		{"ny-range-and-ca-range", "Salary range in NY: $150,000 - $180,000; in CA: $160,000 - $190,000", "150000-190000"},
		{"ranges-bonus-between", "Annual bonus range is between $10,000 and $20,000. Salary range is between $150,000 and $180,000.", "150000-180000"},
		{"comp-range-equity-after-first", "Compensation range: $150,000 - $180,000 + equity $100,000 - $150,000", "150000-180000"},
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
