package rkbresume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func (f *fixture) pool() (PoolView, error) {
	f.t.Helper()
	return Pool(context.Background(), f.cfg, renderReq)
}

// continuationOfAny reports whether id is a continuation under some entry.
func continuationOfAny(b *builder, id string) bool {
	for entry := range b.cont {
		if b.cont.is(entry, id) {
			return true
		}
	}
	return false
}

func poolBulletIDs(v PoolView) map[string]PoolBullet {
	m := map[string]PoolBullet{}
	for _, b := range v.Summary {
		m[b.ID] = b
	}
	for _, e := range v.Entries {
		for _, b := range e.Bullets {
			m[b.ID] = b
		}
	}
	return m
}

// The pool lists exactly the bullets kbsync.Eligible lets through, with the
// wording rkb prints and its hash, and the continuation predecessor.
func TestPoolListsEligibleBulletsWithRkbWording(t *testing.T) {
	f := newFixture(t)
	v, err := f.pool()
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := poolBulletIDs(v)
	for id, ok := range b.el.Bullets {
		// A continuation whose predecessor is not offered under its entry is the one
		// kind of eligible bullet that is left out (the model is never offered it either).
		if ok && got[id].ID == "" && !continuationOfAny(b, id) {
			t.Errorf("eligible bullet %s is not in the pool", id)
		}
	}
	for id := range got {
		if !b.el.Bullets[id] {
			t.Errorf("bullet %s is in the pool but not eligible", id)
		}
	}
	if _, ok := got["x1"]; ok {
		t.Error("an unreviewed bullet is in the pool")
	}
	for id, pb := range got {
		plain := kbsync.RKBPlain(bulletTexts[id], claimValues)
		if pb.Text != plain || pb.TextHash != kbsync.WordingHash(plain) {
			t.Errorf("%s: text %q hash %q, want %q %q", id, pb.Text, pb.TextHash, plain, kbsync.WordingHash(plain))
		}
	}
	if strings.Contains(got["a1"].Text, "{{") || !strings.Contains(got["a1"].Text, "40%") {
		t.Errorf("a1 text = %q: claims are resolved", got["a1"].Text)
	}
	follows := 0
	for _, e := range v.Entries {
		for _, pb := range e.Bullets {
			if want := b.cont.pred(e.ID, pb.ID); pb.Follows != want {
				t.Errorf("%s under %s follows %q, want %q", pb.ID, e.ID, pb.Follows, want)
			}
			if pb.Follows != "" {
				follows++
			}
		}
	}
	if follows == 0 {
		t.Error("the fixture has continuations and none is marked")
	}
	if len(v.Summary) == 0 || len(v.SummarySections) != 1 || v.SummarySections[0] != "Summary" {
		t.Errorf("summary %v in %v", v.Summary, v.SummarySections)
	}
}

// Each entry says which sections it may go in, by title.
func TestPoolSaysWhichSectionsEachEntryMayGoIn(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	for _, e := range v.Entries {
		sections[e.ID] = strings.Join(e.Sections, ",")
	}
	if sections["exp-a"] != "Experience" || sections["exp-b"] != "Experience" {
		t.Errorf("jobs: %v", sections)
	}
	if sections["proj-a"] != "Agent Systems,GPU Systems" || sections["proj-b"] != "Agent Systems,GPU Systems" {
		t.Errorf("projects: %v", sections)
	}
	for _, e := range v.Entries {
		if e.Title == "" || len(e.Sections) == 0 || len(e.Bullets) == 0 {
			t.Errorf("entry %+v", e)
		}
	}
}

// The pool states which sections may be reordered and where an entry may stay as a
// heading, by the rules a build applies, so the editor needs no inference.
func TestPoolStatesTheReorderableSectionsAndWhereAnEntryMayStayAsAHeading(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	editJSON(t, f.bank, func(m map[string]any) {
		m["entries"].(map[string]any)["proj-bare"] = map[string]any{"title": "Listed Bare", "bullets": []string{}}
	})
	editJSON(t, f.base, func(m map[string]any) {
		for _, s := range m["sections"].([]any) {
			sec := s.(map[string]any)
			if sec["title"] == "Agent Systems" {
				sec["entries"] = append(sec["entries"].([]any), map[string]any{"entry": "proj-bare", "bullets": []string{}})
			}
		}
	})
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(v.ReorderableSections, ","); got != "Agent Systems,GPU Systems" {
		t.Errorf("reorderable = %q: the project run after Experience, and nothing else", got)
	}
	var heads []string
	for _, h := range v.Headings {
		heads = append(heads, h.Section+"/"+h.Entry)
	}
	// Every job may stay as a heading; a project only where the base lists it bare.
	want := "Experience/exp-a,Experience/exp-b,Experience/exp-c,Agent Systems/proj-bare"
	if got := strings.Join(heads, ","); got != want {
		t.Errorf("headings = %q, want %q", got, want)
	}
	// The pool agrees with the rule a build applies, for every entry in every section.
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, h := range v.Headings {
		listed[h.Section+"/"+h.Entry] = true
	}
	for _, e := range v.Entries {
		for i, s := range b.base.sections {
			if s.choosable && s.kind == "entries" && b.canHold(i, e.ID) && listed[s.title+"/"+e.ID] != b.base.keepsHeading(i, e.ID) {
				t.Errorf("%s in %s: pool says %v, keepsHeading says %v", e.ID, s.title, listed[s.title+"/"+e.ID], b.base.keepsHeading(i, e.ID))
			}
		}
	}
	// Never null on the wire, even with nothing to say.
	raw, _ := json.Marshal(PoolView{Summary: []PoolBullet{}, SummarySections: []string{}, Entries: []PoolEntry{}, ReorderableSections: []string{}, Headings: []PoolHeading{}})
	if strings.Contains(string(raw), `"reorderable_sections":null`) || strings.Contains(string(raw), `"headings":null`) {
		t.Errorf("null on the wire: %s", raw)
	}
}

func TestPoolWithNoProjectSectionsHasNoReorderableOnesAndNeverNull(t *testing.T) {
	f := newFixture(t) // Experience only: nothing to reorder
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	if v.ReorderableSections == nil || len(v.ReorderableSections) != 0 || v.Headings == nil || len(v.Headings) == 0 {
		t.Errorf("reorderable %#v, headings %#v", v.ReorderableSections, v.Headings)
	}
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"reorderable_sections":[]`) {
		t.Errorf("wire: %s", raw)
	}
}

// The base's choice is in the edit shape: Render's own reader accepts it and it
// is the base choice.
func TestPoolBaseIsTheBaseChoiceInTheEditShape(t *testing.T) {
	f := newFixture(t)
	addProjects(t, f)
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := b.buildPoolSized(func(string) bool { return true }, 0)
	ch, err := b.ownerChoice(v.Base, p, testJD)
	if err != nil {
		t.Fatalf("ownerChoice(base) = %v\n%s", err, v.Base)
	}
	want, _ := b.base.selectionJSON(b.cfg.Bank, b.base.baseChoice(b.el.Allowed, b.isCont))
	got, _ := b.base.selectionJSON(b.cfg.Bank, ch)
	if string(want) != string(got) {
		t.Errorf("the pool's base is not the base choice\n%s\n%s", want, got)
	}
}

// No header and no path: the pool is bank bullets and entry titles only.
func TestPoolHoldsNoHeaderAndNoPath(t *testing.T) {
	f := newFixture(t)
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	for _, bad := range []string{ownerName, ownerContact, f.dir, "/fake", "header"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("pool holds %q", bad)
		}
	}
}

// Pool uses no model, runs no rkb render and takes no build lock.
func TestPoolUsesNoModelRendersNothingAndTakesNoLock(t *testing.T) {
	f := newFixture(t)
	sp := &scriptedProvider{prices: testRates, calls: []call{{text: validReply()}}}
	f.cfg.Provider, f.cfg.Model = sp, "m"
	f.cfg.LockDir = f.dir
	f.cfg.WorkDir = filepath.Join(f.dir, "work")
	held, err := filelock.TryAcquire(filepath.Join(f.dir, buildLockName)) // a paid build is running
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := f.pool(); err != nil {
		t.Fatalf("Pool while a build holds the lock: %v", err)
	}
	if sp.n != 0 {
		t.Errorf("the model was asked %d times", sp.n)
	}
	for _, step := range f.rkb.steps {
		if step != "claims-plain" {
			t.Errorf("rkb ran %q: Pool only exports claims", step)
		}
	}
	if got := workEntries(t, f.cfg.WorkDir); len(got) != 0 {
		t.Errorf("build folder left: %v", got)
	}
}

// Eligibility is the bank's now: a reworded bullet leaves the pool.
func TestPoolEligibilityIsFresh(t *testing.T) {
	f := newFixture(t)
	v, _ := f.pool()
	if _, ok := poolBulletIDs(v)["a1"]; !ok {
		t.Fatal("scenario: a1 is not in the pool")
	}
	editJSON(t, f.bank, func(m map[string]any) {
		m["bullets"].(map[string]any)["a1"].(map[string]any)["text"] = "reworded after review"
	})
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := poolBulletIDs(v)["a1"]; ok {
		t.Error("a bullet reworded since review is still offered")
	}
}

// The pool is the whole pool, not the model's byte-capped share, and a long
// bullet is not cut.
func TestPoolIsNotCappedLikeTheModelsShare(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("word ", 200) // 1000 characters
	var ids []string
	editJSON(t, f.bank, func(m map[string]any) {
		bl := m["bullets"].(map[string]any)
		eb := m["entries"].(map[string]any)["exp-b"].(map[string]any)
		list := eb["bullets"].([]any)
		for i := 0; i < 70; i++ {
			id := fmt.Sprintf("z%d", i)
			text := fmt.Sprintf("%s%d", long, i)
			bl[id] = map[string]any{"text": text, "review": reviewFor(text)}
			list = append(list, id)
			ids = append(ids, id)
		}
		eb["bullets"] = list
	})
	editJSON(t, f.other, func(m map[string]any) {
		exp := m["sections"].([]any)[1].(map[string]any)
		eb := exp["entries"].([]any)[1].(map[string]any)
		eb["bullets"] = append(append([]any{}, eb["bullets"].([]any)...), anyStrings(ids)...)
	})
	v, err := f.pool()
	if err != nil {
		t.Fatal(err)
	}
	got := poolBulletIDs(v)
	for i, id := range ids {
		if want := fmt.Sprintf("%s%d", long, i); got[id].Text != want {
			t.Fatalf("%s: %d characters of text, want the full %d", id, len(got[id].Text), len(want))
		}
	}
	// The same bank in a Render: an edit naming the last bullet is on offer.
	edit := editWith(`{"entry":"exp-a","bullets":["a3"]}`, `{"entry":"exp-b","bullets":["`+ids[len(ids)-1]+`"]}`)
	if _, err := f.render([]byte(edit), true); err != nil {
		t.Errorf("Render of a bullet past the model's cap: %v", err)
	}
}

func anyStrings(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// Cancelled, rkb down and a bad config are coded errors, never an empty pool.
func TestPoolFailuresAreCodedErrors(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Pool(ctx, f.cfg, renderReq)
		wantCode(t, err, CodeCancel)
	})
	t.Run("rkb down", func(t *testing.T) {
		f := newFixture(t)
		f.rkb.runErr = map[string]error{"claims-plain": errors.New("rkb: exec format error")}
		v, err := f.pool()
		wantCode(t, err, CodeKB)
		if len(v.Entries) != 0 || v.Base != nil {
			t.Errorf("pool after a failure: %+v", v)
		}
	})
	t.Run("bad config", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Bank = "bank.json"
		_, err := f.pool()
		wantCode(t, err, CodeConfig)
	})
	t.Run("no bank", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Bank = filepath.Join(f.dir, "gone.json")
		_, err := f.pool()
		wantCode(t, err, CodeKB)
	})
}
