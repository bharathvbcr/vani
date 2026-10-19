package kbsync

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic: "shared" is listed by every entry, the way 14 real rkb bullets
// are listed by more than one entry.
func sharedBank(entries ...string) string {
	var b strings.Builder
	b.WriteString(`{"entries": {`)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + e + `": {"title": "Role ` + e + `", "bullets": ["shared", "own-` + e + `"]}`)
	}
	b.WriteString(`}, "bullets": {"shared": {"text": "Shipped the shared thing."}`)
	for _, e := range entries {
		b.WriteString(`, "own-` + e + `": {"text": "Did ` + e + ` work."}`)
	}
	b.WriteString(`}}`)
	return b.String()
}

func payloadBulletIDs(t *testing.T, payload []byte) []string {
	t.Helper()
	var blk struct {
		Bullets []struct {
			ID string `json:"id"`
		} `json:"bullets"`
	}
	if err := json.Unmarshal(payload, &blk); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range blk.Bullets {
		ids = append(ids, b.ID)
	}
	return ids
}

// Two blocks listing one rkb bullet must send DevPrism two bullet ids:
// DevPrism keys the bullet's embedding and its parent lookup by that id.
func TestSharedBulletGetsDistinctDevPrismIDs(t *testing.T) {
	r := ResolveBank(mustBank(t, sharedBank("one", "two")), Claims{})
	if len(r.Blocks) != 2 || r.Bullets != 4 {
		t.Fatalf("blocks=%d bullets=%d", len(r.Blocks), r.Bullets)
	}
	a, b := r.Blocks[0].Bullets[0], r.Blocks[1].Bullets[0]
	if a.RkbID != "shared" || b.RkbID != "shared" {
		t.Fatalf("rkb ids %q %q", a.RkbID, b.RkbID)
	}
	if a.ID == b.ID {
		t.Fatalf("both blocks send bullet id %q", a.ID)
	}
	if a.ID != "one/shared" || b.ID != "two/shared" {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}

	// The same holds on the wire, through Plan and Apply.
	f := newFake()
	_, res, err := applyAll(t, f, r, NewSyncState(filepath.Join(t.TempDir(), "s.json")))
	if err != nil || res.Added != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	seen := map[string]string{}
	for _, c := range f.upserts() {
		var a struct {
			Block json.RawMessage `json:"block"`
		}
		if err := json.Unmarshal(c.Args, &a); err != nil {
			t.Fatal(err)
		}
		for _, id := range payloadBulletIDs(t, a.Block) {
			if prev, dup := seen[id]; dup {
				t.Fatalf("bullet id %q sent in two blocks (%s and this one)", id, prev)
			}
			seen[id] = string(c.Args)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("distinct bullet ids sent: %d", len(seen))
	}
}

func TestBulletSharedByThreeEntries(t *testing.T) {
	r := ResolveBank(mustBank(t, sharedBank("a", "b", "c")), Claims{})
	ids := map[string]bool{}
	for _, blk := range r.Blocks {
		if blk.Blocked != "" {
			t.Fatalf("%s blocked: %s", blk.ID, blk.Blocked)
		}
		got := blk.Bullets[0]
		if got.ID != blk.ID+"/shared" || got.Canonical != "Shipped the shared thing." {
			t.Fatalf("block %s bullet %+v", blk.ID, got)
		}
		ids[got.ID] = true
	}
	if len(ids) != 3 || r.Bullets != 6 {
		t.Fatalf("ids=%v bullets=%d", ids, r.Bullets)
	}
}

// rkb ids may be MaxIDLen long; the scoped id is then 2*MaxIDLen+1, which
// DevPrism accepts (Bullet.id is an unbounded String; only the whole block is
// capped at 1 MiB).
func TestScopedIDAtLengthLimit(t *testing.T) {
	entry := strings.Repeat("e", MaxIDLen)
	bullet := strings.Repeat("b", MaxIDLen-2) + ".:"
	src := `{"entries": {"` + entry + `": {"title": "T", "bullets": ["` + bullet + `"]}}, "bullets": {"` + bullet + `": {"text": "x"}}}`
	r := ResolveBank(mustBank(t, src), Claims{})
	blk := r.Blocks[0]
	if blk.Blocked != "" {
		t.Fatalf("blocked: %s", blk.Blocked)
	}
	want := entry + "/" + bullet
	if got := blk.Bullets[0].ID; got != want || len(got) != 2*MaxIDLen+1 {
		t.Fatalf("id len %d", len(got))
	}
	p := Plan(r, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	if p.Items[0].Action != ActionAdd || len(p.Items[0].Payload) > maxPayloadBytes {
		t.Fatalf("item %+v", p.Items[0])
	}
	if ids := payloadBulletIDs(t, p.Items[0].Payload); len(ids) != 1 || ids[0] != want {
		t.Fatalf("payload ids %v", ids)
	}
	// One past the limit is still refused at the bank boundary.
	over := strings.Repeat("e", MaxIDLen+1)
	if _, err := Parse([]byte(`{"entries": {"` + over + `": {"title": "T", "bullets": []}}, "bullets": {}}`)); err == nil {
		t.Fatal("over-long entry id accepted")
	}
}

func TestScopedBulletIDRefusesAmbiguousParts(t *testing.T) {
	for _, c := range []struct{ entry, bullet string }{
		{"", "b1"},
		{"e1", ""},
		{"", ""},
		{"a/b", "c"}, // would collide with ("a", "b/c")
		{"a", "b/c"},
		{"/", "x"},
	} {
		if id, err := scopedBulletID(c.entry, c.bullet); err == nil {
			t.Errorf("(%q, %q) gave %q, want an error", c.entry, c.bullet, id)
		}
	}
	if id, err := scopedBulletID("e1", "b1"); err != nil || id != "e1/b1" {
		t.Fatalf("%q %v", id, err)
	}
}

// Load refuses empty ids, but ResolveBank is exported and a Bank can be built
// directly. An empty entry id must block the entry, never send "/b1".
func TestResolveBankBlocksEmptyEntryID(t *testing.T) {
	b := Bank{
		Entries: map[string]Entry{
			"":   {Title: "No id", Bullets: []string{"b1"}},
			"ok": {Title: "Fine", Bullets: []string{"b1"}},
		},
		Bullets: map[string]Bullet{"b1": {Text: "x"}},
	}
	r := ResolveBank(b, Claims{})
	byID := map[string]Block{}
	for _, blk := range r.Blocks {
		byID[blk.ID] = blk
	}
	if got := byID[""].Blocked; !strings.Contains(got, "entry has no id") || strings.Count(got, "entry has no id") != 1 {
		t.Fatalf("empty id block: %q", got)
	}
	if byID["ok"].Blocked != "" || byID["ok"].Bullets[0].ID != "ok/b1" {
		t.Fatalf("ok block: %+v", byID["ok"])
	}
	p := Plan(r, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	for _, it := range p.Items {
		if it.ID == "" && (it.Action != ActionBlocked || it.Payload != nil) {
			t.Fatalf("empty-id item %+v", it)
		}
	}
	// A slash in a directly built bank id is refused the same way.
	b.Entries = map[string]Entry{"x/y": {Title: "T", Bullets: []string{"b1"}}}
	if r := ResolveBank(b, Claims{}); !strings.Contains(r.Blocks[0].Blocked, "separator") {
		t.Fatalf("slash id: %q", r.Blocks[0].Blocked)
	}
}

// Every hash changes under hashVersion 2, so a state file written by the old
// mapping reads as "update", never "unchanged". The pinned values are the
// fixture's block hashes under hashVersion 1 with bare bullet ids, taken from
// the pre-change code. gamma-edu has no bullets, so only the version bump
// changes its hash.
func TestHashVersionBumpPlansUpdate(t *testing.T) {
	v1 := map[string]string{
		"alpha-job": "7bc6061fad3783f33e8031c999ea41ad69a7d0216d80344e99155fa46a7e290b",
		"beta-proj": "65b81d768b98a50d4f419644bf8e2040ab57ac5d9c8fd91f7d441a5a12420684",
		"gamma-edu": "cc7b0dcb72905a14d0993bdf5643becb3fba75c06601c3b7f17466110fba6dc8",
	}
	r := fixtureResolved(t)
	for _, b := range r.Blocks {
		if b.Hash == "" || b.Hash == v1[b.ID] {
			t.Fatalf("%s hash %q unchanged across the id change", b.ID, b.Hash)
		}
	}
	const v1AlphaJob = "7bc6061fad3783f33e8031c999ea41ad69a7d0216d80344e99155fa46a7e290b"
	f := newFake()
	f.seed(t, `{"id":"alpha-job","kind":"work","title":"Engineer","org":"Acme","dateRange":{"start":"JAN 2020","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],"bullets":[]}`)
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	st.Blocks["alpha-job"] = v1AlphaJob
	cur, err := FetchState(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range Plan(r, cur, *st, fixedNow).Items {
		if it.ID == "alpha-job" && it.Action != ActionUpdate {
			t.Fatalf("v1-synced block planned as %s", it.Action)
		}
	}
}

// A block synced before bullet ids were scoped holds bare rkb ids. The update
// adopts each such bullet under its scoped id, keeping what DevPrism owns on
// it, so text is not duplicated. DevPrism counts the vanished bare id as a
// dropped bullet and asks for confirmation; Apply reports that and never
// confirms, and no hash is recorded.
func TestLegacyBareIDsAdoptedAndNeverAutoConfirmed(t *testing.T) {
	legacy := `{"id":"alpha-job","kind":"work","title":"Engineer","org":"Acme","dateRange":{"start":"JAN 2020","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
	  "bullets":[{"id":"b1","canonical":"Cut latency by 42% using caching.","variants":{"ai":"mine"},"metrics":[{"value":"42%","kind":"metric"}],"evidenceRefs":["e1"],"locked":false},
	             {"id":"b2","canonical":"Wrote docs (https://example.invalid/d) for it.","variants":{},"metrics":[],"evidenceRefs":[],"locked":false},
	             {"id":"devprism-only","canonical":"keep","variants":{},"metrics":[],"evidenceRefs":[],"locked":false}]}`
	f := newFake()
	f.seed(t, legacy)
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	p, res, err := applyAll(t, f, fixtureResolved(t), st)
	if err != nil {
		t.Fatal(err)
	}
	var it Item
	for _, x := range p.Items {
		if x.ID == "alpha-job" {
			it = x
		}
	}
	if it.Action != ActionUpdate {
		t.Fatalf("action %s", it.Action)
	}
	var blk struct {
		Bullets []map[string]any `json:"bullets"`
	}
	if err := json.Unmarshal(it.Payload, &blk); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range blk.Bullets {
		ids = append(ids, b["id"].(string))
	}
	if strings.Join(ids, ",") != "alpha-job/b1,alpha-job/b2,devprism-only" {
		t.Fatalf("bullet ids %v (a bare id kept would duplicate the text)", ids)
	}
	if blk.Bullets[0]["variants"].(map[string]any)["ai"] != "mine" || blk.Bullets[0]["evidenceRefs"].([]any)[0] != "e1" {
		t.Fatalf("adopted bullet lost DevPrism fields: %v", blk.Bullets[0])
	}

	if res.NeedsConfirmation != 1 || res.Added != 2 || res.Updated != 0 {
		t.Fatalf("result %+v", res)
	}
	for _, c := range f.upserts() {
		if bytes.Contains(c.Args, []byte("request_state")) || bytes.Contains(c.Args, []byte("input_responses")) {
			t.Fatal("Apply tried to confirm")
		}
	}
	if _, ok := st.Blocks["alpha-job"]; ok {
		t.Fatal("hash recorded for an unconfirmed block")
	}
	if got := f.blocks["alpha-job"]["bullets"].([]any)[0].(map[string]any)["id"]; got != "b1" {
		t.Fatalf("DevPrism block changed without confirmation: first bullet id %v", got)
	}
	// The next run asks again; nothing is confirmed on the user's behalf.
	_, res2, err := applyAll(t, f, fixtureResolved(t), st)
	if err != nil || res2.NeedsConfirmation != 1 {
		t.Fatalf("second run: %v %+v", err, res2)
	}
}

// When the block already holds the scoped id, a stray bare id is left alone
// as a DevPrism-only bullet: nothing is renamed, so nothing is dropped.
func TestScopedIDWinsOverBareID(t *testing.T) {
	existing := `{"id":"beta-proj","kind":"project","title":"Widget","org":"","dateRange":{"start":"","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
	  "bullets":[{"id":"b3","canonical":"stray","variants":{},"metrics":[],"evidenceRefs":[],"locked":false},
	             {"id":"beta-proj/b3","canonical":"old","variants":{},"metrics":[],"evidenceRefs":[],"locked":false}]}`
	cur := DevPrismState{Blocks: map[string]json.RawMessage{"beta-proj": json.RawMessage(existing)}}
	for _, it := range Plan(fixtureResolved(t), cur, *NewSyncState(""), fixedNow).Items {
		if it.ID != "beta-proj" {
			continue
		}
		ids := payloadBulletIDs(t, it.Payload)
		if strings.Join(ids, ",") != "beta-proj/b3,b3" {
			t.Fatalf("ids %v", ids)
		}
		f := newFake()
		f.seed(t, existing)
		if overwriteLoses(f.blocks["beta-proj"], mustMap(t, it.Payload)) {
			t.Fatal("update would drop a bullet")
		}
		return
	}
	t.Fatal("no beta-proj item")
}
