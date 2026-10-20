package resume

import (
	"context"
	"errors"
	"testing"
)

// DevPrism reports a null coverage for a JD with no must-have skills. That is
// valid only when there are no must-haves (extractionEmpty, or no items); a
// null beside must-haves, a missing mustHave or a missing coverage key stays
// malformed. Vani reads both the old 0 and the new null as "no must-haves".
func TestGapNullCoverage(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	for _, jd := range []string{"NULLNOMUST", "NULLEMPTYITEMS", "NOMUST"} {
		g, err := c.Gap(context.Background(), jd, "")
		if err != nil {
			t.Fatalf("%s: %v", jd, err)
		}
		if !g.NoMustHaves || g.CoveragePct != 0 || len(g.MustHaves) != 0 {
			t.Errorf("%s: gap %+v, want NoMustHaves with no coverage", jd, g)
		}
	}
	for _, jd := range []string{"NULLWITHITEMS", "NULLNOMUSTHAVEKEY", "NOCOVERAGEKEY"} {
		if _, err := c.Gap(context.Background(), jd, ""); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", jd, err)
		}
	}
}

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
