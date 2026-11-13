package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The silver rescue (D6 of docs/RANKING_PLAN.md; stage 8 of
// build/fable-plan-2026-11-12-ranking.md): a model drop that the pairwise
// would_apply auditor keeps picking over its stratum-mates goes back to
// Unsure, its readings kept. The auditor's pick is an annotation (Rule 7);
// this engine rule turns it into Unsure, which only loosens: the posting is
// scored and shown like any other uncertain one, and no drop is ever made
// here. It is off unless the owner runs it, capped per day, and the caller
// decides when the auditor may be trusted (cmd/vani: its AUC verdict).
//
// These are drops today's rules still make (a drop the rules no longer make
// is RetriagePolicyDrops'). A drop on a watched board is not put back at once:
// a dropped posting is closed, so the ledger cannot say whether its board still
// lists it, and a long-gone role must not reach scoring and the paid notes
// call. Like every reopen, it waits as a pending reopen (class "silver") for
// its board's next full listing to show it, and is judged again there on the
// listed text (SightingReopen). The runner does not triage an Unsure record
// again (it goes on to scoring), so the rescue holds; an owner exclusion still
// drops it, as it should.

// AnnSilverRescue marks a record the silver rescue returned to Unsure: the day
// (UTC, YYYY-MM-DD) the rescue was decided first, then the auditor's score and
// its comparison counts. The daily cap counts these and today's pending
// "silver" reopens. The key sits outside the triage.* annotations, which a
// retriage forgets, so a later event does not lose it.
const AnnSilverRescue = "audit.would_apply.rescue"

// MinSilverRescueGames is the fewest usable comparisons a pick rests on, and
// MaxSilverRescuePerDay the most rescues one day may make.
const (
	MinSilverRescueGames  = 3
	MaxSilverRescuePerDay = 50
)

// SilverPick is the auditor's standing for one posting (eval.PostingScore).
type SilverPick struct {
	Score   float64
	Games   int
	Picked  int
	Neither int
}

// SilverRule is when a pick rescues a drop.
type SilverRule struct {
	// MinScore is the smoothed pick rate (0-1) at or above which a posting
	// counts as wanted; the score is relative to the posting's stratum, so a
	// dropped stratum's best is not by itself wanted. MinGames is the fewest
	// comparisons; MaxPerDay the most rescues per UTC day, counted from the
	// ledger's AnnSilverRescue stamps and the day's pending silver reopens.
	MinScore  float64
	MinGames  int
	MaxPerDay int
}

// Check refuses a rule that would rescue on too little.
func (r SilverRule) Check() error {
	switch {
	case !(r.MinScore > 0.5) || r.MinScore > 1:
		return fmt.Errorf("pipeline: silver rescue min score %v must be above 0.5 and at most 1", r.MinScore)
	case r.MinGames < MinSilverRescueGames:
		return fmt.Errorf("pipeline: silver rescue needs at least %d comparisons, got %d", MinSilverRescueGames, r.MinGames)
	case r.MaxPerDay < 1 || r.MaxPerDay > MaxSilverRescuePerDay:
		return fmt.Errorf("pipeline: silver rescue cap %d must be within 1..%d", r.MaxPerDay, MaxSilverRescuePerDay)
	}
	return nil
}

// SilverCandidate is a model drop the rule would rescue.
type SilverCandidate struct {
	Key   posting.Key `json:"key"`
	Score float64     `json:"score"`
	Games int         `json:"games"`
}

// SilverRescue is what RescueSilverDrops did, or with dryRun would do.
type SilverRescue struct {
	// Candidates are every drop the rule admits, best score first. Rescued
	// are those taken within today's cap (or, dry, that would be): back to
	// Unsure now, or, for the ones also in Deferred, waiting for their board's
	// next listing.
	Candidates []SilverCandidate `json:"candidates"`
	Rescued    []posting.Key     `json:"rescued"`
	Deferred   []posting.Key     `json:"deferred"`
	// Today is how many rescues were decided earlier today (written or
	// pending); CapLeft what the cap left for this call.
	Today   int `json:"today"`
	CapLeft int `json:"cap_left"`
	// Left counts the drops the rule passed over, by reason (fixed strings).
	Left map[string]int `json:"left"`
}

// Why a drop with a pick was passed over.
const (
	silverLeftNotModel  = "not a model drop (title or owner exclusion)"
	silverLeftProtected = "a sponsorship, work_mode or exclusion drop"
	silverLeftGames     = "too few comparisons"
	silverLeftNeither   = "an answer said neither posting is wanted"
	silverLeftScore     = "score under the rule's"
	silverLeftNoBody    = "description could not be read back"
	silverLeftCap       = "over today's cap"
	silverLeftStale     = "on a board no longer watched"
	silverLeftPending   = "already waiting for its board to list it"
	silverLeftOverBound = "over the pending-reopen bound"
)

// isModelDrop reports whether a decision model dropped r: a backend recorded
// its reading, and neither a title reading nor an owner exclusion made the drop.
func isModelDrop(r ledger.Record) bool {
	backend := r.Annotations[ledger.AnnTriagePrefix+"backend"]
	return backend != "" && backend != TitleBackend && backend != ExcludeBackend && !Excluded(r) && !TitleDropped(r)
}

// silverStamp is the AnnSilverRescue value of a rescue decided on day.
func silverStamp(day string, c SilverCandidate) string {
	return fmt.Sprintf("%s score=%.3f games=%d", day, c.Score, c.Games)
}

// silverEvent is the Unsure event of a silver rescue, stamped stamp.
func silverEvent(k posting.Key, now time.Time, stamp, why string) ledger.Event {
	return ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: posting.Unsure,
		Annotations: map[string]string{
			AnnSilverRescue:        stamp,
			ledger.AnnTriageUnsure: "the would_apply auditor picked it over its stratum (" + why + "); the drop rule still holds",
		},
		Reason: "silver rescue: the would_apply auditor wants a drop"}
}

// RescueSilverDrops returns to Unsure, best score first and at most the
// rule's daily cap, every Dropped record a model dropped whose pick passes the
// rule. protected reports a drop the auditor must never rescue (a work
// authorization, work_mode or owner exclusion drop); it is required. sc bounds
// what may come back as it bounds RetriagePolicyDrops: a drop on a board no
// longer watched stays dropped, and one sc defers is marked as a pending
// "silver" reopen in sc.Pending instead of written (it counts against the day's
// cap all the same). With dryRun it only reports. A second call the same day
// finds the cap spent or nothing left to rescue.
func RescueSilverDrops(ctx context.Context, l ledger.Ledger, now time.Time, picks map[posting.Key]SilverPick, rule SilverRule, protected func(ledger.Record) bool, sc ReopenScope, dryRun bool) (SilverRescue, error) {
	out := SilverRescue{Candidates: []SilverCandidate{}, Rescued: []posting.Key{}, Deferred: []posting.Key{}, Left: map[string]int{}}
	if err := rule.Check(); err != nil {
		return out, err
	}
	if protected == nil {
		return out, errors.New("pipeline: silver rescue needs the protected-drop test")
	}
	if sc.Defer != nil && sc.Pending == nil {
		return out, errors.New("pipeline: a deferring rescue needs somewhere to keep its pending reopens")
	}
	day := now.UTC().Format(time.DateOnly)
	if sc.Pending != nil {
		for _, e := range sc.Pending.Items {
			if e.Class == reopenClassSilver && e.At.UTC().Format(time.DateOnly) == day {
				out.Today++
			}
		}
	}
	byKey := map[posting.Key]ledger.Record{}
	for _, r := range l.All() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if strings.HasPrefix(r.Annotations[AnnSilverRescue], day) {
			out.Today++
		}
		if r.Status != posting.Dropped {
			continue
		}
		p, ok := picks[r.Key]
		if !ok {
			continue
		}
		switch {
		case !isModelDrop(r):
			out.Left[silverLeftNotModel]++
		case protected(r):
			out.Left[silverLeftProtected]++
		case p.Games < rule.MinGames:
			out.Left[silverLeftGames]++
		case p.Neither > 0:
			out.Left[silverLeftNeither]++
		case math.IsNaN(p.Score) || p.Score < rule.MinScore:
			out.Left[silverLeftScore]++
		default:
			out.Candidates = append(out.Candidates, SilverCandidate{Key: r.Key, Score: p.Score, Games: p.Games})
			byKey[r.Key] = r
		}
	}
	slices.SortStableFunc(out.Candidates, func(a, b SilverCandidate) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(string(a.Key), string(b.Key))
	})
	out.CapLeft = max(rule.MaxPerDay-out.Today, 0)
	added := 0 // pending reopens this call adds (a dry run adds none to Pending)
	for _, c := range out.Candidates {
		r := byKey[c.Key]
		switch {
		case !sc.reachable(c.Key):
			out.Left[silverLeftStale]++
			continue
		case sc.deferred(c.Key) && sc.Pending.Items[c.Key].Class == reopenClassSilver:
			out.Left[silverLeftPending]++ // counted in Today on the day it was marked
			continue
		case len(out.Rescued) >= out.CapLeft:
			out.Left[silverLeftCap]++
			continue
		}
		stamp := silverStamp(day, c)
		if sc.deferred(c.Key) {
			if _, ok := sc.Pending.Items[c.Key]; !ok && sc.Pending.Len()+added >= MaxPendingReopens {
				out.Left[silverLeftOverBound]++
				continue
			}
			if dryRun {
				added++
			} else {
				sc.Pending.Items[c.Key] = PendingReopen{Class: reopenClassSilver, At: now.UTC(), Reason: stamp}
			}
			out.Rescued = append(out.Rescued, c.Key)
			out.Deferred = append(out.Deferred, c.Key)
			continue
		}
		if full := ledger.Full(l, r); full.BodyEvicted {
			out.Left[silverLeftNoBody]++ // reopening an evicted record is refused without its body
			continue
		}
		if !dryRun {
			if err := l.Append(ctx, silverEvent(r.Key, now, stamp, fmt.Sprintf("score %.2f over %d comparisons", c.Score, c.Games))); err != nil {
				return out, fmt.Errorf("pipeline: silver rescue %s: %w", r.Key, err)
			}
		}
		out.Rescued = append(out.Rescued, c.Key)
	}
	return out, nil
}

// silverSightingEvent applies a pending silver reopen at the sighting: the
// posting goes to Unsure on the listed text when it is still a model drop the
// exclusions and title rules let through (what a fresh triage would apply
// before asking a backend); otherwise the reopen is spent and it stays
// dropped. The stamp is the one the rescue was decided under, so the cap counts
// it on that day.
func (s *SightingReopen) silverSightingEvent(rec ledger.Record, listed posting.Posting, stamp string, now time.Time) (ledger.Event, bool) {
	if !isModelDrop(rec) {
		return ledger.Event{}, false
	}
	if _, _, dropped := s.Policy.EvaluatePosting(listed); dropped {
		return ledger.Event{}, false
	}
	ev := silverEvent(rec.Key, now, stamp, "decided "+strings.SplitN(stamp, " ", 2)[0])
	ev.Reason += ", listed again"
	return ev, true
}
