package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Attack tests for the bare-US-place rule: only_locations naming a state keeps
// (and counts as unreadable) a US place with no state read.
func TestAttackOnlyLocationsBareUSMatrix(t *testing.T) {
	type row struct {
		entries    []string
		loc        string
		drop       bool
		unreadable bool
	}
	var rows []row
	for _, e := range [][]string{{"TX"}, {"Texas"}, {"tx"}, {" TX "}, {"TX", "TX"}, {"", "TX"}, {"Canada", "TX"}} {
		rows = append(rows,
			row{e, "US", false, true}, row{e, "United States", false, true}, row{e, "Nationwide", false, true},
			row{e, "Anywhere in the US", false, true}, row{e, "Multiple locations (US)", false, true},
			row{e, "Kansas City", false, true}, row{e, "St. Louis", false, true}, row{e, "Portland", false, true},
			row{e, "Austin", false, true}, row{e, "Remote", false, true},
			row{e, "New York", true, false}, row{e, "Washington", true, false}, row{e, "Georgia", true, false},
			row{e, "Austin, TX; Toronto, ON", false, false},
			row{e, "Denver, CO; Austin", false, true},
			row{e, "Berlin, Germany; Austin", false, true},
		)
	}
	rows = append(rows,
		row{[]string{"TX"}, "London", true, false},
		row{[]string{"TX", "US"}, "Austin", false, false},
		row{[]string{"US"}, "Austin", false, false},
		row{[]string{"Austin"}, "Denver, CO; Austin", false, false},
		row{[]string{"Ontario"}, "Austin", true, false}, // no US state entry: the rule stays out of it
		row{[]string{"remote"}, "Austin", true, false},
		row{[]string{"GA"}, "Atlanta", false, true},
		row{[]string{"DC"}, "Washington, DC", false, false},
	)
	for _, r := range rows {
		x := Exclude{OnlyLocations: r.entries}
		p := posting.Posting{Location: r.loc}
		_, dropped := x.Match(p)
		if dropped != r.drop {
			t.Errorf("%q | %q: dropped %v, want %v", r.entries, r.loc, dropped, r.drop)
		}
		if got := x.LocationUnreadable(p); got != r.unreadable {
			t.Errorf("%q | %q: unreadable %v, want %v", r.entries, r.loc, got, r.unreadable)
		}
		// Invariant: a kept posting that is unreadable never is a dropped one.
		if dropped && x.LocationUnreadable(p) {
			t.Errorf("%q | %q: both dropped and unreadable", r.entries, r.loc)
		}
	}
}

// A board's Remote flag with a bare city, and a stable, bounded answer on
// oversized inputs (1000 entries, a huge location, 500 options).
func TestAttackOnlyLocationsBareUSBounds(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"TX"}}
	if _, d := x.Match(posting.Posting{Location: "Austin", Remote: true}); d || !x.LocationUnreadable(posting.Posting{Location: "Austin", Remote: true}) {
		t.Error("remote flag with a bare city must keep and count as unreadable")
	}
	entries := []string{"TX"}
	for i := 0; i < 1000; i++ {
		entries = append(entries, fmt.Sprintf("Place%d", i))
	}
	big := Exclude{OnlyLocations: entries}
	many := strings.Repeat("Austin; ", 500) + "Denver, CO"
	for _, loc := range []string{"Austin", many, strings.Repeat("Austin ", 100000)} {
		p := posting.Posting{Location: loc}
		_, d1 := big.Match(p)
		_, d2 := big.Match(p)
		if d1 || d2 || !big.LocationUnreadable(p) {
			t.Errorf("%.20q: dropped %v/%v unreadable %v, want kept+unreadable", loc, d1, d2, big.LocationUnreadable(p))
		}
	}
}

func TestAttackOnlyLocationsBareUSConcurrent(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"TX"}}
	done := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		go func() {
			p := posting.Posting{Location: "Austin; Toronto, ON"}
			for j := 0; j < 200; j++ {
				_, d := x.Match(p)
				if d || !x.LocationUnreadable(p) {
					done <- false
					return
				}
			}
			done <- true
		}()
	}
	for i := 0; i < 16; i++ {
		if !<-done {
			t.Fatal("concurrent answer differed")
		}
	}
}
