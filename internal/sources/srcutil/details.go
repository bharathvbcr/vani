package srcutil

import (
	"hash/fnv"
	"time"
)

// The bounded detail step. A source whose listing carries no description
// spends a detail request on each posting (workday, smartrecruiters,
// oraclehcm, rippling). Given the postings an earlier run stored, it reuses
// them, re-reads a share of them each run so an edited description is seen
// within RefreshDays, and spends at most a fixed number of detail requests per
// board per run. New rows past that number are deferred to the next run; not
// being stored, their absence ages nothing.

// RefreshDays is how many runs it takes to re-read every stored posting.
const RefreshDays = 30

// Day is t's day number, the one RefreshTurn takes: days since the Unix epoch,
// in UTC.
func Day(t time.Time) int { return int(t.UTC().Unix() / 86400) }

// RefreshTurn reports whether the stored posting with this id is re-read on
// day: each id's turn comes once every RefreshDays days.
func RefreshTurn(id string, day int) bool {
	h := fnv.New32a()
	h.Write([]byte(id))
	return int(h.Sum32()%RefreshDays) == day%RefreshDays
}

// MaxGone is how many of n fetched details may come back gone (closed since
// the listing) before the board fails: a tenth, at least one. More than that
// is the detail endpoint failing, not postings closing, and would empty a
// healthy board.
func MaxGone(n int) int { return max(1, n/10) }

// DetailRow is what the bounded detail step knows about one listed row before
// any detail request.
type DetailRow struct {
	// ID is the row's stable id, the one its refresh turn is drawn from.
	ID string
	// Stored is whether an earlier run stored the row's posting.
	Stored bool
	// Changed is whether the listing shows that a stored row changed since it
	// was stored. It is re-read ahead of the stored rows whose turn it is.
	Changed bool
	// Drop is whether a new row is dropped before any request (a source's
	// DropListed). It is ignored on a stored row.
	Drop bool
}

// DetailPlan is which listed rows get a detail request. Each slice holds row
// indexes.
type DetailPlan struct {
	// Fetch is the rows that get a detail request, in order: the new rows,
	// then the changed stored rows, then the stored rows whose turn it is.
	Fetch []int
	// New is how many of Fetch, from its start, are new rows.
	New int
	// Reuse is the stored rows returned as stored, with no request: those not
	// due a re-read, and those whose re-read did not fit.
	Reuse []int
	// Deferred is how many new rows were past the cap.
	Deferred int
	// Prefiltered is how many new rows were dropped.
	Prefiltered int
}

// PlanDetails plans the detail requests for rows on day, at most limit of
// them. New rows come first and those past limit are deferred; changed stored
// rows, then stored rows whose RefreshTurn it is, fill what room is left, and a
// stored row whose re-read does not fit is reused. Every row lands in exactly
// one of Fetch, Reuse, Deferred or Prefiltered.
//
// A caller without stored postings must refuse a plan with Deferred > 0: it
// cannot tell a deferred row from a stored one, and leaving out a stored one
// would age it as absent.
func PlanDetails(rows []DetailRow, day, limit int) DetailPlan {
	var p DetailPlan
	var fresh, changed, scheduled []int
	for i, r := range rows {
		switch {
		case r.Stored && r.Changed:
			changed = append(changed, i)
		case r.Stored && RefreshTurn(r.ID, day):
			scheduled = append(scheduled, i)
		case r.Stored:
			p.Reuse = append(p.Reuse, i)
		case r.Drop:
			p.Prefiltered++
		default:
			fresh = append(fresh, i)
		}
	}
	limit = max(limit, 0)
	if len(fresh) > limit {
		p.Deferred = len(fresh) - limit
		fresh = fresh[:limit]
	}
	p.Fetch = fresh
	p.New = len(fresh)
	for _, group := range [][]int{changed, scheduled} {
		room := limit - len(p.Fetch)
		if len(group) > room {
			p.Reuse = append(p.Reuse, group[room:]...)
			group = group[:room]
		}
		p.Fetch = append(p.Fetch, group...)
	}
	return p
}
