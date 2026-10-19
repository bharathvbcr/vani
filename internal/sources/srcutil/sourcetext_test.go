package srcutil

import "testing"

func TestSourceText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"no synthesized lines", "Build.\n\nShip.", "Build.\n\nShip."},
		{"pay line", "Pay range: USD 1 – 2 per year\n\nBuild.", "Build."},
		{"compensation line", "Compensation: $1 - $2\n\nBuild.", "Build."},
		{"several lines and blanks", "Pay range: USD 1 – 2\nPay range: CAD 3 – 4\n\n\nCompensation: x\n\nBuild.\nShip.", "Build.\nShip."},
		{"only synthesized", "Pay range: USD 1 – 2 per year", ""},
		{"indented synthesized", "  Pay range: USD 1 – 2\n\nBuild.", "Build."},
		{"pay-shaped after board text stays", "Build.\n\nPay range: USD 1 – 2", "Build.\n\nPay range: USD 1 – 2"},
		{"prefix inside a line stays", "Our Pay range: USD 1 – 2\n\nBuild.", "Our Pay range: USD 1 – 2\n\nBuild."},
		{"prefix without its space stays", "Pay range:USD 1\n\nBuild.", "Pay range:USD 1\n\nBuild."},
		{"case matters", "pay range: USD 1 – 2\n\nBuild.", "pay range: USD 1 – 2\n\nBuild."},
	}
	for _, c := range cases {
		if got := SourceText(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A long run of synthesized lines is consumed in one pass, never more.
func TestSourceTextOversized(t *testing.T) {
	var b []byte
	for len(b) < MaxDescription {
		b = append(b, "Pay range: USD 1 – 2\n"...)
	}
	b = append(b, "Build."...)
	if got := SourceText(string(b)); got != "Build." {
		t.Fatalf("got %q", got)
	}
}

func TestCompensationLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{" \n\t", ""},
		{"$1 - $2", "Compensation: $1 - $2"},
		{" $1 -\n $2 ", "Compensation: $1 - $2"},
	}
	for _, c := range cases {
		if got := CompensationLine(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}
