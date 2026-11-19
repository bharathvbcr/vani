package jobinsight

import "testing"

// These tests pin KNOWN WRONG behaviour of the pay parser (ft-11d0 item 26
// follow-up). Each phrase below states a bonus, equity or commission amount, so
// the correct reading is no pay at all; the current parser reads it as the annual
// salary. They are pinned, not endorsed, so that any future change to how these
// phrases read is noticed. internal/jobview/select.go (lines 75-79) uses pay for
// the min-pay filter, so a bonus read as a low salary can hide a posting from the
// owner: that is why the misreads are pinned rather than left unwatched. When the
// parser is fixed, flip each case to expect no pay and delete the known-wrong note.
// The old parser (b5831fa) read nothing for all eight.
func TestPayKnownMisreads(t *testing.T) {
	cases := []struct {
		text   string
		lo, hi float64
	}{
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Bonus: 10% ($10,000 - $20,000)", 10000, 20000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Total compensation includes an annual bonus of $10,000 - $20,000", 10000, 20000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Compensation includes a sign-on bonus of $10,000 - $20,000", 10000, 20000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Compensation also includes equity of $50,000 - $100,000", 50000, 100000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Our compensation package includes a signing bonus of $10,000 - $20,000", 10000, 20000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Comp includes commission of $20,000 - $40,000", 20000, 40000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"Total compensation: base plus equity of $50,000 - $100,000", 50000, 100000},
		// CORRECT reading: no pay. Known wrong: ft-11d0 item 26 follow-up.
		{"On-target earnings from commission $20,000 - $40,000", 20000, 40000},
	}
	for _, c := range cases {
		got := ParsePay(c.text)
		if len(got) != 1 {
			t.Errorf("%q: got %d pay readings %+v, today's (wrong) behaviour is exactly one", c.text, len(got), got)
			continue
		}
		p := got[0]
		if p.Currency != "USD" || p.Period != "year" || p.AnnualMin != c.lo || p.AnnualMax != c.hi {
			t.Errorf("%q: got %+v, today's (wrong) behaviour is annual USD %v-%v", c.text, p, c.lo, c.hi)
		}
	}
}
