package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w2aHash(c *Composed, vals map[string]string) string { return c.resumeHash(vals) }

// resumeHash must change when any field a renderer prints, or any claim value
// the resume uses, changes: otherwise a claims.json made before the edit still
// reads as current.

func TestW2AResumeHashStalePerField(t *testing.T) {
	_, vals := w2aEnv(t)
	base := w2aHash(w2aResume(), vals)
	if again := w2aHash(w2aResume(), vals); again != base {
		t.Fatalf("hash is not deterministic: %s vs %s", base, again)
	}
	mut := map[string]func(c *Composed){
		"skills line text":  func(c *Composed) { c.Sections[0].Lines[0].Text = "Go, Rust and {{clmLine}} jobs" },
		"skills line label": func(c *Composed) { c.Sections[0].Lines[0].Label = "Languages" },
		"entry title":       func(c *Composed) { c.Sections[1].Entries[0].Title = "Other engine {{clmTitle}}" },
		"entry org":         func(c *Composed) { c.Sections[1].Entries[0].Org = "Initech {{clmOrg}}" },
		"entry dates":       func(c *Composed) { c.Sections[1].Entries[0].Dates = "{{clmDates}} years" },
		"entry stack":       func(c *Composed) { c.Sections[1].Entries[0].Stack = "{{clmStack}} shards" },
		"entry location":    func(c *Composed) { c.Sections[1].Entries[0].Location = "Remote" },
		"entry sub":         func(c *Composed) { c.Sections[1].Entries[0].Sub = "side project" },
		"entry lead":        func(c *Composed) { c.Sections[1].Entries[0].Lead = "A lead line" },
		"entry layout":      func(c *Composed) { c.Sections[1].Entries[0].Layout = "stacked" },
		"entry members":     func(c *Composed) { c.Sections[1].Entries[0].Members = []string{"x"} },
		"header headline":   func(c *Composed) { c.Header.Headline = "Cut spend by {{clmHead}}" },
		"header contact":    func(c *Composed) { c.Header.Contact = []string{"other@b.c"} },
		"header name":       func(c *Composed) { c.Header.Name = "Other Person" },
		"section title":     func(c *Composed) { c.Sections[0].Title = "Tooling" },
		"section lead":      func(c *Composed) { c.Sections[0].Lead = "Lead text" },
		"bullet text":       func(c *Composed) { c.Sections[1].Entries[0].Bullets[0].Text = w2aBullet + " today" },
		"bullet id":         func(c *Composed) { c.Sections[1].Entries[0].Bullets[0].ID = "b9" },
	}
	for name, f := range mut {
		c := w2aResume()
		f(c)
		if got := w2aHash(c, vals); got == base {
			t.Errorf("editing the %s does not change the resume hash", name)
		}
	}
	// a claim value the resume uses, in any field
	for id := range vals {
		v2 := map[string]string{}
		for k, v := range vals {
			v2[k] = v
		}
		v2[id] += "0"
		if got := w2aHash(w2aResume(), v2); got == base {
			t.Errorf("editing the value of %s does not change the resume hash", id)
		}
	}
}

func TestW2AResumeHashFieldBoundaries(t *testing.T) {
	a := &Composed{Header: Header{Name: "ab", Headline: "c"}}
	b := &Composed{Header: Header{Name: "a", Headline: "bc"}}
	if w2aHash(a, nil) == w2aHash(b, nil) {
		t.Errorf("moving a character across a field boundary must change the hash")
	}
	if w2aHash(&Composed{}, nil) == "" {
		t.Errorf("an empty resume still has a hash")
	}
}

func TestW2AFinalGateSeesClaimsTexEdit(t *testing.T) {
	root, vals := w2aEnv(t)
	c := w2aResume()
	cf := w2aClaims(c, vals)
	stale := func() bool {
		for _, e := range finalPreconditions(c, cf, "") {
			if strings.Contains(e, "exported for different content") {
				return true
			}
		}
		return false
	}
	if stale() {
		t.Fatalf("a fresh export must not read as stale: %v", finalPreconditions(c, cf, ""))
	}
	p := filepath.Join(root, cfg.ClaimsTex[0])
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), "{"+vals["clmLine"]+"}", "{"+vals["clmLine"]+"0}", 1)
	if edited == string(b) {
		t.Fatal("test setup: the value was not found in Claims.tex")
	}
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if !stale() {
		t.Errorf("a Claims.tex value edited after the export must make claims.json stale")
	}
}

func TestW2AFinalGateUnreadableClaimsTexIsAnError(t *testing.T) {
	root, vals := w2aEnv(t)
	c := w2aResume()
	cf := w2aClaims(c, vals)
	if err := os.Remove(filepath.Join(root, cfg.ClaimsTex[0])); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w2aJoin(finalPreconditions(c, cf, "")), "Claims.tex cannot be read") {
		t.Errorf("a check that could not run must not read as a pass")
	}
}
