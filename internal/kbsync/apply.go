package kbsync

import (
	"context"
	"encoding/json"
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
	Orphaned          []string
}

// Apply sends every added or updated block through career_upsert_block, in
// block-id order, and stops at the first error. A block's hash is recorded in
// state (and the state file saved) only after DevPrism accepted that block,
// so a stop leaves state and DevPrism in agreement.
//
// DevPrism answers an overwrite that would drop bullets/facts or rewrite a
// locked bullet with an inputRequired confirmation request
// (tools_career.rs:926-959). Apply does not confirm it: no request_state or
// input_responses is ever sent. The block is reported as needs_confirmation
// with the server's message and the sync continues with the others.
func Apply(ctx context.Context, caller ToolCaller, plan SyncPlan, state *SyncState) (Result, error) {
	res := Result{Orphaned: plan.Orphaned}
	if state == nil {
		return res, fmt.Errorf("%w: Apply needs a state", ErrState)
	}
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
			Success *bool  `json:"success"`
			BlockID string `json:"blockId"`
		}
		if err := json.Unmarshal(raw, &ans); err != nil {
			return res, fmt.Errorf("%w: career_upsert_block %s: answer is not an object", ErrDevPrism, it.ID)
		}
		switch {
		case ans.ResultType == "inputRequired":
			msg := "DevPrism asked for confirmation"
			if c, ok := ans.InputRequests["confirm"]; ok && c.Message != "" {
				msg = truncate(c.Message, 400)
			}
			add(Outcome{ID: it.ID, Status: StatusNeedsConfirmation, Reason: msg})
		case ans.Success != nil && *ans.Success && ans.BlockID == it.ID:
			if err := state.record(it.ID, it.Hash, time.Now()); err != nil {
				return res, err
			}
			st := StatusUpdated
			if it.Action == ActionAdd {
				st = StatusAdded
			}
			add(Outcome{ID: it.ID, Status: st})
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
	return fmt.Sprintf("%d added, %d updated, %d unchanged, %d blocked, %d need confirmation, %d orphaned",
		r.Added, r.Updated, r.Unchanged, r.Blocked, r.NeedsConfirmation, len(r.Orphaned))
}
