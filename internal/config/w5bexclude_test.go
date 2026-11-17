package config

import (
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestW5bRevisionTracksContent(t *testing.T) {
	a := Exclude{Titles: []string{"intern"}, Companies: []string{"Acme"}}
	if a.Revision() != a.Clone().Revision() {
		t.Fatal("equal lists, different revisions")
	}
	if (Exclude{}).Revision() != (Exclude{Titles: []string{}}).Revision() {
		t.Error("nil and empty lists differ")
	}
	// The same entry in another list, a moved boundary and a reorder all differ.
	for name, b := range map[string]Exclude{
		"other list":  {Locations: []string{"intern"}, Companies: []string{"Acme"}},
		"boundary":    {Titles: []string{"inter", "n"}, Companies: []string{"Acme"}},
		"reordered":   {Titles: []string{"b", "a"}},
		"case":        {Titles: []string{"Intern"}, Companies: []string{"Acme"}},
		"extra entry": {Titles: []string{"intern", "x"}, Companies: []string{"Acme"}},
	} {
		if a.Revision() == b.Revision() {
			t.Errorf("%s: same revision as %+v", name, a)
		}
	}
	if (Exclude{Titles: []string{"a", "b"}}).Revision() == (Exclude{Titles: []string{"b", "a"}}).Revision() {
		t.Error("order is not part of the revision")
	}
}

func TestW5bMerge(t *testing.T) {
	cur := Exclude{Titles: []string{"intern", "trainee"}, Locations: []string{"New York"}}
	got := cur.Merge(
		Exclude{Titles: []string{"Co-op", " INTERN", ""}, Locations: []string{"new  york", "Texas"}},
		Exclude{Titles: []string{"Trainee"}, Companies: []string{"nobody"}})
	want := Exclude{Titles: []string{"intern", "Co-op"}, Locations: []string{"New York", "Texas"}}
	if !slices.Equal(got.Titles, want.Titles) || !slices.Equal(got.Locations, want.Locations) || got.Companies != nil {
		t.Errorf("Merge = %+v, want %+v", got, want)
	}
	if !slices.Equal(cur.Titles, []string{"intern", "trainee"}) {
		t.Errorf("Merge changed its receiver: %+v", cur)
	}
	// Removing everything leaves nil, so the key leaves the file.
	if got := cur.Merge(Exclude{}, cur); got.Titles != nil || got.Locations != nil || got.Revision() != (Exclude{}).Revision() {
		t.Errorf("remove all = %+v", got)
	}
	// An add and a remove of one entry: the add wins (remove runs first).
	if got := cur.Merge(Exclude{Titles: []string{"intern"}}, Exclude{Titles: []string{"intern"}}); !slices.Equal(got.Titles, []string{"trainee", "intern"}) {
		t.Errorf("remove then add = %+v", got.Titles)
	}
}

// Many writers each add one entry through Update with Merge: none is lost.
func TestW5bConcurrentMergesKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Create(path, Default()); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(path, func(c *Config) error {
				c.Exclude = c.Exclude.Merge(Exclude{Companies: []string{"co" + string(rune('a'+i))}}, Exclude{})
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Exclude.Companies); got != n {
		t.Fatalf("%d entries from %d writers: %q", got, n, c.Exclude.Companies)
	}
}
