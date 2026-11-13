package location

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestCityTableSet holds the US city table to testdata/us_citytable.tsv:
// every sampled table name read bare is the US, every distractor that puts a
// table name beside a foreign country or region, or inside a longer foreign
// name, is not, and one beside a US state is.
func TestCityTableSet(t *testing.T) {
	f, err := os.Open("testdata/us_citytable.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
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
		n++
		l := Parse(c[2], c[1] == "true")
		want := c[0] == "U"
		if got := InUS(l); got != want {
			t.Errorf("%s %q (%s): InUS %v, want %v (parsed %q)", c[0], c[2], c[3], got, want, l.String())
		}
		if c[0] == "U" {
			for _, p := range l.Places {
				for _, o := range PlaceOptions(p) {
					if o.Verdict != VerdictUS {
						t.Errorf("%q: option %q is %v; every option is US", c[2], o.Text, o.Verdict)
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 50 {
		t.Fatalf("%d rows; want at least 50", n)
	}
}
