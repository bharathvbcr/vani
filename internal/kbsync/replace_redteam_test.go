package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rtState(t *testing.T, blocks ...string) DevPrismState {
	t.Helper()
	st := DevPrismState{Blocks: map[string]json.RawMessage{}}
	for _, raw := range blocks {
		var h struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(raw), &h); err != nil {
			t.Fatal(err)
		}
		st.Blocks[h.ID] = json.RawMessage(raw)
	}
	return st
}

func rtBlockJSON(id, kind string, bullets ...[2]string) string {
	var bl []string
	for _, b := range bullets {
		bl = append(bl, fmt.Sprintf(`{"id":%q,"canonical":%q,"variants":{},"metrics":[],"evidenceRefs":[],"locked":false}`, b[0], b[1]))
	}
	return fmt.Sprintf(`{"id":%q,"kind":%q,"title":"T %s","org":"O","dateRange":{"start":"2020","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],"bullets":[%s]}`,
		id, kind, id, strings.Join(bl, ","))
}

// An rkb variant DevPrism already holds (synced before hashVersion 3) is
// "known" to Plan, so it is neither an orphan nor spared: `--replace` reports
// nothing about it and leaves its bullets, superseded wordings included, in the
// table ResolvePool's promise says ranking is fed from the approved pool alone.
func TestRedteamReplaceDoesNotLeaveAStaleVariantBlock(t *testing.T) {
	b, c := poolBank(t)
	b.Entries["clean-v2"] = Entry{Title: "Clean (variant)", Org: "Acme", Dates: "JAN 2020 – Present", Stack: "Go", Origin: []string{"clean"}, Bullets: []string{"good"}}
	p, err := ResolvePool(b, c, []Selection{sel(
		SelEntry{Entry: "clean", Bullets: []string{"lonely", "good"}},
		SelEntry{Entry: "clean-v2", Bullets: []string{"good"}},
	)})
	if err != nil {
		t.Fatal(err)
	}
	if blk, ok := blockByID(p.Resolved, "clean-v2"); !ok || blk.VariantOf != "clean" {
		t.Fatalf("test assumption: clean-v2 is a variant of clean in the pool: %+v %v", blk, ok)
	}
	st := rtState(t, rtBlockJSON("clean-v2", "work", [2]string{"clean-v2/old-superseded", "A withdrawn 99% claim"}))
	plan := Plan(p.Resolved, st, *NewSyncState(""), fixedNow)
	rp, err := PlanReplace(plan, st, ReplaceOptions{AllKinds: true})
	if err != nil {
		t.Fatal(err)
	}
	judged := false
	for _, d := range rp.Delete {
		judged = judged || d.ID == "clean-v2"
	}
	for _, k := range rp.Keep {
		judged = judged || k.ID == "clean-v2"
	}
	if !judged {
		t.Fatalf("FAIL: the stale variant block clean-v2 (holding a withdrawn claim) is in neither Delete nor Keep: plan.Orphaned=%v delete=%v keep=%v", plan.Orphaned, ids(rp.Delete), rp.Keep)
	}
}

// buildUpdate drops a bullet only under this block's scoped prefix. A bullet a
// hashVersion-1 sync stored under its BARE rkb id is not recognised as ours
// unless the pool lists it again, so an ineligible bullet synced by v1 survives
// every update, `--replace` included.
func TestRedteamUpdateDropsLegacyBareIDBulletsThePoolWithholds(t *testing.T) {
	b, c := poolBank(t)
	p, err := ResolvePool(b, c, []Selection{poolSelection()})
	if err != nil {
		t.Fatal(err)
	}
	// proj's pool is good, cleared. rag (CAUTION) and unreviewed were synced by
	// hashVersion 1 under their bare rkb ids, as was good.
	st := rtState(t, rtBlockJSON("proj", "project",
		[2]string{"good", "old good"},
		[2]string{"rag", "Built retrieval at 92% recall."},
		[2]string{"unreviewed", "Built W alone."},
	))
	plan := Plan(p.Resolved, st, *NewSyncState(""), fixedNow)
	var payload json.RawMessage
	for _, it := range plan.Items {
		if it.ID == "proj" {
			payload = it.Payload
		}
	}
	if payload == nil {
		t.Fatalf("no payload for proj: %+v", plan.Items)
	}
	for _, withheld := range []string{"Built retrieval at 92% recall.", "Built W alone"} {
		if strings.Contains(string(payload), withheld) {
			t.Fatalf("FAIL: the update payload keeps %q, a bullet rkb listed once under its bare id and the pool now withholds: %s", withheld, payload)
		}
	}
}

func TestRedteamBackupAwkwardPaths(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	root := t.TempDir()
	for name, seg := range map[string]string{
		"semicolon": "a;b",
		"backslash": `a\b`,
		"dollar":    "a$HOME",
		"backtick":  "a`id`",
		"unicode":   "données-日本",
		"brackets":  "a[1](2){3}",
		"percent":   "a%20b",
		"hash":      "a#b?c",
		"ampersand": "a&b",
		"dash lead": "-weird",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, seg)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Skipf("fs refuses %q: %v", seg, err)
			}
			db := filepath.Join(dir, "career.db")
			makeCareerDB(t, sqlite, db, "a1", "b2")
			info, err := Backuper{}.Backup(context.Background(), db, filepath.Join(dir, "backups "+seg), time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC))
			if err != nil {
				t.Fatalf("an ordinary path with %q was refused or broke the dot-command: %v", seg, err)
			}
			if strings.Join(info.BlockIDs, ",") != "a1,b2" {
				t.Fatalf("backup holds %v", info.BlockIDs)
			}
		})
	}
}

// A database another process holds EXCLUSIVE (rollback-journal mode) cannot be
// copied: the backup must fail fast and leave no file named like a backup.
func TestRedteamBackupWhileLockedFailsClosedAndFast(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "career.db")
	if out, err := exec.Command(sqlite, db, "CREATE TABLE blocks (id TEXT PRIMARY KEY, kind TEXT, json TEXT NOT NULL, updated_at INTEGER); INSERT INTO blocks VALUES ('a1','project','{}',1);").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cmd := exec.Command(sqlite, db)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	if _, err := fmt.Fprintln(in, "BEGIN EXCLUSIVE; INSERT INTO blocks VALUES ('held','project','{}',2);"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	backups := filepath.Join(dir, "backups")
	start := time.Now()
	info, err := Backuper{}.Backup(context.Background(), db, backups, time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC))
	elapsed := time.Since(start)
	if elapsed > 30*time.Second {
		t.Fatalf("FAIL: the backup waited %s on a locked database", elapsed)
	}
	if err == nil {
		// A snapshot that omits the uncommitted row is also correct. What must
		// not happen is a copy that includes it.
		for _, id := range info.BlockIDs {
			if id == "held" {
				t.Fatal("FAIL: the backup holds an uncommitted row")
			}
		}
		return
	}
	if !errors.Is(err, ErrBackup) {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(backups); len(ents) != 0 {
		t.Fatalf("FAIL: a failed backup left %d file(s) behind", len(ents))
	}
}

// An rkb that spawns a grandchild holding stdout open must still be bounded by
// the timeout (WaitDelay), not hang the run.
func TestRedteamRKBGrandchildHoldingThePipeIsBounded(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "rkb")
	body := "#!/bin/sh\nsleep 60 &\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := RKB{Binary: script, Runner: ExecRunner{Timeout: 300 * time.Millisecond}}.Claims(context.Background())
	if err == nil || !errors.Is(err, ErrResolve) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("FAIL: a stuck rkb held the run for %s", d)
	}
}

// A cancelled context stops ApplyDeletes before the first delete call.
func TestRedteamApplyDeletesCancelledBeforeAnyCall(t *testing.T) {
	d, plan, st := replaceSetup(t)
	rp, _ := PlanReplace(plan, st, ReplaceOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ApplyDeletes(ctx, d, rp, NewSyncState(""), confirmAll)
	if !errors.Is(err, context.Canceled) || d.deleteCalls() != 0 {
		t.Fatalf("err = %v, delete calls = %d", err, d.deleteCalls())
	}
}
