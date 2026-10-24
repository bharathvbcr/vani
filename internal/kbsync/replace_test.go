package kbsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// deleteFake adds career_delete_block to the shared fake, as DevPrism answers it
// (tools_career.rs:984-1131): a block with bullets or facts is first answered
// with inputRequired, any other is deleted at once, a missing one is
// invalid_params "not found".
type deleteFake struct {
	*fakeDevPrism
	// rejects names ids whose confirming call is refused with -32002.
	rejects map[string]bool
	// down names an id whose delete call fails as if DevPrism died.
	down string
	// afterAsk runs (lock held) after DevPrism asked about id.
	afterAsk func(f *fakeDevPrism, id string)
	deleted  []string
}

func (d *deleteFake) CallTool(ctx context.Context, name string, args any) (json.RawMessage, error) {
	if name != "career_delete_block" {
		return d.fakeDevPrism.CallTool(ctx, name, args)
	}
	f := d.fakeDevPrism
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	f.calls = append(f.calls, fakeCall{Tool: name, Args: raw})
	var in struct {
		BlockID      string  `json:"block_id"`
		RequestState *string `json:"request_state"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.BlockID == "" {
		return nil, errors.New("fake: bad delete arguments")
	}
	id := in.BlockID
	if d.down == id {
		return nil, errors.New("simulated transport failure")
	}
	blk, ok := f.blocks[id]
	if !ok {
		return nil, &resume.ToolError{Code: -32602, Message: "Block '" + id + "' not found"}
	}
	remove := func() (json.RawMessage, error) {
		delete(f.blocks, id)
		d.deleted = append(d.deleted, id)
		return json.Marshal(map[string]any{"success": true, "deletedBlockId": id})
	}
	if in.RequestState != nil {
		if d.rejects[id] {
			return nil, &resume.ToolError{Code: -32002, Message: "requestState was not issued by this server, or has already been used"}
		}
		return remove()
	}
	if len(asList(blk["bullets"]))+len(asList(blk["facts"])) == 0 {
		return remove()
	}
	st := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"tool":"career_delete_block","blockId":%q,"title":"t","org":"o","timestamp":1,"__nonce":"n"}`, id)))
	if d.afterAsk != nil {
		defer d.afterAsk(f, id)
	}
	return json.Marshal(map[string]any{"resultType": "inputRequired", "inputRequests": map[string]any{"confirm": map[string]any{"message": "sure?"}}, "requestState": st})
}

func (d *deleteFake) deleteCalls() int {
	n := 0
	for _, c := range d.calls {
		if c.Tool == "career_delete_block" {
			n++
		}
	}
	return n
}

func seedBlock(f *fakeDevPrism, t *testing.T, id, kind string, bullets int) {
	t.Helper()
	var bl []string
	for i := 0; i < bullets; i++ {
		bl = append(bl, fmt.Sprintf(`{"id":"%s/b%d","canonical":"text %d","variants":{},"metrics":[],"evidenceRefs":[],"locked":false}`, id, i, i))
	}
	f.seed(t, fmt.Sprintf(`{"id":%q,"kind":%q,"title":"Title of %s","org":"O","dateRange":{"start":"2020","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],"bullets":[%s]}`,
		id, kind, id, strings.Join(bl, ",")))
}

// replaceSetup: an rkb pool of the fixture's three blocks, and DevPrism holding
// old blocks the pool does not produce, of several kinds.
func replaceSetup(t *testing.T) (*deleteFake, SyncPlan, DevPrismState) {
	t.Helper()
	f := newFake()
	seedBlock(f, t, "exp_old1", "experience", 2)
	seedBlock(f, t, "pro_old2", "project", 0)
	seedBlock(f, t, "edu_old3", "education", 1)
	seedBlock(f, t, "pub_old4", "publication", 1)
	d := &deleteFake{fakeDevPrism: f}
	st, err := FetchState(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan(fixtureResolved(t), st, *NewSyncState(""), fixedNow)
	d.calls = nil
	return d, plan, st
}

func ids(ds []Doomed) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return strings.Join(out, ",")
}

func TestPlanReplaceDeletesOnlyOwnedKindOrphans(t *testing.T) {
	_, plan, st := replaceSetup(t)
	rp, err := PlanReplace(plan, st, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rp.Delete); got != "edu_old3,exp_old1,pro_old2" {
		t.Fatalf("delete %q", got)
	}
	if len(rp.Keep) != 1 || rp.Keep[0].ID != "pub_old4" || rp.Keep[0].Kind != "publication" || !strings.Contains(rp.Keep[0].Why, "--delete-all-kinds") {
		t.Fatalf("keep %+v: a publication has no rkb source to re-add it from", rp.Keep)
	}
	if d := rp.Delete[1]; d.Bullets != 2 || d.Title != "Title of exp_old1" {
		t.Fatalf("doomed %+v", d)
	}
	all, err := PlanReplace(plan, st, ReplaceOptions{AllKinds: true})
	if err != nil || len(all.Delete) != 4 || len(all.Keep) != 0 {
		t.Fatalf("AllKinds: %+v %v", all, err)
	}
	kept, err := PlanReplace(plan, st, ReplaceOptions{Keep: map[string]bool{"exp_old1": true}})
	if err != nil || ids(kept.Delete) != "edu_old3,pro_old2" || kept.Keep[0].Why != "named by --keep" {
		t.Fatalf("Keep: %+v %v", kept, err)
	}
	// The pool's own blocks are never orphans, so never deleted.
	for _, d := range rp.Delete {
		for _, it := range plan.Items {
			if it.ID == d.ID {
				t.Fatalf("%s is a pool block", d.ID)
			}
		}
	}
}

func TestPlanReplaceRefusesKeepThatIsNotAnOrphan(t *testing.T) {
	_, plan, st := replaceSetup(t)
	for _, id := range []string{"typo", "alpha-job"} { // unknown; and a pool block id
		if _, err := PlanReplace(plan, st, ReplaceOptions{Keep: map[string]bool{id: true}}); !errors.Is(err, ErrReplaceRefused) {
			t.Fatalf("--keep %q: err = %v", id, err)
		}
	}
}

func TestPlanReplaceSparesWhatItCannotRead(t *testing.T) {
	_, plan, st := replaceSetup(t)
	st.Blocks["junk"] = json.RawMessage(`"not an object"`)
	plan = Plan(fixtureResolved(t), st, *NewSyncState(""), fixedNow)
	rp, err := PlanReplace(plan, st, ReplaceOptions{AllKinds: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ids(rp.Delete), "junk") || len(rp.Keep) != 1 || rp.Keep[0].ID != "junk" {
		t.Fatalf("an unreadable block must be spared: %+v", rp)
	}
}

func TestReplaceGuards(t *testing.T) {
	_, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	if err := rp.Check(plan, 3); err != nil {
		t.Fatalf("3 deletes at cap 3 must pass: %v", err)
	}
	if err := rp.Check(plan, 2); !errors.Is(err, ErrReplaceRefused) || !strings.Contains(err.Error(), "over the cap of 2") {
		t.Fatalf("over the cap: %v", err)
	}
	for _, max := range []int{0, -1, MaxDeletesCeiling + 1} {
		if err := rp.Check(plan, max); !errors.Is(err, ErrReplaceRefused) {
			t.Fatalf("cap %d: %v", max, err)
		}
	}
	// An empty pool: every block would be an orphan and none would be added.
	empty := Plan(Resolved{}, st, *NewSyncState(""), fixedNow)
	all, _ := PlanReplace(empty, st, ReplaceOptions{AllKinds: true})
	if len(all.Delete) != 4 {
		t.Fatalf("an empty pool orphans everything: %d", len(all.Delete))
	}
	if err := all.Check(empty, MaxDeletesCeiling); !errors.Is(err, ErrReplaceRefused) || !strings.Contains(err.Error(), "no block to sync") {
		t.Fatalf("an empty pool must never wipe DevPrism: %v", err)
	}
	// A pool of nothing but blocked or variant items syncs nothing either.
	blocked := SyncPlan{Items: []Item{{ID: "x", Action: ActionBlocked}, {ID: "y", Action: ActionVariant}}, Orphaned: plan.Orphaned}
	if err := all.Check(blocked, MaxDeletesCeiling); !errors.Is(err, ErrReplaceRefused) {
		t.Fatalf("blocked-only pool: %v", err)
	}
	// Nothing to delete is fine whatever the pool.
	if err := (ReplacePlan{}).Check(empty, 1); err != nil {
		t.Fatal(err)
	}
}

func TestLandedRefusesWhileAPoolBlockIsOutstanding(t *testing.T) {
	if err := (Result{Added: 3, Updated: 1, Unchanged: 2, Confirmed: 1, Variants: 1}).Landed(); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Result{{Blocked: 1}, {NeedsConfirmation: 1}, {ConfirmRejected: 1}, {Stale: 1}} {
		if err := r.Landed(); !errors.Is(err, ErrReplaceRefused) {
			t.Fatalf("%+v: %v", r, err)
		}
	}
}

func confirmAll(string, Loss) bool { return true }

func TestApplyDeletesDeletesOnlyThePlannedOrphans(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	state := NewSyncState(t.TempDir() + "/state.json")
	state.record("edu_old3", "h", fixedNow) // a stale record for a block about to be deleted
	res, err := ApplyDeletes(context.Background(), d, rp, state, confirmAll)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 3 || strings.Join(d.deleted, ",") != "edu_old3,exp_old1,pro_old2" {
		t.Fatalf("deleted %v, result %+v", d.deleted, res)
	}
	if _, ok := d.blocks["pub_old4"]; !ok {
		t.Fatal("the spared publication was deleted")
	}
	if _, ok := state.Blocks["edu_old3"]; ok {
		t.Fatal("state still remembers a deleted block")
	}
	for _, c := range d.calls {
		if c.Tool != "career_delete_block" && c.Tool != "career_get_profile" { // the profile read is the stale check
			t.Fatalf("ApplyDeletes called %s", c.Tool)
		}
	}
	// pro_old2 had no bullets: DevPrism deleted it without asking, so exactly
	// one call; the other two took a request and a confirmation.
	if d.deleteCalls() != 5 {
		t.Fatalf("%d delete calls, want 5 (2+2+1)", d.deleteCalls())
	}
}

func TestApplyDeletesWithoutConfirmerLeavesBlocksWithBullets(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	res, err := ApplyDeletes(context.Background(), d, rp, NewSyncState(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 1 || res.NeedsConfirmation != 2 || strings.Join(d.deleted, ",") != "pro_old2" {
		t.Fatalf("deleted %v, result %+v", d.deleted, res)
	}
	for _, c := range d.calls {
		if strings.Contains(string(c.Args), "request_state") {
			t.Fatal("a confirmation was sent without a Confirmer")
		}
	}
}

func TestApplyDeletesConfirmationRefusedContinues(t *testing.T) {
	d, plan, st := replaceSetup(t)
	d.rejects = map[string]bool{"edu_old3": true}
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	res, err := ApplyDeletes(context.Background(), d, rp, NewSyncState(""), confirmAll)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 1 || res.Deleted != 2 || strings.Join(d.deleted, ",") != "exp_old1,pro_old2" {
		t.Fatalf("deleted %v, result %+v", d.deleted, res)
	}
	if _, ok := d.blocks["edu_old3"]; !ok {
		t.Fatal("a refused deletion removed the block")
	}
}

func TestApplyDeletesStopsWhenDevPrismGoesDown(t *testing.T) {
	d, plan, st := replaceSetup(t)
	d.down = "exp_old1"
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	state := NewSyncState(t.TempDir() + "/state.json")
	res, err := ApplyDeletes(context.Background(), d, rp, state, confirmAll)
	if err == nil || !strings.Contains(err.Error(), "exp_old1") {
		t.Fatalf("err = %v", err)
	}
	if res.Deleted != 1 || strings.Join(d.deleted, ",") != "edu_old3" {
		t.Fatalf("deleted %v, result %+v: the run must stop at the first error, keeping what was done", d.deleted, res)
	}
	if _, ok := d.blocks["pro_old2"]; !ok {
		t.Fatal("a block after the failure was deleted")
	}
}

// A block that changed between the plan and the confirmation is not deleted:
// DevPrism would drop more than the owner saw listed.
func TestApplyDeletesStaleBlockIsNotConfirmed(t *testing.T) {
	d, plan, st := replaceSetup(t)
	d.afterAsk = func(f *fakeDevPrism, id string) {
		if id == "exp_old1" {
			f.blocks[id]["title"] = "edited in DevPrism meanwhile"
		}
	}
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	res, err := ApplyDeletes(context.Background(), d, rp, NewSyncState(""), confirmAll)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stale != 1 || res.Deleted != 2 {
		t.Fatalf("result %+v", res)
	}
	if _, ok := d.blocks["exp_old1"]; !ok {
		t.Fatal("a block that changed under the run was deleted")
	}
	for _, c := range d.calls {
		if c.Tool == "career_delete_block" && strings.Contains(string(c.Args), "exp_old1") && strings.Contains(string(c.Args), "request_state") {
			t.Fatal("a confirmation was sent for a stale block")
		}
	}
}

// A block already gone is not an error: the owner (or a second run) removed it.
func TestApplyDeletesGoneBlockIsNotAnError(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	delete(d.blocks, "exp_old1")
	res, err := ApplyDeletes(context.Background(), d, rp, NewSyncState(""), confirmAll)
	if err != nil {
		t.Fatal(err)
	}
	if res.Gone != 1 || res.Deleted != 2 {
		t.Fatalf("result %+v", res)
	}
}

// A requestState for another block, or another tool, is never confirmed.
func TestDeleteStateMustNameTheBlock(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for name, state := range map[string]string{
		"empty":      "",
		"not base64": "***",
		"not json":   enc("nope"),
		"other tool": enc(`{"tool":"career_upsert_block","blockId":"x"}`),
		"other id":   enc(`{"tool":"career_delete_block","blockId":"y"}`),
		"no id":      enc(`{"tool":"career_delete_block"}`),
		"too long":   strings.Repeat("A", maxRequestStateChars+4),
	} {
		if _, err := decodeDeleteState("x")(state, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := decodeDeleteState("x")(enc(`{"tool":"career_delete_block","blockId":"x"}`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestApplyDeletesHonoursContext(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ApplyDeletes(ctx, d, rp, NewSyncState(""), confirmAll); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if d.deleteCalls() != 0 {
		t.Fatal("a cancelled run deleted")
	}
}

func TestDryRunReplaceCallsNothingAndListsKinds(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	var out strings.Builder
	if err := DryRunReplace(&out, plan, rp); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 to delete, 1 spared", `delete    exp_old1 [experience] "Title of exp_old1" (2 bullets, 0 facts)`, "spared    pub_old4 [publication]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("a dry run called %v", d.calls)
	}
}
