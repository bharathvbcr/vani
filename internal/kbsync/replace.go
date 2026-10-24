package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// Replace mode. A plain sync never deletes. `kb sync --replace` also removes the
// DevPrism blocks this sync does not own, so ranking (resume_gap_analysis) is
// fed from the approved pool alone. Deleting is the one irreversible step, so
// it is bounded and ordered after everything else:
//
//   - only an orphan (a DevPrism block the approved pool does not produce) can
//     be deleted, and only one of a kind the sync itself writes unless the owner
//     says otherwise (publications, certifications and the like have no rkb
//     source to re-add them from);
//   - the owner can protect any orphan by id;
//   - a run deletes nothing unless every pool block landed, and never more than
//     a cap, and never when the pool is empty (a misconfigured selection would
//     otherwise read as "delete everything");
//   - each deletion goes through career_delete_block's own confirmation, by the
//     same round trip as an upsert confirmation (confirm.go).

// Deletion bounds.
const (
	// DefaultMaxDeletes is how many blocks one run may delete unless told
	// otherwise. DevPrism's table held 43 blocks when this was written.
	DefaultMaxDeletes = 100
	// MaxDeletesCeiling is the most a caller may raise the cap to.
	MaxDeletesCeiling = 500
)

// ErrReplaceRefused wraps every refusal to delete.
var ErrReplaceRefused = errors.New("kbsync: replace refused")

// syncedKinds are the DevPrism block kinds the sync writes (blocks.go; DevPrism
// files work under "experience", src/lib/resume-sections.ts BLOCK_KINDS). An
// orphan of any other kind is not a stale copy of something rkb owns.
var syncedKinds = map[string]bool{"experience": true, KindWork: true, KindProject: true, KindEducation: true}

// Doomed is an orphan the run would delete.
type Doomed struct {
	ID, Kind, Title string
	Bullets, Facts  int
	// Prior is DevPrism's copy this plan was built from.
	Prior json.RawMessage
}

// Kept is an orphan the run leaves alone, and why.
type Kept struct {
	ID, Kind, Title, Why string
}

// ReplacePlan is what a replace run would delete and what it spares.
type ReplacePlan struct {
	Delete []Doomed
	Keep   []Kept
}

// ReplaceOptions tunes PlanReplace.
type ReplaceOptions struct {
	// Keep names orphans to spare. Each must be an orphan in DevPrism now: a
	// typo or a stale id is an error, never a silent no-op.
	Keep map[string]bool
	// AllKinds also deletes orphans of kinds the sync does not write.
	AllKinds bool
}

// PlanReplace splits the plan's orphans into those to delete and those to spare.
// A block whose JSON cannot be read is spared: what cannot be read is not
// judged.
func PlanReplace(plan SyncPlan, current DevPrismState, opt ReplaceOptions) (ReplacePlan, error) {
	orphan := map[string]bool{}
	for _, id := range plan.Orphaned {
		orphan[id] = true
	}
	for _, id := range sortedKeys(opt.Keep) {
		if !orphan[id] {
			return ReplacePlan{}, fmt.Errorf("%w: --keep %q is not an orphan in DevPrism (no such block, or the approved pool owns it)", ErrReplaceRefused, truncate(id, 80))
		}
	}
	var rp ReplacePlan
	for _, id := range plan.Orphaned {
		raw := current.Blocks[id]
		var h struct {
			Kind    string            `json:"kind"`
			Title   string            `json:"title"`
			Bullets []json.RawMessage `json:"bullets"`
			Facts   []json.RawMessage `json:"facts"`
		}
		if json.Unmarshal(raw, &h) != nil {
			rp.Keep = append(rp.Keep, Kept{ID: id, Why: "its JSON is unreadable, so it is not judged"})
			continue
		}
		title := truncate(h.Title, 80)
		switch {
		case opt.Keep[id]:
			rp.Keep = append(rp.Keep, Kept{id, h.Kind, title, "named by --keep"})
		case !syncedKinds[h.Kind] && !opt.AllKinds:
			rp.Keep = append(rp.Keep, Kept{id, h.Kind, title, "kind " + fmt.Sprintf("%q", truncate(h.Kind, 40)) + " is not one the sync writes (--delete-all-kinds removes it)"})
		default:
			rp.Delete = append(rp.Delete, Doomed{ID: id, Kind: h.Kind, Title: title, Bullets: len(h.Bullets), Facts: len(h.Facts), Prior: raw})
		}
	}
	return rp, nil
}

// Check refuses a replace the guards forbid. max is the per-run cap; it must be
// within 1..MaxDeletesCeiling.
func (r ReplacePlan) Check(plan SyncPlan, max int) error {
	if max < 1 || max > MaxDeletesCeiling {
		return fmt.Errorf("%w: the delete cap %d is outside 1..%d", ErrReplaceRefused, max, MaxDeletesCeiling)
	}
	if len(r.Delete) == 0 {
		return nil
	}
	if len(r.Delete) > max {
		return fmt.Errorf("%w: it would delete %d blocks, over the cap of %d (raise --max-delete after reading the dry run)", ErrReplaceRefused, len(r.Delete), max)
	}
	if plan.Count(ActionAdd)+plan.Count(ActionUpdate)+plan.Count(ActionUnchanged) == 0 {
		return fmt.Errorf("%w: the approved pool has no block to sync, so replace would only delete (check kb.selections and the reviews behind them)", ErrReplaceRefused)
	}
	return nil
}

// Landed reports whether every pool block reached DevPrism. A block that was
// blocked, needs confirmation, was refused or went stale has not, and deleting
// the old blocks around it would leave DevPrism with less than the pool.
func (r Result) Landed() error {
	if n := r.Blocked + r.NeedsConfirmation + r.ConfirmRejected + r.Stale; n > 0 {
		return fmt.Errorf("%w: %d pool block(s) did not reach DevPrism (%d blocked, %d need confirmation, %d refused, %d changed); nothing was deleted",
			ErrReplaceRefused, n, r.Blocked, r.NeedsConfirmation, r.ConfirmRejected, r.Stale)
	}
	return nil
}

// Delete outcome statuses (besides the shared StatusNeedsConfirmation,
// StatusConfirmRejected and StatusStale).
const (
	StatusDeleted = "deleted"
	// StatusGone: DevPrism no longer holds the block; nothing to delete.
	StatusGone = "gone"
)

// DeleteOutcome is what happened to one orphan.
type DeleteOutcome struct {
	ID, Status, Reason string
}

// DeleteResult summarises ApplyDeletes. It is returned even when ApplyDeletes
// stops on an error.
type DeleteResult struct {
	Outcomes                                   []DeleteOutcome
	Deleted, Gone, NeedsConfirmation, Rejected int
	Stale                                      int
}

// Summary is a one-line account of a DeleteResult.
func (d DeleteResult) Summary() string {
	return fmt.Sprintf("%d deleted, %d already gone, %d need confirmation, %d confirmation refused, %d changed in DevPrism (not deleted)",
		d.Deleted, d.Gone, d.NeedsConfirmation, d.Rejected, d.Stale)
}

// errInvalidParams is JSON-RPC's invalid-params code, which DevPrism answers a
// missing block with (tools_career.rs:1076-1078).
const errInvalidParams = -32602

// ApplyDeletes deletes the plan's orphans in id order through
// career_delete_block, stopping at the first DevPrism error. A block with
// bullets or facts is answered with a confirmation request; with a Confirmer it
// goes through the same round trip as an upsert confirmation, with DevPrism's
// copy re-read first and required to be the one this plan was built from; with
// a nil Confirmer it is reported as needing confirmation and left. A block
// DevPrism no longer holds is "gone", not an error. The state forgets a block
// that is deleted, so a later pool that lists it again adds it afresh.
func ApplyDeletes(ctx context.Context, caller ToolCaller, rp ReplacePlan, state *SyncState, confirmer Confirmer) (res DeleteResult, err error) {
	if state == nil {
		return res, fmt.Errorf("%w: ApplyDeletes needs a state", ErrState)
	}
	defer func() {
		if ferr := state.flush(); ferr != nil {
			err = errors.Join(err, ferr)
		}
	}()
	add := func(o DeleteOutcome) {
		res.Outcomes = append(res.Outcomes, o)
		switch o.Status {
		case StatusDeleted:
			res.Deleted++
			if _, had := state.Blocks[o.ID]; had {
				delete(state.Blocks, o.ID)
				delete(state.Synced, o.ID)
				state.unsaved++
			}
		case StatusGone:
			res.Gone++
		case StatusNeedsConfirmation:
			res.NeedsConfirmation++
		case StatusConfirmRejected:
			res.Rejected++
		case StatusStale:
			res.Stale++
		}
	}
	for _, d := range rp.Delete {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		id, merr := json.Marshal(d.ID)
		if merr != nil {
			return res, merr
		}
		first := map[string]json.RawMessage{"block_id": id}
		raw, cerr := caller.CallTool(ctx, "career_delete_block", first)
		var te *resume.ToolError
		if errors.As(cerr, &te) && te.Code == errInvalidParams && strings.Contains(strings.ToLower(te.Message), "not found") {
			add(DeleteOutcome{d.ID, StatusGone, "DevPrism no longer holds it"})
			continue
		}
		if cerr != nil {
			return res, fmt.Errorf("kbsync: career_delete_block %s: %w", d.ID, cerr)
		}
		var ans struct {
			ResultType     string `json:"resultType"`
			RequestState   string `json:"requestState"`
			Success        *bool  `json:"success"`
			DeletedBlockID string `json:"deletedBlockId"`
		}
		if err := json.Unmarshal(raw, &ans); err != nil {
			return res, fmt.Errorf("%w: career_delete_block %s: answer is not an object", ErrDevPrism, d.ID)
		}
		switch {
		case ans.ResultType == "inputRequired" && confirmer != nil:
			c, err := confirmCall{
				tool: "career_delete_block", id: d.ID, prior: d.Prior, first: first,
				decode: decodeDeleteState(d.ID),
				doneID: func(_, deleted string) string { return deleted },
			}.run(ctx, caller, ans.RequestState, confirmer)
			if err != nil {
				return res, err
			}
			if c.status == StatusConfirmed {
				add(DeleteOutcome{d.ID, StatusDeleted, ""})
			} else {
				add(DeleteOutcome{d.ID, c.status, c.reason})
			}
		case ans.ResultType == "inputRequired":
			add(DeleteOutcome{d.ID, StatusNeedsConfirmation, "DevPrism asked for confirmation"})
		case ans.Success != nil && *ans.Success && ans.DeletedBlockID == d.ID:
			add(DeleteOutcome{d.ID, StatusDeleted, ""})
		default:
			return res, fmt.Errorf("%w: career_delete_block %s: unexpected answer %s", ErrDevPrism, d.ID, truncate(string(raw), 200))
		}
	}
	return res, nil
}
