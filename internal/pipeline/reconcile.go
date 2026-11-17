// Package pipeline is the deterministic per-posting state machine: it
// reconciles a board listing against the ledger and advances each posting
// through triage, scoring and explanation. It makes no network or model
// calls itself; every stage is an injected interface.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// DescHashKey holds the description hash of the stored posting version.
	// The key itself is the ledger contract's.
	DescHashKey = ledger.AnnDescHash
	// DuplicateOfKey names the ATS-keyed record a fallback-keyed posting repeats.
	DuplicateOfKey = "duplicate_of"
	// DiedFromKey holds the status a posting had when absence killed it, so
	// relisting the same content restores it instead of running it again.
	DiedFromKey = "died_from"
	// MissesToDead is how many consecutive absent listings kill a posting.
	MissesToDead = 2
)

// Summary counts what one Reconcile did.
type Summary struct {
	New     int
	Changed int
	// Rerendered postings carry the same source text in a new rendering (an
	// adapter now adds, changes or drops a synthesized line such as a pay
	// range): the stored posting takes the new rendering and keeps its status
	// and annotations.
	Rerendered int
	// Kept postings changed in a near-identical way (KeepAcrossEdit): the new
	// text is stored and the triage, score and explanation stand. Not in Changed.
	Kept int
	// Redated postings were stored without a posted date and are listed with
	// one (an adapter that learned to read it): the stored posting takes the
	// date and keeps its status and annotations. A stored date is never moved.
	Redated int
	// Refreshed postings carry the same content with other display fields
	// (refreshFields): the stored posting takes them and keeps its status and
	// annotations.
	Refreshed  int
	Unchanged  int
	Reappeared int // seen again after a miss (or after being marked dead)
	Missed     int // absent, below the death threshold
	// Held postings are absent with enough misses to die but inside their
	// hold (liveness.go): OrdinaryHold, or the mass hold when mass marked.
	// Deferred of them were due only by counting more than ClockWindow of the
	// gap since their last event, and wait one more read (liveness.go "The
	// clock").
	Held     int
	Deferred int
	Dead     int
	// MassDead of Dead were mass marked by a true mass absence (the share rule or
	// an empty listing): they waited out the mass hold. ShortDead of Dead were
	// held only because a short listing left them out (MassShortKey): expected
	// closures, reported and not counted in MassDead.
	MassDead  int
	ShortDead int
	// UnreadableDead of MassDead were held by a listing that skipped rows it
	// could not read (MassShortKey "unreadable"): counted in MassDead, so they
	// fail the run, and named apart so the report says why.
	UnreadableDead int
	// Open counts the board's open postings before a full listing; Absent how
	// many of them it left out. Zero for a partial listing.
	Open, Absent int
	// Mass says this listing was a mass absence (liveness.go); MassMarked counts
	// the postings it newly mass-marked (ones marked by an earlier listing and
	// still absent are Held, not counted again).
	MassMarked int
	Mass       bool
	// ClockBehind: the ledger holds events stamped ahead of this read's clock
	// (liveness.go "The clock"), so this full listing that left postings absent
	// aged nothing. ClockAhead is the event that says so: aging resumes once the
	// clock passes it. Either the clock is behind or those events were written
	// under a clock that ran ahead; the ledger cannot tell which.
	ClockBehind bool
	// ClockSetBack says the freeze rests only on a quorum of the board's own events
	// further ahead than ClockWindow (a clock set back by days), none within it.
	ClockSetBack bool
	ClockAhead   time.Time
	// Duplicate counts every record this fold stored or marked as a
	// duplicate: listed postings stored as one (ListedDuplicate) and other
	// records this listing marked as their twins.
	Duplicate int
	// ListedDuplicate is the part of Duplicate that is listed postings: new
	// ones stored as a duplicate of another record (twinOf). Each counts one
	// listed posting, as New does; the rest of Duplicate is not a listed one.
	ListedDuplicate int
	// Revived duplicates were listed again while their survivor was gone
	// (reviveDuplicate): live again, or a duplicate of another live record.
	Revived int
	// Reopened dropped postings had a pending reopen (SightingReopen) that
	// this full listing, showing them again, applied.
	Reopened int
	Invalid  int // listed postings skipped for a missing or malformed key
}

// staleAnnotations lists the derived results a changed description invalidates.
func staleAnnotations(a map[string]string) map[string]string {
	out := map[string]string{}
	for k := range a {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) || strings.HasPrefix(k, "score.") ||
			strings.HasPrefix(k, "gap.") || k == ledger.AnnExplanation || k == ledger.AnnRank || k == ledger.AnnCategory {
			out[k] = ""
		}
	}
	return out
}

// hasPendingReopen reports whether k waits for a sighting.
func (rc *Reconciler) hasPendingReopen(k posting.Key) bool {
	_, ok := rc.reopen.pending(k)
	return ok
}

// Reconcile folds one board's full listing into the ledger. A run with many
// boards uses a Reconciler, which reads the ledger once for all of them.
func Reconcile(ctx context.Context, l ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return NewReconciler(l).Reconcile(ctx, b, listed, now)
}

// Reconcile folds one board's full listing into the ledger. It sees the
// ledger as it stands when the board starts, as a fresh scan would: the
// boards before it included, this board's own writes not.
func (rc *Reconciler) Reconcile(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, true, shortNone)
}

// ReconcileShort folds a board's listing that counted more rows than it listed
// (a stub row, Workday's split allowance): the listing is real for the
// postings it lists, so sightings, pending reopens and field updates apply to
// them as in Reconcile, but it is not the board's whole word about what it
// left out, so every open posting absent from it is treated as a mass absence
// (liveness.go): kept and held for MassHold from its own first miss, and
// never killed by the ordinary rule. Pending reopens the listing leaves out
// are not aged by it at all (only a clean full listing counts toward
// PendingReopenExpiry): it cannot say the posting is gone.
func (rc *Reconciler) ReconcileShort(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, true, shortCounted)
}

// ReconcileUnreadable folds a short listing whose shortfall includes rows the
// adapter could not read (srcutil.BadRowSink). It is ReconcileShort with one
// difference that matters: a posting behind an unreadable row looks exactly like
// one its board stopped listing, so this listing never kills what it leaves
// out, however long it has been held, and a posting it marked dies later only on
// a listing that skipped nothing, as a mass death (it fails the run, and counts
// in Summary.UnreadableDead so the report says why). A board whose rows are
// unreadable on every run therefore keeps its postings, held, rather than
// losing them as an "expected closure".
func (rc *Reconciler) ReconcileUnreadable(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, true, shortUnreadable)
}

// shortKind is what a full listing says about the postings it leaves out.
type shortKind int

const (
	shortNone       shortKind = iota // it is the board's whole word
	shortCounted                     // it counted more rows than it listed
	shortUnreadable                  // ... and some rows it skipped could not be read
)

// ReconcilePartial folds postings that are part of a board's listing, not the
// whole of it (a page the user scraped in the browser): it shares Reconcile's
// listing loop but runs no absence pass, so a posting missing from the batch is
// never missed or aged. A partial listing says nothing about what is absent.
func (rc *Reconciler) ReconcilePartial(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, false, shortNone)
}

// reconcile is the listing loop both entry points share; full says the listing
// is the board's whole, so records it lacks are aged.
//
// The board's events are one ledger group (ledger.InGroup): written and folded
// as they come, so the loop reads its own writes, and made durable by one
// fsync when the board is done (more only past ledger.MaxBatchEvents) rather
// than one per event. A crash mid-board leaves a whole-line prefix of the
// board's events; the next listing writes the rest again. If the group's fsync
// fails the ledger is broken and none of the board's events stand, so the
// summary is zero.
func (rc *Reconciler) reconcile(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time, full bool, short shortKind) (Summary, error) {
	var s Summary
	var fnErr error
	err := ledger.InGroup(ctx, rc.l, func(g ledger.Ledger) error {
		s, fnErr = rc.reconcileIn(ctx, g, b, listed, now, full, short)
		return fnErr
	})
	// Group returns fn's error itself when its sync succeeded, so any other
	// error means the sync failed.
	if err != nil && err != fnErr {
		return Summary{}, err
	}
	return s, err
}

// reconcileIn is reconcile's body on the group's ledger.
func (rc *Reconciler) reconcileIn(ctx context.Context, g ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time, full bool, short shortKind) (Summary, error) {
	var s Summary
	prefix := b.KeyPrefix()
	idx := rc.index()
	l := touching{Ledger: g, touched: idx.touched}

	// Company|title|location tuples, for cross-source dedupe (see
	// ledgerIndex). A weakly keyed posting (fallback or careers page) that
	// repeats an ATS-keyed one is the duplicate, never the other way round.
	// twinned holds the tuples whose weak records this board already marked.
	twinned := map[posting.Key]bool{}

	seen := map[posting.Key]bool{}
	for _, p := range listed {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		if p.Key == "" || p.Key.ATS() == "" || seen[p.Key] {
			if p.Key == "" || p.Key.ATS() == "" {
				s.Invalid++
			}
			continue
		}
		// An adapter must key every posting under its board, or absence
		// tracking would age the wrong records.
		if p.Key.ATS() != posting.Other && !strings.HasPrefix(string(p.Key), prefix) {
			s.Invalid++
			continue
		}
		seen[p.Key] = true
		wasGone := full && rc.reopen.sighted(p.Key)
		hash := p.DescriptionHash()
		rec, known := l.Get(p.Key)
		if known && p.Form == nil {
			// A listing that did not read the form (a capped or failed read) says
			// nothing about it: the stored summary, if any, stands.
			p.Form = rec.Posting.Form
		}
		rendered, changed, restore := textChange(rec, hash, p)
		rendered, changed, restore = known && rendered, known && changed, known && restore
		// relist is what a same-content listing stores over a known record.
		relist := relisted(rec.Posting, p, full)

		switch {
		case !known:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "new"}
			if orig, why, ok := twinOf(p, now, idx); ok {
				e.Status = posting.Duplicate
				e.Annotations[DuplicateOfKey] = string(orig)
				e.Reason = "duplicate of " + string(orig)
				if why != "" {
					e.Reason += " (" + why + ")"
				}
				s.Duplicate++
				s.ListedDuplicate++
			}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			if e.Status == posting.Live {
				s.New++
				// An ATS posting may repeat a fallback record, or the same role
				// under another ATS, seen earlier.
				n, err := markTwins(ctx, l, p, now, now, idx, twinned, rc.protected)
				if err != nil {
					return s, err
				}
				s.Duplicate += n
			}
		case rec.Status == posting.Duplicate:
			revived, n, err := reviveDuplicate(ctx, l, rec, p, hash, now, idx, twinned, rc.protected)
			if err != nil {
				return s, err
			}
			s.Duplicate += n
			if revived {
				s.Revived++
			} else {
				s.Unchanged++
			}
		case changed:
			if score, keep := keepAcrossEdit(rec, p); keep {
				// A near-identical edit that touches no authorization, work-mode or
				// seniority sentence: the triage still stands.
				if err := l.Append(ctx, keptEvent(rec, p, hash, now, score)); err != nil {
					return s, err
				}
				s.Kept++
				break
			}
			ann := clearMissMarks(staleAnnotations(rec.Annotations))
			ann[DescHashKey] = hash
			ann[DiedFromKey] = ""
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: ann, Reason: "changed"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Changed++
		case full && rec.Status == posting.Dropped && (rc.hasPendingReopen(p.Key) || wasGone || rc.reopen.movedExclusion(rec, relist)):
			// The board lists a drop that waits to be let back, or an exclusion
			// drop whose title, company, location or remote flag moved since it
			// was judged (the board added Austin to a London role): judged again
			// on this text, it comes back now or stays dropped, and either way
			// a pending reopen is spent. An exclusion drop that stays takes the
			// moved fields too (a later pass judges the board's text, not stale
			// fields); that event dates the drop the digest footer counts, once
			// per move.
			// Either way the listing shows it, so an absence episode it was in when
			// dropped ends here (liveness.go), as it would on any listing: a later
			// reopen must not carry the old misses into its next absence.
			e, ok := rc.reopen.event(l, rec, relist, hash, now)
			if !ok {
				if rec.MissedRuns == 0 {
					s.Unchanged++
					break
				}
				if err := l.Append(ctx, reappearedEvent(rec, relist, hash, rendered, now)); err != nil {
					return s, err
				}
				s.Reappeared++
				break
			}
			e.Annotations = clearMissMarks(e.Annotations)
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			if e.Status == posting.Dropped {
				s.Refreshed++ // an exclusion drop that stays, taking the listed fields
				break
			}
			s.Reopened++
		case rec.Status == posting.Dead:
			// Same content, listed again: its annotations still hold, so it goes
			// back to the status it died in (a digested posting is not triaged or
			// digested twice). A record with no usable died_from is live again.
			st := posting.Live
			if from := posting.Status(rec.Annotations[DiedFromKey]); from.Valid() && !closed(from) {
				st = from
			}
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: st, Posting: &relist,
				Annotations: clearMissMarks(map[string]string{DescHashKey: hash, DiedFromKey: ""}), Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		case rec.MissedRuns > 0:
			if err := l.Append(ctx, reappearedEvent(rec, relist, hash, rendered, now)); err != nil {
				return s, err
			}
			s.Reappeared++
		case restore:
			// The stored body cannot be read back, the listing is the text it
			// held: the stored posting takes the listing, so the body is
			// readable again, and keeps its status and annotations (no
			// re-triage). Unlike a re-render this includes a dropped record;
			// its event is the only way the body comes back.
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Reason: "refreshed"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Refreshed++
		case rendered && rec.Status != posting.Dropped:
			// Same board text in a new rendering: the stored posting takes it
			// (so the digest shows it) and keeps its status and annotations.
			// A dropped posting is shown nowhere and gets no event: its last
			// event dates the drop the digest footer counts.
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "re-rendered"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Rerendered++
		case rec.Status != posting.Dropped && rec.Posting.PostedAt.IsZero() && !p.PostedAt.IsZero():
			// Same content, stored undated, now listed with a posted date: the
			// stored posting takes it so the advance order can rank it by
			// freshness. Status and annotations stay. A stored date is never
			// moved, as relative phrases drift a day between runs.
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Reason: "re-dated"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Redated++
		case rec.Status != posting.Dropped && refreshFields(rec.Posting, relist):
			// Same content with other display fields (an adapter that learned
			// a posting's other places or its remote flag): the stored posting
			// takes them and keeps its status and annotations, as a re-render
			// does. The title stays, being what triage and the title rules
			// read; a title-only change waits for the text to change.
			relist.Title = rec.Posting.Title
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Reason: "refreshed"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Refreshed++
		default:
			s.Unchanged++
		}
	}

	if !full {
		return s, nil
	}
	// Pending reopens of this board that the listing left out for
	// PendingReopenExpiry, or whose record is no longer dropped, are cleared
	// (pendingreopen.go). Dropped records are closed: the absence pass below
	// never ages them, so the two never touch the same record.
	clock := idx.clockEvidence(prefix, now)
	rc.reopen.expire(l, prefix, seen, now, clock.behind, short != shortNone)
	// The absence pass (liveness.go). open is the board's open keys as the
	// index held them when the board started, before this listing.
	var open []posting.Key
	for _, k := range idx.board(prefix) { // in key order
		if !closed(idx.info[k].status) {
			open = append(open, k)
		}
	}
	a, err := ageAbsent(ctx, l, rc.live, open, seen, clock, now, short)
	s.addAbsence(a)
	return s, err
}

// reappearedEvent is what a listing writes for rec, which had missed runs:
// the listed posting, the same status, and every miss mark forgotten (its
// absence episode is over).
func reappearedEvent(rec ledger.Record, relist posting.Posting, hash string, rendered bool, now time.Time) ledger.Event {
	e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: rec.Key, Status: rec.Status, Posting: &relist,
		Annotations: clearMissMarks(nil), Reason: "reappeared"}
	if rendered {
		e.Annotations[DescHashKey] = hash
	}
	return e
}

// addAbsence folds one absence pass into the summary.
func (s *Summary) addAbsence(a absence) {
	s.Open += a.open
	s.Absent += a.absent
	s.Missed += a.missed
	s.Held += a.held
	s.Deferred += a.deferred
	s.Dead += a.dead
	s.MassDead += a.massDead
	s.ShortDead += a.shortDead
	s.UnreadableDead += a.unreadableDead
	s.MassMarked += a.massMarked
	s.Mass = s.Mass || a.mass
	s.ClockBehind = s.ClockBehind || a.clockBehind
	s.ClockSetBack = s.ClockSetBack || a.clockSetBack
	if a.clockAhead.After(s.ClockAhead) {
		s.ClockAhead = a.clockAhead
	}
}

// batchByteBudget is how much encoded event data appendBatched puts in one
// batch: ledger.MaxBatchBytes less headroom for the ledger's own re-encoding.
const batchByteBudget = ledger.MaxBatchBytes - 1<<20

// appendBatched writes evs in batches of at most ledger.MaxBatchEvents events
// and batchByteBudget encoded bytes, and returns how many events were
// acknowledged. Each batch is all-or-nothing, so a failure leaves exactly the
// earlier batches written. An event that alone exceeds the byte budget is an
// error naming its key, before that event is written.
func appendBatched(ctx context.Context, l ledger.Ledger, evs []ledger.Event) (int, error) {
	done := 0
	for len(evs) > 0 {
		n, size := 0, 0
		for n < len(evs) && n < ledger.MaxBatchEvents {
			b, err := json.Marshal(evs[n])
			if err != nil {
				return done, fmt.Errorf("pipeline: encode event for %q: %w", evs[n].Key, err)
			}
			if len(b)+1 > batchByteBudget {
				if n == 0 {
					return done, fmt.Errorf("pipeline: event for %q encodes to %d bytes, over the %d-byte batch limit", evs[n].Key, len(b)+1, batchByteBudget)
				}
				break
			}
			if size+len(b)+1 > batchByteBudget {
				break
			}
			size += len(b) + 1
			n++
		}
		if err := l.AppendBatch(ctx, evs[:n]); err != nil {
			return done, err
		}
		done += n
		evs = evs[n:]
	}
	return done, nil
}

// relisted is the posting a listing with the stored content stores: the
// listed one, except that a field the listing left blank keeps its stored
// value (an adapter or a scraped page that could not read a location says
// nothing about it), a stored posted date never moves (relative phrases drift
// a day between runs), and only a full listing, the board's own word, may
// clear Remote: a partial one (a scraped page) can set it, not clear it.
func relisted(stored, listed posting.Posting, full bool) posting.Posting {
	out := listed
	for _, f := range []struct {
		dst *string
		old string
	}{
		{&out.Title, stored.Title}, {&out.Company, stored.Company}, {&out.Location, stored.Location},
		{&out.Department, stored.Department}, {&out.URL, stored.URL}, {&out.ApplyURL, stored.ApplyURL},
	} {
		if strings.TrimSpace(*f.dst) == "" {
			*f.dst = f.old
		}
	}
	if !stored.PostedAt.IsZero() {
		out.PostedAt = stored.PostedAt
	}
	if !full {
		out.Remote = out.Remote || stored.Remote
	}
	return out
}

// refreshFields reports whether relist, a same-content relisting, changes a
// display field of stored: where the posting is (Location, Remote), how it is
// filed and reached (Department, URL, ApplyURL) and whose it is (Company, a
// board's configured name), and what its application form asks (Form: a form
// read for the first time, or one that changed). Not compared: Title (triage
// and the title drop rules read it), PostedAt (never moved), UpdatedAt and
// SeenAt (they move on every re-save and run, and would write an event each
// time).
func refreshFields(stored, relist posting.Posting) bool {
	return stored.Location != relist.Location || stored.Remote != relist.Remote ||
		stored.Department != relist.Department || stored.URL != relist.URL ||
		stored.ApplyURL != relist.ApplyURL || stored.Company != relist.Company ||
		!stored.Form.Equal(relist.Form)
}

// textChange compares the listed text (p, whose DescriptionHash is hash) with
// the text rec was stored with. rendered means the full hash differs; changed
// means the board's own text differs, which re-triages. restore means the
// stored body cannot be read back and the listing is the text it was stored
// with: the record keeps its status and triage and takes the listed posting, so
// its body is readable again.
//
// A record whose body is evicted and cannot be read back (rec.BodyEvicted after
// ledger.Get: its Description is empty, not its text) is judged on the
// description hash its last write recorded (DescHashKey). An equal hash is
// restore, not a change. A differing hash is a change: it is of the full text,
// so it cannot tell a re-rendering from an edit, and counts as an edit, as it
// always did. With no stored hash nothing is known of the text and the record
// is judged on the empty text it reads as, as it was before unreadable bodies
// were told apart (a scan of the owner's ledger found no key without a hash).
func textChange(rec ledger.Record, hash string, p posting.Posting) (rendered, changed, restore bool) {
	if rec.BodyEvicted {
		if stored := rec.Annotations[DescHashKey]; stored != "" {
			if stored == hash {
				return false, false, true
			}
			return true, true, false
		}
	}
	if rec.Posting.DescriptionHash() == hash {
		return false, false, false
	}
	return true, sourceHash(rec.Posting.Description) != sourceHash(p.Description), false
}

// sourceHash identifies the board's own text in a stored or listed
// description: its DescriptionHash without the lines an adapter synthesized
// ahead of it (srcutil.SourceText). Only a difference here is a change that
// re-triages; a difference in the full hash alone is a re-render.
func sourceHash(desc string) string {
	return posting.Posting{Description: srcutil.SourceText(desc)}.DescriptionHash()
}

// KeepsStoredBody reports whether reconcile keeps r's stored posting when the
// board lists the same text again, in another rendering or with other display
// fields: a dropped record gets no event (its last event dates the drop the
// digest footer counts), and a duplicate gets none unless its survivor is gone.
// A record with missed runs is not one: it reappears and takes the listing.
//
// What an adapter reads only to put in such a posting's body or display fields
// (Greenhouse pay lines, an Ashby application form) is thrown away, so while
// the text is unchanged an adapter need not ask for it. A duplicate revived in
// that same run is stored without it, and the next run asks.
func KeepsStoredBody(r ledger.Record) bool {
	return (r.Status == posting.Dropped || r.Status == posting.Duplicate) && r.MissedRuns == 0
}

// closed reports whether a record has left the board's lifecycle, so absence
// no longer ages it. This is narrower than Status.Terminal: a digested posting
// runs no further stage but is still listed, and must die when its board drops it.
func closed(s posting.Status) bool {
	return s == posting.Dead || s == posting.Duplicate || s == posting.Dropped
}

// weakKey reports whether k identifies a posting by something other than an
// ATS id (a fallback hash or a careers-page URL). Weak postings dedupe onto
// ATS-keyed ones.
func weakKey(k posting.Key) bool {
	a := k.ATS()
	return a == posting.Other || a == posting.Page
}

// markTwins is what a record that is live and ATS-keyed does to the records
// that repeat it: its weak twins and, when it ranks first, its cross-ATS twins
// become its duplicates. rank is when p was first seen (see markCrossTwins).
func markTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, rank, now time.Time, idx *ledgerIndex, twinned map[posting.Key]bool, protected func(posting.Key) bool) (int, error) {
	if weakKey(p.Key) {
		return 0, nil
	}
	n, err := markWeakTwins(ctx, l, p, idx, twinned, now)
	if err != nil {
		return n, err
	}
	m, err := markCrossTwins(ctx, l, p, rank, now, idx, protected)
	return n + m, err
}

// markWeakTwins marks live weakly keyed records that repeat the ATS posting p
// as duplicates of it, so arrival order does not matter. A tuple is marked
// once per board (twinned).
func markWeakTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, idx *ledgerIndex, twinned map[posting.Key]bool, now time.Time) (int, error) {
	fk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil || twinned[fk] {
		return 0, nil
	}
	var evs []ledger.Event
	for _, k := range idx.weak[fk] {
		if !sharesIdentity(p, idx.info[k]) {
			continue // the tuple alone is not proof (reconcile_requisition.go)
		}
		r, ok := l.Get(k)
		if !ok || r.Status.Terminal() {
			continue
		}
		evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: posting.Duplicate,
			Annotations: map[string]string{DuplicateOfKey: string(p.Key)}, Reason: "duplicate of " + string(p.Key)})
	}
	n, err := appendBatched(ctx, l, evs)
	if err != nil {
		return n, err
	}
	if n > 0 {
		twinned[fk] = true
	}
	return n, nil
}
