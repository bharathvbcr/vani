package jobinsight

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestPostingPay(t *testing.T) {
	year := func(cur string, lo, hi float64) []payWant { return []payWant{{cur, Year, lo, hi}} }
	// Verbatim from Qualcomm's Markham posting 446718103204 (Eightfold,
	// 2026-10-20): a bare "$" in Canada.
	const markham = "Pay range: $114,400.00 - $164,400.00"
	for _, c := range []struct {
		name, loc string
		remote    bool
		desc      string
		want      []payWant
	}{
		{"canada", "Markham, Ontario, Canada", false, markham, year("CAD", 114400, 164400)},
		{"canada, two places", "Markham, Ontario, Canada; Toronto, Ontario, Canada", false, markham, year("CAD", 114400, 164400)},
		{"bare country", "Canada", false, markham, year("CAD", 114400, 164400)},
		{"australia", "Sydney, NSW, Australia", false, "Salary: $150,000 - $180,000", year("AUD", 150000, 180000)},
		{"new zealand", "Auckland, New Zealand", false, "Salary: $120,000 - $140,000", year("NZD", 120000, 140000)},
		{"singapore alone", "Singapore", false, "Salary: $90,000 - $120,000", year("SGD", 90000, 120000)},
		{"remote in canada", "Remote, Canada", false, markham, year("CAD", 114400, 164400)},
		{"place and remote scope agree", "Toronto, Canada; Remote, Canada", false, markham, year("CAD", 114400, 164400)},

		{"explicit code wins", "Markham, Ontario, Canada", false, "Salary: $100,000 - $120,000 USD", year("USD", 100000, 120000)},
		{"explicit code on one end wins", "Markham, Ontario, Canada", false, "Salary: $100,000 - US$120,000", year("USD", 100000, 120000)},
		{"US$ is not bare", "Markham, Ontario, Canada", false, "Salary: US$100,000 - US$120,000", year("USD", 100000, 120000)},
		{"CAD $ still CAD in the US", "Austin, Texas", false, "Salary: CAD $100,000 - CAD $120,000", year("CAD", 100000, 120000)},
		{"euro unaffected", "Markham, Ontario, Canada", false, "Salary: €60,000 - €80,000", year("EUR", 60000, 80000)},
		{"pound unaffected", "Toronto, Canada", false, "Salary: £60,000 - £80,000", year("GBP", 60000, 80000)},
		{"yen unaffected", "Toronto, Canada", false, "Salary: ¥5,000,000 - ¥8,000,000", year("JPY", 5000000, 8000000)},

		{"us and canada", "Austin, Texas; Markham, Ontario, Canada", false, markham, year("USD", 114400, 164400)},
		{"canada and singapore", "Toronto, Canada; Singapore", false, markham, year("USD", 114400, 164400)},
		{"province only", "Markham, ON", false, markham, year("USD", 114400, 164400)},
		{"bare city", "Markham", false, markham, year("USD", 114400, 164400)},
		{"empty location", "", false, markham, year("USD", 114400, 164400)},
		{"remote, no scope", "Remote", true, markham, year("USD", 114400, 164400)},
		{"remote across us and canada", "Remote (US or Canada)", false, markham, year("USD", 114400, 164400)},
		{"canada place, remote anywhere", "Toronto, Canada; Remote - Anywhere", false, markham, year("USD", 114400, 164400)},
		{"united states", "New York, NY, United States", false, markham, year("USD", 114400, 164400)},
		{"placeholder", "N/A", false, markham, year("USD", 114400, 164400)},
		// A hint never makes an amount pay: CAD has USD's bounds.
		{"out of bounds stays out", "Toronto, Canada", false, "Salary: $50 - $60", nil},
		{"no pay", "Toronto, Canada", false, "We build compilers.", nil},
	} {
		p := posting.Posting{Location: c.loc, Remote: c.remote, Description: c.desc}
		checkPays(t, c.name, PostingPay(p), c.want)
	}
}

// ParsePay has no location: a bare "$" is still USD there.
func TestParsePayStaysUSD(t *testing.T) {
	checkPays(t, "text only", ParsePay("Markham, Ontario, Canada. Pay range: $114,400.00 - $164,400.00"),
		[]payWant{{"USD", Year, 114400, 164400}})
}

func TestPostingPayOversized(t *testing.T) {
	p := posting.Posting{
		Location:    strings.Repeat("Toronto, Canada; ", 1<<14),
		Description: strings.Repeat("$", 1<<16) + " Salary: $100,000 - $120,000",
	}
	_ = PostingPay(p) // bounded and must not panic
	p.Description = "Salary: $100,000 - $120,000"
	checkPays(t, "many canadian places", PostingPay(p), []payWant{{"CAD", Year, 100000, 120000}})
	p.Location = strings.Repeat("x", 1<<20)
	checkPays(t, "huge junk location", PostingPay(p), []payWant{{"USD", Year, 100000, 120000}})
}

// The currencies AMD's careers site lists pay in (2026-10-20), as the Jibe
// adapter's pay line renders them, and the bounds around them.
func TestAddedCurrencies(t *testing.T) {
	year := func(cur string, lo, hi float64) []payWant { return []payWant{{cur, Year, lo, hi}} }
	for _, c := range []struct {
		name, text string
		want       []payWant
	}{
		{"twd", "Pay range: TWD 430430 – 614900 per year", year("TWD", 430430, 614900)},
		{"myr", "Pay range: MYR 37800 – 54000 per year", year("MYR", 37800, 54000)},
		{"cny with ¥", "Salary: CNY ¥156,170 - ¥223,100 per year", year("CNY", 156170, 223100)},
		{"krw", "Pay range: KRW 80570000 – 115100000 per year", year("KRW", 80570000, 115100000)},
		{"ron", "Pay range: RON 85190 – 121700 per year", year("RON", 85190, 121700)},
		{"rsd", "Pay range: RSD 2979340 – 4256200 per year", year("RSD", 2979340, 4256200)},
		{"sar", "Pay range: SAR 316680 – 452400 per year", year("SAR", 316680, 452400)},
		{"twd top of AMD", "Pay range: TWD 3748150 – 5354500 per year", year("TWD", 3748150, 5354500)},
		{"krw too small", "Salary: KRW 5,000 - 8,000 per year", nil},
		{"myr too large", "Salary: MYR 50,000,000 - 60,000,000 per year", nil},
		{"rsd monthly", "Plata: RSD 250,000 - 350,000 per month", []payWant{{"RSD", Month, 250000, 350000}}},
		{"armenian dram is not read", "Pay range: AMD 18142110 – 25917300 per year", nil},
		{"two new currencies", "Salary: TWD 1,000,000 - 1,500,000; MYR 100,000 - 150,000", []payWant{{"TWD", Year, 1000000, 1500000}, {"MYR", Year, 100000, 150000}}},
	} {
		checkPays(t, c.name, ParsePay(c.text), c.want)
	}
	for _, cur := range []string{"TWD", "MYR", "CNY", "KRW", "RON", "RSD", "SAR"} {
		if PayStep(cur) <= 0 {
			t.Errorf("%s has no stats step", cur)
		}
	}
	for _, cur := range PayCurrencies() {
		if cur == "AMD" {
			t.Error("AMD is a currency")
		}
	}
}
