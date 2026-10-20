package pipeline

import "sync"

// Causes a check could not run and the posting was kept anyway (failed open).
// A kept posting whose check never ran is not a posting that passed it, so
// each cause is counted and surfaced in the run report.
const (
	// CauseEmptyDescription: the description was blank or shorter than the
	// triage minimum, so the paid triage call was skipped.
	CauseEmptyDescription = "empty_description"
	// CauseTitleUnread: title readings are configured but the title was blank,
	// so no title rule could read it.
	CauseTitleUnread = "title_unread"
	// CauseLocationUnreadable: an exclusion rule could not read the posting's
	// location, so the posting was kept. Counted by the exclusion stage.
	CauseLocationUnreadable = "location_unreadable"
	// CausePrescoreFailed: DevPrism failed before triage, so coverage is
	// unknown; triage went ahead (coverage is never a gate).
	CausePrescoreFailed = "prescore_failed"
)

// FailOpenCounts is a run's fail-open tally, one counter per cause. It is
// comparable so it can sit inside a report struct.
type FailOpenCounts struct {
	EmptyDescription   int `json:"empty_description,omitempty"`
	TitleUnread        int `json:"title_unread,omitempty"`
	LocationUnreadable int `json:"location_unreadable,omitempty"`
	PrescoreFailed     int `json:"prescore_failed,omitempty"`
	// Other counts a cause this build has no counter for, so an unknown
	// cause is never dropped silently.
	Other int `json:"other,omitempty"`
}

// Total is every kept-unchecked posting.
func (c FailOpenCounts) Total() int {
	return c.EmptyDescription + c.TitleUnread + c.LocationUnreadable + c.PrescoreFailed + c.Other
}

// FailOpen counts fail-open keeps by cause. Safe for concurrent use; a nil
// *FailOpen counts nothing.
type FailOpen struct {
	mu sync.Mutex
	c  FailOpenCounts
}

// Note counts one posting kept because the check named by cause could not run.
func (f *FailOpen) Note(cause string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch cause {
	case CauseEmptyDescription:
		f.c.EmptyDescription++
	case CauseTitleUnread:
		f.c.TitleUnread++
	case CauseLocationUnreadable:
		f.c.LocationUnreadable++
	case CausePrescoreFailed:
		f.c.PrescoreFailed++
	default:
		f.c.Other++
	}
}

// Counts is the tally so far.
func (f *FailOpen) Counts() FailOpenCounts {
	if f == nil {
		return FailOpenCounts{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}
