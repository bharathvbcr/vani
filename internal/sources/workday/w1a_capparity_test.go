package workday

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1a: the adapter's largest legitimate location (maxLocations places, each as
// long as srcutil.RealisticPlaceBytes) is a row Storable accepts. A workday
// board fails whole on a row Storable refuses, so a cap under this refuses rows
// the adapter lists today.
func TestW1aMaxLocationsAtTheRealisticPlaceSizePassStorable(t *testing.T) {
	var more []string
	for i := 1; i < maxLocations; i++ {
		more = append(more, placeOf(i))
	}
	loc := detailLocation(placeOf(0), more)
	if n := strings.Count(loc, "; ") + 1; n != maxLocations {
		t.Fatalf("%d places joined, want %d", n, maxLocations)
	}
	p := posting.Posting{
		Key: posting.Key("wd:nvidia:site:r-1"), ATS: posting.Workday, Company: "C", Title: "T",
		Location: loc, URL: "https://h.test/j/1",
	}
	if _, err := srcutil.Storable(board, p); err != nil {
		t.Errorf("a %d-byte location the adapter produces is refused: %v", len(loc), err)
	}
}

// placeOf is place i, distinct and srcutil.RealisticPlaceBytes long.
func placeOf(i int) string {
	head := string(rune('A'+i%26)) + string(rune('a'+i/26))
	return head + strings.Repeat("界", (srcutil.RealisticPlaceBytes-len(head))/3)
}
