package jobview

import (
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Scope is the part of a `jobs list` selection that reads the posting alone,
// with no triage reading or score: the companies and the places. A run can
// apply it before anything is triaged (run-once --company, --state, --area,
// --remote and --or-remote limit its advance phase), and `jobs list --view
// pending` with the same flags lists exactly the postings awaiting triage that
// such a run would advance, because both ask Keep. A nil set takes everything.
type Scope struct {
	Companies        map[string]bool // CompanyKey of each company
	States, Areas    map[string]bool // as Opts: USPS codes, folded areas, "none"
	Remote, OrRemote bool
}

// IsZero reports a scope that takes every posting.
func (s Scope) IsZero() bool {
	return s.Companies == nil && s.States == nil && s.Areas == nil && !s.Remote && !s.OrRemote
}

// Keep reports whether the posting is in the scope: one of its companies, and
// in its places as Opts.PlaceOK reads them.
func (s Scope) Keep(p posting.Posting) bool {
	if s.Companies != nil && !s.Companies[CompanyKey(CleanText(p.Company, FieldRunes, false))] {
		return false
	}
	if !s.Remote && s.States == nil && s.Areas == nil {
		return true
	}
	var j Job
	Locate(&j, p)
	o := Opts{States: s.States, Areas: s.Areas, Remote: s.Remote, OrRemote: s.OrRemote}
	return o.PlaceOK(j)
}

// CompanyKey is how company names compare: case and spacing folded.
func CompanyKey(company string) string {
	return strings.ToLower(strings.Join(strings.Fields(company), " "))
}
