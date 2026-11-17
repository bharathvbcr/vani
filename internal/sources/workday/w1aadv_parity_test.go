package workday

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1aadv: parity. The adapter itself allows up to maxLocations (20) places in a
// posting's location and a workday board fails whole on any row Storable
// refuses. A realistic multi-location posting (20 places of the form NVIDIA and
// Intel list, about 25 bytes each) is listed by the adapter today; Storable's
// 512-byte location cap refuses it, so the seam would fail the whole board.
func TestW1aadvParityManyLocationsListedButNotStorable(t *testing.T) {
	var more []string
	for i := 1; i < maxLocations; i++ {
		more = append(more, fmt.Sprintf("India, Karnataka, Site %02d", i))
	}
	raw, err := json.Marshal(more)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newSource(withAdditional(t, string(raw)))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	loc := got[0].Location
	if n := strings.Count(loc, ";") + 1; n != maxLocations {
		t.Fatalf("adapter listed %d places, want %d: %q", n, maxLocations, loc)
	}
	if _, err := srcutil.Storable(board, got[0]); err != nil {
		t.Errorf("adapter lists this row today (location %d bytes) but Storable refuses it, which fails the whole workday board: %v", len(loc), err)
	}
}
