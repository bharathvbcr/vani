package jobview

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// verdictUS reads a board location the way the location package's per-option
// verdicts do, composing only PlaceOptions and ScopeTakesInUS: some option of
// some place is US, or a remote scope takes in the US. An ambiguous option
// ("Cambridge", "San Jose") is not US here, as for any ordering.
func verdictUS(loc string, remote bool) bool {
	l := location.Parse(loc, remote)
	for _, p := range l.Places {
		for _, o := range location.PlaceOptions(p) {
			if o.Verdict == location.VerdictUS {
				return true
			}
		}
	}
	return location.ScopeTakesInUS(l.RemoteScope)
}

// InUS has one owner, the location package: --us-first orders by exactly the
// per-option verdicts, over every hand-labelled board location the package
// holds (oracle, held-out and city-table sets) and the awkward forms below.
func TestInUSIsTheLocationVerdict(t *testing.T) {
	locs := map[string]bool{
		"": true, "Remote": true, "2 Locations": true, "Cambridge": true, "San Jose": true,
		"Paris": true, "Paris, France": true, "Paris, TX": true, "Springfield": true, "Smallville, XX": true,
		"Remote - outside US": true, "Remote - North America": true, "Houston, Scotland": true,
		"Santa Clara, Cuba": true, "Toronto, ON": true, "Berlin, NH": true, "Zzyzx": true,
		strings.Repeat("San Francisco; ", 200): true,
	}
	for _, name := range []string{"us_oracle.tsv", "us_heldout.tsv", "us_heldout2.tsv", "us_citytable.tsv"} {
		f, err := os.Open("../location/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Every fixture puts the board location in a tab-separated column;
			// feeding each non-label column covers it whichever one it is.
			for _, c := range strings.Split(line, "\t")[1:] {
				if c != "" && c != "true" && c != "false" {
					locs[c] = true
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if len(locs) < 300 {
		t.Fatalf("only %d locations read from the location fixtures", len(locs))
	}
	for loc := range locs {
		for _, remote := range []bool{false, true} {
			want := verdictUS(loc, remote)
			if got := InUS(posting.Posting{Location: loc, Remote: remote}); got != want {
				t.Errorf("InUS(%q, remote=%v) = %v; the location package's verdicts say %v", loc, remote, got, want)
			}
		}
	}
}
