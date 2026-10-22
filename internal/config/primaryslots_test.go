package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
)

func TestLappiPrimarySlotsRules(t *testing.T) {
	a, b := jdtriage.Families[0].Slot.Name, jdtriage.Families[1].Slot.Name
	all := make([]string, len(jdtriage.Families))
	for i, f := range jdtriage.Families {
		all[i] = f.Slot.Name
	}
	check := func(name string, mut func(c *Config), frag string) {
		t.Helper()
		c := valid()
		mut(&c)
		err := c.Validate()
		switch {
		case frag == "" && err != nil:
			t.Errorf("%s: rejected: %v", name, err)
		case frag != "" && (err == nil || !strings.Contains(err.Error(), frag)):
			t.Errorf("%s: err = %v, want %q", name, err, frag)
		}
	}
	check("slots alone", func(c *Config) { c.Decide.Lappi = &Lappi{PrimarySlots: []string{a}} }, "")
	check("slots with shadow lappi", func(c *Config) {
		c.Decide.Lappi = &Lappi{PrimarySlots: []string{a, b}}
		c.Decide.Shadow = "lappi"
	}, "")
	check("both primary and slots", func(c *Config) { c.Decide.Lappi = &Lappi{Primary: true, PrimarySlots: []string{a}} }, "exclusive")
	check("unknown slot names the slot", func(c *Config) { c.Decide.Lappi = &Lappi{PrimarySlots: []string{a, "nonsense_slot"}} }, `"nonsense_slot"`)
	check("duplicate slot", func(c *Config) { c.Decide.Lappi = &Lappi{PrimarySlots: []string{a, a}} }, "listed twice")
	check("every slot with shadow lappi", func(c *Config) {
		c.Decide.Lappi = &Lappi{PrimarySlots: all}
		c.Decide.Shadow = "lappi"
	}, "nothing to read")
	check("every slot alone", func(c *Config) { c.Decide.Lappi = &Lappi{PrimarySlots: all} }, "")
	// The old rules are unchanged.
	check("primary with shadow lappi", func(c *Config) {
		c.Decide.Lappi = &Lappi{Primary: true}
		c.Decide.Shadow = "lappi"
	}, "exclusive")
	check("unused lappi", func(c *Config) { c.Decide.Lappi = &Lappi{} }, "configured but unused")
}

func TestLappiPromotedSlotsFollowFixtureOrder(t *testing.T) {
	a, b := jdtriage.Families[0].Slot.Name, jdtriage.Families[1].Slot.Name
	got := (&Lappi{PrimarySlots: []string{b, a}}).PromotedSlots()
	if !slices.Equal(got, []string{a, b}) {
		t.Fatalf("promoted %v", got)
	}
	if (&Lappi{Primary: true}).PromotedSlots() != nil || (*Lappi)(nil).PromotedSlots() != nil {
		t.Fatal("primary or nil reports promoted slots")
	}
}

func TestLappiPrimarySlotsRoundTrip(t *testing.T) {
	a, b := jdtriage.Families[0].Slot.Name, jdtriage.Families[2].Slot.Name
	c := valid()
	c.Decide.Lappi = &Lappi{PrimarySlots: []string{a, b}}
	p := filepath.Join(t.TempDir(), "c.json")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decide.Lappi == nil || !slices.Equal(got.Decide.Lappi.PrimarySlots, []string{a, b}) || got.Decide.Lappi.Primary {
		t.Fatalf("round trip: %+v", got.Decide.Lappi)
	}
}
