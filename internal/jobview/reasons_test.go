package jobview

import (
	"fmt"
	"testing"
)

func TestParseReasons(t *testing.T) {
	pct := func(p int) *int { return &p }
	cases := []struct {
		in   string
		want []Reason
	}{
		{"", nil},
		{"Fits: A (91%).", []Reason{{"fits", "A", pct(91)}}},
		{"Fits: A (91%). Gap: B. Check: C (5%).", []Reason{{"fits", "A", pct(91)}, {"gap", "B", nil}, {"check", "C", pct(5)}}},
		{"Gap: Go. Rust. (40%).", []Reason{{"gap", "Go. Rust.", pct(40)}}},
		{"Fits: odd (250%).", []Reason{{"fits", "odd (250%)", nil}}},
		{"No listed reason applies.", []Reason{{"other", "No listed reason applies.", nil}}},
		{"Fits: A. trailing words", []Reason{{"fits", "A. trailing words", nil}}},
		{"Gap: B. Fits: A.", []Reason{{"gap", "B", nil}, {"fits", "A", nil}}},
		{"Fits: ", []Reason{{"other", "Fits:", nil}}},
		{"Fits: (91%).", []Reason{{"other", "Fits: (91%).", nil}}},
	}
	for _, c := range cases {
		got := ParseReasons(c.in)
		if fmt.Sprint(reasonStrings(got)) != fmt.Sprint(reasonStrings(c.want)) {
			t.Errorf("ParseReasons(%q) = %v, want %v", c.in, reasonStrings(got), reasonStrings(c.want))
		}
	}
}

func reasonStrings(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		p := "-"
		if r.Pct != nil {
			p = fmt.Sprint(*r.Pct)
		}
		out = append(out, r.Kind+"|"+r.Text+"|"+p)
	}
	return out
}
