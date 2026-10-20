package pipeline

// The brute-force side of the identity rules (reconcile_identity.go): every
// answer is a scan of the records as they stood when the board started, with
// none of the index's maps. oracleReconcile uses it, and the parity test holds
// Reconciler to the ledger it leaves.

import (
	"context"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func tupleOf(p posting.Posting) (posting.Key, bool) {
	if p.Company == "" || p.Title == "" {
		return "", false
	}
	fk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	return fk, err == nil
}

func bruteTwinOf(p posting.Posting, now time.Time, existing []ledger.Record) (posting.Key, string, bool) {
	// Rule 1: a link exactly one live ATS-keyed record carries.
	var found posting.Key
	for _, u := range urlKeys(p) {
		var claim []posting.Key
		for _, r := range existing {
			if r.Key == p.Key || weakKey(r.Key) || closed(r.Status) {
				continue
			}
			if slices.Contains(urlKeys(r.Posting), u) {
				claim = append(claim, r.Key)
			}
		}
		if len(claim) != 1 {
			continue
		}
		if found != "" && found != claim[0] {
			found = ""
			break
		}
		found = claim[0]
	}
	if found != "" {
		return found, "same url", true
	}
	// A conflict between two links is no answer from rule 1 at all.
	fk, hasTuple := tupleOf(p)
	if !hasTuple {
		return "", "", false
	}
	if weakKey(p.Key) {
		// Rule 3: the first ATS-keyed record, in key order, with p's tuple, that is not Dead.
		for _, r := range existing {
			if t, ok := tupleOf(r.Posting); ok && !weakKey(r.Key) && t == fk && r.Status != posting.Dead {
				return r.Key, "", true
			}
		}
		return "", "", false
	}
	// Rule 2.
	if s, ok := bruteSurvivor(p, now, fk, existing); ok && s != p.Key {
		return s, "same role on another ats", true
	}
	return "", "", false
}

func bruteCrossCandidates(p posting.Posting, fk posting.Key, existing []ledger.Record) []ledger.Record {
	var out []ledger.Record
	for _, r := range existing {
		t, ok := tupleOf(r.Posting)
		if ok && t == fk && !weakKey(r.Key) && r.Key != p.Key && r.Key.ATS() != p.Key.ATS() && !closed(r.Status) {
			out = append(out, r)
		}
	}
	return out
}

func bruteSurvivor(p posting.Posting, now time.Time, fk posting.Key, existing []ledger.Record) (posting.Key, bool) {
	cands := bruteCrossCandidates(p, fk, existing)
	if len(cands) == 0 {
		return "", false
	}
	// The earliest first seen, ties by key: sort everyone and take the head.
	type ranked struct {
		at  time.Time
		key posting.Key
	}
	all := []ranked{{now, p.Key}}
	for _, r := range cands {
		all = append(all, ranked{r.FirstSeen, r.Key})
	}
	slices.SortFunc(all, func(a, b ranked) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return compareKeys(a.key, b.key)
	})
	return all[0].key, true
}

func compareKeys(a, b posting.Key) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func oracleMarkCrossTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, rank, now time.Time, existing []ledger.Record) (int, error) {
	fk, ok := tupleOf(p)
	if !ok || weakKey(p.Key) {
		return 0, nil
	}
	if s, found := bruteSurvivor(p, rank, fk, existing); !found || s != p.Key {
		return 0, nil
	}
	n := 0
	for _, c := range bruteCrossCandidates(p, fk, existing) {
		r, ok := l.Get(c.Key)
		if !ok || r.Status.Terminal() {
			continue
		}
		e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: c.Key, Status: posting.Duplicate,
			Annotations: map[string]string{DuplicateOfKey: string(p.Key)}, Reason: "duplicate of " + string(p.Key) + " (same role on another ats)"}
		if err := l.Append(ctx, e); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
