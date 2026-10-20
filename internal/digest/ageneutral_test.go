package digest

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Two undated postings, equal in every annotation, both from the first crawl of
// their boards and first seen 40 days ago. Without a policy both are aged by
// that sighting and tie; an employer that closes by application cap is held at
// a flat freshness and so leads. The keys are chosen so a tie lists Acme first.
func TestDigestHoldsAgeNeutralEmployersFlat(t *testing.T) {
	ann := func() map[string]string {
		return map[string]string{
			ledger.AnnRank: "70", ledger.AnnCoverage: "70",
			"triage.role_fit": "partial_fit", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9",
		}
	}
	seen := day.Add(-40 * 24 * time.Hour)
	mk := func(key, company string) ledger.Record {
		r := rec(key, posting.Explained, "Software Engineer", ann())
		r.Posting.Company, r.FirstSeen = company, seen
		return r
	}
	acme, google := mk("aa:acme:1", "Acme"), mk("zz:google:1", "Google")
	recs := []ledger.Record{acme, google}

	_, tie := RenderWith(recs, nil, day, Counts{}, Options{})
	if len(tie) != 2 || tie[0] != "aa:acme:1" {
		t.Fatalf("with no policy the two tie, in key order: got %v", tie)
	}
	o := Options{AgeNeutral: jobinsight.AgeNeutralSet([]string{"Google"})}
	_, got := RenderWith(recs, nil, day, Counts{}, o)
	if len(got) != 2 || got[0] != "zz:google:1" {
		t.Fatalf("the age-neutral employer should lead: got %v", got)
	}
}
