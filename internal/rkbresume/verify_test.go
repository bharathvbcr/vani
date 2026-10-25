package rkbresume

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A stored version is vouched for only while rkb still double-verifies the
// figures of its bullets for the sentences that version uses. The pool's own
// export shows no claim verified (it uses no selection), so PoolAndVerify exports
// each stored choice, as the build that made it did.

func TestPoolAndVerifyNamesTheBulletsWhoseFigureNoLongerVerifies(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()} // s1 and a1 use clmLat; a2 uses clmThr, not on this page
	built := mustBuild(t, f)
	f.rkb.steps, f.rkb.calls = nil, nil

	// Nothing has moved: the choice verifies.
	view, vs, err := PoolAndVerify(context.Background(), f.cfg, [][]byte{built.SelectionJSON})
	if err != nil || len(vs) != 1 || vs[0].Err != "" || len(vs[0].Unverified) != 0 {
		t.Fatalf("PoolAndVerify = %+v, %v", vs, err)
	}
	if len(view.Entries) == 0 || view.Fingerprints.Bank == "" {
		t.Fatalf("the pool is missing: %+v", view)
	}
	if got := strings.Join(f.rkb.steps, " "); got != "claims-plain claims-used" {
		t.Fatalf("steps = %q: one export for the pool and one for the choice, nothing rendered or fitted", got)
	}

	// rkb now has a reason against clmLat for this choice (a source file changed, a pass
	// was voided): the bullets that print it are named, in page order.
	f.rkb.unverified = map[string][]string{"clmLat": nil}
	_, vs, err = PoolAndVerify(context.Background(), f.cfg, [][]byte{built.SelectionJSON})
	if err != nil || vs[0].Err != "" || strings.Join(vs[0].Unverified, ",") != "s1,a1" {
		t.Fatalf("PoolAndVerify = %+v, %v; want s1,a1", vs, err)
	}

	// A figure that only matters on another page does not touch this one.
	f.rkb.unverified = map[string][]string{"clmThr": nil}
	_, vs, _ = PoolAndVerify(context.Background(), f.cfg, [][]byte{built.SelectionJSON})
	if len(vs[0].Unverified) != 0 {
		t.Fatalf("clmThr is not on this page: %+v", vs[0])
	}
}

// The pool's plain export reports every claim as not double-verified, because it
// uses no sentence; reading that as evidence would make every version stale. Only
// the per-choice export decides.
func TestThePlainExportsFlagIsNeverReadAsEvidence(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	built := mustBuild(t, f)
	_, vs, err := PoolAndVerify(context.Background(), f.cfg, [][]byte{built.SelectionJSON})
	if err != nil || len(vs[0].Unverified) != 0 {
		t.Fatalf("PoolAndVerify = %+v, %v", vs, err)
	}
}

func TestPoolAndVerifyVerifiesEachChoiceOnItsOwn(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	a := mustBuild(t, f)
	f.provider.replies, f.provider.reqs = []string{replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a2"]},{"entry":"exp-b","bullets":["b1"]}]}`)}, nil
	b := mustBuild(t, f)
	f.rkb.steps = nil
	// clmThr is used by a2, which only the second choice has.
	f.rkb.unverified = map[string][]string{"clmThr": nil}
	_, vs, err := PoolAndVerify(context.Background(), f.cfg, [][]byte{a.SelectionJSON, b.SelectionJSON})
	if err != nil || len(vs) != 2 {
		t.Fatalf("PoolAndVerify = %+v, %v", vs, err)
	}
	if len(vs[0].Unverified) != 0 || strings.Join(vs[1].Unverified, ",") != "a2" {
		t.Fatalf("verdicts = %+v", vs)
	}
	if n := f.rkb.count("claims-used"); n != 2 {
		t.Fatalf("%d used-in exports for two choices", n)
	}
}

// A choice the live bank cannot place is its own verdict: no rkb run for it, and an
// Err that is never read as "verified".
func TestPoolAndVerifyReportsAChoiceItCannotReadOrPlaceWithoutRunningRkbForIt(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	built := mustBuild(t, f)
	f.rkb.steps = nil
	editJSON(t, f.bank, func(m map[string]any) {
		delete(m["bullets"].(map[string]any)["a3"].(map[string]any), "review") // a3 is no longer eligible
	})
	for name, edit := range map[string]string{
		"a bullet no longer offered": string(built.SelectionJSON),
		"not json":                   `{`,
		"empty":                      ``,
		"no bullet on the page":      `{"sections":[]}`,
		"an unknown section":         `{"sections":[{"title":"Nowhere","bullets":["s1"]}]}`,
	} {
		_, vs, err := PoolAndVerify(context.Background(), f.cfg, [][]byte{[]byte(edit)})
		if err != nil || len(vs) != 1 || vs[0].Err == "" || len(vs[0].Unverified) != 0 {
			t.Errorf("%s: %+v, %v", name, vs, err)
		}
		if strings.Contains(vs[0].Err, ownerName) || strings.Contains(vs[0].Err, ownerContact) {
			t.Errorf("%s: the verdict carries the header: %s", name, vs[0].Err)
		}
	}
	if n := f.rkb.count("claims-used"); n != 0 {
		t.Fatalf("%d used-in exports for choices that could not be placed", n)
	}
}

// rkb failing for one choice is that choice's Err; the context ending is the
// call's own error, never a half answer.
func TestPoolAndVerifyReportsRkbFailingPerChoiceAndPropagatesCancellation(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	built := mustBuild(t, f)
	f.rkb.steps = nil
	f.rkb.onStep = func(step string, c Command) {
		if step == "claims-used" {
			f.rkb.runErr = map[string]error{"claims-used": context.DeadlineExceeded}
		}
	}
	_, vs, err := PoolAndVerify(context.Background(), f.cfg, [][]byte{built.SelectionJSON})
	if err != nil || len(vs) != 1 || vs[0].Err == "" {
		t.Fatalf("a failed export: %+v, %v", vs, err)
	}

	f2 := newFixture(t)
	f2.provider.replies = []string{validReply()}
	built2 := mustBuild(t, f2)
	f2.rkb.block = "claims-used"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, vs2, err := PoolAndVerify(ctx, f2.cfg, [][]byte{built2.SelectionJSON, built2.SelectionJSON})
	wantCode(t, err, CodeTimeout)
	if vs2 != nil {
		t.Fatalf("a timed-out call returned verdicts: %+v", vs2)
	}
}

func TestPoolAndVerifyIsBounded(t *testing.T) {
	f := newFixture(t)
	edits := make([][]byte, maxVerify+1)
	_, _, err := PoolAndVerify(context.Background(), f.cfg, edits)
	wantCode(t, err, CodeConfig)
	if len(f.rkb.calls) != 0 {
		t.Fatalf("rkb ran for an over-sized request")
	}
	// None is a pool alone, as Pool gives.
	view, vs, err := PoolAndVerify(context.Background(), f.cfg, nil)
	if err != nil || len(vs) != 0 || len(view.Entries) == 0 {
		t.Fatalf("no choices: %+v %+v %v", view, vs, err)
	}
}
