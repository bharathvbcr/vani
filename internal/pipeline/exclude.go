package pipeline

import (
	"context"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// exclusionBatch bounds the events one ApplyExclusions write carries.
const exclusionBatch = 256

// excludable are the statuses an exclusion drops: every posting still on its
// way to the owner or listed in Jobs. Dead, duplicate and dropped postings are
// already out of view.
var excludable = map[posting.Status]bool{
	posting.Live: true, posting.Triaged: true, posting.Unsure: true,
	posting.Scored: true, posting.Explained: true, posting.Digested: true,
}

// Excluded reports whether r was dropped by an exclusion (EvaluatePosting or
// ApplyExclusions).
func Excluded(r ledger.Record) bool {
	return r.Status == posting.Dropped && r.Annotations[AnnExcluded] != ""
}

// ExclusionChange is what the exclusions would do to the ledger: the records
// they would drop, with the reason for each, and the excluded records they no
// longer match, which return to Live.
type ExclusionChange struct {
	Drop    []ledger.Record
	Reasons []string // Reasons[i] is why Drop[i] is dropped
	Reopen  []ledger.Record
	// Unread are the Live, unprotected postings kept because a location rule
	// could not read their location (DropPolicy.ExcludeUnreadable): the check
	// could not run, which is not the same as passing it.
	Unread []ledger.Record
}

// ExclusionChanges reads what p's exclusions would do to recs, in their
// order. A record protect reports (one the owner marked) is never dropped; an
// excluded record is reopened whether or not it is marked. It writes nothing.
func ExclusionChanges(recs []ledger.Record, p DropPolicy, protect func(posting.Key) bool) ExclusionChange {
	var ch ExclusionChange
	for _, r := range recs {
		why, ok := p.excluded(r.Posting)
		switch {
		case Excluded(r):
			if !ok {
				ch.Reopen = append(ch.Reopen, r)
			}
		case ok && excludable[r.Status] && (protect == nil || !protect(r.Key)):
			ch.Drop = append(ch.Drop, r)
			ch.Reasons = append(ch.Reasons, why)
		case !ok && r.Status == posting.Live && p.ExcludeUnreadable != nil && p.ExcludeUnreadable(r.Posting) &&
			(protect == nil || !protect(r.Key)):
			ch.Unread = append(ch.Unread, r)
		}
	}
	return ch
}

// ApplyExclusions makes the ledger agree with p's exclusions: it drops every
// unprotected Live or kept record they match, recording the reason under
// AnnExcluded and keeping the record's other annotations (a model's reading
// stays as evidence for `vani prefilter`), and returns to Live every record an
// exclusion dropped that they no longer match, forgetting what triage built
// on it. It asks no backend. It writes in batches of at most exclusionBatch;
// an error leaves the batches before it written, and the next run finishes.
func ApplyExclusions(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, protect func(posting.Key) bool, fo *FailOpen) (ExclusionChange, error) {
	recs := l.All()
	for i, r := range recs {
		// An exclusion on descriptions is judged against the text, which a
		// Dropped record keeps out of memory; without it the exclusion would
		// look lifted and the record would be reopened.
		if r.BodyEvicted && (Excluded(r) || excludable[r.Status]) {
			recs[i] = ledger.Full(l, r)
		}
	}
	ch := ExclusionChanges(recs, p, protect)
	for range ch.Unread {
		fo.Note(CauseLocationUnreadable)
	}
	evs := make([]ledger.Event, 0, len(ch.Drop)+len(ch.Reopen))
	for i, r := range ch.Drop {
		evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Dropped,
			Annotations: map[string]string{AnnExcluded: ch.Reasons[i]}, Reason: ch.Reasons[i]})
	}
	for _, r := range ch.Reopen {
		evs = append(evs, reopenEvent(r, now, "the exclusions no longer match it"))
	}
	for len(evs) > 0 {
		if err := ctx.Err(); err != nil {
			return ch, err
		}
		n := min(len(evs), exclusionBatch)
		if err := l.AppendBatch(ctx, evs[:n]); err != nil {
			return ch, err
		}
		evs = evs[n:]
	}
	return ch, nil
}
