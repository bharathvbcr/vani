package kbsync

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// staleVariantFixture is a pool in which clean-v2 is an rkb variant of clean,
// and a DevPrism that holds a copy of clean-v2 from before hashVersion 3.
func staleVariantFixture(t *testing.T) (Pool, DevPrismState) {
	t.Helper()
	b, c := poolBank(t)
	b.Entries["clean-v2"] = Entry{Title: "Clean (variant)", Org: "Acme", Dates: "JAN 2020 – Present", Stack: "Go", Origin: []string{"clean"}, Bullets: []string{"good"}}
	p, err := ResolvePool(b, c, []Selection{sel(
		SelEntry{Entry: "clean", Bullets: []string{"lonely", "good"}},
		SelEntry{Entry: "clean-v2", Bullets: []string{"good"}},
	)})
	if err != nil {
		t.Fatal(err)
	}
	return p, rtState(t, rtBlockJSON("clean-v2", "work", [2]string{"clean-v2/old-superseded", "old wording"}))
}

// A plain sync never deletes, but reports the stale variant block DevPrism
// holds: as an orphan, and with a reason on the variant item.
func TestPlanReportsAStaleVariantBlockAsOrphaned(t *testing.T) {
	p, st := staleVariantFixture(t)
	plan := Plan(p.Resolved, st, *NewSyncState(""), fixedNow)
	if len(plan.Orphaned) != 1 || plan.Orphaned[0] != "clean-v2" {
		t.Fatalf("orphaned = %v, want the held variant block", plan.Orphaned)
	}
	var reason string
	for _, it := range plan.Items {
		if it.ID == "clean-v2" {
			if it.Action != ActionVariant {
				t.Fatalf("variant item action = %q", it.Action)
			}
			reason = it.Reason
		}
	}
	if !strings.Contains(reason, "DevPrism holds an older copy") {
		t.Fatalf("variant reason %q does not say DevPrism holds a copy", reason)
	}
	var out bytes.Buffer
	if err := DryRun(&out, plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "orphaned  clean-v2") || !strings.Contains(out.String(), "older copy") {
		t.Fatalf("a plain dry run does not report the stale variant:\n%s", out.String())
	}
}

// A variant DevPrism does not hold is no orphan, and one it holds is deleted by
// --replace by default (kind work) or spared when named by --keep.
func TestReplaceDeletesOrSparesAStaleVariantBlock(t *testing.T) {
	p, st := staleVariantFixture(t)
	plan := Plan(p.Resolved, st, *NewSyncState(""), fixedNow)

	rp, err := PlanReplace(plan, st, ReplaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ids(rp.Delete) != "clean-v2" || len(rp.Keep) != 0 {
		t.Fatalf("delete=%q keep=%v, want clean-v2 deleted", ids(rp.Delete), rp.Keep)
	}
	var out bytes.Buffer
	if err := DryRunReplace(&out, plan, rp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "delete    clean-v2") || !strings.Contains(out.String(), "1 to delete") {
		t.Fatalf("the --replace dry run does not list the variant deletion:\n%s", out.String())
	}

	rp, err = PlanReplace(plan, st, ReplaceOptions{Keep: map[string]bool{"clean-v2": true}})
	if err != nil || len(rp.Delete) != 0 || len(rp.Keep) != 1 || rp.Keep[0].Why != "named by --keep" {
		t.Fatalf("--keep clean-v2: %+v %v", rp, err)
	}

	none := Plan(p.Resolved, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	if len(none.Orphaned) != 0 {
		t.Fatalf("a variant DevPrism does not hold is orphaned: %v", none.Orphaned)
	}
}

// Bullets a hashVersion 1 sync stored under bare rkb ids go when the pool
// withholds them or no longer lists them for the entry; one it still lists is
// adopted under its scoped id; the owner's own bullet stays. The dry run says
// how many an update drops.
func TestUpdateDropsLegacyBareIDBulletsAndSaysSo(t *testing.T) {
	b, c := poolBank(t)
	p, err := ResolvePool(b, c, []Selection{poolSelection()})
	if err != nil {
		t.Fatal(err)
	}
	st := rtState(t, rtBlockJSON("proj", "project",
		[2]string{"good", "old good"},
		[2]string{"rag", "Built retrieval at 92% recall."},
		[2]string{"unreviewed", "Built W alone."},
		[2]string{"typed-in-devprism", "the owner wrote this"},
	))
	plan := Plan(p.Resolved, st, *NewSyncState(""), fixedNow)
	var it Item
	for _, x := range plan.Items {
		if x.ID == "proj" {
			it = x
		}
	}
	if it.Action != ActionUpdate || it.Dropped != 2 {
		t.Fatalf("proj item = action %q dropped %d, want update dropping the two withheld bullets", it.Action, it.Dropped)
	}
	var got struct {
		Bullets []struct {
			ID string `json:"id"`
		} `json:"bullets"`
	}
	if err := json.Unmarshal(it.Payload, &got); err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, x := range got.Bullets {
		have = append(have, x.ID)
	}
	if strings.Join(have, ",") != "proj/good,proj/cleared,typed-in-devprism" {
		t.Fatalf("bullets after update = %v, want the pool's bullets (good adopted from its bare id) and the owner's own", have)
	}
	var out bytes.Buffer
	if err := DryRunReplace(&out, plan, ReplacePlan{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "drops 2 bullets") {
		t.Fatalf("the dry run does not say the update drops bullets:\n%s", out.String())
	}
}

// A plan with no bank ids (a hand-built Resolved) drops only scoped bullets: an
// unknown id is never presumed to be rkb's.
func TestBuildUpdateKeepsBareIDsItCannotAttributeToTheBank(t *testing.T) {
	b := Block{ID: "alpha-job", Kind: KindWork, Title: "Engineer", Org: "Acme", Start: "2020-01"}
	existing := json.RawMessage(`{"id":"alpha-job","kind":"work","title":"Engineer","org":"Acme","dateRange":{"start":"2020-01","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
	  "bullets":[{"id":"b9","canonical":"bare","variants":{},"metrics":[],"evidenceRefs":[],"locked":false}]}`)
	raw, dropped, why := buildUpdate(b, existing, nil, fixedNow)
	if why != "" || dropped != 0 || !strings.Contains(string(raw), `"id":"b9"`) {
		t.Fatalf("dropped=%d why=%q raw=%s", dropped, why, raw)
	}
}
