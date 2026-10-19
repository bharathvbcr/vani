// Package pipeline is the deterministic per-posting state machine: it
// reconciles a board listing against the ledger and advances each posting
// through triage, scoring and explanation. It makes no network or model
// calls itself; every stage is an injected interface.
package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const (
	// DescHashKey holds the description hash of the stored posting version.
	DescHashKey = "desc_hash"
	// DuplicateOfKey names the ATS-keyed record a fallback-keyed posting repeats.
	DuplicateOfKey = "duplicate_of"
	// MissesToDead is how many consecutive absent listings kill a posting.
	MissesToDead = 2
)

// Summary counts what one Reconcile did.
type Summary struct {
	New        int
	Changed    int
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

// Reconcile folds one board's full listing into the ledger.
func Reconcile(ctx context.Context, l ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	var s Summary
	prefix := b.KeyPrefix()
	existing := l.All()

	// Company|title|location tuple -> records, for cross-source dedupe. A
	// weakly keyed posting (fallback or careers page) that repeats an
	// ATS-keyed one is the duplicate, never the other way round.
	atsByTuple := map[posting.Key]posting.Key{}
	weakByTuple := map[posting.Key][]posting.Key{}
	for _, r := range existing {
		if r.Posting.Company == "" || r.Posting.Title == "" {
			continue
		}
		fk, err := posting.FallbackKey(r.Posting.Company, r.Posting.Title, r.Posting.Location)
		if err != nil {
			continue
		}
		if weakKey(r.Key) {
			if !r.Status.Terminal() {
				weakByTuple[fk] = append(weakByTuple[fk], r.Key)
			}
			continue
		}
		if _, dup := atsByTuple[fk]; !dup {
			atsByTuple[fk] = r.Key
		}
	}

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

		switch {
		case !known:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "new"}
			if weakKey(p.Key) {
				if fk, err := posting.FallbackKey(p.Company, p.Title, p.Location); err == nil {
					if orig, ok := atsByTuple[fk]; ok {
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
					n, err := markWeakTwins(ctx, l, p, weakByTuple, now)
					if err != nil {
						return s, err
					}
					s.Duplicate += n
				}
			}
		case rec.Status == posting.Duplicate:
			s.Unchanged++
		case rec.Posting.DescriptionHash() != hash:
			ann := staleAnnotations(rec.Annotations)
			ann[DescHashKey] = hash
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: ann, Reason: "changed"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Changed++
		case rec.Status == posting.Dead:
			// Same content, listed again: it is live, and nothing downstream ran for a dead record.
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		case rec.MissedRuns > 0:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &p,
				Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		default:
			s.Unchanged++
		}
	}

	var absent []ledger.Record
	for _, r := range existing {
		if strings.HasPrefix(string(r.Key), prefix) && !r.Status.Terminal() && !seen[r.Key] {
			absent = append(absent, r)
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i].Key < absent[j].Key })
	for _, r := range absent {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		cur, ok := l.Get(r.Key)
		if !ok || cur.Status.Terminal() {
			continue
		}
		misses := cur.MissedRuns + 1
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: cur.Status,
			Annotations: map[string]string{ledger.MissedRunsKey: strconv.Itoa(misses)},
			Reason:      fmt.Sprintf("absent from board (%d)", misses)}
		if misses >= MissesToDead {
			e.Status = posting.Dead
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

// weakKey reports whether k identifies a posting by something other than an
// ATS id (a fallback hash or a careers-page URL). Weak postings dedupe onto
// ATS-keyed ones.
func weakKey(k posting.Key) bool {
	a := k.ATS()
	return a == posting.Other || a == posting.Page
}

// markWeakTwins marks live weakly keyed records that repeat the ATS posting p
// as duplicates of it, so arrival order does not matter.
func markWeakTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, weak map[posting.Key][]posting.Key, now time.Time) (int, error) {
	fk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil {
		return 0, nil
	}
	n := 0
	for _, k := range weak[fk] {
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
	delete(weak, fk)
	return n, nil
}
