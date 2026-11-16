package main

import "testing"

func TestRoundNeverInflates(t *testing.T) {
	cases := []struct {
		claim, src string
		ok         bool
	}{
		{"40", "39", false}, {"40", "40", true}, {"5200", "5246", true}, {"13000", "13412", true},
		{"5300", "5246", false}, {"270", "274", true}, {"3.4", "3.41", true}, {"57", "56.96", true},
	}
	for _, c := range cases {
		if got := numEq(c.claim, c.src, 1); got != c.ok {
			t.Errorf("numEq(%q,%q)=%v want %v", c.claim, c.src, got, c.ok)
		}
	}
}
