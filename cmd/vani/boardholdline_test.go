package main

import (
	"strings"
	"testing"
	"time"
)

// The attention line for a board that lists short says how long what it leaves
// out is held. That is liveness.mass_hold_hours, recorded with the run, not a
// fixed fortnight: a run configured for a 3-day hold says 3d, a default run 14d.
func TestShortBoardAttentionLineNamesTheConfiguredHold(t *testing.T) {
	since := bhNow.Add(-40 * 24 * time.Hour)
	item := wdRead(wdBoard("a/B"))
	item.Short = 5
	item.ShortSince = &since
	for name, tc := range map[string]struct {
		hours int
		want  string
	}{
		"default":    {0, "held 14d from their first miss"},
		"three days": {72, "held 3d from their first miss"},
		"a day":      {24, "held 1d from their first miss"},
		"twenty hrs": {20, "held 20h from their first miss"},
	} {
		rep := &boardsReport{Configured: 1, Read: 1, Items: []boardReport{item}, MassHoldHours: tc.hours}
		got := boardAttentionLine(rep, bhNow, true)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not say %q", name, got, tc.want)
		}
	}
}
