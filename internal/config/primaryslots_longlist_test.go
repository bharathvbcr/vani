package config

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
)

// A very long list is refused, and the refusal stays small enough to read: one
// line per bad entry is megabytes of error for a broken or hostile file.
func TestAttackPrimarySlotsLongListError(t *testing.T) {
	for _, n := range []int{1000, 100000} {
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("slot_%d", i)
		}
		c := valid()
		c.Decide.Lappi = &Lappi{PrimarySlots: names}
		err := c.Validate()
		if err == nil {
			t.Fatalf("n=%d accepted", n)
		}
		if got := len(err.Error()); got > 16<<10 {
			t.Errorf("n=%d: error is %d bytes, unbounded in the list length", n, got)
		}
	}
	dup := make([]string, 50000)
	for i := range dup {
		dup[i] = jdtriage.Families[0].Slot.Name
	}
	c := valid()
	c.Decide.Lappi = &Lappi{PrimarySlots: dup}
	err := c.Validate()
	if err == nil {
		t.Fatal("duplicates accepted")
	}
	if got := len(err.Error()); got > 16<<10 {
		t.Errorf("50000 duplicates: error is %d bytes", got)
	}
}
