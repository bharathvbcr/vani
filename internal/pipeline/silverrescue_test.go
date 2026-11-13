package pipeline

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var silverRule = SilverRule{MinScore: 0.7, MinGames: 4, MaxPerDay: 10}

func never(ledger.Record) bool { return false }

// A model drop the auditor wants goes to Unsure with its readings and the
// auditor's stamp, holds through the next advance though the drop rule still
// holds, and is not rescued twice.
func TestRescueSilverDrops(t *testing.T) {
	r, l, k := modelDropped(t)
	at := t0.Add(2 * time.Hour)
	picks := map[posting.Key]SilverPick{k: {Score: 0.82, Games: 5, Picked: 4}}

	out, err := RescueSilverDrops(ctx, l, at, picks, silverRule, never, true)
	if err != nil || len(out.Rescued) != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %+v (%v), status %s", out, err, rec(t, l, k).Status)
	}
	out, err = RescueSilverDrops(ctx, l, at, picks, silverRule, never, false)
	if err != nil || len(out.Rescued) != 1 || out.Rescued[0] != k || out.Today != 0 {
		t.Fatalf("rescue: %+v (%v)", out, err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Unsure || got.Annotations["triage.seniority"] != "staff_plus" ||
		!strings.HasPrefix(got.Annotations[AnnSilverRescue], "2026-") || !strings.Contains(got.Annotations[ledger.AnnTriageUnsure], "auditor") {
		t.Fatalf("after rescue: %s %v", got.Status, got.Annotations)
	}

	// The drop rule still matches the readings; the runner scores an Unsure
	// record rather than triaging it again, so the rescue holds.
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Explained {
		t.Fatalf("advance after rescue: %v, status %s", err, rec(t, l, k).Status)
	}
	// The stamp outlives the later events, so the cap still counts it.
	again, err := RescueSilverDrops(ctx, l, at.Add(time.Hour), picks, silverRule, never, false)
	if err != nil || len(again.Rescued) != 0 || again.Today != 1 {
		t.Fatalf("second call: %+v (%v)", again, err)
	}
}

// Every way a pick can fall short leaves the drop alone and says why.
func TestRescueSilverDropsPassesOver(t *testing.T) {
	for name, tc := range map[string]struct {
		pick      SilverPick
		protected func(ledger.Record) bool
		why       string
	}{
		"few games": {SilverPick{Score: 0.9, Games: 3}, never, silverLeftGames},
		"neither":   {SilverPick{Score: 0.9, Games: 6, Neither: 1}, never, silverLeftNeither},
		"low score": {SilverPick{Score: 0.69, Games: 6}, never, silverLeftScore},
		"NaN score": {SilverPick{Score: math.NaN(), Games: 6}, never, silverLeftScore},
		"protected": {SilverPick{Score: 0.9, Games: 6}, func(ledger.Record) bool { return true }, silverLeftProtected},
	} {
		t.Run(name, func(t *testing.T) {
			_, l, k := modelDropped(t)
			out, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: tc.pick}, silverRule, tc.protected, false)
			if err != nil || len(out.Rescued) != 0 || out.Left[tc.why] != 1 || rec(t, l, k).Status != posting.Dropped {
				t.Fatalf("%+v (%v), status %s", out, err, rec(t, l, k).Status)
			}
		})
	}
}

// A title drop is never rescued: Unsure would leave it unread.
func TestRescueSilverDropsLeavesTitleDrops(t *testing.T) {
	r, _, l, k := titleRunner(t, "Sales Manager", titleRules)
	if err := r.Advance(ctx, k); err != nil || !TitleDropped(rec(t, l, k)) {
		t.Fatalf("setup: %v", err)
	}
	out, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: {Score: 0.95, Games: 9}}, silverRule, never, false)
	if err != nil || len(out.Rescued) != 0 || out.Left[silverLeftNotModel] != 1 {
		t.Fatalf("%+v (%v)", out, err)
	}
}

// The cap counts today's earlier rescues and stops there; tomorrow it starts over.
func TestRescueSilverDropsCap(t *testing.T) {
	_, l, k := modelDropped(t)
	at := t0.Add(time.Hour)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: "gh:other:1", Status: posting.Unsure,
		Posting:     &posting.Posting{Key: "gh:other:1", ATS: "gh", Company: "Acme", Title: "x", Description: "y"},
		Annotations: map[string]string{AnnSilverRescue: at.UTC().Format(time.DateOnly) + " score=0.900 games=5"}}); err != nil {
		t.Fatal(err)
	}
	picks := map[posting.Key]SilverPick{k: {Score: 0.9, Games: 5}}
	one := silverRule
	one.MaxPerDay = 1
	out, err := RescueSilverDrops(ctx, l, at, picks, one, never, false)
	if err != nil || len(out.Rescued) != 0 || out.Today != 1 || out.CapLeft != 0 || out.Left[silverLeftCap] != 1 {
		t.Fatalf("over the cap: %+v (%v)", out, err)
	}
	out, err = RescueSilverDrops(ctx, l, at.Add(24*time.Hour), picks, one, never, true)
	if err != nil || len(out.Rescued) != 1 || out.Today != 0 {
		t.Fatalf("next day: %+v (%v)", out, err)
	}
}

func TestSilverRuleRefusals(t *testing.T) {
	for _, r := range []SilverRule{
		{MinScore: 0.5, MinGames: 4, MaxPerDay: 1},
		{MinScore: 1.1, MinGames: 4, MaxPerDay: 1},
		{MinScore: math.NaN(), MinGames: 4, MaxPerDay: 1},
		{MinScore: 0.8, MinGames: 2, MaxPerDay: 1},
		{MinScore: 0.8, MinGames: 4, MaxPerDay: 0},
		{MinScore: 0.8, MinGames: 4, MaxPerDay: MaxSilverRescuePerDay + 1},
	} {
		if r.Check() == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	_, l, _ := modelDropped(t)
	if _, err := RescueSilverDrops(ctx, l, t0, nil, silverRule, nil, true); err == nil {
		t.Error("no protected-drop test accepted")
	}
}
