package pipeline

// The reconcile before Reconciler indexed the ledger once per run: a fresh
// l.All() scan per board. Kept only as the oracle the parity test compares
// Reconciler against; nothing outside tests calls it.

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

// oracleReconcile folds one board's full listing into the ledger.
func oracleReconcile(ctx context.Context, l ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time) (Summary, error) {
	var s Summary
	prefix := b.KeyPrefix()
	existing := l.All()

	// Company|title|location tuple -> weak records, for cross-source dedupe. A
	// weakly keyed posting (fallback or careers page) that repeats an
	// ATS-keyed one is the duplicate, never the other way round. What a new
	// posting repeats is bruteTwinOf's scan of existing.
	weakByTuple := map[posting.Key][]posting.Key{}
	for _, r := range existing {
		if r.Posting.Company == "" || r.Posting.Title == "" {
			continue
		}
		fk, err := posting.FallbackKey(r.Posting.Company, r.Posting.Title, r.Posting.Location)
		if err != nil {
			continue
		}
		if weakKey(r.Key) && !r.Status.Terminal() {
			weakByTuple[fk] = append(weakByTuple[fk], r.Key)
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
		rendered := known && rec.Posting.DescriptionHash() != hash
		// What a same-content listing stores: blanks keep the stored value, a
		// stored date stays, and this full listing sets Remote as listed.
		relist := p
		if relist.Title == "" {
			relist.Title = rec.Posting.Title
		}
		if relist.Company == "" {
			relist.Company = rec.Posting.Company
		}
		if relist.Location == "" {
			relist.Location = rec.Posting.Location
		}
		if relist.Department == "" {
			relist.Department = rec.Posting.Department
		}
		if relist.URL == "" {
			relist.URL = rec.Posting.URL
		}
		if relist.ApplyURL == "" {
			relist.ApplyURL = rec.Posting.ApplyURL
		}
		if !rec.Posting.PostedAt.IsZero() {
			relist.PostedAt = rec.Posting.PostedAt
		}
		moved := relist.Location != rec.Posting.Location || relist.Remote != rec.Posting.Remote ||
			relist.Department != rec.Posting.Department || relist.URL != rec.Posting.URL ||
			relist.ApplyURL != rec.Posting.ApplyURL || relist.Company != rec.Posting.Company

		switch {
		case !known:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
				Annotations: map[string]string{DescHashKey: hash}, Reason: "new"}
			if orig, why, ok := bruteTwinOf(p, now, existing); ok {
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
				if !weakKey(p.Key) {
					n, err := oracleMarkWeakTwins(ctx, l, p, weakByTuple, now)
					if err != nil {
						return s, err
					}
					s.Duplicate += n
					n, err = oracleMarkCrossTwins(ctx, l, p, now, now, existing)
					if err != nil {
						return s, err
					}
					s.Duplicate += n
				}
			}
		case rec.Status == posting.Duplicate:
			// The revive rule, with the twin found by the brute scan of existing.
			of := posting.Key(rec.Annotations[DuplicateOfKey])
			if of == "" || !survivorGone(l, of) {
				s.Unchanged++
				break
			}
			var twin posting.Key
			var why string
			if k, w, ok := bruteTwinOf(p, rec.FirstSeen, existing); ok && k != of {
				if r, found := l.Get(k); found && !closed(r.Status) {
					twin, why = k, w
				}
			}
			if err := l.Append(ctx, reviveEvent(rec, p, hash, now, twin, why)); err != nil {
				return s, err
			}
			s.Revived++
			if twin == "" && !weakKey(p.Key) {
				n, err := oracleMarkWeakTwins(ctx, l, p, weakByTuple, now)
				if err != nil {
					return s, err
				}
				s.Duplicate += n
				n, err = oracleMarkCrossTwins(ctx, l, p, rec.FirstSeen, now, existing)
				if err != nil {
					return s, err
				}
				s.Duplicate += n
			}
		case rendered && sourceHash(rec.Posting.Description) != sourceHash(p.Description):
			if score, keep := keepAcrossEdit(rec, p); keep {
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
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Reason: "re-dated"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Redated++
		case rec.Status != posting.Dropped && moved:
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

	var absent []ledger.Record
	for _, r := range existing {
		if strings.HasPrefix(string(r.Key), prefix) && !closed(r.Status) && !seen[r.Key] {
			absent = append(absent, r)
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i].Key < absent[j].Key })
	for _, r := range absent {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		cur, ok := l.Get(r.Key)
		if !ok || closed(cur.Status) {
			continue
		}
		misses := cur.MissedRuns + 1
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: cur.Status,
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

// oracleMarkWeakTwins marks live weakly keyed records that repeat the ATS posting p
// as duplicates of it, so arrival order does not matter.
func oracleMarkWeakTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, weak map[posting.Key][]posting.Key, now time.Time) (int, error) {
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
