package pipeline

// The reconcile before Reconciler indexed the ledger once per run: a fresh
// l.All() scan per board. Kept only as the oracle the parity test compares
// Reconciler against; nothing outside tests calls it.

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// oracleReconcile folds one board's full listing (short: a listing that counted
// more rows than it listed) into the ledger.
func oracleReconcile(ctx context.Context, l ledger.Ledger, b sources.Board, listed []posting.Posting, now time.Time, short bool) (Summary, error) {
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
				s.ListedDuplicate++
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
			ann := oracleForgetMisses(staleAnnotations(rec.Annotations))
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
				Annotations: oracleForgetMisses(map[string]string{DescHashKey: hash, DiedFromKey: ""}), Reason: "reappeared"}
			if err := l.Append(ctx, e); err != nil {
				return s, err
			}
			s.Reappeared++
		case rec.MissedRuns > 0:
			e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: p.Key, Status: rec.Status, Posting: &relist,
				Annotations: oracleForgetMisses(nil), Reason: "reappeared"}
			if rendered {
				e.Annotations[DescHashKey] = hash
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

	return oracleAbsence(ctx, l, existing, prefix, seen, now, s, short)
}

// oracleForgetMisses adds the forgetting of every miss mark to ann (a listing
// that shows the posting ends its absence), written from the annotation names.
func oracleForgetMisses(ann map[string]string) map[string]string {
	if ann == nil {
		ann = map[string]string{}
	}
	ann["missed_since"], ann["mass_missed_since"], ann["mass_short"] = "", "", ""
	return ann
}

// oracleAbsence is the absence pass written again from the owner's rule and
// nothing else: it calls no function of liveness.go and uses none of its
// constants, so the parity test compares two implementations of it. The rule:
//
//	a posting absent from a full listing is missed, stamped on its first miss,
//	and dies after at least 2 misses and 20 hours since that first miss; a
//	stamp more than 1 hour ahead of now, or more than 1 hour before the posting was first seen
//	(a first sighting itself more than 1 hour ahead is not compared),
//	is unreadable and restarts at now; a listing that leaves at least a quarter
//	of the board's open postings (distinct keys, before the listing) absent,
//	with at least 10 open, marks every absent posting mass; so does a listing
//	showing none of at least 2 open ones; the mark holds until the posting
//	reappears; on a board with fewer than 10 open, a listing that leaves more
//	than half of them absent, and at least 2, does the same; a mass-marked posting dies after at least 2 misses and 14 days
//	since its own first miss.
//	A short listing (the board counted more rows than it listed) marks every
//	absent posting mass, whatever the share. The mark carries a cause: "short"
//	when only short listings marked the posting in its episode, none of them a
//	mass absence by the rules above; a listing that is a mass absence by those
//	rules clears it. A mass-marked posting's death counts as ShortDead when
//	its cause is "short" and as MassDead otherwise (a mark with no cause is
//	not short).
//	The clock: a listing ages nothing when the ledger holds events between 1
//	hour and 3 days ahead of now on one of the board's own records, or on at
//	least 3 records anywhere, or between 3 and 30 days ahead on at least 3 of
//	the board's own records. A death due more than 3 days after the posting's
//	last event waits one more read.
func oracleAbsence(ctx context.Context, l ledger.Ledger, existing []ledger.Record, prefix string, seen map[posting.Key]bool, now time.Time, s Summary, short bool) (Summary, error) {
	isClosed := func(st posting.Status) bool { return st == "dead" || st == "duplicate" || st == "dropped" }
	var open, absent []posting.Key
	for _, r := range existing {
		if strings.HasPrefix(string(r.Key), prefix) && !isClosed(r.Status) {
			open = append(open, r.Key)
			if !seen[r.Key] {
				absent = append(absent, r.Key)
			}
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i] < absent[j] })
	s.Open, s.Absent = len(open), len(absent)
	if len(absent) == 0 {
		return s, nil
	}
	const window = 72 * time.Hour
	lo, hi := now.Add(time.Hour), now.Add(window)
	inWindow := 0
	var ownNewest, anyNewest time.Time
	for _, r := range existing {
		t := r.LastEvent
		if !t.After(lo) || t.After(hi) {
			continue
		}
		inWindow++
		if t.After(anyNewest) {
			anyNewest = t
		}
		if strings.HasPrefix(string(r.Key), prefix) && t.After(ownNewest) {
			ownNewest = t
		}
	}
	switch {
	case inWindow >= 3:
		s.ClockBehind, s.ClockAhead = true, anyNewest
	case !ownNewest.IsZero():
		s.ClockBehind, s.ClockAhead = true, ownNewest
	}
	// Between 3 and 30 days ahead, on at least 3 of the board's own records.
	far, farNewest := 0, time.Time{}
	for _, r := range existing {
		if t := r.LastEvent; strings.HasPrefix(string(r.Key), prefix) && t.After(hi) && !t.After(now.Add(30*24*time.Hour)) {
			far++
			if t.After(farNewest) {
				farNewest = t
			}
		}
	}
	if far >= 3 {
		if !s.ClockBehind || farNewest.After(s.ClockAhead) {
			s.ClockBehind, s.ClockAhead = true, farNewest
		}
	}
	if s.ClockBehind {
		return s, nil
	}
	n, a := len(open), len(absent)
	trueMass := (n >= 2 && a == n) || (n >= 10 && 4*a >= n) || (n < 10 && a >= 2 && 2*a > n)
	mass := short || trueMass
	for _, k := range absent {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		cur, ok := l.Get(k)
		if !ok || isClosed(cur.Status) {
			continue
		}
		misses := cur.MissedRuns + 1
		first := now
		if cur.MissedRuns > 0 {
			if t, err := time.Parse(time.RFC3339Nano, cur.Annotations["missed_since"]); err == nil && !t.After(now.Add(time.Hour)) && (cur.FirstSeen.After(now.Add(time.Hour)) || !t.Before(cur.FirstSeen.Add(-time.Hour))) {
				first = t
			}
		}
		massed, shortOnly := "", false
		if cur.MissedRuns > 0 && cur.Annotations["mass_missed_since"] != "" {
			massed = cur.Annotations["mass_missed_since"]
			shortOnly = cur.Annotations["mass_short"] == "short"
			if t, err := time.Parse(time.RFC3339Nano, massed); err != nil || t.After(now.Add(time.Hour)) || (!cur.FirstSeen.After(now.Add(time.Hour)) && t.Before(cur.FirstSeen.Add(-time.Hour))) {
				massed = now.UTC().Format(time.RFC3339Nano)
			}
		}
		if mass && massed == "" {
			massed = now.UTC().Format(time.RFC3339Nano)
			shortOnly = !trueMass
			s.MassMarked++
		} else if trueMass {
			shortOnly = false
		}
		cause := ""
		if massed != "" && shortOnly {
			cause = "short"
		}
		hold := 20 * time.Hour
		if massed != "" {
			hold = 14 * 24 * time.Hour
		}
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: cur.Status, Reason: "absent from board",
			Annotations: map[string]string{ledger.MissedRunsKey: strconv.Itoa(misses),
				"missed_since": first.UTC().Format(time.RFC3339Nano), "mass_missed_since": massed, "mass_short": cause}}
		due := misses >= 2 && !now.Before(first.Add(hold))
		waits := false
		if due && !cur.LastEvent.IsZero() && now.Sub(cur.LastEvent) > window {
			due, waits = false, true
		}
		switch {
		case due:
			e.Status = posting.Dead
			e.Annotations["died_from"] = string(cur.Status)
			s.Dead++
			if massed != "" && cause == "short" {
				s.ShortDead++
			} else if massed != "" {
				s.MassDead++
			}
		case misses >= 2:
			s.Held++
			if waits {
				s.Deferred++
			}
		default:
			s.Missed++
		}
		if err := l.Append(ctx, e); err != nil {
			return s, err
		}
	}
	s.Mass = mass
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
