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
	DescHashKey = "desc_hash"
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
	// Redated postings were stored without a posted date and are listed with
	// one (an adapter that learned to read it): the stored posting takes the
	// date and keeps its status and annotations. A stored date is never moved.
	Redated    int
	Unchanged  int
	Reappeared int // seen again after a miss (or after being marked dead)
	Missed     int // absent, below the death threshold
	Dead       int
	Duplicate  int
	Invalid    int // listed postings skipped for a missing or malformed key
}

// staleAnnotations lists the derived results a changed description invalidates.
func staleAnnotations(a map[string]string) map[string]string {
	out := map[string]string{}
	for k := range a {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) || strings.HasPrefix(k, "score.") ||
			strings.HasPrefix(k, "gap.") || k == ledger.AnnExplanation || k == ledger.AnnRank {
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

		switch {
		case !known:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "new"}
			if weakKey(p.Key) {
				if fk, err := posting.FallbackKey(p.Company, p.Title, p.Location); err == nil {
					if orig, ok := idx.atsFor(fk); ok {
						e.Status = posting.Duplicate
						e.Annotations[DuplicateOfKey] = string(orig)
						e.Reason = "duplicate of " + string(orig)
						s.Duplicate++
					}
				}
			}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			if e.Status == posting.Live {
				s.New++
				// An ATS posting may repeat a fallback record seen earlier.
				if !weakKey(p.Key) {
					n, err := markWeakTwins(ctx, l, p, idx, twinned, now)
					if err != nil {
						return s, err
					}
					s.Duplicate += n
				}
			}
		case rec.Status == posting.Duplicate:
			s.Unchanged++
		case rendered && sourceHash(rec.Posting.Description) != sourceHash(p.Description):
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
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: st, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash, DiedFromKey: ""}, Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		case rec.MissedRuns > 0:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &p,
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
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &p,
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
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &p,
				Reason: "re-dated"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Redated++
		default:
			s.Unchanged++
		}
	}

	var absent []posting.Key
	for _, k := range idx.board(prefix) { // in key order
		if !closed(idx.info[k].status) && !seen[k] {
			absent = append(absent, k)
		}
	}
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
			s.Dead++
		} else {
			s.Missed++
		}
		if err := l.Append(ctx, e); err != nil {
			return s, err
		}
	}
	return s, nil
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

// markWeakTwins marks live weakly keyed records that repeat the ATS posting p
// as duplicates of it, so arrival order does not matter. A tuple is marked
// once per board (twinned).
func markWeakTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, idx *ledgerIndex, twinned map[posting.Key]bool, now time.Time) (int, error) {
	fk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil || twinned[fk] {
		return 0, nil
	}
	n := 0
	for _, k := range idx.weak[fk] {
		r, ok := l.Get(k)
		if !ok || r.Status.Terminal() {
			continue
		}
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: posting.Duplicate,
			Annotations: map[string]string{DuplicateOfKey: string(p.Key)}, Reason: "duplicate of " + string(p.Key)}
		if err := l.Append(ctx, e); err != nil {
			return n, err
		}
		n++
	}
	twinned[fk] = true
	return n, nil
}
