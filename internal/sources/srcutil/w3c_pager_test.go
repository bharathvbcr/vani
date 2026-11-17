package srcutil

import (
	"errors"
	"testing"
)

func TestW3cTotalCoversRows(t *testing.T) {
	for _, c := range []struct {
		name          string
		total, served int
		bad           bool
	}{
		{"equal", 100, 100, false},
		{"above", 250, 100, false},
		{"empty listing", 0, 0, false},
		{"below", 50, 100, true},
		{"zero with rows", 0, 3, true},
		{"negative", -1, 0, true},
		{"negative with rows", -5, 2, true},
	} {
		err := TotalCoversRows(c.total, c.served)
		if (err != nil) != c.bad {
			t.Errorf("%s: err %v, want bad=%v", c.name, err, c.bad)
		}
		if err != nil && !errors.Is(err, ErrTotalBelowRows) {
			t.Errorf("%s: %v does not wrap ErrTotalBelowRows", c.name, err)
		}
	}
}
