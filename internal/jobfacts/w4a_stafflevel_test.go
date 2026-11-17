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

// The store's Version exercises StaffLevel (a probe posting derives one), so a
// store written before the fact existed carries another Version and is not read:
// it would answer "" for every posting whose text states years.
func TestW4aVersionProbesStaffLevel(t *testing.T) {
	v := vocab(t)
	found := false
	for _, p := range probe {
		found = found || Derive(v, p).StaffLevel != ""
	}
	if !found {
		t.Fatal("no probe posting derives a StaffLevel, so Version does not depend on it")
	}
}
