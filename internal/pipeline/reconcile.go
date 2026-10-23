// Package pipeline is the deterministic per-posting state machine: it
// reconciles a board listing against the ledger and advances each posting
// through triage, scoring and explanation. It makes no network or model
// calls itself; every stage is an injected interface.
package pipeline

import (
	"context"
	"fmt"
	"strconv"
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
	Dead       int
	Duplicate  int
	// Revived duplicates were listed again while their survivor was gone
	// (reviveDuplicate): live again, or a duplicate of another live record.
	Revived int
	Invalid int // listed postings skipped for a missing or malformed key
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

// Reconcile folds one board's full listing into the ledger. A run with many
// boards uses a Reconciler, which reads the ledger once for all of them.
func Reconcile(ctx context.Context, l ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return NewReconciler(l).Reconcile(ctx, b, listed, now)
}

// Reconcile folds one board's full listing into the ledger. It sees the
// ledger as it stands when the board starts, as a fresh scan would: the
// boards before it included, this board's own writes not.
func (rc *Reconciler) Reconcile(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, true)
}

// ReconcilePartial folds postings that are part of a board's listing, not the
// whole of it (a page the user scraped in the browser): it shares Reconcile's
// listing loop but runs no absence pass, so a posting missing from the batch is
// never missed or aged. A partial listing says nothing about what is absent.
func (rc *Reconciler) ReconcilePartial(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	return rc.reconcile(ctx, b, listed, now, false)
}

// reconcile is the listing loop both entry points share; full says the listing
// is the board's whole, so records it lacks are aged.
func (rc *Reconciler) reconcile(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time, full bool) (Summary, error) {
	var s Summary
	prefix := b.KeyPrefix()
	idx := rc.index()
	l := touching{Ledger: rc.l, touched: idx.touched}

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
		hash := p.DescriptionHash()
		rec, known := l.Get(p.Key)
		rendered := known && rec.Posting.DescriptionHash() != hash
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
		case rendered && sourceHash(rec.Posting.Description) != sourceHash(p.Description):
			if score, keep := keepAcrossEdit(rec, p); keep {
				// A near-identical edit that touches no authorization, work-mode or
				// seniority sentence: the triage still stands.
				if err := l.Append(ctx, keptEvent(rec, p, hash, now, score)); err != nil {
					return s, err
				}
				s.Kept++
				break
			}
			ann := staleAnnotations(rec.Annotations)
			ann[DescHashKey] = hash
			ann[DiedFromKey] = ""
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: ann, Reason: "changed"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Changed++
		case rec.Status == posting.Dead:
			// Same content, listed again: its annotations still hold, so it goes
			// back to the status it died in (a digested posting is not triaged or
			// digested twice). A record with no usable died_from is live again.
			st := posting.Live
			if from := posting.Status(rec.Annotations[DiedFromKey]); from.Valid() && !closed(from) {
				st = from
			}
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: st, Posting: &relist,
				Annotations: map[string]string{DescHashKey: hash, DiedFromKey: ""}, Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		case rec.MissedRuns > 0:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Reason: "reappeared"}
			if rendered {
				e.Annotations = map[string]string{DescHashKey: hash}
			}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
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
	var absent []posting.Key
	for _, k := range idx.board(prefix) { // in key order
		if !closed(idx.info[k].status) && !seen[k] {
			absent = append(absent, k)
		}
	}
	// Every read precedes the write: each absent key is distinct and an event
	// touches only its own record, so the events go in one batch (one fsync).
	var evs []ledger.Event
	var missed, dead int
	for _, k := range absent {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		cur, ok := l.Get(k)
		if !ok || closed(cur.Status) {
			continue
		}
		misses := cur.MissedRuns + 1
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: cur.Status,
			Annotations: map[string]string{ledger.MissedRunsKey: strconv.Itoa(misses)},
			Reason:      fmt.Sprintf("absent from board (%d)", misses)}
		if misses >= MissesToDead {
			e.Status = posting.Dead
			e.Annotations[DiedFromKey] = string(cur.Status)
			e.Reason = "absent from board twice"
			dead++
		} else {
			missed++
		}
		evs = append(evs, e)
	}
	if _, err := appendBatched(ctx, l, evs); err != nil {
		return s, err
	}
	s.Dead += dead
	s.Missed += missed
	return s, nil
}

// appendBatched writes evs in batches of at most ledger.MaxBatchEvents and
// returns how many events were acknowledged. Each batch is all-or-nothing, so a
// failure leaves exactly the earlier batches written.
func appendBatched(ctx context.Context, l ledger.Ledger, evs []ledger.Event) (int, error) {
	done := 0
	for len(evs) > 0 {
		n := min(len(evs), ledger.MaxBatchEvents)
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
// board's configured name). Not compared: Title (triage and the title drop
// rules read it), PostedAt (never moved), UpdatedAt and SeenAt (they move on
// every re-save and run, and would write an event each time).
func refreshFields(stored, relist posting.Posting) bool {
	return stored.Location != relist.Location || stored.Remote != relist.Remote ||
		stored.Department != relist.Department || stored.URL != relist.URL ||
		stored.ApplyURL != relist.ApplyURL || stored.Company != relist.Company
}

// sourceHash identifies the board's own text in a stored or listed
// description: its DescriptionHash without the lines an adapter synthesized
// ahead of it (srcutil.SourceText). Only a difference here is a change that
// re-triages; a difference in the full hash alone is a re-render.
func sourceHash(desc string) string {
	return posting.Posting{Description: srcutil.SourceText(desc)}.DescriptionHash()
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
	twinned[fk] = true
	return n, nil
}
