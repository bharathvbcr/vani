package kbsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- Load ----

func TestLoadValid(t *testing.T) {
	b, err := Load(writeTemp(t, "bank.json", fixtureBank))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entries) != 3 || len(b.Bullets) != 4 || b.Unattached() != 1 {
		t.Fatalf("entries=%d bullets=%d unattached=%d", len(b.Entries), len(b.Bullets), b.Unattached())
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"not json":         `{`,
		"array top":        `[]`,
		"trailing data":    `{"entries":{},"bullets":{}} {}`,
		"duplicate key":    `{"entries":{"a":{"title":"t"},"a":{"title":"u"}},"bullets":{}}`,
		"duplicate nested": `{"entries":{},"bullets":{"b":{"text":"x","text":"y"}}}`,
		"null bullet":      `{"entries":{},"bullets":{"b":null}}`,
		"null entry":       `{"entries":{"e":null},"bullets":{}}`,
		"dangling ref":     `{"entries":{"e":{"title":"t","bullets":["nope"]}},"bullets":{}}`,
		"repeated ref":     `{"entries":{"e":{"title":"t","bullets":["b","b"]}},"bullets":{"b":{"text":"x"}}}`,
		"bad entry id":     `{"entries":{"has space":{"title":"t"}},"bullets":{}}`,
		"empty bullet id":  `{"entries":{},"bullets":{"":{"text":"x"}}}`,
		"long id":          `{"entries":{"` + strings.Repeat("a", MaxIDLen+1) + `":{"title":"t"}},"bullets":{}}`,
		"wrong type":       `{"entries":{"e":{"title":5}},"bullets":{}}`,
		"bullets as list":  `{"entries":{"e":{"title":"t","bullets":"b"}},"bullets":{}}`,
		"empty file":       ``,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatalf("Parse accepted %q", src)
			}
			if !errors.Is(err, ErrBank) {
				t.Fatalf("error does not wrap ErrBank: %v", err)
			}
		})
	}
}

func TestLoadSizeCapAndMissing(t *testing.T) {
	big := `{"entries":{},"bullets":{},"pad":"` + strings.Repeat("x", MaxBankBytes) + `"}`
	if _, err := Load(writeTemp(t, "big.json", big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized bank accepted: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); !errors.Is(err, ErrBank) {
		t.Fatalf("missing bank: %v", err)
	}
	// Exactly at the cap is still readable.
	pad := MaxBankBytes - len(`{"entries":{},"bullets":{},"pad":""}`)
	ok := `{"entries":{},"bullets":{},"pad":"` + strings.Repeat("x", pad) + `"}`
	if len(ok) != MaxBankBytes {
		t.Fatalf("fixture is %d bytes", len(ok))
	}
	if _, err := Load(writeTemp(t, "edge.json", ok)); err != nil {
		t.Fatalf("bank at the cap rejected: %v", err)
	}
}

// ---- claims through the runner ----

func TestRKBClaimsViaRunner(t *testing.T) {
	fr := &fakeRunner{content: []byte(`{"claims":{"clmLat":{"value":"42%"},"clmQps":{"value":"1.2M/s","caution":"CAUTION: scope"}}}`)}
	c, err := RKB{Binary: "/opt/rkb", CodeRoot: "/code", Runner: fr}.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Values["clmLat"] != "42%" || !c.Caution["clmQps"] || c.Caution["clmLat"] {
		t.Fatalf("claims = %+v", c)
	}
	if fr.cmd.Path != "/opt/rkb" || len(fr.cmd.Args) != 4 || strings.Join(fr.cmd.Args[:3], " ") != "claims export --out" {
		t.Fatalf("command = %+v", fr.cmd)
	}
	out := fr.cmd.Args[3]
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("temp claims file was not removed: %v", err)
	}
	if strings.HasPrefix(out, "/code") {
		t.Fatal("claims export must not be written under the code root")
	}
	var sawRoot bool
	for _, e := range fr.cmd.Env {
		switch {
		case e == "RKB_CODE_ROOT=/code":
			sawRoot = true
		case strings.HasPrefix(e, "PATH="), strings.HasPrefix(e, "HOME="), strings.HasPrefix(e, "TMPDIR="):
		default:
			t.Fatalf("environment leaks %q", strings.SplitN(e, "=", 2)[0])
		}
	}
	if !sawRoot {
		t.Fatal("RKB_CODE_ROOT not passed")
	}
}

func TestRKBClaimsFailures(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		rk  RKB
		sub string
	}{
		"relative binary": {RKB{Binary: "rkb", Runner: &fakeRunner{}}, "not absolute"},
		"runner error":    {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{err: errors.New("exit 1")}}, "exit 1"},
		"no file":         {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{noFile: true}}, "wrote no claims file"},
		"not json":        {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{content: []byte("nope")}}, "claims.json"},
		"no claims":       {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{content: []byte(`{"claims":{}}`)}}, "no claims"},
		"empty value":     {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{content: []byte(`{"claims":{"clmA":{"value":" "}}}`)}}, "empty value"},
		"oversized":       {RKB{Binary: "/opt/rkb", Runner: &fakeRunner{content: bytes.Repeat([]byte(" "), MaxClaimsBytes+1)}}, "larger than"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.rk.Claims(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.sub)
			}
		})
	}
}

func TestExecRunnerBoundsAndFailure(t *testing.T) {
	if err := (ExecRunner{}).Run(context.Background(), Command{Path: "/usr/bin/false"}); !errors.Is(err, ErrResolve) {
		t.Fatalf("failing process: %v", err)
	}
	if err := (ExecRunner{}).Run(context.Background(), Command{Path: "/usr/bin/true"}); err != nil {
		t.Fatalf("true: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/sleep", Args: []string{"5"}}); err == nil {
		t.Fatal("cancelled context did not stop the process")
	}
}

func TestResolveTextAndBank(t *testing.T) {
	got, miss := ResolveText("A {{clmX}} and **bold** and *it* and [site](https://e.invalid/p) and [e.invalid](https://e.invalid/p) {{clmY}}", map[string]string{"clmX": "7%"})
	if got != "A 7% and bold and it and site (https://e.invalid/p) and e.invalid {{clmY}}" || len(miss) != 1 || miss[0] != "clmY" {
		t.Fatalf("got %q miss %v", got, miss)
	}
	r := fixtureResolved(t)
	byID := map[string]Block{}
	for _, b := range r.Blocks {
		byID[b.ID] = b
	}
	if byID["alpha-job"].Bullets[0].Canonical != "Cut latency by 42% using caching." {
		t.Fatalf("b1 = %q", byID["alpha-job"].Bullets[0].Canonical)
	}
	if byID["alpha-job"].Bullets[1].Canonical != "Wrote docs (https://example.invalid/d) for it." {
		t.Fatalf("b2 = %q", byID["alpha-job"].Bullets[1].Canonical)
	}
	if got := byID["alpha-job"].Bullets[0].Metrics; len(got) != 1 || got[0] != "42%" {
		t.Fatalf("metrics = %v", got)
	}
	if byID["alpha-job"].Kind != KindWork || byID["beta-proj"].Kind != KindProject || byID["gamma-edu"].Kind != KindEducation {
		t.Fatalf("kinds: %s %s %s", byID["alpha-job"].Kind, byID["beta-proj"].Kind, byID["gamma-edu"].Kind)
	}
	if r.Unattached != 1 || r.Bullets != 3 || r.Unresolved != 0 {
		t.Fatalf("resolved = %+v", r)
	}
	// An unknown token blocks only its block and is counted.
	u := ResolveBank(mustBank(t, fixtureBank), Claims{Values: map[string]string{"clmLat": "42%"}, Caution: map[string]bool{}})
	if u.Unresolved != 1 {
		t.Fatalf("unresolved = %d", u.Unresolved)
	}
	for _, b := range u.Blocks {
		if (b.ID == "beta-proj") != (b.Blocked != "") {
			t.Fatalf("block %s blocked=%q", b.ID, b.Blocked)
		}
	}
}

func TestParseDatesAndKind(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		in    string
		start string
		end   *string
		bad   bool
	}{
		// ISO out (hashVersion 3): DevPrism's recency reads a leading year.
		{"AUG 2023 – APR 2025", "2023-08", str("2025-04"), false},
		{"FEB 2026 – Present", "2026-02", nil, false},
		{"SEP 2025 to Present", "2025-09", nil, false},
		{"MAY 2022", "2022-05", str("2022-05"), false},
		{"", "", nil, false},
		{"A – B – C", "", nil, true},
		{"2020 –", "", nil, true},
		// No start: reported, never stored as start "Present".
		{"Present", "", nil, true},
		{"  current ", "", nil, true},
		{"NOW", "", nil, true},
		{"Present – 2024", "", nil, true},
		{"Present to Present", "", nil, true},
		{"2021 – now", "2021", nil, false},
	}
	for _, c := range cases {
		s, e, err := ParseDates(c.in)
		if (err != nil) != c.bad || s != c.start || (e == nil) != (c.end == nil) || (e != nil && *e != *c.end) {
			t.Errorf("ParseDates(%q) = %q %v %v", c.in, s, e, err)
		}
	}
	for title, want := range map[string]string{"Bachelor of Technology in X": KindEducation, "Master's thesis": KindEducation, "Associate Researcher": KindProject} {
		if got := KindOf(Entry{Title: title}); got != want {
			t.Errorf("KindOf(%q) = %s", title, got)
		}
	}
	if KindOf(Entry{Title: "Associate Researcher", Org: "Lab", Dates: "2020"}) != KindWork {
		t.Error("associate researcher with org and dates is work")
	}
}

// ---- plan ----

func TestPlanAddUpdateUnchangedOrphanedBlocked(t *testing.T) {
	r := fixtureResolved(t)
	alpha := r.Blocks[0] // sorted: alpha-job, beta-proj, gamma-edu
	if alpha.ID != "alpha-job" {
		t.Fatalf("order: %s", alpha.ID)
	}
	cur := DevPrismState{Blocks: map[string]json.RawMessage{
		"alpha-job": json.RawMessage(`{"id":"alpha-job","kind":"work","bullets":[]}`),
		"beta-proj": json.RawMessage(`{"id":"beta-proj","kind":"project","bullets":[]}`),
		"user-only": json.RawMessage(`{"id":"user-only"}`),
	}}
	st := NewSyncState("")
	st.Blocks["alpha-job"] = alpha.Hash       // in DevPrism and recorded: unchanged
	st.Blocks["beta-proj"] = "stale-hash"     // in DevPrism, changed: update
	st.Blocks["gamma-edu"] = r.Blocks[2].Hash // recorded but gone from DevPrism: add again
	p := Plan(r, cur, *st, fixedNow)
	want := map[string]string{"alpha-job": ActionUnchanged, "beta-proj": ActionUpdate, "gamma-edu": ActionAdd}
	for _, it := range p.Items {
		if want[it.ID] != it.Action {
			t.Errorf("%s: %s, want %s", it.ID, it.Action, want[it.ID])
		}
		if (it.Action == ActionAdd || it.Action == ActionUpdate) != (len(it.Payload) > 0) {
			t.Errorf("%s: payload presence wrong for %s", it.ID, it.Action)
		}
	}
	if len(p.Orphaned) != 1 || p.Orphaned[0] != "user-only" {
		t.Fatalf("orphaned = %v", p.Orphaned)
	}
	for _, it := range p.Items {
		if it.ID == "user-only" {
			t.Fatal("an orphan was planned for action")
		}
	}
	// Blocked: unresolved token.
	bad := ResolveBank(mustBank(t, fixtureBank), Claims{Values: map[string]string{"clmLat": "1"}, Caution: map[string]bool{}})
	bp := Plan(bad, DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	if bp.Count(ActionBlocked) != 1 || bp.Count(ActionAdd) != 2 {
		t.Fatalf("blocked plan: %+v", bp.Items)
	}
}

func TestPlanNeverPlansDeletes(t *testing.T) {
	cur := DevPrismState{Blocks: map[string]json.RawMessage{"x": json.RawMessage(`{"id":"x"}`), "y": json.RawMessage(`{"id":"y"}`)}}
	p := Plan(Resolved{}, cur, *NewSyncState(""), fixedNow)
	if len(p.Items) != 0 || len(p.Orphaned) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	f := newFake()
	res, err := Apply(context.Background(), f, p, NewSyncState(filepath.Join(t.TempDir(), "s.json")), nil)
	if err != nil || len(f.calls) != 0 || len(res.Orphaned) != 2 {
		t.Fatalf("apply on orphans only: %v calls=%d %+v", err, len(f.calls), res)
	}
}

func TestPlanOversizedPayloadIsBlocked(t *testing.T) {
	big := strings.Repeat("x", maxPayloadBytes)
	b := mustBank(t, `{"entries":{"e":{"title":"T","bullets":["b"]}},"bullets":{"b":{"text":"`+big+`"}}}`)
	p := Plan(ResolveBank(b, fixtureClaims), DevPrismState{Blocks: map[string]json.RawMessage{}}, *NewSyncState(""), fixedNow)
	if len(p.Items) != 1 || p.Items[0].Action != ActionBlocked || !strings.Contains(p.Items[0].Reason, "limit") {
		t.Fatalf("items = %+v", p.Items)
	}
}

func TestUpdatePreservesDevPrismOwnedFields(t *testing.T) {
	r := fixtureResolved(t)
	existing := `{"id":"alpha-job","kind":"project","title":"Old","org":"Old","dateRange":{"start":"x","end":null},
	  "personas":["ai"],"domains":["d"],"skills":[{"name":"go","level":3}],"seniorityLevel":"senior","notes":"mine",
	  "embeddingText":"stale","updatedAt":"1",
	  "bullets":[{"id":"alpha-job/b2","canonical":"old","variants":{"ai":"v"},"metrics":[{"value":"9","kind":"metric"}],"evidenceRefs":["e1"],"locked":false},
	             {"id":"devprism-only","canonical":"keep me","variants":{},"metrics":[],"evidenceRefs":[],"locked":true}],
	  "facts":[{"id":"f1","text":"fact","skills":[],"metrics":[],"source":"manual","createdAt":"t"}]}`
	cur := DevPrismState{Blocks: map[string]json.RawMessage{"alpha-job": json.RawMessage(existing)}}
	p := Plan(r, cur, *NewSyncState(""), fixedNow)
	var it Item
	for _, x := range p.Items {
		if x.ID == "alpha-job" {
			it = x
		}
	}
	if it.Action != ActionUpdate {
		t.Fatalf("action %s", it.Action)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(it.Payload, &got); err != nil {
		t.Fatal(err)
	}
	// By value: skills are re-encoded with sorted keys so a replay is byte
	// stable. rkb's "Go" matches DevPrism's "go", and "ai" is already there,
	// so the union adds nothing.
	for _, k := range []string{"personas", "domains", "skills", "seniorityLevel", "notes", "facts"} {
		var old map[string]json.RawMessage
		_ = json.Unmarshal([]byte(existing), &old)
		var a, b any
		if json.Unmarshal(got[k], &a) != nil || json.Unmarshal(old[k], &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s changed: %s", k, got[k])
		}
	}
	if _, ok := got["embeddingText"]; ok {
		t.Error("stale embeddingText kept")
	}
	if string(got["kind"]) != `"project"` {
		t.Errorf("existing kind overwritten: %s", got["kind"])
	}
	if string(got["title"]) != `"Engineer"` || string(got["updatedAt"]) != `"2026-01-02T03:04:05Z"` {
		t.Errorf("title/updatedAt: %s %s", got["title"], got["updatedAt"])
	}
	var bullets []map[string]any
	_ = json.Unmarshal(got["bullets"], &bullets)
	ids := []string{}
	for _, b := range bullets {
		ids = append(ids, b["id"].(string))
	}
	if strings.Join(ids, ",") != "alpha-job/b1,alpha-job/b2,devprism-only" {
		t.Fatalf("bullet order %v", ids)
	}
	b2 := bullets[1]
	if b2["canonical"] != "Wrote docs (https://example.invalid/d) for it." || b2["evidenceRefs"].([]any)[0] != "e1" || b2["variants"].(map[string]any)["ai"] != "v" {
		t.Fatalf("b2 not merged: %v", b2)
	}
	if bullets[2]["locked"] != true {
		t.Fatal("DevPrism-only bullet lost its lock")
	}
	// The fake DevPrism sees no loss, so this update needs no confirmation.
	f := newFake()
	f.seed(t, existing)
	if overwriteLoses(f.blocks["alpha-job"], mustMap(t, it.Payload)) {
		t.Fatal("merged update would still drop content")
	}
}

func mustMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ---- apply ----

func applyAll(t *testing.T, f *fakeDevPrism, r Resolved, st *SyncState) (SyncPlan, Result, error) {
	t.Helper()
	cur, err := FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(r, cur, *st, fixedNow)
	res, err := Apply(context.Background(), f, p, st, nil)
	return p, res, err
}

func TestApplyIdempotent(t *testing.T) {
	f := newFake()
	path := filepath.Join(t.TempDir(), "state", "kbsync.json")
	st := NewSyncState(path)
	r := fixtureResolved(t)
	_, res, err := applyAll(t, f, r, st)
	if err != nil || res.Added != 3 || res.Updated != 0 || len(f.upserts()) != 3 {
		t.Fatalf("first apply: %v %+v", err, res)
	}
	// Plan again against the live fake and a state reloaded from disk.
	st2, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	p, res2, err := applyAll(t, f, r, st2)
	if err != nil || p.Count(ActionUnchanged) != 3 || res2.Unchanged != 3 || len(f.upserts()) != 3 {
		t.Fatalf("second apply: %v plan=%+v calls=%d", err, p.Items, len(f.upserts()))
	}
	// A changed bullet text updates exactly that block.
	changed := strings.Replace(fixtureBank, "Handled {{clmQps}} requests.", "Handled {{clmQps}} requests daily.", 1)
	p3, res3, err := applyAll(t, f, ResolveBank(mustBank(t, changed), fixtureClaims), st2)
	if err != nil || res3.Updated != 1 || p3.Count(ActionUnchanged) != 2 || len(f.upserts()) != 4 {
		t.Fatalf("third apply: %v %+v", err, res3)
	}
}

func TestApplyStopsAtFirstErrorWithConsistentState(t *testing.T) {
	f := newFake()
	f.failOn = 2
	path := filepath.Join(t.TempDir(), "kbsync.json")
	st := NewSyncState(path)
	r := fixtureResolved(t)
	_, res, err := applyAll(t, f, r, st)
	if err == nil || !strings.Contains(err.Error(), "beta-proj") {
		t.Fatalf("err = %v", err)
	}
	if res.Added != 1 || len(f.upserts()) != 2 {
		t.Fatalf("result %+v calls %d (third block must not be attempted)", res, len(f.upserts()))
	}
	disk, lerr := LoadState(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(disk.Blocks) != 1 || disk.Blocks["alpha-job"] != r.Blocks[0].Hash {
		t.Fatalf("state on disk = %v", disk.Blocks)
	}
	// Retry: only the two unsynced blocks are sent.
	f.failOn = 0
	_, res, err = applyAll(t, f, r, disk)
	if err != nil || res.Added != 2 || res.Unchanged != 1 {
		t.Fatalf("retry: %v %+v", err, res)
	}
}

func TestApplyNeverAutoConfirms(t *testing.T) {
	f := newFake()
	f.seed(t, `{"id":"alpha-job","kind":"work","title":"E","org":"A","dateRange":{"start":"x","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
	  "bullets":[{"id":"alpha-job/b1","canonical":"user locked wording","variants":{},"metrics":[],"evidenceRefs":[],"locked":true}]}`)
	path := filepath.Join(t.TempDir(), "kbsync.json")
	st := NewSyncState(path)
	_, res, err := applyAll(t, f, fixtureResolved(t), st)
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsConfirmation != 1 || res.Added != 2 {
		t.Fatalf("result %+v", res)
	}
	var nc Outcome
	for _, o := range res.Outcomes {
		if o.Status == StatusNeedsConfirmation {
			nc = o
		}
	}
	if nc.ID != "alpha-job" || !strings.Contains(nc.Reason, "locked bullet") {
		t.Fatalf("outcome %+v", nc)
	}
	for _, c := range f.upserts() {
		var a map[string]json.RawMessage
		_ = json.Unmarshal(c.Args, &a)
		if len(a) != 1 || a["block"] == nil {
			t.Fatalf("upsert carried more than the block: %s", c.Args)
		}
		if bytes.Contains(c.Args, []byte("request_state")) || bytes.Contains(c.Args, []byte("input_responses")) {
			t.Fatal("Apply tried to confirm")
		}
	}
	if f.blocks["alpha-job"]["bullets"].([]any)[0].(map[string]any)["canonical"] != "user locked wording" {
		t.Fatal("DevPrism block was overwritten without confirmation")
	}
	if _, ok := st.Blocks["alpha-job"]; ok {
		t.Fatal("hash recorded for an unconfirmed block")
	}
	if len(st.Blocks) != 2 {
		t.Fatalf("other blocks were not recorded: %v", st.Blocks)
	}
	// Still planned as an update next time (not silently dropped).
	cur, _ := FetchState(context.Background(), f)
	if Plan(fixtureResolved(t), cur, *st, fixedNow).Count(ActionUpdate) != 1 {
		t.Fatal("unconfirmed block is not retried")
	}
}

func TestApplyRejectsUnexpectedAnswers(t *testing.T) {
	for name, ans := range map[string]string{
		"not object":    `[]`,
		"no success":    `{"blockId":"alpha-job"}`,
		"success false": `{"success":false,"cancelled":true}`,
		"wrong id":      `{"success":true,"blockId":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.weirdAnswer = json.RawMessage(ans)
			st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
			_, res, err := applyAll(t, f, fixtureResolved(t), st)
			if !errors.Is(err, ErrDevPrism) || len(f.upserts()) != 1 || len(st.Blocks) != 0 || res.Added != 0 {
				t.Fatalf("err=%v calls=%d state=%v", err, len(f.upserts()), st.Blocks)
			}
		})
	}
}

func TestApplyStableOrderRegardlessOfBankOrder(t *testing.T) {
	var orders [][]string
	for _, src := range []string{
		fixtureBank,
		`{"entries":{"beta-proj":{"title":"Widget","bullets":["b3"]},"alpha-job":{"title":"Engineer","org":"Acme","dates":"JAN 2020 – Present","bullets":["b1","b2"]},"gamma-edu":{"title":"Master of Science in Things","org":"Example University","dates":"AUG 2018 – MAY 2020","bullets":[]}},` +
			`"bullets":{"unattached":{"text":"u"},"b3":{"text":"Handled {{clmQps}} requests."},"b2":{"text":"Wrote [docs](https://example.invalid/d) for it."},"b1":{"text":"Cut latency by {{clmLat}} using **caching**."}}}`,
	} {
		f := newFake()
		st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
		if _, _, err := applyAll(t, f, ResolveBank(mustBank(t, src), fixtureClaims), st); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, c := range f.upserts() {
			var a struct {
				Block struct {
					ID string `json:"id"`
				} `json:"block"`
			}
			_ = json.Unmarshal(c.Args, &a)
			ids = append(ids, a.Block.ID)
		}
		orders = append(orders, ids)
	}
	if strings.Join(orders[0], ",") != "alpha-job,beta-proj,gamma-edu" || strings.Join(orders[1], ",") != strings.Join(orders[0], ",") {
		t.Fatalf("orders = %v", orders)
	}
}

func TestDryRunCallsNothingAndLeaksNothing(t *testing.T) {
	r := fixtureResolved(t)
	p := Plan(r, DevPrismState{Blocks: map[string]json.RawMessage{"user-only": json.RawMessage(`{"id":"user-only"}`)}}, *NewSyncState(""), fixedNow)
	var out bytes.Buffer
	if err := DryRun(&out, p); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"3 add", "orphaned  user-only", "alpha-job", "1 rkb bullets belong to no entry"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, s)
		}
	}
	for _, leak := range []string{"Synthetic Person", "example.invalid", "Cut latency", "42%"} {
		if strings.Contains(s, leak) {
			t.Errorf("dry run leaks %q", leak)
		}
	}
	// DryRun has no ToolCaller parameter, so it cannot call a tool; assert
	// the fake saw nothing from planning either.
	f := newFake()
	if _, err := FetchState(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c.Tool != "career_get_profile" {
			t.Fatalf("unexpected call %s", c.Tool)
		}
	}
}

func TestFetchStateRejectsBadProfiles(t *testing.T) {
	for name, ans := range map[string]string{
		"no blocks key": `{"personas":[]}`,
		"null blocks":   `{"blocks":null}`,
		"not json":      `7`,
		"block no id":   `{"blocks":[{"title":"x"}]}`,
		"duplicate id":  `{"blocks":[{"id":"a"},{"id":"a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FetchState(context.Background(), staticCaller(ans)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := FetchState(context.Background(), staticCaller(`{"blocks":[]}`)); err != nil {
		t.Fatal(err)
	}
}

type staticCaller string

func (s staticCaller) CallTool(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(s), nil
}

// ---- state file ----

func TestStateFileAtomicAnd0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "kbsync.json")
	st := NewSyncState(path)
	st.record("a", "h1", fixedNow)
	if err := st.flush(); err != nil {
		t.Fatal(err)
	}
	st.record("b", "h2", fixedNow)
	if err := st.flush(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", di.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
	back, err := LoadState(path)
	if err != nil || back.Blocks["a"] != "h1" || back.Blocks["b"] != "h2" || back.Synced["a"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	// A failing write leaves the previous file intact: make the directory
	// unwritable so the temp file cannot be created.
	if os.Geteuid() != 0 {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o700)
		before, _ := os.ReadFile(path)
		st.record("c", "h3", fixedNow)
		if err := st.flush(); err == nil {
			t.Fatal("save into a read-only dir succeeded")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed save altered the state file")
		}
	}
}

func TestLoadStateFailsClosed(t *testing.T) {
	if st, err := LoadState(filepath.Join(t.TempDir(), "absent.json")); err != nil || len(st.Blocks) != 0 {
		t.Fatalf("missing file should be an empty state: %v", err)
	}
	for name, content := range map[string]string{
		"corrupt":       `{"version":1,"blocks":`,
		"wrong version": `{"version":9,"blocks":{}}`,
		"oversized":     `{"version":1,"blocks":{},"pad":"` + strings.Repeat("x", MaxStateBytes) + `"}`,
	} {
		if _, err := LoadState(writeTemp(t, "s.json", content)); !errors.Is(err, ErrState) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (&SyncState{}).Save(); !errors.Is(err, ErrState) {
		t.Errorf("save without path: %v", err)
	}
}
