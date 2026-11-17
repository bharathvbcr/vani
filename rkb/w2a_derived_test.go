package main

import "testing"

// A derived figure is the change or ratio of two quantities of one kind. Two
// quoted numbers of different kinds ("5 servers", "20 ms") have no meaningful
// percentage between them, so a "300%" computed from them is not derived and
// must itself be in the quote.
func TestW2ADerivedNeedsSameClass(t *testing.T) {
	for _, tc := range []struct {
		name, quote, found string
		want               bool
	}{
		{"count vs time", "5 servers, 20 ms", "5 servers -> 20 ms (300%)", false},
		{"time vs count", "20 ms, 5 servers", "20 ms -> 5 servers (25%)", false},
		{"usd vs count", "$5 and 20 users", "$5 -> 20 users (400%)", false},
		{"usd vs pct operand", "$5 and 20 ms", "$5 -> 20 ms (400%)", false},
		{"time vs unit-less multiple", "5 ms and 20 widgets", "5 ms -> 20 widgets (4x)", false},
		{"time", "p95 470 ms then 94 ms", "470 ms -> 94 ms (-80%)", true},
		{"time scaled", "p95 2 s then 500 ms", "2 s -> 500 ms (-75%)", true},
		{"unit-less", "12 calls then 3 calls", "12 -> 3 (-75%)", true},
		{"unit-less multiple", "3 calls then 12 calls", "3 -> 12 (4x)", true},
		{"usd", "$10 then $4", "$10 -> $4 (-60%)", true},
		{"percent operands excluded", "18% then 7%", "18% -> 7% (61%)", false},
	} {
		if got := quoteHasFigure(tc.quote, tc.found); got != tc.want {
			t.Errorf("%s: quoteHasFigure(%q, %q) = %v, want %v", tc.name, tc.quote, tc.found, got, tc.want)
		}
	}
}

func TestW2ADerivedFromClassGate(t *testing.T) {
	ms := figuresIn("5 ms")[0]
	n20 := figuresIn("20")[0]
	d := figuresIn("300%")[0]
	if derivedFrom(d, ms, n20) || derivedFrom(d, n20, ms) {
		t.Errorf("derivedFrom accepted a time figure and a unit-less figure")
	}
	a, b := figuresIn("5")[0], figuresIn("20")[0]
	if !derivedFrom(d, a, b) {
		t.Errorf("derivedFrom rejected two unit-less figures (5 -> 20 is 300%%)")
	}
}
