package pipeline

import (
	"context"
	"errors"
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
	// Reopen are the excluded records the exclusions no longer match: the ones
	// that would come back, whether ApplyExclusions lets them back at once or
	// waits for a sighting (Deferred).
	Reopen []ledger.Record
	// What ApplyExclusions did with Reopen under a ReopenScope. Reopened came
	// back to Live at once (no full listing to wait for: a weakly keyed posting,
	// or no scope); Deferred were marked as pending reopens, still dropped, for
	// their board's next full listing to show them (SightingReopen);
	// AlreadyPending were marked by an earlier pass; OverCap were left out
	// because the set or its exclusion share is full (MaxPendingReopens,
	// MaxPendingExclusions); Stale are on a board no longer watched and stay
	// dropped, as in RetriagePolicyDrops; Unqueued counts the pending reopens it
	// cleared because the exclusions match their record again; Gone are excluded
	// records whose pending reopen already expired unseen, left unmarked until
	// their board lists them (PendingReopens.Gone).
	Reopened, Deferred, AlreadyPending, OverCap, Stale, Unqueued, Gone int
	// Unread are the Live, unprotected postings kept because a location rule
	// could not read their location (DropPolicy.ExcludeUnreadable): the check
	// could not run, which is not the same as passing it.
	Unread []ledger.Record
	// BodyUnavailable counts the records ApplyExclusions left untouched because
	// their evicted description could not be read back from the ledger.
	BodyUnavailable int
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
// stays as evidence for `vani prefilter`), and lets back every record an
// exclusion dropped that they no longer match, forgetting what triage built
// on it. It asks no backend. It writes in batches of at most exclusionBatch;
// an error leaves the batches before it written, and the next run finishes.
//
// A drop is closed, so no listing ages it and the ledger cannot say whether its
// board still lists the role: letting back every record a loosened exclusion
// frees at once would flood scoring, the paid notes call and the digest with
// roles that may be long gone. So under sc (the one pending-reopen gate
// RetriagePolicyDrops uses, pendingreopen.go) a record on a watched board is
// only marked as a pending reopen, still dropped; the run's listing lets it
// back when its board lists it again, judged on the listed text, and clears it
// after PendingReopenExpiry of absence. A record with no full listing to wait
// for (weakly keyed) comes back at once; one on a board no longer watched
// stays dropped. The caller saves sc.Pending afterwards, whatever the error. A
// nil sc has no listing to wait for and lets every record back at once; a sc
// with no Pending is refused.
func ApplyExclusions(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, protect func(posting.Key) bool, fo *FailOpen, sc *ReopenScope) (ExclusionChange, error) {
	if sc != nil && sc.Pending == nil {
		return ExclusionChange{}, errors.New("pipeline: letting back excluded postings needs somewhere to keep their pending reopens")
	}
	all := l.All()
	recs := all[:0:0]
	unavailable := 0
	for _, r := range all {
		// An exclusion on descriptions is judged against the text, which a
		// Dropped record keeps out of memory; without it the exclusion would
		// look lifted and the record would be reopened.
		if r.BodyEvicted && (Excluded(r) || excludable[r.Status]) {
			if r = ledger.Full(l, r); r.BodyEvicted {
				// The body cannot be read back, so the exclusions cannot be
				// judged: leave the record as it is (a check that could not
				// run is not a pass, and a refused reopen would fail the whole
				// batch and starve every other drop), and count it.
				unavailable++
				continue
			}
		}
		recs = append(recs, r)
	}
	ch := ExclusionChanges(recs, p, protect)
	ch.BodyUnavailable = unavailable
	for range ch.Unread {
		fo.Note(CauseLocationUnreadable)
	}
	evs := make([]ledger.Event, 0, len(ch.Drop)+len(ch.Reopen))
	for i, r := range ch.Drop {
		evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Dropped,
			Annotations: map[string]string{AnnExcluded: ch.Reasons[i]}, Reason: ch.Reasons[i]})
	}
	for _, r := range letBack(&ch, recs, p, now, sc) {
		evs = append(evs, reopenEvent(r, now, reasonExclusionDrop))
	}
	ch.Reopened = len(evs) - len(ch.Drop)
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

// letBack sorts ch.Reopen under sc and returns the records to reopen now,
// marking the others pending (counting each outcome on ch). It also clears the
// pending reopens of excluded records the exclusions match again (the owner put
// the rule back before the board listed them), judged on recs.
func letBack(ch *ExclusionChange, recs []ledger.Record, p DropPolicy, now time.Time, sc *ReopenScope) []ledger.Record {
	if sc == nil {
		return ch.Reopen
	}
	for _, r := range recs {
		if e, ok := sc.Pending.Items[r.Key]; ok && e.Class == reopenClassExclusion && Excluded(r) {
			if _, matched := p.excluded(r.Posting); matched {
				delete(sc.Pending.Items, r.Key)
				ch.Unqueued++
			}
		}
	}
	// An exclusion drop that expired unseen (Gone) is not an excluded record any
	// more once something else reopened it; forget those.
	if len(sc.Pending.Gone) > 0 {
		excluded := make(map[posting.Key]bool, len(recs))
		for _, r := range recs {
			if Excluded(r) {
				excluded[r.Key] = true
			}
		}
		for k := range sc.Pending.Gone {
			if !excluded[k] {
				delete(sc.Pending.Gone, k)
			}
		}
	}
	excl := sc.Pending.count(reopenClassExclusion)
	var atOnce []ledger.Record
	for _, r := range ch.Reopen {
		switch {
		case !sc.reachable(r.Key):
			ch.Stale++
		case sc.deferred(r.Key):
			prev, queued := sc.Pending.Items[r.Key]
			_, gone := sc.Pending.Gone[r.Key]
			switch {
			case queued && prev.Class == reopenClassExclusion:
				ch.AlreadyPending++
			case gone:
				// Its pending reopen expired unseen, so its board has not listed
				// it for PendingReopenExpiry: it stays dropped until a full
				// listing shows it again (SightingReopen.sighted), not marked
				// again every two weeks.
				ch.Gone++
			case !queued && (sc.Pending.Len() >= MaxPendingReopens || excl >= MaxPendingExclusions):
				ch.OverCap++
			default:
				excl++
				sc.Pending.Items[r.Key] = PendingReopen{Class: reopenClassExclusion, At: now.UTC(), Reason: reasonExclusionDrop}
				ch.Deferred++
			}
		default:
			atOnce = append(atOnce, r)
		}
	}
	return atOnce
}
