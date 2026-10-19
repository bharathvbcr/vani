package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// maxPendingCompanies bounds pendingJSON.Companies; Total still counts all.
const maxPendingCompanies = 20

// pendingJSON is what the ledger holds that no view lists yet: postings read
// from a board but not triaged (status live). Views list only postings that
// passed triage (keptStatus), so without this a board whose postings are still
// waiting — the per-run cap reached, or every backend refused — looks like a
// board with no jobs at all.
type pendingJSON struct {
	// Total is every posting awaiting triage.
	Total int `json:"total"`
	// Matching is how many of them the query names (Total when there is no
	// query), so "no Acme jobs" can read "N Acme jobs await triage".
	Matching int `json:"matching"`
	// Companies are the companies with the most postings awaiting triage,
	// most first, at most maxPendingCompanies of them.
	Companies []pendingCompanyJSON `json:"companies"`
}

type pendingCompanyJSON struct {
	Company string `json:"company"`
	N       int    `json:"n"`
}

// printPending is the text form: one line, only when something is waiting.
func (e *env) printPending(p pendingJSON, query string) {
	if p.Total == 0 {
		return
	}
	if query != "" {
		fmt.Fprintf(e.stdout, "awaiting triage (not listed until a run triages them): %d matching %q, %d in all\n", p.Matching, query, p.Total)
		return
	}
	var top []string
	for _, c := range p.Companies[:min(len(p.Companies), 5)] {
		top = append(top, fmt.Sprintf("%s %d", c.Company, c.N))
	}
	fmt.Fprintf(e.stdout, "awaiting triage (not listed until a run triages them): %d (%s)\n", p.Total, strings.Join(top, ", "))
}

// pendingOf counts the postings awaiting triage, and those the query
// (matchesQuery's rule, q lowered and trimmed) names.
func pendingOf(snap ledger.Snapshot, m map[posting.Key]marks.Entry, q string) pendingJSON {
	out := pendingJSON{Companies: []pendingCompanyJSON{}}
	by := map[string]int{}
	for _, r := range snap.Records {
		if r.Status != posting.Live {
			continue
		}
		out.Total++
		j := toJob(r, m[r.Key])
		by[j.Company]++
		if matchesQuery(j, q) {
			out.Matching++
		}
	}
	for c, n := range by {
		out.Companies = append(out.Companies, pendingCompanyJSON{Company: c, N: n})
	}
	slices.SortFunc(out.Companies, func(a, b pendingCompanyJSON) int {
		if c := cmp.Compare(b.N, a.N); c != 0 {
			return c
		}
		return strings.Compare(a.Company, b.Company)
	})
	if len(out.Companies) > maxPendingCompanies {
		out.Companies = out.Companies[:maxPendingCompanies]
	}
	return out
}
