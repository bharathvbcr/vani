package pipeline

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The production floor is 40 non-space runes: 39 is skipped (kept unread), 40 is read.
func TestDescriptionFloorBoundaryAtForty(t *testing.T) {
	r := &Runner{Funnel: Funnel{MinDescriptionRunes: 40}}
	for _, tc := range []struct {
		runes int
		short bool
	}{{0, true}, {39, true}, {40, false}, {41, false}} {
		p := posting.Posting{Description: strings.Repeat("é", tc.runes)}
		if got := r.descriptionTooShort(p); got != tc.short {
			t.Fatalf("%d runes: too short = %v, want %v", tc.runes, got, tc.short)
		}
	}
	// Spaces do not count toward the floor.
	if !r.descriptionTooShort(posting.Posting{Description: strings.Repeat("a ", 39)}) {
		t.Fatal("39 non-space runes padded with spaces was read")
	}
}
