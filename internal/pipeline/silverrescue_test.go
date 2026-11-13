package pipeline

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

var silverRule = SilverRule{MinScore: 0.7, MinGames: 4, MaxPerDay: 10}

func never(ledger.Record) bool { return false }

// silverDropped is a posting the decision model dropped as staff_plus under
// title-reading rules (so a later title can drop it too), on a watched board.
func silverDropped(t *testing.T) (*Runner, *ledger.File, posting.Key) {
	t.Helper()
	r, ft, l, k := titleRunner(t, "Software Engineer", titleRules)
	ft.ans = decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority":   jev("staff_plus", 0.9124),
		"role_fit":    jev("mismatch_seniority", 0.5958),
		"sponsorship": jev("not_stated", 0.8847),
	}}
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("setup: %v, status %s", err, rec(t, l, k).Status)
	}
	return r, l, k
}

// A model drop the auditor wants, with no board listing to wait for (a scope
// that defers nothing), goes to Unsure at once with its readings and the
// auditor's stamp, holds through the next advance though the drop rule still
// holds, and is not rescued twice.
func TestRescueSilverDrops(t *testing.T) {
	r, l, k := silverDropped(t)
	at := t0.Add(2 * time.Hour)
	picks := map[posting.Key]SilverPick{k: {Score: 0.82, Games: 5, Picked: 4}}

	out, err := RescueSilverDrops(ctx, l, at, picks, silverRule, never, reviewScope(), true)
	if err != nil || len(out.Rescued) != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %+v (%v), status %s", out, err, rec(t, l, k).Status)
	}
	out, err = RescueSilverDrops(ctx, l, at, picks, silverRule, never, reviewScope(), false)
	if err != nil || !slices.Equal(out.Rescued, []posting.Key{k}) || len(out.Deferred) != 0 || out.Today != 0 {
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
	again, err := RescueSilverDrops(ctx, l, at.Add(time.Hour), picks, silverRule, never, reviewScope(), false)
	if err != nil || len(again.Rescued) != 0 || again.Today != 1 {
		t.Fatalf("second call: %+v (%v)", again, err)
	}
}

// The reason for the pending reopen: a drop on a watched board is closed, so
// the rescue cannot know the role is still listed. It waits, still dropped, as
// a pending "silver" reopen that counts against the day's cap, and goes to
// Unsure when the board's next full listing shows it, stamped with the day it
// was decided.
func TestSilverRescueWaitsForItsBoardToListItAgain(t *testing.T) {
	r, l, k := silverDropped(t)
	at := t0.Add(2 * time.Hour)
	picks := map[posting.Key]SilverPick{k: {Score: 0.9, Games: 6}}
	s := NewPendingReopens()

	dry, err := RescueSilverDrops(ctx, l, at, picks, silverRule, never, deferScope(s), true)
	if err != nil || !slices.Equal(dry.Deferred, []posting.Key{k}) || s.Len() != 0 {
		t.Fatalf("dry run: %+v (%v), %d pending", dry, err, s.Len())
	}
	out, err := RescueSilverDrops(ctx, l, at, picks, silverRule, never, deferScope(s), false)
	if err != nil || !slices.Equal(out.Rescued, []posting.Key{k}) || !slices.Equal(out.Deferred, []posting.Key{k}) {
		t.Fatalf("rescue: %+v (%v)", out, err)
	}
	if st := rec(t, l, k).Status; st != posting.Dropped || s.Items[k].Class != reopenClassSilver {
		t.Fatalf("after marking: %s, pending %+v; want still dropped and a silver reopen", st, s.Items[k])
	}
	// Marking again the same day adds nothing, and the mark counts against the cap.
	again, err := RescueSilverDrops(ctx, l, at.Add(time.Minute), picks, silverRule, never, deferScope(s), false)
	if err != nil || len(again.Rescued) != 0 || again.Today != 1 || again.Left[silverLeftPending] != 1 {
		t.Fatalf("second mark: %+v (%v)", again, err)
	}

	sum, so := sighting(t, l, s, r.Policy, at.Add(24*time.Hour), true, listedAgain(t, l, k))
	if so.Applied != 1 || s.Len() != 0 || !so.Changed() {
		t.Fatalf("sighting: %+v %+v, %d pending", sum, so, s.Len())
	}
	got := rec(t, l, k)
	if got.Status != posting.Unsure || !strings.HasPrefix(got.Annotations[AnnSilverRescue], at.UTC().Format(time.DateOnly)) ||
		got.Annotations["triage.seniority"] != "staff_plus" {
		t.Fatalf("after sighting: %s %v", got.Status, got.Annotations)
	}
}

// At the sighting the listed text is judged as a fresh triage would judge it
// first: a title the title rules now drop keeps it dropped, and the pending
// reopen is spent.
func TestSilverRescueSightingRejudgesTheListedText(t *testing.T) {
	r, l, k := silverDropped(t)
	s := NewPendingReopens()
	if _, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: {Score: 0.9, Games: 6}}, silverRule, never, deferScope(s), false); err != nil || s.Len() != 1 {
		t.Fatalf("mark: %v, %d pending", err, s.Len())
	}
	listed := listedAgain(t, l, k)
	listed.Title = "Staff Software Engineer" // the title rules read staff_plus, which the rules drop
	_, so := sighting(t, l, s, r.Policy, t0.Add(2*time.Hour), true, listed)
	if so.Applied != 0 || so.Cleared != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("sighting: %+v, %d pending, status %s", so, s.Len(), rec(t, l, k).Status)
	}
}

// A drop on a board no longer watched stays dropped: no listing would age it.
func TestSilverRescueLeavesUnwatchedBoards(t *testing.T) {
	_, l, k := silverDropped(t)
	sc := reviewScope()
	sc.Watched = func(posting.Key) bool { return false }
	out, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: {Score: 0.9, Games: 6}}, silverRule, never, sc, false)
	if err != nil || len(out.Rescued) != 0 || out.Left[silverLeftStale] != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v)", out, err)
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
			_, l, k := silverDropped(t)
			out, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: tc.pick}, silverRule, tc.protected, reviewScope(), false)
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
	out, err := RescueSilverDrops(ctx, l, t0.Add(time.Hour), map[posting.Key]SilverPick{k: {Score: 0.95, Games: 9}}, silverRule, never, reviewScope(), false)
	if err != nil || len(out.Rescued) != 0 || out.Left[silverLeftNotModel] != 1 {
		t.Fatalf("%+v (%v)", out, err)
	}
}

// The cap counts today's earlier rescues, written or pending, and stops there;
// tomorrow it starts over.
func TestRescueSilverDropsCap(t *testing.T) {
	_, l, k := silverDropped(t)
	at := t0.Add(time.Hour)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: "gh:other:1", Status: posting.Unsure,
		Posting:     &posting.Posting{Key: "gh:other:1", ATS: "gh", Company: "Acme", Title: "x", Description: "y"},
		Annotations: map[string]string{AnnSilverRescue: at.UTC().Format(time.DateOnly) + " score=0.900 games=5"}}); err != nil {
		t.Fatal(err)
	}
	picks := map[posting.Key]SilverPick{k: {Score: 0.9, Games: 5}}
	one := silverRule
	one.MaxPerDay = 1
	out, err := RescueSilverDrops(ctx, l, at, picks, one, never, reviewScope(), false)
	if err != nil || len(out.Rescued) != 0 || out.Today != 1 || out.CapLeft != 0 || out.Left[silverLeftCap] != 1 {
		t.Fatalf("over the cap: %+v (%v)", out, err)
	}
	out, err = RescueSilverDrops(ctx, l, at.Add(24*time.Hour), picks, one, never, reviewScope(), true)
	if err != nil || len(out.Rescued) != 1 || out.Today != 0 {
		t.Fatalf("next day: %+v (%v)", out, err)
	}
	// A pending silver reopen marked today counts as well.
	s := NewPendingReopens()
	s.Items["gh:other:2"] = PendingReopen{Class: reopenClassSilver, At: at.Add(24 * time.Hour), Reason: "x"}
	out, err = RescueSilverDrops(ctx, l, at.Add(24*time.Hour), picks, one, never, deferScope(s), true)
	if err != nil || out.Today != 1 || len(out.Rescued) != 0 {
		t.Fatalf("a pending mark today: %+v (%v)", out, err)
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
	_, l, _ := silverDropped(t)
	if _, err := RescueSilverDrops(ctx, l, t0, nil, silverRule, nil, reviewScope(), true); err == nil {
		t.Error("no protected-drop test accepted")
	}
	sc := reviewScope()
	sc.Defer = sc.Watched
	if _, err := RescueSilverDrops(ctx, l, t0, nil, silverRule, never, sc, true); err == nil {
		t.Error("a deferring scope with nowhere to keep its marks accepted")
	}
}
