package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// oldBlock is a block DevPrism holds from an earlier import: ids unlike any rkb
// slug, so none of them is in the approved pool.
func oldBlock(id, kind string, bullets int) json.RawMessage {
	var bl []string
	for i := 0; i < bullets; i++ {
		bl = append(bl, fmt.Sprintf(`{"id":"%s-b%d","canonical":"old text %d"}`, id, i, i))
	}
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"kind":%q,"title":"Old %s","bullets":[%s],"facts":[]}`, id, kind, id, strings.Join(bl, ",")))
}

func oldBlocks() []json.RawMessage {
	return []json.RawMessage{
		oldBlock("edu_old3", "education", 1),
		oldBlock("exp_old1", "experience", 2),
		oldBlock("pro_old2", "project", 0),
		oldBlock("pub_old4", "publication", 1),
	}
}

func blockIDs(bs []json.RawMessage) []string {
	var ids []string
	for _, b := range bs {
		var h struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(b, &h)
		ids = append(ids, h.ID)
	}
	return ids
}

// replaceRun is a stubbed career.db backup and a recorded order of events.
type replaceRun struct {
	order   []string
	backups int
	err     error
	ids     []string // the ids the backup reports; nil means DevPrism's own
	db, dir string
}

func stubBackup(t *testing.T, r *replaceRun, f *fakeDevPrism) {
	t.Helper()
	old := careerBackup
	careerBackup = func(_ context.Context, db, dir string, _ time.Time) (kbsync.BackupInfo, error) {
		r.backups++
		r.order = append(r.order, "backup")
		r.db, r.dir = db, dir
		if r.err != nil {
			return kbsync.BackupInfo{}, r.err
		}
		ids := r.ids
		if ids == nil {
			ids = blockIDs(f.blocks)
		}
		return kbsync.BackupInfo{Path: filepath.Join(dir, "career-test.db"), BlockIDs: ids, Bytes: 123}, nil
	}
	t.Cleanup(func() { careerBackup = old })
	oldStart := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) {
		r.order = append(r.order, "start")
		return f, nil
	}
	t.Cleanup(func() { startKB = oldStart })
}

func replaceSetupCmd(t *testing.T) (bank, rkb string, f *fakeDevPrism, r *replaceRun) {
	t.Helper()
	bank, rkb, _ = kbApproved(t, "/opt/devprism/devprism")
	f = &fakeDevPrism{blocks: oldBlocks()}
	r = &replaceRun{}
	stubBackup(t, r, f)
	return bank, rkb, f, r
}

// firstIndex is the index of the first call named tool, or -1.
func firstIndex(calls []string, tool string) int {
	for i, c := range calls {
		if c == tool {
			return i
		}
	}
	return -1
}

func lastIndex(calls []string, tool string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i] == tool {
			return i
		}
	}
	return -1
}

// --replace --dry-run reads the block list so it can name what would go, and
// calls nothing that changes DevPrism. It makes no backup (nothing is changed).
func TestKBReplaceDryRunMakesNoMutatingCall(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	o, _ := mustCode(t, 0, "kb", "sync", "--replace", "--dry-run", "--bank", bank, "--rkb", rkb)
	if got := strings.Join(f.calls, ","); got != "career_get_profile" {
		t.Fatalf("a --replace --dry-run called %q, want only career_get_profile", got)
	}
	if f.mutating() || len(f.upserts) != 0 || len(f.deletes) != 0 || len(f.blocks) != 4 {
		t.Fatalf("a dry run changed DevPrism: %v %v", f.upserts, f.deletes)
	}
	if r.backups != 0 {
		t.Fatal("a dry run took a backup it has no use for")
	}
	for _, want := range []string{
		"3 add", "3 to delete, 1 spared",
		`delete    exp_old1 [experience] "Old exp_old1" (2 bullets, 0 facts)`,
		`spared    pub_old4 [publication] "Old pub_old4"`,
		"--delete-all-kinds",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	for _, leak := range []string{"person@example.invalid", "Synthetic Person", "gizmo", "draft nobody read"} {
		if strings.Contains(o, leak) {
			t.Errorf("dry run printed %q", leak)
		}
	}
}

// The order of a real run: the copy, then DevPrism starts, then every upsert,
// then the deletions. Pool blocks are never deleted and the spared kind stays.
func TestKBReplaceCopiesThenUpsertsThenDeletesOnlyOrphans(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	o, _ := mustCode(t, 0, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
	if strings.Join(r.order, ",") != "backup,start" || r.backups != 1 {
		t.Fatalf("order %v: the copy must come first, once", r.order)
	}
	if !strings.HasSuffix(r.dir, filepath.Join("kbsync", "backups")) {
		t.Fatalf("backup dir %q is not under the state dir's kbsync/backups", r.dir)
	}
	if !strings.HasSuffix(r.db, filepath.Join("DevPrism", "career.db")) {
		t.Fatalf("copied %q, want DevPrism's career.db", r.db)
	}
	if got := strings.Join(f.upserts, ","); got != "alpha-job,edu,gamma-proj" {
		t.Fatalf("upserted %q", got)
	}
	if got := strings.Join(f.deletes, ","); got != "edu_old3,exp_old1,pro_old2" {
		t.Fatalf("deleted %q, want the three orphans of a kind the sync writes", got)
	}
	if lastIndex(f.calls, "career_upsert_block") > firstIndex(f.calls, "career_delete_block") {
		t.Fatalf("a delete came before the last upsert: %v", f.calls)
	}
	for _, id := range blockIDs(f.blocks) {
		if id != "pub_old4" {
			t.Fatalf("DevPrism still holds %q; the spared publication should be all that is left of the old blocks", id)
		}
	}
	for _, want := range []string{"backup: ", "kb sync --replace: 3 deleted", "DevPrism now holds 1 blocks (it held 4)"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
}

func TestKBReplaceKeepAndAllKinds(t *testing.T) {
	bank, rkb, f, _ := replaceSetupCmd(t)
	mustCode(t, 0, "kb", "sync", "--replace", "--keep", "exp_old1, edu_old3", "--delete-all-kinds", "--bank", bank, "--rkb", rkb)
	if got := strings.Join(f.deletes, ","); got != "pro_old2,pub_old4" {
		t.Fatalf("deleted %q", got)
	}
}

// Whatever fails before the first upsert leaves DevPrism untouched, and a run
// that cannot copy career.db does not even start DevPrism.
func TestKBReplaceBackupFailureChangesNothing(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	r.err = errors.New("no sqlite3")
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "no career.db backup") || !strings.Contains(e, "no sqlite3") {
		t.Fatalf("stderr %q", e)
	}
	if strings.Join(r.order, ",") != "backup" || len(f.calls) != 0 {
		t.Fatalf("order %v, calls %v: DevPrism must not be started without a backup", r.order, f.calls)
	}
}

// A backup that does not hold what DevPrism lists is not one a deletion can be
// undone from.
func TestKBReplaceBackupMismatchChangesNothing(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	r.ids = []string{"exp_old1"}
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "career.db changed") {
		t.Fatalf("stderr %q", e)
	}
	if f.mutating() {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestKBReplaceDeleteCap(t *testing.T) {
	bank, rkb, f, _ := replaceSetupCmd(t)
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--max-delete", "2", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "over the cap of 2") {
		t.Fatalf("stderr %q", e)
	}
	if f.mutating() {
		t.Fatalf("a refused replace changed DevPrism: %v", f.calls)
	}
	// The same refusal shows in the dry run, so the owner sees it before the real run.
	_, e = mustCode(t, 1, "kb", "sync", "--replace", "--dry-run", "--max-delete", "2", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "over the cap of 2") {
		t.Fatalf("dry run stderr %q", e)
	}
}

// A selection that approves nothing leaves an empty pool; replace must not read
// that as "delete everything".
func TestKBReplaceEmptyPoolNeverWipesDevPrism(t *testing.T) {
	_, rkb, selection := kbApproved(t, "/opt/devprism/devprism")
	if err := os.WriteFile(selection, []byte(`{"sections":[{"title":"x","kind":"entries","entries":[{"entry":"delta-proj","bullets":[]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeDevPrism{blocks: oldBlocks()}
	r := &replaceRun{}
	stubBackup(t, r, f)
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--delete-all-kinds", "--rkb", rkb)
	if !strings.Contains(e, "no block to sync") {
		t.Fatalf("stderr %q", e)
	}
	if f.mutating() || len(f.blocks) != 4 {
		t.Fatalf("an empty pool changed DevPrism: %v", f.calls)
	}
}

// A pool block DevPrism wants confirmed (and --confirm was not given) has not
// landed, so nothing is deleted around it.
func TestKBReplaceDeletesNothingWhileAPoolBlockIsOutstanding(t *testing.T) {
	bank, rkb, f, _ := replaceSetupCmd(t)
	f.confirm = map[string]bool{"alpha-job": true}
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "did not reach DevPrism") {
		t.Fatalf("stderr %q", e)
	}
	if len(f.deletes) != 0 || firstIndex(f.calls, "career_delete_block") >= 0 {
		t.Fatalf("deleted %v", f.deletes)
	}
}

func TestKBReplaceDeleteRefusedDevPrismDownAndStale(t *testing.T) {
	t.Run("confirmation refused", func(t *testing.T) {
		bank, rkb, f, _ := replaceSetupCmd(t)
		f.deleteRejects = map[string]bool{"edu_old3": true}
		o, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
		if !strings.Contains(o, "2 deleted") || !strings.Contains(o, "1 confirmation refused") || !strings.Contains(e, "not deleted") {
			t.Fatalf("stdout %q stderr %q", o, e)
		}
		if got := strings.Join(f.deletes, ","); got != "exp_old1,pro_old2" {
			t.Fatalf("deleted %q", got)
		}
	})
	t.Run("DevPrism down mid-run", func(t *testing.T) {
		bank, rkb, f, _ := replaceSetupCmd(t)
		f.deleteDown = "exp_old1"
		o, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
		if !strings.Contains(o, "stopped early") || !strings.Contains(e, "devprism went away") {
			t.Fatalf("stdout %q stderr %q", o, e)
		}
		if got := strings.Join(f.deletes, ","); got != "edu_old3" {
			t.Fatalf("deleted %q: the run must stop at the first error", got)
		}
	})
	t.Run("block changed under the run", func(t *testing.T) {
		bank, rkb, f, _ := replaceSetupCmd(t)
		f.afterDeleteAsk = func(f *fakeDevPrism, id string) {
			if id == "exp_old1" {
				for i, b := range f.blocks {
					if strings.Contains(string(b), `"exp_old1"`) {
						f.blocks[i] = json.RawMessage(strings.Replace(string(b), "Old exp_old1", "edited meanwhile", 1))
					}
				}
			}
		}
		o, e := mustCode(t, 1, "kb", "sync", "--replace", "--bank", bank, "--rkb", rkb)
		if !strings.Contains(o, "1 changed in DevPrism") || !strings.Contains(e, "not deleted") {
			t.Fatalf("stdout %q stderr %q", o, e)
		}
		for _, id := range f.deletes {
			if id == "exp_old1" {
				t.Fatal("a block that changed under the run was deleted")
			}
		}
	})
}

func TestKBReplaceFlagRefusals(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	for name, args := range map[string][]string{
		"keep without replace":      {"--keep", "x"},
		"all kinds without replace": {"--delete-all-kinds"},
		"cap without replace":       {"--max-delete", "5"},
		"career-db without replace": {"--career-db", "/x/career.db"},
		"cap zero":                  {"--replace", "--max-delete", "0"},
		"cap over the ceiling":      {"--replace", "--max-delete", fmt.Sprint(kbsync.MaxDeletesCeiling + 1)},
		"relative career-db":        {"--replace", "--career-db", "career.db"},
	} {
		base := []string{"kb", "sync", "--bank", bank, "--rkb", rkb}
		mustCode(t, 2, append(base, args...)...)
		_ = name
	}
	if len(f.calls) != 0 || r.backups != 0 {
		t.Fatalf("a refused flag started work: %v, %d backups", f.calls, r.backups)
	}
	// A --keep that names no orphan is refused after DevPrism is read, before anything is sent.
	_, e := mustCode(t, 1, "kb", "sync", "--replace", "--keep", "no_such_block", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "--keep") || f.mutating() {
		t.Fatalf("stderr %q calls %v", e, f.calls)
	}
}

// A plain dry run says which listed entries and bullets the rule withheld, by
// id and reason, and prints neither bullet text nor the header.
func TestKBSyncDryRunReportsWhatTheRuleWithheld(t *testing.T) {
	bank, rkb, _ := kbApproved(t, "")
	o, _ := mustCode(t, 0, "kb", "sync", "--dry-run", "--bank", bank, "--rkb", rkb)
	for _, want := range []string{
		"3 add",
		"withheld entry  beta-proj: no bullet of it is eligible",
		"ineligible      b3: claim clmRag is flagged CAUTION",
		"ineligible      b5:",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	for _, leak := range []string{"person@example.invalid", "Synthetic Person", "Reached 92%", "draft nobody read"} {
		if strings.Contains(o, leak) {
			t.Errorf("dry run printed %q", leak)
		}
	}
}
