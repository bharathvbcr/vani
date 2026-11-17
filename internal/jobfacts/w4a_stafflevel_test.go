package jobfacts

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/expyears"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// StaffLevel is what expyears.BelowStaff says of the description, so the
// stored fact and a read of the whole text agree.
func TestW4aDeriveStaffLevel(t *testing.T) {
	v := vocab(t)
	for desc, want := range map[string]string{
		"":                                 "",
		"No figure stated.":                "",
		"Requires 3+ years of experience.": expyears.ValueMid,
		"Requires 6 years of experience.":  expyears.ValueSenior,
		"Requires 12+ years of experience": "",
	} {
		if got := Derive(v, posting.Posting{Title: "Staff Engineer", Description: desc}).StaffLevel; got != want {
			t.Errorf("%q: StaffLevel %q, want %q", desc, got, want)
		}
	}
}

// A store from before StaffLevel existed is not read: it would answer "" for
// every posting whose text states years.
func TestW4aStoreFormatMovedWithStaffLevel(t *testing.T) {
	if format < 2 {
		t.Fatalf("format %d predates the StaffLevel fact", format)
	}
}
