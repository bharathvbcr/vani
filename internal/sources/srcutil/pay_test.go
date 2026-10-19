package srcutil

import (
	"math"
	"testing"
)

func TestPayLine(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name     string
		cur      string
		lo, hi   *float64
		interval string
		want     string
	}{
		{"range per year", "USD", f(120000), f(150000), "per-year-salary", "Pay range: USD 120000 – 150000 per year"},
		{"ashby interval", "usd", f(150000), f(200000), "1 YEAR", "Pay range: USD 150000 – 200000 per year"},
		{"hourly decimals", "USD", f(52.125), f(60), "per-hour-wage", "Pay range: USD 52.13 – 60 per hour"},
		{"single", "EUR", f(70000), f(70000), "", "Pay range: EUR 70000"},
		{"only min", "GBP", f(70000), nil, "", "Pay range: GBP 70000"},
		{"only max", "GBP", nil, f(90000), "monthly", "Pay range: GBP 90000 per month"},
		{"unknown interval left out", "USD", f(1), f(2), "NONE", "Pay range: USD 1 – 2"},
		{"no amounts", "USD", nil, nil, "", ""},
		{"zero and negative", "USD", f(0), f(-5), "", ""},
		{"not finite", "USD", f(math.Inf(1)), f(math.NaN()), "", ""},
		{"inverted", "USD", f(200), f(100), "", ""},
		{"no currency", "", f(1), f(2), "", ""},
		{"bad currency", "US$", f(1), f(2), "", ""},
		{"long currency", "USDX", f(1), f(2), "", ""},
	}
	for _, c := range cases {
		if got := PayLine(c.cur, c.lo, c.hi, c.interval); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
