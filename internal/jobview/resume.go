package jobview

import (
	"errors"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// ResumeSummary is a job's tailored resume as `vani jobs` reports it: how many
// versions the store holds, which is current (the one the Studio shows) and
// which is used (bound for applying), and the current version's pages, fit,
// eligibility and source. It is read from the version records only; the
// choice and the PDF never come through here.
//
// A job with no versions has no summary (Job.Resume is nil). A store that could
// not be read is a summary with Error set ("unreadable"), never an absent one:
// absence would read as "no resume was ever built".
type ResumeSummary struct {
	Current  int `json:"current,omitempty"`
	Used     int `json:"used,omitempty"`
	Versions int `json:"versions"`
	// Pages, Fit, Eligibility and Source are the current version's. Fit and
	// Eligibility are the check states resumestore writes (never ""); Pages 0
	// is unknown.
	Pages       int    `json:"pages,omitempty"`
	Fit         string `json:"fit,omitempty"`
	Eligibility string `json:"eligibility,omitempty"`
	Source      string `json:"source,omitempty"`
	// Error is "unreadable" when the store could not be read or holds damage
	// for this job, "unsupported" when its records are of another schema. The
	// other fields are then absent: the numbers are not known.
	Error string `json:"error,omitempty"`
}

// ResumeOf is the summary of s, or of err when the job's resume could not be
// read. nil means the job has no resume.
func ResumeOf(s resumestore.Summary, err error) *ResumeSummary {
	switch {
	case errors.Is(err, resumestore.ErrSchema):
		return &ResumeSummary{Error: "unsupported"}
	case err != nil:
		return &ResumeSummary{Error: "unreadable"}
	case s.Versions == 0:
		return nil
	}
	r := &ResumeSummary{Versions: s.Versions}
	if s.Current != nil {
		r.Current = s.Current.Version
		r.Pages, r.Source = s.Current.Pages, s.Current.Source
		r.Fit, r.Eligibility = s.Current.Checks.Fit, s.Current.Checks.Eligibility
	}
	if s.Used != nil {
		r.Used = s.Used.Version
	}
	return r
}

// ResumeFor is key's summary from x, the store read once for the whole listing
// (resumestore.Store.Summaries); nil x (no store) is no resume. It touches no
// file: a listing of many jobs does one walk, not a probe per job.
func ResumeFor(x *resumestore.Summaries, key string) *ResumeSummary {
	if x == nil {
		return nil
	}
	return ResumeOf(x.Of(key))
}
