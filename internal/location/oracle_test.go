package location

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// oracleRow is one hand-labelled board location (testdata/us_*.tsv).
type oracleRow struct {
	Label    string // Y, N, A or ?
	Remote   bool
	Location string
}

// oracleSets are the hand-labelled sets and the rows each must hold: the
// oracle the change was built against, and the held-out set drawn after it.
var oracleSets = []struct {
	path    string
	minRows int
}{
	{"testdata/us_oracle.tsv", 250},
	{"testdata/us_heldout.tsv", 100},
}

func readOracle(t *testing.T, path string, minRows int) []oracleRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []oracleRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) != 5 {
			t.Fatalf("%s: line has %d columns: %q", path, len(c), line)
		}
		switch c[0] {
		case "Y", "N", "A", "?":
		default:
			t.Fatalf("%s: label %q in %q", path, c[0], line)
		}
		rows = append(rows, oracleRow{Label: c[0], Remote: c[1] == "true", Location: c[2]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < minRows {
		t.Fatalf("%s has %d rows; want at least %d", path, len(rows), minRows)
	}
	return rows
}

// TestInUSOracle holds InUS to the hand-labelled sets: of the strings that
// list a US option, at least 98% read as US (recall), and of those InUS calls
// US, at least 99% do (precision). A name that is a well-known place both in
// and outside the US (label A) must never read as US.
func TestInUSOracle(t *testing.T) {
	for _, set := range oracleSets {
		t.Run(set.path, func(t *testing.T) {
			var tp, fp, fn, tn int
			for _, r := range readOracle(t, set.path, set.minRows) {
				got := InUS(Parse(r.Location, r.Remote))
				switch r.Label {
				case "Y":
					if got {
						tp++
					} else {
						fn++
						t.Logf("missed US: %q (remote %v)", r.Location, r.Remote)
					}
				case "N":
					if got {
						fp++
						t.Logf("false US: %q (remote %v)", r.Location, r.Remote)
					} else {
						tn++
					}
				case "A":
					if got {
						t.Errorf("ambiguous %q read as US", r.Location)
					}
				}
			}
			recall := float64(tp) / float64(tp+fn)
			precision := float64(tp) / float64(tp+fp)
			t.Logf("InUS: tp %d fp %d fn %d tn %d; recall %.3f precision %.3f", tp, fp, fn, tn, recall, precision)
			if recall < 0.98 {
				t.Errorf("US recall %.3f, want >= 0.98", recall)
			}
			if precision < 0.99 {
				t.Errorf("US precision %.3f, want >= 0.99", precision)
			}
		})
	}
}
