package resume

import (
	"context"
	"testing"
)

// DevPrism answers 0% when it extracted no must-have (0 of 0). Gap must say so
// instead of handing the ranker a measured-looking zero.
func TestGapFlagsNoMustHaves(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	g, err := c.Gap(context.Background(), "NOMUST", "")
	if err != nil {
		t.Fatal(err)
	}
	if !g.NoMustHaves || g.CoveragePct != 0 || len(g.MustHaves) != 0 {
		t.Fatalf("gap %+v", g)
	}
	// A normal answer with must-haves is a measurement.
	g, err = c.Gap(context.Background(), "a job", "")
	if err != nil {
		t.Fatal(err)
	}
	if g.NoMustHaves {
		t.Fatalf("gap with must-haves flagged unknown: %+v", g)
	}
}
