package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A Duplicate is hidden because its duplicate_of survivor stands in for it. If
// the survivor is later gone while the board still lists the duplicate, nothing
// stands in for it and the job would vanish from every view. This rule brings
// such a duplicate back. It is keyed on the duplicate_of annotation (Rule 7:
// an engine rule on an annotation), runs only for a record the board lists
// this run, and decides on the survivor's state as the ledger holds it now.
//
// Gone means missing from the ledger or Dead. A Dropped survivor is not gone:
// an engine rule rejected that job on its triage annotations, and its duplicate
// carries the same role and text, so reviving it would only triage and drop it
// again. A survivor that is itself a Duplicate is not followed (a chain is
// revived link by link, as each link is listed).

// dupAnnPrefix prefixes the near-duplicate annotations (dup.method, dup.score,
// dup.candidate_of, ...), which describe the survivor the duplicate was matched to.
const dupAnnPrefix = "dup."

// survivorGone reports whether the record a duplicate points at no longer
// stands in for it.
func survivorGone(l ledger.Ledger, k posting.Key) bool {
	r, ok := l.Get(k)
	return !ok || r.Status == posting.Dead
}

// reviveDuplicate takes the listed duplicate rec back when its survivor is
// gone, and reports whether it did. p is the listing's current version. The
// record goes Live with its duplicate annotations cleared, so the pipeline
// triages it; if another live record is provably the same job (twinOf, ranked
// at the record's own first sighting) it instead becomes that record's
// duplicate in the same event, unless the user marked it (protected). A record
// that lands Live then does what a newly written one does (markTwins): the live
// records it outranks become its duplicates in this run. marked counts those.
func reviveDuplicate(ctx context.Context, l ledger.Ledger, rec ledger.Record, p posting.Posting, hash string, now time.Time, idx *ledgerIndex, twinned map[posting.Key]bool, protected func(posting.Key) bool) (revived bool, marked int, err error) {
	of := posting.Key(rec.Annotations[DuplicateOfKey])
	if of == "" || !survivorGone(l, of) {
		return false, 0, nil
	}
	var twin posting.Key
	var why string
	if protected == nil || !protected(rec.Key) {
		if k, w, ok := twinOf(p, rec.FirstSeen, idx); ok && k != of {
			if r, found := l.Get(k); found && !closed(r.Status) {
				twin, why = k, w
			}
		}
	}
	if err := l.Append(ctx, reviveEvent(rec, p, hash, now, twin, why)); err != nil {
		return false, 0, err
	}
	if twin != "" {
		return true, 0, nil
	}
	marked, err = markTwins(ctx, l, p, rec.FirstSeen, now, idx, twinned, protected)
	return true, marked, err
}

// reviveEvent is the event that takes rec back from its gone survivor: Live,
// or a duplicate of twin when one is given. It carries the listed posting and
// clears every duplicate annotation the record holds.
func reviveEvent(rec ledger.Record, p posting.Posting, hash string, now time.Time, twin posting.Key, why string) ledger.Event {
	ann := map[string]string{DescHashKey: hash, DuplicateOfKey: ""}
	if sourceHash(rec.Posting.Description) != sourceHash(p.Description) {
		if score, keep := keepAcrossEdit(rec, p); keep {
			for k, v := range keptAnnotations(rec, score) {
				ann[k] = v
			}
		} else {
			for k, v := range staleAnnotations(rec.Annotations) {
				ann[k] = v
			}
		}
	}
	for k := range rec.Annotations {
		if strings.HasPrefix(k, dupAnnPrefix) {
			ann[k] = ""
		}
	}
	e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: rec.Key, Status: posting.Live, Posting: &p, Annotations: ann}
	gone := rec.Annotations[DuplicateOfKey]
	e.Reason = "survivor " + gone + " gone: revived"
	if twin != "" {
		e.Status = posting.Duplicate
		ann[DuplicateOfKey] = string(twin)
		e.Reason = "survivor " + gone + " gone: duplicate of " + string(twin)
		if why != "" {
			e.Reason += " (" + why + ")"
		}
	}
	return e
}
