package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Why an advance phase ended before it had worked through every eligible
// posting (AdvanceReport.Stopped).
const (
	StopCap          = "cap"           // advance_per_run reached
	StopUnreadStreak = "unread_streak" // MaxConsecutiveUnread unread in a row
	StopNoneRead     = "none_read"     // no backend read any posting
	StopFailStreak   = "fail_streak"   // MaxConsecutiveFailed stage failures in a row
	StopNoneAdvanced = "none_advanced" // every attempted posting failed or went unread
	StopCancelled    = "cancelled"
)

// MaxConsecutiveUnread stops a run's advance phase once this many postings in
// a row could not be read by any decision backend: a refused key or an outage
// fails every call, and spending the whole cap on it repeats the same failure
// (and the shadow's work) hundreds of times.
const MaxConsecutiveUnread = 20

// MaxConsecutiveFailed does the same for any other stage failure (DevPrism
// down, the explainer refused): that many in a row is an outage, not a bad
// posting, and the run fails rather than publish a digest that hides it.
const MaxConsecutiveFailed = 20

// MaxUnreadExamples bounds the per-posting lines for unread postings; the rest
// are counted in one summary line.
const MaxUnreadExamples = 3

// MaxDistinctCauses bounds how many different advance failures a run reports
// one by one; later new causes are only counted.
const MaxDistinctCauses = 20

// ErrAdvanceFailing marks a run stopped because postings kept failing a stage
// other than triage.
var ErrAdvanceFailing = errors.New("advance: postings keep failing a stage")

// AdvanceReport is what one run's advance phase did. Deferred postings were
// not attempted this run and wait, still listed, for the next one.
type AdvanceReport struct {
	// Eligible postings were waiting for a stage when the phase started.
	Eligible int `json:"eligible"`
	// Limit is the per-run cap (config advance_per_run).
	Limit     int `json:"limit"`
	Attempted int `json:"attempted"`
	// Unread postings no decision backend could read; they stay live.
	Unread int `json:"unread"`
	// Failed postings hit another stage error; they wait for the next run.
	Failed   int    `json:"failed"`
	Deferred int    `json:"deferred"`
	Stopped  string `json:"stopped,omitempty"`
	// FailOpen counts postings kept because a check could not run, by cause
	// (empty description, unread title or location, DevPrism down). A kept
	// posting here did not pass the check; it was never checked.
	FailOpen FailOpenCounts `json:"fail_open,omitzero"`
	// Notes is what the notes gate did: below the floor, deferred for unknown
	// relevance, deferred because the notes budget was spent.
	Notes NotesCounts `json:"notes,omitzero"`
}

// requestID matches a provider request id (Anthropic "req_…", OpenRouter
// "gen-…"), which differs on every call.
var requestID = regexp.MustCompile(`\b(req|gen)[_-][A-Za-z0-9_-]+`)

// ErrorKey is the identity of a backend error for de-duplication: the same
// refusal with a new request id is the same error. The first message is still
// printed whole, request id included.
func ErrorKey(msg string) string { return requestID.ReplaceAllString(msg, "$1_*") }

// AdvancePlan is everything that varies between the callers of Advance (the
// daily run, the inbox poll, rescore), as named fields. The callers share one
// engine; a difference between them is a field here, set in one place by the
// plan's constructor, never an extra argument.
type AdvancePlan struct {
	// Limit is the cap on postings attempted (config advance_per_run, or a
	// tuned cap) and Workers how many advance at a time. Both must be above 0.
	Limit, Workers int
	// Skip is a run's --list-only: Run does not start the phase at all.
	Skip bool
	// Select says which records the phase advances, in place of the
	// pipeline-status rule (Live, Triaged, Unsure, Scored). Nil keeps it.
	Select func(ledger.Record) bool
	// Order sorts the selected records in the order they advance, given every
	// ledger record (it says when each board was first crawled). Nil keeps
	// ledger order. It only orders (Rule 7): no posting is dropped. It lives
	// in the caller because the ranking owner (jobinsight) imports this
	// package.
	Order func(todo, all []ledger.Record)
	// TitleDrop reports whether advancing a record drops it by its title
	// without asking any backend. Such a posting counts toward Limit, but it
	// is no backend reading: it neither ends an unread streak nor keeps the
	// phase from failing when no backend read anything. Nil: none.
	TitleDrop func(ledger.Record) bool
	// QuietCap leaves out the "advance cap reached" line, for a caller whose
	// cap is not advance_per_run (rescore's --limit).
	QuietCap bool
	// FillFunnel copies the Runner's fail-open and notes-gate counts into the
	// report, and says so when a check could not run (the run's record).
	FillFunnel bool
	// Warn prints one warning line. Nil discards.
	Warn func(format string, args ...any)
	// OnCounted, set only by tests, is called on the phase's goroutine after
	// each finished posting is counted, with whether the phase has decided to
	// stop. A test uses it to finish postings in an exact order.
	OnCounted func(key posting.Key, stopped bool)
}

// AdvanceDeps is what an advance runs against: the records it may advance,
// the per-posting stage chain and, for FillFunnel, the Runner that counted.
type AdvanceDeps struct {
	Records []ledger.Record
	Advance func(context.Context, posting.Key) error
	Runner  *Runner
}

// Advance advances up to plan.Limit postings. They go in plan.Order (the
// daily run: highest title rank first, then freshest first, ties in ledger
// order), so the cap spends itself on new postings with the titles the active
// track wants. Order only orders: every eligible posting is still advanced,
// this run or a later one. A posting no backend could read stays Live for the
// next run. The run fails, so a shell loop stops and no empty digest is
// published, when no backend read MaxConsecutiveUnread postings in a row, when
// MaxConsecutiveFailed in a row failed another stage, or when none of the
// postings it tried advanced. Any other stage failure is printed once per
// cause (the posting key and provider request id are not part of the cause)
// and counted after that. The report says what happened however the phase
// ended, so the run record can show what the cap or a stop left waiting.
//
// Up to plan.Workers postings (config advance_concurrency) are advanced at a
// time. They start in order and their results are counted on this goroutine,
// so the report, the warnings and the stop rules need no lock. The ledger
// serialises its own writes, and each posting is advanced by one goroutine
// only. The streaks are counted in dispatch order, not finishing order: a
// posting that ends a streak resets only the postings dispatched before it. A
// stop (unread or failure streak, cancellation) starts no further posting but
// waits for those in flight, so nothing is written to the ledger after Advance
// returns. A streak stop therefore attempts at most MaxConsecutiveUnread (or
// MaxConsecutiveFailed) postings after the last posting that ended the streak,
// plus workers-1 in flight, plus any postings dropped by title among them
// (they count as attempted but end and extend no streak).
func Advance(ctx context.Context, plan AdvancePlan, deps AdvanceDeps) (AdvanceReport, error) {
	rep, err := advance(ctx, plan, deps)
	if plan.FillFunnel && deps.Runner != nil {
		FillFunnelReport(&rep, deps.Runner, plan.warnf)
	}
	return rep, err
}

func (p AdvancePlan) warnf(format string, a ...any) {
	if p.Warn != nil {
		p.Warn(format, a...)
	}
}

// FillFunnelReport copies the run's fail-open and notes-gate counts into the
// advance report and says so when any check could not run.
func FillFunnelReport(rep *AdvanceReport, r *Runner, warnf func(string, ...any)) {
	rep.FailOpen, rep.Notes = r.FailOpen.Counts(), r.Notes.Counts()
	if n := rep.FailOpen.Total(); n > 0 {
		warnf("%d posting(s) kept because a check could not run: %+v", n, rep.FailOpen)
	}
	if n := rep.Notes.UnknownRelevance + rep.Notes.BudgetSpent; n > 0 {
		warnf("%d note(s) deferred (%d relevance unknown, %d notes budget spent); they stay scored for a later run", n, rep.Notes.UnknownRelevance, rep.Notes.BudgetSpent)
	}
}

func advance(ctx context.Context, plan AdvancePlan, deps AdvanceDeps) (AdvanceReport, error) {
	limit, workers, warnf := plan.Limit, plan.Workers, plan.warnf
	if limit <= 0 {
		return AdvanceReport{}, fmt.Errorf("advance limit %d must be above 0", limit)
	}
	if workers <= 0 {
		return AdvanceReport{}, fmt.Errorf("advance concurrency %d must be above 0", workers)
	}
	if deps.Advance == nil {
		return AdvanceReport{}, fmt.Errorf("advance: %w: no stage chain", ErrStageMissing)
	}
	recs := deps.Records
	var todo []ledger.Record
	for _, r := range recs {
		if plan.Select != nil {
			if plan.Select(r) {
				todo = append(todo, r)
			}
			continue
		}
		switch r.Status {
		case posting.Live, posting.Triaged, posting.Unsure, posting.Scored:
			todo = append(todo, r)
		}
	}
	if plan.Order != nil {
		plan.Order(todo, recs)
	}
	rep := AdvanceReport{Eligible: len(todo), Limit: limit}
	done := func(stopped string) AdvanceReport {
		rep.Deferred = rep.Eligible - rep.Attempted
		rep.Stopped = stopped
		return rep
	}
	n := min(len(todo), limit)
	// The stop rules count in dispatch (rank) order, not finishing order. A
	// streak counts the finished postings of its kind dispatched after the
	// last finished posting that ends it (since), so a posting dispatched
	// before an outage that finishes after postings dispatched behind it
	// cannot reset their count. A stop therefore comes once MaxConsecutive*
	// postings dispatched after that posting have finished, with at most
	// workers-1 others in flight: Attempted <= since+1+MaxConsecutive*+
	// workers-1, plus the title drops among them, which are attempted but
	// neutral.
	const (
		pending byte = iota
		wasRead
		wasUnread
		wasFailed
		neutral // dropped by title or cut short by cancellation: ends nothing
	)
	outcomes := make([]byte, n)
	type streakOf struct {
		kind         byte
		since, count int
	}
	unreadRun, failRun := streakOf{kind: wasUnread, since: -1}, streakOf{kind: wasFailed, since: -1}
	next := 0 // postings dispatched so far
	fold := func(s *streakOf, i int) {
		switch c := outcomes[i]; {
		case c == s.kind:
			if i > s.since {
				s.count++
			}
		case c == wasRead || c == wasUnread || c == wasFailed:
			if i > s.since {
				s.since, s.count = i, 0
				for j := i + 1; j < next; j++ {
					if outcomes[j] == s.kind {
						s.count++
					}
				}
			}
		}
	}
	type cause struct {
		first posting.Key
		more  int
	}
	causes := map[string]*cause{}
	var order []string
	uncounted, byTitle := 0, 0
	// reportCauses prints the counts the first line of each cause left out.
	reportCauses := func() {
		for _, k := range order {
			if c := causes[k]; c.more > 0 {
				warnf("%d more posting(s) failed the same way as %s; they wait for the next run", c.more, c.first)
			}
		}
		if uncounted > 0 {
			warnf("%d more posting(s) failed with other errors (more than %d distinct causes); they wait for the next run", uncounted, MaxDistinctCauses)
		}
	}
	stopped := ""
	var cancelErr error
	// classify counts one finished posting into the report and says what it
	// was for the stop rules. free: the posting was dropped by its title, so
	// no backend was asked.
	classify := func(key posting.Key, free bool, err error) byte {
		if err != nil && ctx.Err() != nil {
			// Cut short by cancellation: neither unread nor failed.
			if stopped == "" {
				stopped, cancelErr = StopCancelled, ctx.Err()
			}
			return neutral
		}
		if err == nil && free {
			byTitle++
			return neutral
		}
		if errors.Is(err, ErrTriageUnavailable) {
			rep.Unread++
			if rep.Unread <= MaxUnreadExamples {
				warnf("advance %s: %v", key, err)
			}
			return wasUnread
		}
		if err == nil {
			return wasRead
		}
		rep.Failed++
		ek := ErrorKey(strings.ReplaceAll(err.Error(), string(key), "<posting>"))
		switch c, seen := causes[ek]; {
		case seen:
			c.more++
		case len(causes) >= MaxDistinctCauses:
			uncounted++
		default:
			causes[ek] = &cause{first: key}
			order = append(order, ek)
			warnf("advance %s: %v", key, err)
		}
		return wasFailed
	}
	// count folds the posting dispatched i-th into the report and the stop rules.
	count := func(i int, key posting.Key, free bool, err error) {
		outcomes[i] = classify(key, free, err)
		fold(&unreadRun, i)
		fold(&failRun, i)
		if stopped != "" {
			return
		}
		switch {
		case unreadRun.count >= MaxConsecutiveUnread:
			stopped = StopUnreadStreak
		case failRun.count >= MaxConsecutiveFailed:
			stopped = StopFailStreak
		}
	}

	type result struct {
		idx      int
		key      posting.Key
		free     bool
		err      error
		panicked bool
		value    any
	}
	// Buffered to workers, so a finished posting never blocks its goroutine.
	results := make(chan result, workers)
	inflight := 0
	var panicked *result
	for {
		for stopped == "" && panicked == nil && next < n && inflight < workers {
			if err := ctx.Err(); err != nil {
				stopped, cancelErr = StopCancelled, err
				break
			}
			idx, key := next, todo[next].Key
			// Decided here, before the posting advances and its status moves.
			free := plan.TitleDrop != nil && plan.TitleDrop(todo[next])
			next++
			inflight++
			rep.Attempted++
			go func() {
				// A panic is carried back and re-raised on the caller's
				// goroutine, where the daemon contains it as it did before
				// postings ran concurrently.
				defer func() {
					if p := recover(); p != nil {
						results <- result{idx: idx, key: key, panicked: true, value: p}
					}
				}()
				err := deps.Advance(ctx, key)
				results <- result{idx: idx, key: key, free: free, err: err}
			}()
		}
		if inflight == 0 {
			break
		}
		r := <-results
		inflight--
		if r.panicked {
			if panicked == nil {
				panicked = &r
			}
			continue
		}
		count(r.idx, r.key, r.free, r.err)
		if plan.OnCounted != nil {
			plan.OnCounted(r.key, stopped != "")
		}
	}
	if panicked != nil {
		panic(fmt.Sprintf("advance %s: %v", panicked.key, panicked.value))
	}
	switch stopped {
	case StopCancelled:
		return done(StopCancelled), cancelErr
	case StopUnreadStreak:
		return done(StopUnreadStreak), fmt.Errorf("no decision backend read %d postings in a row; stopped advancing after %d (they stay live; see the backend errors above): %w",
			MaxConsecutiveUnread, rep.Attempted, ErrTriageUnavailable)
	case StopFailStreak:
		reportCauses()
		return done(StopFailStreak), fmt.Errorf("%d postings in a row failed a stage; stopped advancing after %d (they wait for the next run; see the errors above): %w",
			MaxConsecutiveFailed, rep.Attempted, ErrAdvanceFailing)
	}
	if len(todo) > limit && !plan.QuietCap {
		warnf("advance cap %d reached; %d posting(s) wait for the next run (advance_per_run)", limit, len(todo)-rep.Attempted)
	}
	reportCauses()
	if byTitle > 0 {
		warnf("%d posting(s) dropped by title before triage (title_signals.read); no backend was asked", byTitle)
	}
	if rep.Unread > 0 {
		if rep.Unread == rep.Attempted-byTitle {
			return done(StopNoneRead), fmt.Errorf("no decision backend read any posting: %d of %d unread (they stay live; see the backend errors above): %w",
				rep.Unread, rep.Attempted, ErrTriageUnavailable)
		}
		warnf("%d posting(s) could not be read by any decision backend; they stay live for the next run", rep.Unread)
	}
	// Title drops asked no stage, so like the unread check above they neither
	// count as advancing nor hide a stage that failed everything else.
	if asked := rep.Attempted - byTitle; asked > 0 && rep.Unread+rep.Failed == asked {
		return done(StopNoneAdvanced), fmt.Errorf("no posting advanced: %d failed a stage and %d were unread, of %d attempted (they wait for the next run; see the errors above): %w",
			rep.Failed, rep.Unread, rep.Attempted, ErrAdvanceFailing)
	}
	if rep.Attempted >= limit && rep.Attempted < rep.Eligible {
		return done(StopCap), nil
	}
	return done(""), nil
}
