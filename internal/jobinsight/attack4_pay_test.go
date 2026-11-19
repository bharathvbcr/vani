package jobinsight

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const a4Salary = "Base salary: $150,000 - $180,000. "

// Round-3 attack tests for ft-11d0. Each case is one realistic posting.
func TestAttack4BonusNotRead(t *testing.T) {
	usd := []payWant{{"USD", Year, 150000, 180000}}
	bonuses := []string{
		"Our annual bonus target range is $10,000 - $20,000.",
		"Equity: typical grant range $50,000 - $90,000.",
		"Commission earnings between $20,000 and $40,000.",
		"Sign-on bonus range: $10,000 - $20,000.",
		"The target bonus is between $10,000 and $20,000.",
		"Bonus: the expected range for this role is $10,000 to $20,000.",
		"Bonus (target) range: $10,000 - $20,000.",
	}
	for i, b := range bonuses {
		for _, layout := range []string{"before", "after", "list"} {
			var text string
			switch layout {
			case "before":
				text = "About the role. We are hiring.\n" + a4Salary + "\nBenefits: health, dental. " + b
			case "after":
				text = "About the role. We are hiring.\n" + b + " " + a4Salary + "\nBenefits: health."
			case "list":
				text = "Compensation\n\u2022 " + b + "\n\u2022 " + a4Salary + "\n"
			}
			t.Run(strings.ReplaceAll(layout+"/"+b, " ", "_")+string(rune('a'+i)), func(t *testing.T) {
				checkPays(t, b, ParsePay(text), usd)
			})
		}
	}
}

func TestAttack4GenericSalaryKept(t *testing.T) {
	usd := []payWant{{"USD", Year, 150000, 180000}}
	pre := "We offer an annual bonus and equity for all staff. "
	salaries := []string{
		"Compensation range: $150,000 - $180,000",
		"The expected range for this role is $150,000 to $180,000",
		"Starting between $150,000 and $180,000",
		"The typical range for this role is $150,000 - $180,000",
		"Our target range is $150,000 - $180,000",
		"Salary range between $150,000 and $180,000",
		"Expected compensation range: $150,000 - $180,000",
	}
	joins := []string{" ", "\n", "\n\n", "\r\n", "\u2028", "\n\u2022 "}
	for _, s := range salaries {
		for _, j := range joins {
			text := pre + j + s
			t.Run(s+"|"+strings.NewReplacer("\n", "N", "\r", "R", "\u2028", "L", "\u2022", "B").Replace(j), func(t *testing.T) {
				checkPays(t, text, ParsePay(text), usd)
			})
		}
	}
	// bonus without a stop before it, same line, then a generic-only salary
	for _, text := range []string{
		"Includes bonus and equity. Compensation range: $150,000 - $180,000",
		"Bonus and equity eligible\nCompensation range: $150,000 - $180,000",
		"Equity: yes\nThe expected range for this role is $150,000 to $180,000",
		"Equity and compensation range: $150,000 - $180,000",
		"Bonus eligible; starting between $150,000 and $180,000",
		"Bonus eligible - starting between $150,000 and $180,000",
		"Bonus eligible (the expected range for this role is $150,000 - $180,000)",
		"Stock: the expected range for this role is $150,000 - $180,000",
	} {
		t.Run(text, func(t *testing.T) { checkPays(t, text, ParsePay(text), usd) })
	}
}

func TestAttack4Multibyte(t *testing.T) {
	hr := []payWant{{"USD", Hour, 90, 110}}
	pads := []string{"", "\u00a0", "\u202f", "\u3000", "e\u0301", "\U0001F600", "\u4e2d\u6587", "\u0e01\u0e33", "\ufffd", "\xff", "\u200b"}
	for _, pre := range pads {
		for _, sp := range []string{" ", "\u00a0", "\u202f", "\u3000", "\u2009", "\u1680"} {
			text := strings.Repeat(pre, 40) + "Pay: $90 - $110" + sp + "per" + sp + "hour" + pre + " USD"
			t.Run("pre="+pre+"|sp="+sp, func(t *testing.T) {
				got := ParsePay(text)
				checkPays(t, text, got, hr)
				for _, p := range got {
					if !utf8.ValidString(p.Text) && utf8.ValidString(text) {
						t.Errorf("invalid utf8 in Text %q", p.Text)
					}
				}
			})
		}
	}
	// the period word span: Text must end exactly after the period word
	text := "Pay $90 - $110\u3000\u00a0\u202fper\u00a0hour, plus things"
	got := ParsePay(text)
	if len(got) != 1 || !strings.HasSuffix(got[0].Text, "hour") {
		t.Errorf("span: %+v", got)
	}
	// CJK and emoji between the amount and the period word break the filler rule
	for _, s := range []string{"Pay $90 - $110 \u4e2d\u6587 per hour"} {
		for _, p := range ParsePay(s) {
			if p.Period == Hour {
				t.Errorf("%q: read hour through foreign text", s)
			}
		}
	}
}

func TestAttack4FoldInvariants(t *testing.T) {
	for _, win := range []string{"", " ", "\u00a0", "\xff", "a\xffb", "\u3000\u3000", "PER\u202fHOUR", "e\u0301\u0301", "\U0001F600\U0001F600", "\u2028\u0085\u2029", "-_-"} {
		low, at := foldPeriodWindow(win)
		if len(at) != len(low)+1 {
			t.Errorf("%q: len(at)=%d len(low)=%d", win, len(at), len(low))
		}
		prev := 0
		for i, a := range at {
			if a < prev || a > len(win) {
				t.Errorf("%q: at[%d]=%d (prev %d, len %d)", win, i, a, prev, len(win))
			}
			prev = a
		}
	}
}

func FuzzAttack4Fold(f *testing.F) {
	for _, s := range []string{"per hour", "PER\u00a0HOUR", "\u3000\u202f", "e\u0301", "\U0001F600 per\u2028hour", "\xff\xfe", "-/hr", "\u0085"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, win string) {
		low, at := foldPeriodWindow(win)
		if len(at) != len(low)+1 {
			t.Fatalf("len(at)=%d len(low)=%d", len(at), len(low))
		}
		prev := 0
		for _, a := range at {
			if a < prev || a > len(win) {
				t.Fatalf("offset %d out of order/range (prev %d, len %d)", a, prev, len(win))
			}
			prev = a
		}
		// readPeriod on text with the window: offsets must stay inside the text
		text := "$90 - $110" + win
		_, ne, far := readPeriod(text, len("$90 - $110"))
		if ne < len("$90 - $110") || ne > len(text) {
			t.Fatalf("nearEnd %d outside text of %d", ne, len(text))
		}
		for _, fa := range far {
			if fa.end < len("$90 - $110") || fa.end > len(text) {
				t.Fatalf("far end %d outside", fa.end)
			}
		}
	})
}

func FuzzAttack4ParsePay(f *testing.F) {
	for _, s := range []string{"Bonus range: $1,000 - $2,000 per\u00a0hour", "Base salary $90 - $110\u202f/hr", "up to$180,000", "NT$1,000", "\u3000Equity\ttarget range $5 - $9\u2028per hour"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, p := range ParsePay(s) {
			if p.Min > p.Max || p.AnnualMin > p.AnnualMax || p.Min <= 0 {
				t.Fatalf("bad pay %+v", p)
			}
		}
	})
}

func TestAttack4Glued(t *testing.T) {
	for w := range glued {
		for _, form := range []string{w, strings.ToUpper(w), strings.ToUpper(w[:1]) + w[1:]} {
			text := "Salary " + form + "$150,000 - " + form + "$180,000"
			got := ParsePay(text)
			if form == w {
				checkPays(t, text, got, []payWant{{"USD", Year, 150000, 180000}})
			} else if len(got) != 0 {
				t.Logf("%q (non-lowercase glued) -> %+v", text, got)
			}
		}
		// glued word, then a real range partner with a plain dollar
		text := "Salary " + w + "$150,000 - $180,000"
		checkPays(t, text, ParsePay(text), []payWant{{"USD", Year, 150000, 180000}})
	}
}

// Real foreign prefixes that collide with a glued word in some case.
func TestAttack4GluedForeignCollision(t *testing.T) {
	for _, text := range []string{
		"Salary AS$150,000 - AS$180,000", // not a code
		"Salary BE$150,000 - BE$180,000",
		"Salary IN$150,000 - IN$180,000",
		"Salary OR$150,000 - OR$180,000",
		"Salary PER$150,000 - PER$180,000",
		"Salary AN$150,000 - AN$180,000",
		"Salary AT$150,000 - AT$180,000",
		"Salary ON$150,000 - ON$180,000",
		"Salary BY$150,000 - BY$180,000",
		"Salary IS$150,000 - IS$180,000",
		"Salary GET$150,000 - GET$180,000",
		"Salary PAY$150,000 - PAY$180,000",
		"Salary Per$150,000 - Per$180,000",
	} {
		if got := ParsePay(text); len(got) != 0 {
			t.Errorf("%q: read %+v", text, got)
		}
	}
	// codes that are real currency codes and glued words in some case: any case works
	for _, c := range []struct{ text, cur string }{
		{"Salary all$150,000 - all$180,000", "ALL"},
		{"Salary try$150,000 - try$180,000", "TRY"},
		{"Salary cad$150,000 - cad$180,000", "CAD"},
	} {
		if got := ParsePay(c.text); len(got) > 1 {
			t.Errorf("%q: %+v", c.text, got)
		}
	}
}

func TestAttack4Linear(t *testing.T) {
	units := []string{
		"Bonus target range is between $1 - $2 \u00a0 per\u00a0hour. ",
		"Equity range, " + strings.Repeat("the ", 50) + "$80 - $120\u3000",
		strings.Repeat("bonus target of range ", 20) + "$80 - $120\n",
		"$80 - $120" + strings.Repeat("\u00a0", 200) + "\n",
		"range range range range range range range range range range range range $80-$120 ",
		strings.Repeat("e\u0301", 100) + "$5-$9 ",
	}
	for _, u := range units {
		s := strings.Repeat(u, (1<<20)/len(u))
		st := time.Now()
		_ = ParsePay(s)
		if d := time.Since(st); d > 2*time.Second {
			t.Errorf("%.30q: %v for 1 MiB", u, d)
		}
	}
}
