package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Outcome statuses.
const (
	StatusAdded             = "added"
	StatusUpdated           = "updated"
	StatusUnchanged         = "unchanged"
	StatusBlocked           = "blocked"
	StatusNeedsConfirmation = "needs_confirmation"
	// StatusConfirmed: DevPrism asked, the overwrite was confirmed and accepted.
	StatusConfirmed = "confirmed"
	// StatusConfirmRejected: DevPrism refused the confirmation (-32002).
	StatusConfirmRejected = "confirm_rejected"
	// StatusStale: DevPrism's copy changed after the run read it, so the
	// confirmation was not sent.
	StatusStale = "stale"
)

// Outcome is what happened to one block.
type Outcome struct {
	ID     string
	Status string
	Reason string // blocked reason, or DevPrism's confirmation message
}

// Result summarises an Apply. It is returned even when Apply stops on an
// error, so the caller sees what had already been accepted.
type Result struct {
	Outcomes          []Outcome
	Added             int
	Updated           int
	Unchanged         int
	Blocked           int
	NeedsConfirmation int
	Confirmed         int
	ConfirmRejected   int
	Stale             int
	Orphaned          []string
}

// Apply sends every added or updated block through career_upsert_block, in
// block-id order, and stops at the first error. A block's hash is recorded in
// state only after DevPrism accepted that block, so state never runs ahead of
// DevPrism. The state file is saved every flushEvery accepted blocks and on
// every return, error or not, so a stop leaves the two in agreement; a crash
// between saves leaves state behind, and those blocks are sent again.
//
// DevPrism answers an overwrite that would drop bullets/facts or rewrite a
// locked bullet with an inputRequired confirmation request
// (tools_career.rs:926-959). With a nil Confirmer, Apply does not confirm it:
// no request_state or input_responses is ever sent, the block is reported as
// needs_confirmation with the server's message, and the sync continues with
// the others. With a Confirmer, each such block, and only such a block, goes
// through confirm (confirm.go): what it would lose is read from DevPrism's
// requestState and shown to the Confirmer, DevPrism's copy is re-read and
// must be the one this plan was built from, and then that block's own
// requestState is sent back with {confirm:true}. A refusal or a change is
// reported for the block and the sync continues; DevPrism down or timing out
// stops it as any other call does.
func Apply(ctx context.Context, caller ToolCaller, plan SyncPlan, state *SyncState, confirmer Confirmer) (res Result, err error) {
	res = Result{Orphaned: plan.Orphaned}
	if state == nil {
		return res, fmt.Errorf("%w: Apply needs a state", ErrState)
	}
	defer func() {
		if ferr := state.flush(); ferr != nil {
			err = errors.Join(err, ferr)
		}
	}()
	add := func(o Outcome) {
		res.Outcomes = append(res.Outcomes, o)
		switch o.Status {
		case StatusAdded:
			res.Added++
		case StatusUpdated:
			res.Updated++
		case StatusUnchanged:
			res.Unchanged++
		case StatusBlocked:
			res.Blocked++
		case StatusNeedsConfirmation:
			res.NeedsConfirmation++
		case StatusConfirmed:
			res.Confirmed++
		case StatusConfirmRejected:
			res.ConfirmRejected++
		case StatusStale:
			res.Stale++
		}
	}
	for _, it := range plan.Items {
		switch it.Action {
		case ActionUnchanged:
			add(Outcome{ID: it.ID, Status: StatusUnchanged})
			continue
		case ActionBlocked:
			add(Outcome{ID: it.ID, Status: StatusBlocked, Reason: it.Reason})
			continue
		case ActionAdd, ActionUpdate:
		default:
			return res, fmt.Errorf("kbsync: plan item %q has unknown action %q", it.ID, it.Action)
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if len(it.Payload) == 0 {
			return res, fmt.Errorf("kbsync: plan item %q has no payload", it.ID)
		}
		args := map[string]json.RawMessage{"block": it.Payload}
		raw, err := caller.CallTool(ctx, "career_upsert_block", args)
		if err != nil {
			return res, fmt.Errorf("kbsync: career_upsert_block %s: %w", it.ID, err)
		}
		var ans struct {
			ResultType    string `json:"resultType"`
			InputRequests map[string]struct {
				Message string `json:"message"`
			} `json:"inputRequests"`
			RequestState string `json:"requestState"`
			Success      *bool  `json:"success"`
			BlockID      string `json:"blockId"`
		}
		if err := json.Unmarshal(raw, &ans); err != nil {
			return res, fmt.Errorf("%w: career_upsert_block %s: answer is not an object", ErrDevPrism, it.ID)
		}
		accepted := func(st string) error {
			state.record(it.ID, it.Hash, time.Now())
			if state.unsaved >= flushEvery {
				if err := state.flush(); err != nil {
					return err
				}
			}
			add(Outcome{ID: it.ID, Status: st})
			return nil
		}
		switch {
		case ans.ResultType == "inputRequired" && confirmer != nil:
			c, err := confirm(ctx, caller, it, ans.RequestState, confirmer)
			if err != nil {
				return res, err
			}
			if c.status == StatusConfirmed {
				if err := accepted(StatusConfirmed); err != nil {
					return res, err
				}
				continue
			}
			add(Outcome{ID: it.ID, Status: c.status, Reason: c.reason})
		case ans.ResultType == "inputRequired":
			msg := "DevPrism asked for confirmation"
			if c, ok := ans.InputRequests["confirm"]; ok && c.Message != "" {
				msg = truncate(c.Message, 400)
			}
			add(Outcome{ID: it.ID, Status: StatusNeedsConfirmation, Reason: msg})
		case ans.Success != nil && *ans.Success && ans.BlockID == it.ID:
			st := StatusUpdated
			if it.Action == ActionAdd {
				st = StatusAdded
			}
			if err := accepted(st); err != nil {
				return res, err
			}
		default:
			return res, fmt.Errorf("%w: career_upsert_block %s: unexpected answer %s", ErrDevPrism, it.ID, truncate(string(raw), 200))
		}
	}
	return res, nil
}

// DryRun prints the plan and calls nothing: it takes no ToolCaller. It names
// block ids and counts only.
func DryRun(w io.Writer, plan SyncPlan) error {
	var werr error
	pf := func(format string, a ...any) {
		if werr == nil {
			_, werr = fmt.Fprintf(w, format, a...)
		}
	}
	pf("kbsync plan (dry run, nothing sent): %d add, %d update, %d unchanged, %d blocked, %d orphaned\n",
		plan.Count(ActionAdd), plan.Count(ActionUpdate), plan.Count(ActionUnchanged), plan.Count(ActionBlocked), len(plan.Orphaned))
	for _, it := range plan.Items {
		if it.Action == ActionBlocked {
			pf("  %-9s %s (%d bullets): %s\n", it.Action, it.ID, it.Bullets, it.Reason)
			continue
		}
		pf("  %-9s %s (%d bullets)\n", it.Action, it.ID, it.Bullets)
	}
	for _, id := range plan.Orphaned {
		pf("  orphaned  %s (in DevPrism, not in rkb; left alone)\n", id)
	}
	if plan.Unattached > 0 {
		pf("  note: %d rkb bullets belong to no entry and are not synced\n", plan.Unattached)
	}
	if len(plan.Cautioned) > 0 {
		pf("  note: synced bullets use CAUTION claims: %v\n", plan.Cautioned)
	}
	return werr
}

// Summary is a one-line account of a Result.
func (r Result) Summary() string {
	s := fmt.Sprintf("%d added, %d updated, %d unchanged, %d blocked, %d need confirmation, %d orphaned",
		r.Added, r.Updated, r.Unchanged, r.Blocked, r.NeedsConfirmation, len(r.Orphaned))
	if r.Confirmed+r.ConfirmRejected+r.Stale > 0 {
		s += fmt.Sprintf("; %d confirmed, %d confirmation refused, %d changed in DevPrism (not confirmed)",
			r.Confirmed, r.ConfirmRejected, r.Stale)
	}
	return s
}
