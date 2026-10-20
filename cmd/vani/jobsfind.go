package main

import (
	"fmt"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/joburl"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani jobs find --url URL` answers "which tracked posting is this page?" for
// the browser extension, which offers to mark that job Applied. It only reads:
// the mark itself is `jobs mark`, behind the user's click. The URL is page
// text and untrusted; it is parsed (internal/joburl), compared and never
// fetched. Pages are matched on scheme, host and path (plus the few query
// parameters that name a job, joburl.IDParams) against each posting's url and
// apply_url. When nothing matches exactly, an application sub-page
// (/apply, /application) is matched to the posting it belongs to, and then a
// Workday page is matched across its routes (joburl.WorkdayPosting).

// Which stored link a page matched.
const (
	matchedURL      = "url"
	matchedApplyURL = "apply_url"
	matchedBase     = "base" // an application sub-page of the posting's link
	// matchedPosting: another route to the same Workday posting (locale, location,
	// /details, apply pages) than the stored link.
	matchedPosting = "posting"
)

// jobRef is the slice of a job the extension needs to ask its question.
type jobRef struct {
	Key       posting.Key    `json:"key"`
	Company   string         `json:"company"`
	Title     string         `json:"title"`
	Status    posting.Status `json:"status"`
	Closed    bool           `json:"closed,omitempty"`
	Mark      marks.Mark     `json:"mark,omitempty"`
	MatchedOn string         `json:"matched_on"` // url, apply_url or base
	// Row is the versioned contract (internal/jobview): Vani's own relevance,
	// priority and fit label for this posting, for the extension to show.
	Row *jobview.Row `json:"row,omitempty"`
}

type jobFindJSON struct {
	// URL is the page as compared: scheme, host, path and job-naming parameters.
	URL string `json:"url"`
	// Matches counts every record the URL names, duplicates included.
	Matches int `json:"matches"`
	// Ambiguous: more than one posting claims the URL, so none is offered.
	Ambiguous bool    `json:"ambiguous,omitempty"`
	Job       *jobRef `json:"job,omitempty"`
	Torn      bool    `json:"torn,omitempty"`
	// Ledger and Records say which state was searched, so a no-match from the
	// wrong state folder is not mistaken for "not a tracked job".
	Ledger  string `json:"ledger"`
	Records int    `json:"records"`
}

func (e *env) jobsFind(args []string) error {
	fs := e.newFlags("jobs find")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	rawURL := fs.String("url", "", "the page URL (http or https, at most 2048 bytes)")
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobFindJSON
	err := func() error {
		if fs.NArg() != 0 {
			return usagef("jobs find: unexpected argument")
		}
		page, err := joburl.Normalize(*rawURL)
		if err != nil {
			return usagef("jobs find: --url: %v", err)
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		out = matchPageURL(snap, m, page)
		if out.Job != nil {
			if r, ok := findRecord(snap, out.Job.Key); ok {
				in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
				if err != nil {
					return err
				}
				j := toJob(r, m[r.Key])
				in.enrich(&j, r, 0)
				out.Job.Row = j.Row
			}
		}
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		switch {
		case out.Job != nil:
			fmt.Fprintf(e.stdout, "%s: %s at %s (%s)\n", out.Job.Key, out.Job.Title, out.Job.Company, orDash(string(out.Job.Mark)))
		case out.Ambiguous:
			fmt.Fprintf(e.stdout, "%d postings share %s\n", out.Matches, out.URL)
		default:
			fmt.Fprintf(e.stdout, "no posting at %s\n", out.URL)
		}
	})
}

type pageHit struct {
	r  ledger.Record
	on string
}

// matchPageURL matches the normalised page URL against every record's url and
// apply_url, then, only if none matches, against the posting an application
// sub-page belongs to. A posting that is a duplicate of another only counts
// when nothing else claims the page, and more than one claimant means no
// answer: marking the wrong job is worse than not offering.
func matchPageURL(snap ledger.Snapshot, m map[posting.Key]marks.Entry, page string) jobFindJSON {
	out := jobFindJSON{URL: page, Torn: snap.Torn}
	pageBase, pageIsSub := joburl.ApplyBase(page)
	if !pageIsSub {
		pageBase = page
	}
	pageFold, pageIsWorkday := joburl.WorkdayPosting(page)
	var exact, sub, fold []pageHit
	for _, r := range snap.Records {
		if on := exactLink(r.Posting, page); on != "" {
			exact = append(exact, pageHit{r, on})
		} else if baseLink(r.Posting, pageBase, pageIsSub) {
			sub = append(sub, pageHit{r, matchedBase})
		} else if pageIsWorkday && workdayLink(r.Posting, pageFold) {
			fold = append(fold, pageHit{r, matchedPosting})
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = sub
	}
	if len(hits) == 0 {
		hits = fold
	}
	out.Matches = len(hits)
	var firm, dup []pageHit
	for _, h := range hits {
		if h.r.Status == posting.Duplicate {
			dup = append(dup, h)
		} else {
			firm = append(firm, h)
		}
	}
	cands := firm
	if len(cands) == 0 {
		cands = dup
	}
	switch {
	case len(cands) == 0:
	case len(cands) > 1:
		out.Ambiguous = true
	default:
		r := cands[0].r
		j := toJob(r, m[r.Key])
		out.Job = &jobRef{Key: j.Key, Company: j.Company, Title: j.Title, Status: j.Status, Closed: j.Closed, Mark: j.Mark, MatchedOn: cands[0].on}
	}
	return out
}

// exactLink says which of the posting's links is the page, or "". A link that
// cannot be normalised matches nothing.
func exactLink(p posting.Posting, page string) string {
	if n, err := joburl.Normalize(p.URL); err == nil && n == page {
		return matchedURL
	}
	if n, err := joburl.Normalize(p.ApplyURL); err == nil && n == page {
		return matchedApplyURL
	}
	return ""
}

// workdayLink reports whether one of the posting's links is the same Workday
// posting as the page once both are folded across routes (locale, location
// encoding, /job vs /details, apply tail; joburl.WorkdayPosting).
func workdayLink(p posting.Posting, pageFold string) bool {
	for _, link := range []string{p.URL, p.ApplyURL} {
		n, err := joburl.Normalize(link)
		if err != nil {
			continue
		}
		if f, ok := joburl.WorkdayPosting(n); ok && f == pageFold {
			return true
		}
	}
	return false
}

// baseLink reports whether one of the posting's links and the page are the
// same posting once application sub-pages are cut back to it. At least one of
// the two must have had a sub-page segment, or exactLink would have matched.
func baseLink(p posting.Posting, pageBase string, pageIsSub bool) bool {
	for _, link := range []string{p.URL, p.ApplyURL} {
		n, err := joburl.Normalize(link)
		if err != nil {
			continue
		}
		b, isSub := joburl.ApplyBase(n)
		if !isSub {
			b = n
		}
		if (isSub || pageIsSub) && b == pageBase {
			return true
		}
	}
	return false
}
