package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// There is no list of target companies. The companies the owner has shown
// interest in are the next best thing: ones they saved or applied to a posting
// of, or had the browser extension ingest a posting of. When no board of the
// watchlist covers such a company, its new roles never reach them, which is
// worth an attention line. This is reporting only: it reads the ledger snapshot
// and the marks the status command already has, and asks the network nothing.

// maxUnwatchedListed bounds the companies `vani status` names.
const maxUnwatchedListed = 20

// unwatchedCompany is one company the owner engaged with that no watched board
// covers, and how: postings applied to or saved, and postings ingested through
// the extension (a scraped page's job, key pg:<host>-scrape:...).
type unwatchedCompany struct {
	Company  string `json:"company"`
	Applied  int    `json:"applied,omitempty"`
	Saved    int    `json:"saved,omitempty"`
	Ingested int    `json:"ingested,omitempty"`
}

// unwatchedFacet is status's "companies you engaged with but don't watch".
// Count is every such company; Companies the first Listed of them, most engaged
// first. Not ok means the marks could not be read, so saved and applied
// postings are left out of Count.
type unwatchedFacet struct {
	facet
	Count     int                `json:"count"`
	Listed    int                `json:"listed"`
	Companies []unwatchedCompany `json:"companies,omitempty"`
}

// unwatchedCompaniesOf finds the engaged companies among recs that cfg's
// watchlist does not cover. A company is covered when one of its postings sits
// under a watched board's key prefix, or a board's company name is the same
// company: case, punctuation and a legal suffix ("Inc") do not tell two
// companies apart (pipeline.LooseCompany). A hidden posting is no engagement,
// and neither is one nobody saved, applied to or ingested.
func unwatchedCompaniesOf(recs []ledger.Record, cfg config.Config, stateDir string) unwatchedFacet {
	f := unwatchedFacet{facet: facet{OK: true}}
	m, err := marks.ReadMarks(marks.Dir(stateDir))
	if err != nil {
		f.OK, f.Reason = false, oneLine("marks unreadable, so saved and applied postings are not counted: "+err.Error(), 300)
		m = nil
	}
	watchedKey := pipeline.WatchedBy(cfg.Watchlist)
	watchedName := map[string]bool{}
	for _, b := range cfg.Watchlist {
		if k := pipeline.LooseCompany(b.Company); k != "" {
			watchedName[k] = true
		}
	}
	type acc struct {
		unwatchedCompany
		names map[string]int
	}
	by := map[string]*acc{}
	for _, r := range recs {
		var applied, saved, ingested int
		switch m[r.Key] {
		case marks.Applied:
			applied = 1
		case marks.Saved:
			saved = 1
		}
		if isScrapedKey(r.Key) {
			ingested = 1
		}
		name := strings.Join(strings.Fields(r.Posting.Company), " ")
		if applied+saved+ingested == 0 || name == "" {
			continue
		}
		key := pipeline.LooseCompany(name)
		if key == "" || watchedName[key] || watchedKey(r.Key) {
			continue
		}
		a := by[key]
		if a == nil {
			a = &acc{names: map[string]int{}}
			by[key] = a
		}
		a.Applied += applied
		a.Saved += saved
		a.Ingested += ingested
		a.names[name]++
	}
	all := make([]unwatchedCompany, 0, len(by))
	for _, a := range by {
		// The spelling most postings use; a tie goes to the shorter, then the
		// first in order, so the answer does not depend on map order.
		best := ""
		for n, c := range a.names {
			if best == "" || c > a.names[best] || c == a.names[best] && (len(n) < len(best) || len(n) == len(best) && n < best) {
				best = n
			}
		}
		a.Company = jobview.CleanText(best, 120, false)
		all = append(all, a.unwatchedCompany)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.Applied != b.Applied:
			return a.Applied > b.Applied
		case a.Saved != b.Saved:
			return a.Saved > b.Saved
		case a.Ingested != b.Ingested:
			return a.Ingested > b.Ingested
		}
		return a.Company < b.Company
	})
	f.Count = len(all)
	f.Companies = all[:min(len(all), maxUnwatchedListed)]
	f.Listed = len(f.Companies)
	return f
}

// isScrapedKey reports whether k is a posting the browser extension ingested
// from a page that is not a saved board's own (config.ScrapeSuffix ends the
// board token of those batches).
func isScrapedKey(k posting.Key) bool {
	if k.ATS() != posting.Page {
		return false
	}
	parts := strings.SplitN(string(k), ":", 3)
	return len(parts) == 3 && strings.HasSuffix(parts[1], config.ScrapeSuffix)
}

// unwatchedLines are the status lines for f: nothing when every engaged company
// is watched, and a line of its own when the marks could not be read.
func unwatchedLines(f unwatchedFacet) []string {
	var out []string
	if f.Reason != "" {
		out = append(out, "engaged companies: "+f.Reason)
	}
	if f.Count == 0 {
		return out
	}
	out = append(out, fmt.Sprintf("companies you engaged with but don't watch: %d (saved, applied to or ingested through the extension; no board of the watchlist covers them)", f.Count))
	for _, c := range f.Companies {
		var how []string
		if c.Applied > 0 {
			how = append(how, fmt.Sprintf("applied %d", c.Applied))
		}
		if c.Saved > 0 {
			how = append(how, fmt.Sprintf("saved %d", c.Saved))
		}
		if c.Ingested > 0 {
			how = append(how, fmt.Sprintf("ingested %d", c.Ingested))
		}
		out = append(out, fmt.Sprintf("  %s (%s)", oneLine(c.Company, 120), strings.Join(how, ", ")))
	}
	if hidden := f.Count - f.Listed; hidden > 0 {
		out = append(out, fmt.Sprintf("  (%d more)", hidden))
	}
	return out
}
