package rkbresume

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Adversarial cases for the contact rules in checkInstruction. Inputs are
// synthetic shapes only. Expectations follow the contract: a separator run
// joins only up to three runes, a year range is exactly two ASCII years with
// one hyphen or en dash and no spaces, and an email is an '@' with a non-space
// non-'@' rune on both sides after Unicode spaces are skipped.

func TestPhoneSeparatorRunBoundaries(t *testing.T) {
	if phoneMaxSeparator != 3 {
		t.Fatalf("phoneMaxSeparator = %d, contract caps a joining run at 3", phoneMaxSeparator)
	}
	left, right := "555", "4567" // seven digits once joined
	for _, sepRune := range []string{"-", " ", ".", "\u2013", "("} {
		for n := 0; n <= 4; n++ {
			in := left + strings.Repeat(sepRune, n) + right
			want := n <= 3 // 0 merges into one group of seven; 4 must not join
			if got := phoneLike(in); got != want {
				t.Errorf("phoneLike(%q) = %v, want %v (sep %q len %d)", in, got, want, sepRune, n)
			}
		}
	}
	// Mixed runs. " - " / " --" / "---" are three runes and join; a fourth does not.
	mixed := []struct {
		in   string
		want bool
	}{
		{"555 - 4567", true},
		{"555 --4567", true},
		{"555-- 4567", true},
		{"555---4567", true},
		{"555 - -4567", false}, // " - -" is four; three are consumed and the next '-' blocks
		{"555 -- 4567", false}, // " -- " is four
		{"555 ---4567", false},
		{"555--- 4567", false},
		{"555 --- 4567", false},
		{"555----4567", false},
		{"12 - 34 - 567", true},    // 2+2+3, each gap is " - "
		{"12 -- 34 -- 567", false}, // each gap is " -- "
		{"12---34---567", true},    // three hyphens join; 2+2+3
		{"12----34----567", false},
		{"555   4567", true},
		{"555    4567", false},
		{"555...4567", true},
		{"555....4567", false},
		{"555\u2013\u2013\u20134567", true},
		{"555\u2013\u2013\u2013\u20134567", false},
		{"(555) (1234)", true},   // ") (" is three; 3+4 digits
		{"(555) (123)", false},   // same gap, only six digits
		{"(555)  (1234)", false}, // ")  (" is four
		{"555(((4567", true},
		{"555((((4567", false},
	}
	for _, c := range mixed {
		if got := phoneLike(c.in); got != c.want {
			t.Errorf("phoneLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPhoneUnicodeDigitsDoNotFalsePositive(t *testing.T) {
	// unicode.IsDigit counts Nd, so seven non-ASCII digits are refused (safe).
	// Fewer than seven, or numbers that are not Nd, stay allowed.
	cases := []struct {
		in   string
		want bool
	}{
		{"１２３４５６７", true}, // seven fullwidth
		{"１２３４５６", false}, // six fullwidth
		{"١٢٣٤٥٦٧", true}, // seven Arabic-Indic
		{"١٢٣٤٥٦", false},
		{"१२३४५६७", true}, // seven Devanagari
		{"१२३४५६", false},
		{"section ١ of the notes", false},
		{"123４５６７", true},  // ASCII plus fullwidth, seven Nd runes in one group
		{"123456²", false}, // superscript two is not Nd
		{"version 3²", false},
		{"①②③④⑤⑥⑦", false},      // circled digits are not Nd
		{"٢٠١٩-٢٠٢٤", true},     // eight Nd, not an ASCII year range
		{"٥٥٥ --- ٤٥٦٧", false}, // four-plus separator, both sides under seven
		{"2019-2024", false},
	}
	for _, c := range cases {
		if got := phoneLike(c.in); got != c.want {
			t.Errorf("phoneLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPhoneParenthesesPlusAndDots(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"(555) 123-4567", true},
		{"+1 555 123 4567", true},
		{"+1 (555) 123-4567", true},
		{"+1.555.123.4567", true},
		{"555.123.4567", true},
		{"((555)) 123-4567", true},
		{"555+123456", false}, // '+' is not a separator; neither side has seven
		{"555+1234567", true}, // the seven-digit group stands alone
		{"+5554567", true},
		{"3.12", false},
		{"Go 1.26", false},
		{"12.34.56", false}, // six digits
		{"12.34.567", true}, // seven
		{"100,000", false},
		{"1,000,000", false}, // comma does not join
		{"1.000.000", true},  // dot does
		{"100.0000", true},   // 3+4
	}
	for _, c := range cases {
		if got := phoneLike(c.in); got != c.want {
			t.Errorf("phoneLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPhoneYearBoundaries(t *testing.T) {
	// Exactly two groups, separator exactly "-" or "–" with no spaces, both
	// four ASCII digits in 1900..2099. Anything else with seven or more joined
	// digits is a phone. An em dash or a minus does not join.
	cases := []struct {
		in   string
		want bool
	}{
		{"1899-2024", true},
		{"1900-2024", false},
		{"2099-1900", false},
		{"2100-2024", true},
		{"1899-1900", true},
		{"1900-1899", true},
		{"2099-2100", true},
		{"2100-2099", true},
		{"1900-2099", false},
		{"1899-2099", true},
		{"1900-2100", true},
		{"1899-1899", true},
		{"1900-1900", false},
		{"2099-2099", false},
		{"2100-2100", true},
		{"0190-2024", true}, // 0190 is four digits but 190, not a year
		{"1900-209", true},  // seven digits; the second group is not four
		{"1900-20", false},  // six digits, and not two years
		{"2019-2024", false},
		{"2019\u20132024", false}, // en dash
		{"1900\u20132099", false},
		{"1899\u20131900", true},
		{"2099\u20132100", true},
		{"2100\u20131900", true},
		{"2019\u20142024", false}, // em dash joins nothing
		{"2019\u22122024", false}, // minus sign joins nothing
		{"2019 - 2024", true},
		{"2019- 2024", true},
		{"2019 -2024", true},
		{"2019 2024", true},
		{"2019  2024", true},
		{"2019   2024", true},
		{"2019    2024", false}, // four spaces do not join
		{"2019\u2013 2024", true},
		{"2019 \u20132024", true},
		{"2019 \u2013 2024", true},
		{"2019\u2014 2024", false},
		{"2019 \u20142024", false},
		{"1900--2099", true},
		{"1900---2099", true},
		{"1900----2099", false},
		{"1900-2099-1", true}, // three groups; the two-year exception does not apply
		{"20192024", true},    // eight digits, no separator, not a range
		{"from 2019-2024 on", false},
		{"from 2019 - 2024 on", true},
		{"worked 2019-2024, call 5551234", true}, // the year does not whitelist the rest
	}
	for _, c := range cases {
		if got := phoneLike(c.in); got != c.want {
			t.Errorf("phoneLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPhoneWindowEdges(t *testing.T) {
	if phoneWindow != 20 || phoneDigits != 7 {
		t.Fatalf("window %d digits %d, contract is 20 and 7", phoneWindow, phoneDigits)
	}
	// Span is the rune distance from the first digit's start to the exclusive
	// end of the last. Equal to the window still counts; one over does not,
	// when no seven-digit subset fits inside the window.
	fit := chainDigits('1', []string{"---", "--", "--", "--", "--", "--"})   // 7 + 13 = 20
	over := chainDigits('1', []string{"---", "---", "--", "--", "--", "--"}) // 7 + 14 = 21
	if n := utf8.RuneCountInString(fit); n != phoneWindow {
		t.Fatalf("fit span = %d, want %d", n, phoneWindow)
	}
	if n := utf8.RuneCountInString(over); n != phoneWindow+1 {
		t.Fatalf("over span = %d, want %d", n, phoneWindow+1)
	}
	spread := "1 - 2 - 3 - 4 - 5 - 6 - 7" // seven digits, span 25
	cases := []struct {
		in   string
		want bool
	}{
		{fit, true},
		{over, false},
		{spread, false},
		{strings.Repeat("9", phoneDigits-1), false},
		{strings.Repeat("9", phoneDigits), true},
		{strings.Repeat("9", phoneWindow), true}, // one group; the window does not split it
		{strings.Repeat("9", phoneWindow+1), true},
		{"123456----654321", false}, // longer than the window, gap of four, each side six
		{strings.Repeat("n", phoneWindow+5), false},
	}
	for _, c := range cases {
		if got := phoneLike(c.in); got != c.want {
			t.Errorf("phoneLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEmailUnicodeSpacesAndAt(t *testing.T) {
	nbsp := "\u00a0"
	ideo := "\u3000" // ideographic space, the fullwidth space
	emsp := "\u2003"
	cases := []struct {
		in   string
		want bool
	}{
		{"ada@example.com", true},
		{"ada @ example.com", true},
		{"ada  @  example.com", true},
		{"ada   @   example.com", true},
		{"ada" + nbsp + "@" + nbsp + "example.com", true},
		{"ada" + ideo + "@" + ideo + "example.com", true},
		{"ada" + ideo + ideo + "@" + ideo + ideo + "example.com", true},
		{"ada" + emsp + "@" + emsp + "example.com", true},
		{"ada\u202f@\u202fexample.com", true}, // narrow no-break space, Zs
		{"à@example.com", true},
		{"a@é.example", true},
		{"日本語@例", true},
		{"kernels@scale", true},
		{"a\u0301@b\u0301", true}, // combining mark is a rune beside '@'
		{"🙂@x", true},
		{"cost is high", false},
		{"@", false},
		{"@@", false},
		{"@@@", false},
		{"@ @", false},
		{"word @", false},
		{"@ word", false},
		{"@handle", false},
		{"end@", false},
		{"a@@b", false},
		{"a@@@b", false},
		{"a @ @ b", false},
		{"a  @  @  b", false},
		{"a@ @b", false},
		{"a@@b@c", true}, // the '@' before c has non-'@' runes on both sides
		{"a@b@c", true},
		{"ada\u200b@example.com", true}, // ZWSP is not a space, so it counts as a neighbor
	}
	for _, c := range cases {
		if got := emailLike(c.in); got != c.want {
			t.Errorf("emailLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCheckInstructionAdversarial(t *testing.T) {
	phones := []string{
		"555 - 123 - 4567",
		"555--4567",
		"555---4567",
		"(555) 123-4567",
		"+1 555 123 4567",
		"555.123.4567",
		"2019 - 2024",
		"2019 2024",
		"1899-1900",
		"2099-2100",
		"2100-2099",
		"reach me at 555 - 123 - 4567 tomorrow",
		"shipped 12 services, call 555-123-4567 if needed",
		"worked 2019-2024, call 5551234",
		"１２３４５６７",
		strings.Repeat("9", phoneWindow+1),
		"  555-123-4567  ",
	}
	for _, in := range phones {
		assertContactRefusal(t, in, "phone number", digitsOf(in)...)
	}
	mails := []string{
		"ada @ example.com",
		"ada@example.com",
		"ada   @   example.com",
		"à@example.com",
		"a@é.example",
		"日本語@例",
		"kernels@scale",
		"a\u0301@b",
		"write ada@example.com today",
		"2019-2024 ada@example.com",
		"ada@example.com 555-123-4567",
	}
	for _, in := range mails {
		leaks := []string{"example.com", "ada@", "é.example", "日本語", "kernels@scale"}
		assertContactRefusal(t, in, "email address", leaks...)
	}
	// Email is decided before a phone, so a string with both names the mailbox rule
	// and still must not repeat either shape.
	both := "ada@example.com 555-123-4567"
	_, err := checkInstruction(both)
	e := wantCode(t, err, CodeBadInstruction)
	if !strings.Contains(e.Msg, "email address") || strings.Contains(e.Msg, "phone number") {
		t.Errorf("both: message = %q, want the email rule only", e.Msg)
	}
	if strings.ContainsAny(e.Msg, "0123456789@") {
		t.Errorf("both: message echoes a digit or '@': %s", e.Msg)
	}

	allowed := []string{
		"",
		"@",
		"@@",
		"a@@b",
		"a @ @ b",
		"word @",
		"@ word",
		"cost is high",
		"2019-2024",
		"2019\u20132024",
		"1900-2099",
		"2099-1900",
		"3.12",
		"Go 1.26",
		"100,000",
		"1,000,000",
		"555 --- 4567",
		"555----4567",
		"2019    2024",
		"1900----2099",
		"from 2019-2024 on",
		"１２３４５６",
		"①②③④⑤⑥⑦",
		strings.Repeat("n", phoneWindow+5),
	}
	for _, in := range allowed {
		got, err := checkInstruction(in)
		if err != nil || got != in {
			t.Errorf("checkInstruction(%q) = %q, %v; want the text unchanged", in, got, err)
		}
	}

	// Surrounding Unicode spaces trim away; an interior one is not a printable line.
	got, err := checkInstruction("\u00a0cost is high\u3000")
	if err != nil || got != "cost is high" {
		t.Errorf("surrounding unicode spaces: got %q, %v", got, err)
	}
	assertNoEcho(t, "prefer\u00a0tracing", "")
	assertNoEcho(t, "prefer\u3000tracing", "")

	// Only spaces, only '@' already covered, a bare newline, and an embedded one.
	_, err = checkInstruction(" ")
	e = wantCode(t, err, CodeBadInstruction)
	if !strings.Contains(e.Msg, "whitespace") {
		t.Errorf("single space: message = %q", e.Msg)
	}
	for _, in := range []string{"  ", "   ", "\t", "\n", " \n\t ", strings.Repeat(" ", phoneWindow+1), strings.Repeat(" ", MaxInstructionRunes+10)} {
		assertNoEcho(t, in, "")
		_, err := checkInstruction(in)
		e := wantCode(t, err, CodeBadInstruction)
		if strings.Contains(e.Msg, "over ") {
			t.Errorf("%q: whitespace was reported as over-long: %s", in, e.Msg)
		}
	}
	assertNoEcho(t, "keep\n555-123-4567", "555-123-4567")
	_, err = checkInstruction("keep\nlines")
	e = wantCode(t, err, CodeBadInstruction)
	if !strings.Contains(e.Msg, "printable") {
		t.Errorf("newline message = %q, want the printable-line rule", e.Msg)
	}

	over := strings.Repeat("p", MaxInstructionRunes+1)
	assertNoEcho(t, over, "pppp")
	_, err = checkInstruction(over)
	e = wantCode(t, err, CodeBadInstruction)
	if !strings.Contains(e.Msg, "over ") || strings.Contains(e.Msg, over) {
		t.Errorf("over-long message = %q", e.Msg)
	}
	// Length wins over a later rule, and still does not echo the mailbox or the number.
	stuffed := strings.Repeat("555-123-4567 ada@example.com ", 40)
	if utf8.RuneCountInString(strings.TrimSpace(stuffed)) <= MaxInstructionRunes {
		t.Fatal("fixture is not over the instruction cap")
	}
	assertNoEcho(t, stuffed, "555-123-4567", "ada@example.com", "@")
	exact := strings.Repeat("p", MaxInstructionRunes-len("ada@example.com")) + "ada@example.com"
	if utf8.RuneCountInString(exact) != MaxInstructionRunes {
		t.Fatalf("exact fixture = %d runes", utf8.RuneCountInString(exact))
	}
	assertContactRefusal(t, exact, "email address", "ada@example.com", "pppp")

	marks := "\u0301\u0300\u0327"
	got, err = checkInstruction(marks)
	if err != nil || got != marks {
		t.Errorf("combining marks: got %q, %v", got, err)
	}
	if phoneLike(marks) || emailLike(marks) {
		t.Errorf("combining marks looked like contact details")
	}
	longMarks := strings.Repeat("\u0301", phoneWindow+1)
	got, err = checkInstruction(longMarks)
	if err != nil || got != longMarks {
		t.Errorf("long combining marks: got %q, %v", got, err)
	}

	// Invalid UTF-8 is refused without echoing the bytes. The pure helpers do not panic.
	bad := string([]byte{0xff, 0xfe})
	if phoneLike(bad) || emailLike(bad) {
		t.Errorf("invalid UTF-8 looked like contact details")
	}
	assertNoEcho(t, bad, bad)

	for _, in := range []string{"call 555 - 123 - 4567 tomorrow", "write ada@example.com today"} {
		_, err := CheckTailor(Tailor{Instruction: in}, false)
		e := wantCode(t, err, CodeBadInstruction)
		if strings.Contains(e.Msg, in) || strings.Contains(e.Error(), in) {
			t.Errorf("CheckTailor echoes %q in %s", in, e.Msg)
		}
	}
}

func assertContactRefusal(t *testing.T, in, rule string, leaks ...string) {
	t.Helper()
	_, err := checkInstruction(in)
	e := wantCode(t, err, CodeBadInstruction)
	if !strings.Contains(e.Msg, rule) {
		t.Errorf("%q: message %q does not name %q", in, e.Msg, rule)
	}
	if strings.ContainsAny(e.Msg, "0123456789") || strings.Contains(e.Msg, "@") {
		t.Errorf("%q: message echoes a digit or '@': %s", in, e.Msg)
	}
	assertNoEcho(t, in, leaks...)
}

func assertNoEcho(t *testing.T, in string, leaks ...string) {
	t.Helper()
	_, err := checkInstruction(in)
	if err == nil {
		t.Errorf("%q: accepted, want a refusal", in)
		return
	}
	e := wantCode(t, err, CodeBadInstruction)
	if in != "" && strings.Contains(e.Msg, in) {
		t.Errorf("%q: message echoes the instruction: %s", in, e.Msg)
	}
	for _, s := range leaks {
		if s != "" && strings.Contains(e.Msg, s) {
			t.Errorf("%q: message echoes %q: %s", in, s, e.Msg)
		}
	}
}

func digitsOf(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func chainDigits(d byte, gaps []string) string {
	var b strings.Builder
	b.WriteByte(d)
	for _, g := range gaps {
		b.WriteString(g)
		b.WriteByte(d)
	}
	return b.String()
}
