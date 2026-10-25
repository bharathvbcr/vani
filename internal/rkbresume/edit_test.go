package rkbresume

import (
	"encoding/json"
	"strings"
	"testing"
)

// roundTrip writes ch in the owner-edit shape and reads it back through the
// same decode and checks an owner's edit goes through, returning the new choice
// and its two selection files (rkb's, from the original and from the copy).
func roundTrip(t *testing.T, b *builder, p pool, ch choice) (choice, []byte, []byte, []byte) {
	t.Helper()
	edit, err := ch.editJSON(b.base)
	if err != nil {
		t.Fatalf("editJSON: %v", err)
	}
	back, err := b.ownerChoice(edit, p, testJD)
	if err != nil {
		t.Fatalf("ownerChoice(editJSON) = %v\nedit: %s", err, edit)
	}
	want, err := b.base.selectionJSON(b.cfg.Bank, ch)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.base.selectionJSON(b.cfg.Bank, back)
	if err != nil {
		t.Fatal(err)
	}
	return back, want, got, edit
}

// secExpBare names exp-b with b2: projectBuilder's base lists exp-b bare, so only the base choice holds it as a heading.
const secExpBare = `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b2"]}]}`

func projectBuilder(t *testing.T) (*fixture, *builder, pool) {
	t.Helper()
	f := newFixture(t)
	addProjects(t, f)
	editJSON(t, f.base, func(m map[string]any) { // exp-b listed bare: a heading the edit must keep
		exp := m["sections"].([]any)[1].(map[string]any)
		exp["entries"].([]any)[1] = map[string]any{"entry": "exp-b", "bullets": []string{}}
	})
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return f, b, b.buildPool(func(string) bool { return true })
}

// The base choice, a tailored choice (projects reordered, keywords) and an
// owner's choice with overrides each survive editJSON then ownerChoice: the
// same selection file, byte for byte.
func TestEditJSONRoundTripsEveryKindOfChoice(t *testing.T) {
	_, b, p := projectBuilder(t)
	base := b.base.baseChoice(b.el.Allowed, b.isCont)
	tailored, err := b.parseChoice(replyWith(secSummary, secExpBare, secGPU, secAgent), p, testJD)
	if err != nil {
		t.Fatalf("parseChoice: %v", err)
	}
	owner, err := b.ownerChoice([]byte(replyWith(secSummary,
		`{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"],"title":"Staff Engineer","stack":"Go, Rust","lead":"-","dates":"2022 – 2024","org":"Acme Labs"},{"entry":"exp-b","bullets":["b2"]}]}`,
		secAgent)), p, testJD)
	if err != nil {
		t.Fatalf("ownerChoice: %v", err)
	}
	for name, ch := range map[string]choice{"base": base, "tailored": tailored, "owner with overrides": owner} {
		t.Run(name, func(t *testing.T) {
			back, want, got, edit := roundTrip(t, b, p, ch)
			if string(want) != string(got) {
				t.Fatalf("selection changed across the round trip\nwant: %s\ngot:  %s\nedit: %s", want, got, edit)
			}
			// And it is stable: writing the copy again gives the same edit.
			again, err := back.editJSON(b.base)
			if err != nil || string(again) != string(edit) {
				t.Fatalf("editJSON is not stable: %v\n%s\n%s", err, edit, again)
			}
		})
	}
}

// The shape is the edit's keys and nothing else: no header, bank, meta or path
// can leave through it, and no personal data is in it.
func TestEditJSONCarriesOnlyEditKeys(t *testing.T) {
	_, b, _ := projectBuilder(t)
	edit, err := b.base.baseChoice(b.el.Allowed, b.isCont).editJSON(b.base)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(edit, &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		if k != "sections" && k != "jd_keywords" {
			t.Errorf("top-level key %q", k)
		}
	}
	if err := exactKeys(edit, editKeys); err != nil {
		t.Errorf("keys outside the edit's: %v", err)
	}
	for _, bad := range []string{ownerName, ownerContact, "bank", "header", "meta", b.cfg.Bank} {
		if bad != "" && strings.Contains(string(edit), bad) {
			t.Errorf("edit holds %q: %s", bad, edit)
		}
	}
}

// A base that carries keywords of its own (the fixture's meta.jd_keywords) writes
// none: a later render of the edit must not check them against a posting they
// were never taken from.
func TestEditJSONWritesNoKeywordsWhenTheChoiceHasNone(t *testing.T) {
	_, b, _ := projectBuilder(t)
	edit, err := b.base.baseChoice(b.el.Allowed, b.isCont).editJSON(b.base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(edit), "jd_keywords") {
		t.Fatalf("keywords in the edit of a choice with none: %s", edit)
	}
}

// A section the choice leaves empty is dropped unless its place matters: an
// empty project section still sits in the order, so the order survives.
func TestEditJSONKeepsAnEmptyProjectSectionInTheOrder(t *testing.T) {
	_, b, p := projectBuilder(t)
	ch, err := b.ownerChoice([]byte(replyWith(secSummary, secExpBare, secGPU)), p, testJD) // Agent Systems left out
	if err != nil {
		t.Fatal(err)
	}
	edit, err := ch.editJSON(b.base)
	if err != nil {
		t.Fatal(err)
	}
	var d editDoc
	if err := json.Unmarshal(edit, &d); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, s := range d.Sections {
		titles = append(titles, s.Title)
	}
	if got := strings.Join(titles, ","); got != "Summary,Experience,GPU Systems,Agent Systems" {
		t.Fatalf("sections = %s", got)
	}
	_, want, got, _ := roundTrip(t, b, p, ch)
	if string(want) != string(got) {
		t.Fatalf("selection changed:\n%s\n%s", want, got)
	}
}

// A choice too large for the edit's own bound is refused, not truncated.
func TestEditJSONRefusesAnOversizedEdit(t *testing.T) {
	_, b, _ := projectBuilder(t)
	ch := b.base.baseChoice(b.el.Allowed, b.isCont)
	ch.keywords = []string{strings.Repeat("k", maxEditBytes)}
	if _, err := ch.editJSON(b.base); err == nil {
		t.Fatal("an edit over maxEditBytes was written")
	}
}
