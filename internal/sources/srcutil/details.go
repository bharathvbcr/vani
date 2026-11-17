package srcutil

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The bounded detail step. A source whose listing carries no description
// spends a detail request on each posting (workday, smartrecruiters,
// oraclehcm, rippling). Given the postings an earlier run stored, it reuses
// them, re-reads a share of them each run so an edited description is seen
// within RefreshDays, and spends at most a fixed number of detail requests per
// board per run. New rows past that number are deferred to the next run; not
// being stored, their absence ages nothing. A detail request that fails
// transiently is left for a later run the same way, within a bound
// (DetailMisses); any other detail failure fails the board.

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

// maxMissesPerBoard bounds the detail requests one board may miss in a run.
// It is a count, not a share: each miss may have cost a timed-out request and
// its retry, so a detail endpoint that stops answering fails the board after a
// few, not after a tenth of a hundred.
const maxMissesPerBoard = 3

// MaxMissed is how many of fetch detail requests may fail transiently
// (Transient) before the board fails: as many as MaxGone allows, at most
// maxMissesPerBoard, and, when no row was reused, fewer than fetch, so a board
// is never answered by nothing the endpoint confirmed.
func MaxMissed(fetch, reused int) int {
	if fetch <= 0 {
		return 0
	}
	m := min(MaxGone(fetch), maxMissesPerBoard)
	if reused == 0 {
		m = min(m, fetch-1)
	}
	return m
}

// DetailMisses is the bounded detail step's tolerance for a detail request
// that failed transiently. Before it, one timed-out posting page failed its
// whole board (Apple's camera-algorithms page and a JPMorgan requisition on
// 2026-11-16, each beside a hundred-odd healthy rows). A missed row is left for
// a later run the way the plan already leaves rows:
//
//   - a stored row whose re-read missed is returned as stored, as if its re-read
//     had not fit (DetailPlan.Reuse), so it is still listed and ages nothing;
//   - a new row that missed is not returned, as if it were past the cap
//     (DetailPlan.Deferred). That is safe only where a deferred row is: the
//     caller passes canDefer false when it cannot tell a new row from a stored
//     one, and a missed new row then fails the board as before;
//   - a stored row with unusable text (DetailRow.Unusable) whose re-read missed
//     can be neither returned as stored (its text is empty) nor left out unannounced
//     (it is stored, so that would age it as absent). It is held: left out of the
//     listing and reported as a shortfall (DetailStep: Rows.Hold), which makes the
//     listing short, so the stored posting is kept and not aged. That needs a
//     BadRowSink (canHold); without one it fails the board.
//
// Anything not Transient, and anything past MaxMissed, still fails the board.
type DetailMisses struct {
	newRows  int
	unusable int
	reused   int
	canDefer bool
	canHold  bool
	allowed  int
	// New is how many new rows missed and were left out; Stored is how many
	// stored rows missed and were returned as stored; Held is how many unusable
	// stored rows missed and were held (left out, reported as a shortfall).
	New, Stored, Held int
}

// NewDetailMisses returns the tolerance for the detail requests of pl.
// canDefer is whether a new row of pl may be left out (see DetailMisses).
func NewDetailMisses(pl DetailPlan, canDefer bool) *DetailMisses {
	return &DetailMisses{newRows: pl.New, unusable: pl.Unusable, reused: len(pl.Reuse), canDefer: canDefer, allowed: MaxMissed(len(pl.Fetch), len(pl.Reuse))}
}

// ErrNothingAnswered is a detail step that missed requests and had no other
// row to show for the board: none reused and none answered. The endpoint, not
// the postings, is what failed.
var ErrNothingAnswered = errors.New("no detail request was answered and none was reused; the detail endpoint is not answering")

// Done checks the detail step once its requests are spent: answered is how
// many detail requests returned a posting. A step that missed any while
// nothing was answered or reused is ErrNothingAnswered, so a board is never
// answered by misses and gone rows alone.
func (m *DetailMisses) Done(answered int) error {
	if m.Total() > 0 && answered == 0 && m.reused == 0 {
		return fmt.Errorf("%w (%d missed)", ErrNothingAnswered, m.Total())
	}
	return nil
}

// Take reports whether the failed detail request for pl.Fetch[n] is left for a
// later run, and counts it when it is. False means the board fails with err.
func (m *DetailMisses) Take(ctx context.Context, n int, err error) bool {
	if ctx.Err() != nil || !Transient(err) || m.Total() >= m.allowed || (m.isUnusable(n) && !m.canHold) {
		return false
	}
	if m.isUnusable(n) {
		m.Held++
		return true
	}
	if n < m.newRows {
		if !m.canDefer {
			return false
		}
		m.New++
		return true
	}
	m.Stored++
	return true
}

// isUnusable is whether pl.Fetch[n] is a stored row whose text cannot be reused
// (DetailRow.Unusable). Its miss can be neither left out silently (it is stored,
// so that would age it as absent) nor returned as stored (its text is empty): it
// is held when the caller can report a shortfall, and fails the board otherwise.
func (m *DetailMisses) isUnusable(n int) bool { return n >= m.newRows && n < m.newRows+m.unusable }

// Total is how many detail requests were missed and left for a later run.
func (m *DetailMisses) Total() int { return m.New + m.Stored + m.Held }

// ErrTooManyMisses is a detail request that failed transiently after the board
// had already left as many for a later run as it may: the detail endpoint, not
// one posting, is what is failing.
var ErrTooManyMisses = errors.New("too many detail requests missed")

// Refusal is the error a board fails with when Take refused the failed request
// for pl.Fetch[n]. Where Take refused only because the cap was reached (the
// request was transient and its row could have been left out), the error says
// so and how many misses were already absorbed, and still wraps err, so a
// timeout stays a timeout to errors.Is. In every other case it is err itself.
func (m *DetailMisses) Refusal(ctx context.Context, n int, err error) error {
	if ctx.Err() != nil || !Transient(err) || m.Total() < m.allowed || (n < m.newRows && !m.canDefer) || (m.isUnusable(n) && !m.canHold) {
		return err
	}
	return fmt.Errorf("%w: %d already left for a later run, the most this board may leave is %d: %w", ErrTooManyMisses, m.Total(), m.allowed, err)
}

// unusableAt is whether Fetch[n] is an unusable stored row.
func (p DetailPlan) unusableAt(n int) bool { return n >= p.New && n < p.New+p.Unusable }

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
	// Unusable is whether the stored row's text must not be reused: the ledger
	// could not read it back, so the stored posting has no description
	// (Marked: an explicit mark beside the stored postings, never inferred from
	// an empty description, which a posting may genuinely have). Such a row is
	// read again, ahead of the other stored rows, up to the plan's limit, and
	// never reused. It stays a stored row for the guards that ask whether a
	// posting is stored: it is never dropped or deferred, and one the plan cannot
	// read (past the limit, or its read missed) is held, not listed and not aged
	// (DetailPlan.Held). It is ignored on a row that is not Stored.
	Unusable bool
}

// Marked is whether the adapter's Options.Unusable (nil marks none) marks the
// stored posting k as one whose text cannot be reused: the ledger could not read
// its body back, and handing it back as the listed row would store an empty
// description over the text. It is DetailRow.Unusable for a stored row.
func Marked(unusable func(posting.Key) bool, k posting.Key) bool {
	return unusable != nil && unusable(k)
}

// DetailPlan is which listed rows get a detail request. Each slice holds row
// indexes.
type DetailPlan struct {
	// Fetch is the rows that get a detail request, in order: the new rows,
	// then the changed stored rows, then the stored rows whose turn it is.
	Fetch []int
	// New is how many of Fetch, from its start, are new rows.
	New int
	// Unusable is how many of Fetch, right after the New rows, are stored rows
	// whose text is unusable (DetailRow.Unusable). They come ahead of the other
	// stored rows and take their room from the new rows, but only up to the limit.
	Unusable int
	// Held is the unusable stored rows past the limit, in listing order: not
	// requested, and not reused, since listing one unread would list it with no
	// text. The step leaves them out and reports them as a shortfall, so the
	// listing reads as short and their stored postings are kept, not aged. The
	// next run reads the next batch.
	Held []int
	// Reuse is the stored rows returned as stored, with no request: those not
	// due a re-read, and those whose re-read did not fit.
	Reuse []int
	// Deferred is how many new rows were past the cap.
	Deferred int
	// Prefiltered is how many new rows were dropped.
	Prefiltered int
}

// PlanDetails plans the detail requests for rows on day, at most limit of
// them. New rows come first and those past limit are deferred; stored rows with
// unusable text follow, read ahead of every other stored row and taking their
// room from the new rows, but only up to limit (the rest are Held);
// changed stored rows, then stored rows whose RefreshTurn it is, fill what room is left, and a
// stored row whose re-read does not fit is reused. Every row lands in exactly
// one of Fetch, Reuse, Held, Deferred or Prefiltered.
//
// A caller without stored postings must refuse a plan with Deferred > 0: it
// cannot tell a deferred row from a stored one, and leaving out a stored one
// would age it as absent.
func PlanDetails(rows []DetailRow, day, limit int) DetailPlan {
	var p DetailPlan
	var fresh, unusable, changed, scheduled []int
	for i, r := range rows {
		switch {
		case r.Stored && r.Unusable:
			unusable = append(unusable, i)
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
	if len(unusable) > limit {
		p.Held = unusable[limit:]
		unusable = unusable[:limit]
	}
	if room := max(limit-len(unusable), 0); len(fresh) > room {
		p.Deferred = len(fresh) - room
		fresh = fresh[:room]
	}
	p.New, p.Unusable = len(fresh), len(unusable)
	p.Fetch = append(fresh, unusable...)
	for _, group := range [][]int{changed, scheduled} {
		room := max(limit-len(p.Fetch), 0)
		if len(group) > room {
			p.Reuse = append(p.Reuse, group[room:]...)
			group = group[:room]
		}
		p.Fetch = append(p.Fetch, group...)
	}
	return p
}
