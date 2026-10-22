package main

import (
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobview"
)

// printPending is the text form: one line, only when something is waiting.
func (e *env) printPending(p jobview.Pending, query string) {
	if p.Total == 0 {
		return
	}
	const where = "; list them, unranked, with --view pending"
	if query != "" {
		fmt.Fprintf(e.stdout, "awaiting triage (not listed until a run triages them): %d matching %q, %d in all%s\n", p.Matching, query, p.Total, where)
		return
	}
	var top []string
	for _, c := range p.Companies[:min(len(p.Companies), 5)] {
		top = append(top, fmt.Sprintf("%s %d", c.Company, c.N))
	}
	fmt.Fprintf(e.stdout, "awaiting triage (not listed until a run triages them): %d (%s)%s\n", p.Total, strings.Join(top, ", "), where)
}
