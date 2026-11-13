package config

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestOnlyLocationsUSOracle holds exclude.only_locations ["United States"] to
// the hand-labelled sets in internal/location/testdata: of the strings that
// list a US option, at least 98% are kept (a US role dropped is the costly
// miss), and of the strings kept with a readable answer, at least 99% list a
// US option. A label A row (a name as likely US as foreign) is kept by both
// only_locations and exclude.locations ["United States"]: neither rule may
// drop what it cannot judge.
func TestOnlyLocationsUSOracle(t *testing.T) {
	for _, set := range []struct {
		path    string
		minRows int
	}{
		{"../location/testdata/us_oracle.tsv", 250},
		{"../location/testdata/us_heldout.tsv", 100},
	} {
		t.Run(set.path, func(t *testing.T) {
			f, err := os.Open(set.path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			x := Exclude{OnlyLocations: []string{"United States"}}
			ex := Exclude{Locations: []string{"United States"}}
			var keptUS, droppedUS, keptForeign, droppedForeign, rows int
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := sc.Text()
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				c := strings.Split(line, "\t")
				if len(c) != 5 {
					t.Fatalf("line has %d columns: %q", len(c), line)
				}
				rows++
				p := posting.Posting{Location: c[2], Remote: c[1] == "true"}
				_, dropped := x.Match(p)
				switch c[0] {
				case "Y":
					if dropped {
						droppedUS++
						t.Logf("US dropped: %q (remote %v)", c[2], p.Remote)
					} else {
						keptUS++
					}
				case "N":
					if dropped {
						droppedForeign++
					} else {
						keptForeign++
						t.Logf("foreign kept: %q (remote %v)", c[2], p.Remote)
					}
				case "A":
					if dropped {
						t.Errorf("ambiguous %q (remote %v) dropped by only_locations; an unjudged place is kept", c[2], p.Remote)
					}
					if _, exd := ex.Match(p); exd {
						t.Errorf("ambiguous %q (remote %v) dropped by exclude.locations; an unjudged place is kept", c[2], p.Remote)
					}
				case "?":
					// No readable place to a person. The rule keeps what Parse
					// reads as no place; text Parse takes for an unknown place
					// ("Lovelace HQ") it cannot tell from a foreign one.
					if x.LocationUnreadable(p) && dropped {
						t.Errorf("unreadable %q dropped; an unread location is kept", c[2])
					}
				}
			}
			if err := sc.Err(); err != nil {
				t.Fatal(err)
			}
			if rows < set.minRows {
				t.Fatalf("%d rows; want at least %d", rows, set.minRows)
			}
			recall := float64(keptUS) / float64(keptUS+droppedUS)
			precision := float64(keptUS) / float64(keptUS+keptForeign)
			t.Logf("only_locations [United States]: US kept %d dropped %d; foreign kept %d dropped %d; recall %.3f precision %.3f",
				keptUS, droppedUS, keptForeign, droppedForeign, recall, precision)
			if recall < 0.98 {
				t.Errorf("US recall %.3f, want >= 0.98", recall)
			}
			if precision < 0.99 {
				t.Errorf("precision %.3f, want >= 0.99", precision)
			}
		})
	}
}
