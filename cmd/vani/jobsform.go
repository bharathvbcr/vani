package main

import (
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobview"
)

// formFlag is the list row's mention of an application form that has a
// flagged question ("  (form: sponsorship, places)"), or "". The whole line
// about the form is `jobs show`; a note for a person, never a filter.
func formFlag(j jobview.Job) string {
	if len(j.ApplyFlags) == 0 {
		return ""
	}
	return "  (form: " + strings.Join(j.ApplyFlags, ", ") + ")"
}
