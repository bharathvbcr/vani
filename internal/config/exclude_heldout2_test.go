package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// heldout2Row is one row of internal/location/testdata/us_heldout2.tsv.
type heldout2Row struct {
	label    string // U, M, N, A or ?
	remote   bool
	loc      string
	postings float64
}

func readHeldout2(t *testing.T) []heldout2Row {
	t.Helper()
	f, err := os.Open("../location/testdata/us_heldout2.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []heldout2Row
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
		switch c[0] {
		case "U", "M", "N", "A", "?":
		default:
			t.Fatalf("label %q in %q", c[0], line)
		}
		n, err := strconv.ParseFloat(c[4], 64)
		if err != nil || n < 1 {
			t.Fatalf("postings %q in %q", c[4], line)
		}
		rows = append(rows, heldout2Row{c[0], c[1] == "true", c[2], n})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 150 {
		t.Fatalf("%d rows; want at least 150", len(rows))
	}
	return rows
}

// rate is a recall or precision estimated two ways from a sample drawn with
// probability proportional to posting count: posting-level (each row counts
// once) and string-level (each row weighted by 1/postings, Hájek).
type rate struct{ hitP, allP, hitS, allS float64 }

func (r *rate) add(hit bool, postings float64) {
	r.allP++
	r.allS += 1 / postings
	if hit {
		r.hitP++
		r.hitS += 1 / postings
	}
}

func (r rate) posting() float64 { return r.hitP / r.allP }
func (r rate) str() float64     { return r.hitS / r.allS }

// TestUSRulesHeldout2 scores InUS, exclude.only_locations ["United States"]
// and exclude.locations ["United States"] on the second held-out set, drawn
// after the per-option change and labelled without a verdict column. The
// acceptance was stated before the run:
//   - InUS and only_locations: US recall >= 0.98 and precision >= 0.99, at
//     posting level and at string level;
//   - exclude.locations: no wrong drop (a string with any option outside the
//     US, or one that cannot be judged), and recall >= 0.98 on wholly-US
//     strings;
//   - an A row is kept by both rules and never read as US.
func TestUSRulesHeldout2(t *testing.T) {
	only := Exclude{OnlyLocations: []string{"United States"}}
	ex := Exclude{Locations: []string{"United States"}}
	var inRecall, inPrec, onlyRecall, onlyPrec, exRecall rate
	wrongDrops := 0
	for _, r := range readHeldout2(t) {
		p := posting.Posting{Location: r.loc, Remote: r.remote}
		inUS := location.InUS(location.Parse(r.loc, r.remote))
		_, onlyDrop := only.Match(p)
		_, exDrop := ex.Match(p)
		hasUS := r.label == "U" || r.label == "M"
		switch r.label {
		case "U", "M":
			inRecall.add(inUS, r.postings)
			onlyRecall.add(!onlyDrop, r.postings)
			if !inUS {
				t.Logf("InUS missed %s %q (remote %v)", r.label, r.loc, r.remote)
			}
			if onlyDrop {
				t.Errorf("only_locations dropped %s %q (remote %v)", r.label, r.loc, r.remote)
			}
		case "A":
			if inUS {
				t.Errorf("ambiguous %q read as US", r.loc)
			}
			if onlyDrop || exDrop {
				t.Errorf("ambiguous %q dropped (only %v, exclude %v)", r.loc, onlyDrop, exDrop)
			}
		}
		if inUS {
			inPrec.add(hasUS, r.postings)
			if !hasUS {
				t.Logf("InUS false positive %s %q (remote %v)", r.label, r.loc, r.remote)
			}
		}
		if !onlyDrop && r.label != "A" && r.label != "?" {
			onlyPrec.add(hasUS, r.postings)
			if !hasUS {
				t.Logf("only_locations kept %s %q (remote %v)", r.label, r.loc, r.remote)
			}
		}
		if r.label == "U" {
			exRecall.add(exDrop, r.postings)
			if !exDrop {
				t.Logf("exclude.locations kept wholly-US %q (remote %v)", r.loc, r.remote)
			}
		} else if exDrop {
			wrongDrops++
			t.Errorf("exclude.locations dropped %s %q (remote %v): it has an option outside the US or cannot be judged", r.label, r.loc, r.remote)
		}
	}
	for _, c := range []struct {
		name string
		r    rate
	}{
		{"InUS recall", inRecall}, {"InUS precision", inPrec},
		{"only_locations recall", onlyRecall}, {"only_locations precision", onlyPrec},
		{"exclude.locations recall (wholly-US dropped)", exRecall},
	} {
		t.Logf("%s: posting-level %.3f (n=%.0f), string-level %.3f", c.name, c.r.posting(), c.r.allP, c.r.str())
		bar := 0.98
		if strings.Contains(c.name, "precision") {
			bar = 0.99
		}
		if c.r.posting() < bar || c.r.str() < bar {
			t.Errorf("%s below %.2f: posting-level %.3f, string-level %.3f", c.name, bar, c.r.posting(), c.r.str())
		}
	}
	t.Logf("exclude.locations wrong drops: %d", wrongDrops)
}
