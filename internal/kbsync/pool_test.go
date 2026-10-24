package kbsync

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// poolBank: "proj" lists a reviewed bullet, a CAUTION bullet, an unreviewed one
// and a cleared CAUTION one; "clean" is all reviewed; "other" is reviewed but no
// selection lists it; "edu" is a degree with no bullets; "bare" has only an
// unreviewed bullet.
func poolBank(t *testing.T) (Bank, Claims) {
	t.Helper()
	b, c := eligBank(t)
	b.Entries["clean"] = Entry{Title: "Clean", Org: "Acme", Dates: "JAN 2020 – Present", Stack: "Go", Bullets: []string{"lonely", "good"}}
	b.Entries["edu"] = Entry{Title: "Master of Science in Things", Org: "Example University", Dates: "AUG 2018 – MAY 2020"}
	b.Entries["bare"] = Entry{Title: "Bare", Bullets: []string{"unreviewed"}}
	b.Entries["unlisted-edu"] = Entry{Title: "Bachelor of Technology in Things", Org: "Example University", Dates: "AUG 2014 – MAY 2018"}
	return b, c
}

func poolSelection() Selection {
	return sel(
		SelEntry{Entry: "proj", Bullets: []string{"good", "rag", "unreviewed", "cleared"}},
		SelEntry{Entry: "clean", Bullets: []string{"lonely", "good"}},
		SelEntry{Entry: "edu"},
		SelEntry{Entry: "bare", Bullets: []string{"unreviewed"}},
		SelEntry{Entry: "ghost", Bullets: []string{"good"}},
	)
}

func blockByID(r Resolved, id string) (Block, bool) {
	for _, b := range r.Blocks {
		if b.ID == id {
			return b, true
		}
	}
	return Block{}, false
}

func bulletIDs(b Block) string {
	var ids []string
	for _, x := range b.Bullets {
		ids = append(ids, x.RkbID)
	}
	return strings.Join(ids, ",")
}

// Today's whole-bank resolve sends every entry and every bullet, and reports the
// CAUTION claim as used. The pool keeps to what a selection lists and the rule
// passes.
func TestResolvePoolKeepsOnlyApprovedBullets(t *testing.T) {
	b, c := poolBank(t)
	whole := ResolveBank(b, c)
	if _, ok := blockByID(whole, "other"); !ok || len(whole.Cautioned) == 0 {
		t.Fatalf("whole-bank baseline changed: other present=%v cautioned=%v", ok, whole.Cautioned)
	}
	p, err := ResolvePool(b, c, []Selection{poolSelection()})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, blk := range p.Resolved.Blocks {
		ids = append(ids, blk.ID)
	}
	if got := strings.Join(ids, ","); got != "clean,edu,proj" {
		t.Fatalf("blocks %q, want clean,edu,proj: other and unlisted-edu are in no selection, bare has no eligible bullet, ghost is not in the bank", got)
	}
	proj, _ := blockByID(p.Resolved, "proj")
	if got := bulletIDs(proj); got != "good,cleared" {
		t.Fatalf("proj bullets %q, want good,cleared (rag is CAUTION, unreviewed never passed)", got)
	}
	edu, _ := blockByID(p.Resolved, "edu")
	if edu.Kind != KindEducation || len(edu.Bullets) != 0 || edu.Blocked != "" {
		t.Fatalf("a listed degree syncs bullet-less: %+v", edu)
	}
	if len(p.Resolved.Cautioned) != 0 {
		t.Fatalf("an eligible-only pool must not use an uncleared CAUTION claim: %v", p.Resolved.Cautioned)
	}
	if p.Resolved.Unattached != 0 {
		t.Fatalf("Unattached = %d: against a cut bank it would count the withheld bank", p.Resolved.Unattached)
	}
	plan := Plan(p.Resolved, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	for _, it := range plan.Items {
		if strings.Contains(string(it.Payload), "Built retrieval at 92% recall.") || strings.Contains(string(it.Payload), "Built W") {
			t.Fatalf("%s payload carries a withheld bullet: %s", it.ID, it.Payload)
		}
	}
	if len(plan.Cautioned) != 0 {
		t.Fatalf("plan.Cautioned = %v", plan.Cautioned)
	}
	want := map[string]string{"bare": "no bullet of it is eligible", "ghost": "not in the bank"}
	for _, w := range p.Withheld {
		if !strings.Contains(w.Why, want[w.Entry]) || want[w.Entry] == "" {
			t.Errorf("withheld %+v", w)
		}
		delete(want, w.Entry)
	}
	if len(want) != 0 {
		t.Errorf("not reported withheld: %v", want)
	}
}

// A block whose bullets are all eligible hashes exactly as the whole-bank
// resolve hashed it, so a pool sync does not re-send what DevPrism holds.
func TestResolvePoolDoesNotChurnUnchangedBlocks(t *testing.T) {
	b, c := poolBank(t)
	p, err := ResolvePool(b, c, []Selection{poolSelection()})
	if err != nil {
		t.Fatal(err)
	}
	whole := ResolveBank(b, c)
	for _, id := range []string{"clean", "edu"} {
		got, _ := blockByID(p.Resolved, id)
		want, _ := blockByID(whole, id)
		if got.Hash == "" || got.Hash != want.Hash {
			t.Errorf("%s hash %q, whole-bank %q: an all-eligible block must not churn", id, got.Hash, want.Hash)
		}
	}
	proj, _ := blockByID(p.Resolved, "proj")
	wholeProj, _ := blockByID(whole, "proj")
	if proj.Hash == wholeProj.Hash {
		t.Error("proj lost bullets to the rule, so its hash must change")
	}
}

func TestResolvePoolRefusesNoSelection(t *testing.T) {
	b, c := poolBank(t)
	for _, sels := range [][]Selection{nil, {}} {
		if _, err := ResolvePool(b, c, sels); !errors.Is(err, ErrNoSelection) {
			t.Fatalf("err = %v, want ErrNoSelection", err)
		}
	}
}

// A selection with nothing in it approves nothing: it is not read as "all".
func TestResolvePoolEmptySelectionApprovesNothing(t *testing.T) {
	b, c := poolBank(t)
	p, err := ResolvePool(b, c, []Selection{{Sections: []SelSection{{Kind: "lines"}, {Kind: "entries"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Resolved.Blocks) != 0 {
		t.Fatalf("an empty selection yielded %d blocks", len(p.Resolved.Blocks))
	}
}

// An entry that two selections list contributes the union of eligible bullets,
// each bound to the entry it was selected under.
func TestResolvePoolBindsBulletsToTheirEntry(t *testing.T) {
	b, c := poolBank(t)
	p, err := ResolvePool(b, c, []Selection{
		sel(SelEntry{Entry: "clean", Bullets: []string{"lonely"}}),
		sel(SelEntry{Entry: "proj", Bullets: []string{"good"}}, SelEntry{Entry: "clean", Bullets: []string{"good"}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	clean, _ := blockByID(p.Resolved, "clean")
	proj, _ := blockByID(p.Resolved, "proj")
	if bulletIDs(clean) != "lonely,good" || bulletIDs(proj) != "good" {
		t.Fatalf("clean=%q proj=%q", bulletIDs(clean), bulletIDs(proj))
	}
}

// A bullet rkb sent earlier and no longer lists (superseded wording, or one the
// rule now withholds) must not survive an update in DevPrism; DevPrism's own
// bullets still do.
func TestBuildUpdateDropsRetiredRkbBullets(t *testing.T) {
	b := Block{ID: "alpha-job", Kind: KindWork, Title: "Engineer", Org: "Acme", Start: "2020-01",
		Bullets: []BlockBullet{{ID: "alpha-job/b1", RkbID: "b1", Canonical: "kept"}}}
	existing := json.RawMessage(`{"id":"alpha-job","kind":"work","title":"Engineer","org":"Acme","dateRange":{"start":"2020-01","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
	  "bullets":[
	   {"id":"alpha-job/b1","canonical":"old","variants":{},"metrics":[],"evidenceRefs":[],"locked":false},
	   {"id":"alpha-job/retired","canonical":"superseded wording","variants":{},"metrics":[],"evidenceRefs":[],"locked":false},
	   {"id":"typed-in-devprism","canonical":"the owner wrote this","variants":{},"metrics":[],"evidenceRefs":[],"locked":false}]}`)
	raw, dropped, why := buildUpdate(b, existing, map[string]bool{"b1": true}, fixedNow)
	if why != "" {
		t.Fatal(why)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want the one retired bullet", dropped)
	}
	var got struct {
		Bullets []struct {
			ID string `json:"id"`
		} `json:"bullets"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range got.Bullets {
		ids = append(ids, x.ID)
	}
	if strings.Join(ids, ",") != "alpha-job/b1,typed-in-devprism" {
		t.Fatalf("bullets after update %v, want alpha-job/b1 and the owner's own bullet only", ids)
	}
}
