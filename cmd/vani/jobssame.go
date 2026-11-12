package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/samecompany"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// The two company-history notes on a job (internal/samecompany): other
// postings at the company applied to lately, and a likely relisting of an
// earlier role. Display-only, like the rest of the insight: they never drop or
// block a posting.

// sameCompany builds the samecompany index the first time a row asks for it,
// from the snapshot the insight was built over, so a caller that never
// enriches a row (the daily run's notes gate) pays nothing.
type sameCompany struct {
	once  sync.Once
	build func() *samecompany.Index
	ix    *samecompany.Index
}

func newSameCompany(cfg config.Config, cfgErr error, snap ledger.Snapshot, m map[posting.Key]marks.Entry, now time.Time) *sameCompany {
	return newSameCompanyFrom(cfg, cfgErr, m, now, func(co *samecompany.Companies) *samecompany.Groups {
		return samecompany.NewGroups(snap.Records, co)
	})
}

// newSameCompanyFrom is newSameCompany over groups, which returns the
// company-history groups of the snapshot's records under co: built now, or
// kept by a resident reader (jobsResident.sameGroups). The marks and the clock
// are always this read's.
func newSameCompanyFrom(cfg config.Config, cfgErr error, m map[posting.Key]marks.Entry, now time.Time, groups func(*samecompany.Companies) *samecompany.Groups) *sameCompany {
	var boards []sources.Board
	if cfgErr == nil {
		boards = cfg.Watchlist
	}
	return &sameCompany{build: func() *samecompany.Index {
		return groups(samecompany.NewCompanies(boards)).Index(m, now)
	}}
}

func (s *sameCompany) index() *samecompany.Index {
	if s == nil || s.build == nil {
		return nil
	}
	s.once.Do(func() { s.ix = s.build() })
	return s.ix
}

// annotateSame fills j's company-history notes from r.
func (in *insight) annotateSame(j *jobview.Job, r ledger.Record) {
	ix := in.same.index()
	if ix == nil {
		return
	}
	if a, ok := ix.RepeatApply(r.Key, r.Posting.Company); ok {
		j.RepeatApply = jobview.NewRepeatApplyNote(j.Company, a.Count, samecompany.ApplyWindowDays, a.Latest.Key, a.Latest.Title, a.Latest.At)
	}
	if p, ok := ix.Repost(r); ok {
		j.Repost = jobview.NewRepostNote(p.Key, p.Title, p.At)
	}
}

// sameNote is the text list's flag for a job's company-history notes: "" when
// it has none.
func sameNote(j jobview.Job) string {
	switch {
	case j.RepeatApply != nil && j.Repost != nil:
		return "  (" + j.Repost.Text + "; " + sameApplyShort(j.RepeatApply) + ")"
	case j.Repost != nil:
		return "  (" + j.Repost.Text + ")"
	case j.RepeatApply != nil:
		return "  (" + sameApplyShort(j.RepeatApply) + ")"
	}
	return ""
}

func sameApplyShort(n *jobview.RepeatApplyNote) string {
	return fmt.Sprintf("already applied to %d other role(s) here in %dd", n.Count, n.WindowDays)
}
