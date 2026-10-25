package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// A version stays vouched for only while rkb still double-verifies the figures of
// its bullets. rkb judges that per choice, so show, use and revalidate export each
// version's own choice (rkbresume.PoolAndVerify) rather than read a flag off the
// pool's export, which shows no claim verified at all.

func TestShowSeesAFigureStopVerifyingAndUseRefusesIt(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	jrBuild(t) // v1: b2, b1; b1 prints clmLat

	el := func() map[string]any {
		vs := jrVersionsOf(t, jrShow(t, jrKey))
		e, _ := vs[0]["eligibility"].(map[string]any)
		return e
	}
	if e := el(); e["state"] != "current" || e["unverified"] != nil {
		t.Fatalf("a version rkb still verifies = %v", e)
	}

	f.unverified = true
	e := el()
	if e["state"] != "stale" || !reflect.DeepEqual(e["unverified"], []any{"b1"}) || !strings.Contains(e["why"].(string), "b1") {
		t.Fatalf("after clmLat stopped verifying = %v", e)
	}
	if e["reworded"] != nil || e["gone"] != nil || e["ineligible"] != nil {
		t.Errorf("a figure is not a wording or an eligibility fault: %v", e)
	}

	// use refuses it, naming the bullet and carrying the verdict.
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	r := jrFail(t, out)
	if r.Code != "stale" || !strings.Contains(r.Error, "1 unverified") || r.Eligibility == nil || !reflect.DeepEqual(r.Eligibility.Unverified, []string{"b1"}) {
		t.Fatalf("use = %+v", r)
	}
	// And the store has not bound it.
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("a stale version was bound: %v", err)
	}
}

func TestRevalidateDropsTheBulletsWhoseFigureStoppedVerifying(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	_, st := jrSetup(t, f, prov)
	jrBuild(t)
	f.unverified = true
	out, _ := jrRun(t, 0, "", "revalidate", "--json", jrKey, "v1")
	v2 := jrSaved(t, out)
	if v2.Version != 2 || v2.Source != "revalidated" || !reflect.DeepEqual(v2.DroppedStale, []string{"b1"}) || v2.Parent != 1 {
		t.Fatalf("revalidate = %+v dropped %v", v2.Meta, v2.DroppedStale)
	}
	got, _, err := st.Get(jrKey, 2)
	if err != nil || strings.Contains(string(got.Selection), `"b1"`) || !strings.Contains(string(got.Selection), `"b2"`) {
		t.Fatalf("v2's choice = %s, %v", got.Selection, err)
	}
	// The revalidated version has no figure left to doubt: it is current again.
	vs := jrVersionsOf(t, jrShow(t, jrKey))
	if e, _ := vs[1]["eligibility"].(map[string]any); e["state"] != "current" {
		t.Fatalf("v2 = %v", e)
	}
	if e, _ := vs[0]["eligibility"].(map[string]any); e["state"] != "stale" {
		t.Fatalf("v1 = %v", e)
	}
}

// A version whose figures could not be re-checked is unknown, never current: a
// check that did not run must not look like one that passed.
func TestAFigureCheckThatCouldNotRunIsUnknownAndUseRefusesIt(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	jrSetup(t, f, prov)
	jrBuild(t)
	f.usedErr = errors.New("exec: rkb is gone")
	vs := jrVersionsOf(t, jrShow(t, jrKey))
	e, _ := vs[0]["eligibility"].(map[string]any)
	if e["state"] != "unknown" || !strings.Contains(e["why"].(string), "could not be re-checked") {
		t.Fatalf("eligibility = %v", e)
	}
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "unchecked" {
		t.Fatalf("use = %+v", r)
	}
	out, _ = jrRun(t, 1, "", "revalidate", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "unchecked" {
		t.Fatalf("revalidate = %+v", r)
	}
}

// With no version there is nothing to export: show costs the pool alone.
func TestShowOfAJobWithNoVersionRunsNoPerVersionExport(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	m := jrShow(t, jrKey)
	if m["eligibility_checked"] != true || len(jrVersionsOf(t, m)) != 0 || m["pool"] == nil {
		t.Fatalf("show = %v", m)
	}
	if n := f.usedIn(); n != 0 {
		t.Fatalf("%d used-in exports for a job with no version", n)
	}
	// --no-check runs no rkb at all.
	before := len(f.args)
	jrShow(t, "--no-check", jrKey)
	if len(f.args) != before {
		t.Fatalf("--no-check ran rkb: %v", f.args[before:])
	}
}

func TestFoldVerificationIsPrecise(t *testing.T) {
	cur := resumeEligibility{State: eligCurrent}
	if got := foldVerification(cur, rkbresume.Verification{}); got.State != eligCurrent || got.Unverified != nil {
		t.Errorf("nothing found: %+v", got)
	}
	if got := foldVerification(cur, rkbresume.Verification{Unverified: []string{"a", "b"}}); got.State != eligStale || len(got.Unverified) != 2 || !strings.Contains(got.Why, "a, b") {
		t.Errorf("unverified: %+v", got)
	}
	if got := foldVerification(cur, rkbresume.Verification{Err: "rkb is gone"}); got.State != eligUnknown || got.Why == "" {
		t.Errorf("error: %+v", got)
	}
	// A version already stale keeps its own reason and still lists the figures; an
	// error does not change a stale verdict into an unknown one.
	stale := resumeEligibility{State: eligStale, Reworded: []string{"x"}, Why: "its own reason"}
	if got := foldVerification(stale, rkbresume.Verification{Err: "boom"}); got.State != eligStale || got.Why != "its own reason" {
		t.Errorf("stale + error: %+v", got)
	}
	got := foldVerification(stale, rkbresume.Verification{Unverified: []string{"y"}})
	if got.State != eligStale || got.Why != "its own reason" || !got.staleIDs()["y"] || !got.staleIDs()["x"] {
		t.Errorf("stale + unverified: %+v", got)
	}
	// An error of a long message stays one bounded line.
	long := foldVerification(cur, rkbresume.Verification{Err: strings.Repeat("x", 5000)})
	if len(long.Why) > 400 {
		t.Errorf("why is %d bytes", len(long.Why))
	}
}
