package digest

import (
	"strings"
	"testing"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func payRec(desc string) ledger.Record {
	r := rec("gh:a:1", posting.Explained, "Engineer", map[string]string{"rank": "80"})
	r.Posting.Description = desc
	return r
}

// payLines returns the digest's "- Pay:" lines for one role with desc.
func payLines(t *testing.T, desc string) (string, []string) {
	t.Helper()
	md, _ := Render([]ledger.Record{payRec(desc)}, nil, day, Counts{})
	var out []string
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "- Pay:") {
			out = append(out, l)
		}
	}
	return md, out
}

func TestPayGolden(t *testing.T) {
	md, _ := Render([]ledger.Record{payRec("We build tools.\nThe base salary range for this role is $120,000 - $160,000 per year.")}, nil, day, Counts{New: 1})
	want := "# Vani digest 2026-10-18\n\n" +
		"## Ranked roles (1)\n\n" +
		"### Engineer at Acme\n\n" +
		"- Where: Berlin\n" +
		"- Pay: USD 120k–160k/yr\n" +
		"- Link: <https://example.com/jobs/gh:a:1>\n" +
		"- Must-have coverage: 80%\n\n" +
		"---\n\n" +
		"New today: 1 | Dropped today: 0 | Dead today: 0\n"
	if md != want {
		t.Errorf("digest:\n%s\nwant:\n%s", md, want)
	}
}

func TestPayLine(t *testing.T) {
	for _, c := range []struct {
		name, desc string
		want       []string
	}{
		{"one range", "Compensation: $120,000 – $160,000 a year, plus equity.",
			[]string{"- Pay: USD 120k–160k/yr"}},
		{"decimal amounts are escaped", "Salary range: €153.200 – €180.000 per year.",
			[]string{`- Pay: EUR 153\.2k–180k/yr`}},
		{"two currencies, in order of appearance", "Base salary: £80,000 - £95,000 in London; $140,000 - $170,000 in New York.",
			[]string{"- Pay: GBP 80k–95k/yr; USD 140k–170k/yr"}},
		{"hourly", "Pay rate: $45 - $60 per hour depending on experience.",
			[]string{"- Pay: USD 45–60/hr"}},
		{"none stated", "We pay competitively. 401(k) match up to 4%. Learning budget $1,500 per year.",
			nil},
		{"empty description", "", nil},
		{"amounts that are not pay", "We raised $50M in our Series B. Signing bonus: $10,000.", nil},
	} {
		md, got := payLines(t, c.desc)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: pay lines %q, want %q\n%s", c.name, got, c.want, md)
		}
		if len(got) == 1 {
			// The pay line sits right under Where:, before Link:.
			w, p, l := strings.Index(md, "- Where:"), strings.Index(md, "- Pay:"), strings.Index(md, "- Link:")
			if !(w < p && p < l) {
				t.Errorf("%s: pay line out of place:\n%s", c.name, md)
			}
		}
	}
}

func TestPayHostileDescription(t *testing.T) {
	hostile := strings.Join([]string{
		"\x00\x1b[31mSalary\x1b[0m: $120,000 - $160,000 per year\x07\r\n\x7f ",
		"# Injected heading\n[click](javascript:alert(1)) <script>x</script>",
		"Salary: $" + strings.Repeat("9", 5000) + " - $" + strings.Repeat("1", 40),
		"Pay: $" + strings.Repeat("1,000", 2000),
		strings.Repeat("$£€₹¥", 20000),
		"Salary: USD 1e9 - USD 9e99 per year",
		strings.Repeat("Salary: $1\u202e00,000 - $2\u200b00,000 ", 500),
	}, "\n")
	md, got := payLines(t, hostile)
	// The one honest range survives; the junk adds nothing.
	if len(got) != 1 || got[0] != "- Pay: USD 120k–160k/yr" {
		t.Errorf("pay lines %q:\n%.2000s", got, md)
	}
	if strings.ContainsFunc(md, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) {
		t.Error("control character in digest")
	}
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "# Vani") && !strings.HasPrefix(l, "## ") && !strings.HasPrefix(l, "### ") {
			t.Errorf("stray heading line %q", l)
		}
	}
	if strings.Contains(md, "<script>") || strings.Contains(md, "](javascript") {
		t.Errorf("description content leaked into the digest:\n%.2000s", md)
	}

	// Only junk: no pay line at all, never a guess.
	_, got = payLines(t, strings.Repeat("$", 1<<16)+strings.Repeat("9", 1<<16))
	if len(got) != 0 {
		t.Errorf("junk read as pay: %q", got)
	}
}

// The digest reads pay through jobinsight.PostingPay, so a bare "$" on a
// role in Canada is Canadian dollars there, as in the Jobs list.
func TestPayUsesPostingLocation(t *testing.T) {
	r := payRec("Pay range: $114,400.00 - $164,400.00")
	r.Posting.Location = "Markham, Ontario, Canada"
	md, _ := Render([]ledger.Record{r}, nil, day, Counts{})
	if !strings.Contains(md, `- Pay: CAD 114\.4k–164\.4k/yr`+"\n") {
		t.Errorf("digest:\n%s", md)
	}
	r.Posting.Location = "Austin, Texas"
	if md, _ := Render([]ledger.Record{r}, nil, day, Counts{}); !strings.Contains(md, `- Pay: USD 114\.4k–164\.4k/yr`+"\n") {
		t.Errorf("digest:\n%s", md)
	}
}

func TestPayLineEscapesEverything(t *testing.T) {
	// payLine's output is fixed-form, but it still goes through Escape: no
	// Markdown metacharacter reaches the digest unescaped.
	got := payLine(posting.Posting{Description: "Salary: $100,000.50 - $120,000.75 per year."})
	if got == "" {
		t.Fatal("no pay read")
	}
	if got != Escape(strings.ReplaceAll(got, `\`, ""), maxFieldRunes) {
		t.Errorf("payLine %q is not escaped", got)
	}
}
