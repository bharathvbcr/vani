package eval

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// disagreePool is 100 scored postings (score i) and 40 dropped ones in two
// classes, with teacher readings set so both cells have members: the teacher
// wants scored 0..9 (deciles 1 and 2) and dropped a:0..4, rejects scored 95..99
// (decile 10), and has also read some postings where it agrees with the engine.
func disagreePool() []AuditCandidate {
	cands := scoredPool(100)
	for i := range cands {
		switch {
		case i < 10:
			cands[i].Teacher = TeacherWants
		case i >= 95:
			cands[i].Teacher = TeacherRejects
		case i >= 90:
			cands[i].Teacher = TeacherWants // top decile, wanted: agreement
		case i >= 40 && i < 45:
			cands[i].Teacher = TeacherRejects // middle decile: no cell
		}
	}
	dropped := droppedPool(map[string]int{"a": 20, "b": 20})
	for i := range dropped {
		if strings.HasPrefix(string(dropped[i].Key), "gh:d:a:") && dropped[i].Key < "gh:d:a:005" {
			dropped[i].Teacher = TeacherWants
		}
		if strings.HasPrefix(string(dropped[i].Key), "gh:d:b:") && dropped[i].Key < "gh:d:b:005" {
			dropped[i].Teacher = TeacherRejects // the engine dropped it too: agreement
		}
	}
	closed := []AuditCandidate{{Key: "gh:c:000", Closed: true, Class: "dead", Teacher: TeacherWants}}
	return append(append(cands, dropped...), closed...)
}

func randomPicks(p AuditPlan) []AuditPick {
	return slices.DeleteFunc(slices.Clone(p.Picks), func(pk AuditPick) bool { return SelectedOnReading(pk.Source) })
}

func randomStrata(p AuditPlan) []AuditStratum {
	return slices.DeleteFunc(slices.Clone(p.Strata), func(s AuditStratum) bool { return SelectedOnReading(s.Source) })
}

// The cells are an overlay: whatever q.Disagree is, the random groups draw the
// same postings with the same p_select and report the same strata, so a label
// drawn from a random stratum means what it meant before the cells existed.
func TestDisagreeCellsLeaveTheRandomDrawUnchanged(t *testing.T) {
	cands := disagreePool()
	// The baseline knows no teacher readings at all: the draw before the cells.
	unread := slices.Clone(cands)
	for i := range unread {
		unread[i].Teacher = TeacherUnread
	}
	labeled := map[posting.Key]Entry{"gh:s:001": {}, "gh:d:a:000": {}}
	for _, seed := range []string{"2026-W46", "2026-W47", "x"} {
		base := AuditDraw(unread, labeled, seed, AuditQuotas{Scored: 6, Dropped: 4, Closed: 1})
		if len(base.Picks) == 0 {
			t.Fatalf("%s: the base draw is empty", seed)
		}
		for _, n := range []int{0, 1, 3, 8, 22} {
			got := AuditDraw(cands, labeled, seed, AuditQuotas{Scored: 6, Dropped: 4, Closed: 1, Disagree: n, DisagreeRejects: n / 2})
			if !slices.Equal(randomPicks(got), base.Picks) {
				t.Errorf("%s, disagree %d: random picks changed:\n got %v\nwant %v", seed, n, randomPicks(got), base.Picks)
			}
			if !slices.Equal(randomStrata(got), base.Strata) {
				t.Errorf("%s, disagree %d: random strata changed:\n got %v\nwant %v", seed, n, randomStrata(got), base.Strata)
			}
		}
	}
}

// Cell membership follows the plan: wanted and dropped or in deciles 1-3 is
// CellTeacherWants, rejected in decile 10 is CellTeacherRejects; agreement, a
// middle decile, a closed posting and a labelled one are in no cell; no key is
// drawn twice, and a quota beyond the cells draws them out without padding.
func TestDisagreeCellMembersAndNoKeyTwice(t *testing.T) {
	cands := disagreePool()
	labeled := map[posting.Key]Entry{"gh:s:002": {}}
	p := AuditDraw(cands, labeled, "2026-W46", AuditQuotas{Scored: 10, Dropped: 10, Closed: 1, Disagree: 40, DisagreeRejects: 40})
	seen := map[posting.Key]string{}
	for _, pk := range p.Picks {
		if prev, dup := seen[pk.Key]; dup {
			t.Fatalf("%s drawn twice: %s and %s", pk.Key, prev, pk.Source)
		}
		seen[pk.Key] = pk.Source
		if !(pk.PSelect >= MinInclusionProbability && pk.PSelect <= 1) {
			t.Errorf("%s p_select %v out of range", pk.Key, pk.PSelect)
		}
		if e := (Entry{Key: pk.Key, Labels: map[string]string{WouldApply: "yes"}, Source: pk.Source, PSelect: pk.PSelect}); e.Validate() != nil {
			t.Errorf("a label for %s from %s does not validate: %v", pk.Key, pk.Source, e.Validate())
		}
	}
	want := map[posting.Key]string{}
	for i := 0; i < 10; i++ {
		if i != 2 {
			want[posting.Key(fmt.Sprintf("gh:s:%03d", i))] = CellTeacherWants
		}
	}
	for i := 0; i < 5; i++ {
		want[posting.Key(fmt.Sprintf("gh:d:a:%03d", i))] = CellTeacherWants
		want[posting.Key(fmt.Sprintf("gh:s:%03d", 95+i))] = CellTeacherRejects
	}
	got := map[posting.Key]string{}
	for k, src := range seen {
		if SelectedOnReading(src) {
			got[k] = src
		}
	}
	// Members the random groups took this draw are not in the cells' picks.
	for k, cell := range want {
		if src, ok := seen[k]; ok && !SelectedOnReading(src) {
			delete(want, k)
			continue
		}
		if got[k] != cell {
			t.Errorf("%s: drawn as %q, want %s", k, got[k], cell)
		}
	}
	if len(got) != len(want) {
		t.Errorf("cells drew %d postings, want %d: %v", len(got), len(want), got)
	}
	for k := range got {
		if strings.HasPrefix(string(k), "gh:c:") || k == "gh:s:002" {
			t.Errorf("%s (closed or labelled) is in a cell", k)
		}
	}
}

// An empty cell draws nothing and lends nothing: with no teacher readings the
// draw is the one without the cells, picks and strata alike.
func TestDisagreeEmptyCellsDrawNothing(t *testing.T) {
	cands := scoredPool(50)
	cands = append(cands, droppedPool(map[string]int{"a": 10})...)
	base := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Scored: 5, Dropped: 3})
	got := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Scored: 5, Dropped: 3, Disagree: 7})
	if !slices.Equal(got.Picks, base.Picks) || !slices.Equal(got.Strata, base.Strata) {
		t.Fatalf("empty cells changed the draw:\n got %v %v\nwant %v %v", got.Picks, got.Strata, base.Picks, base.Strata)
	}
	// AuditStrata never lists a cell.
	for name := range AuditStrata(disagreePool(), nil) {
		if SelectedOnReading(name) {
			t.Errorf("AuditStrata lists the cell %s", name)
		}
	}
}

// Each cell is its own group: a short "wants" cell lends nothing to the
// "rejects" cell, which draws only its own places.
func TestDisagreeCellsHaveTheirOwnQuotas(t *testing.T) {
	cands := disagreePool()
	count := func(p AuditPlan) map[string]int {
		m := map[string]int{}
		for _, pk := range p.Picks {
			if SelectedOnReading(pk.Source) {
				m[pk.Source]++
			}
		}
		return m
	}
	// 15 wants members, 5 rejects members; random groups take none here.
	p := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Disagree: 40})
	if c := count(p); c[CellTeacherWants] != 15 || c[CellTeacherRejects] != 0 {
		t.Errorf("wants 40, rejects 0: drew %v; want all 15 wants members and no rejects", c)
	}
	p = AuditDraw(cands, nil, "2026-W46", AuditQuotas{Disagree: 2, DisagreeRejects: 3})
	if c := count(p); c[CellTeacherWants] != 2 || c[CellTeacherRejects] != 3 {
		t.Errorf("2 and 3: drew %v", c)
	}
	p = AuditDraw(cands, nil, "2026-W46", AuditQuotas{DisagreeRejects: 9})
	if c := count(p); c[CellTeacherRejects] != 5 || c[CellTeacherWants] != 0 {
		t.Errorf("rejects 9 over 5 members: drew %v; want 5 and no wants", c)
	}
	if random, all := p.RandomShare(); random != 0 || all != 5 {
		t.Errorf("RandomShare %d of %d, want 0 of 5", random, all)
	}
}

func TestAuditQuotasRandomShareOK(t *testing.T) {
	for _, c := range []struct {
		q  AuditQuotas
		ok bool
	}{
		{AuditQuotas{Scored: 5, Dropped: 10}, true},
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 30}, true}, // a third random exactly
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 31}, false},
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 20, DisagreeRejects: 10}, true},
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 20, DisagreeRejects: 11}, false}, // both cells count
		{AuditQuotas{Scored: 1, Disagree: 2}, true},
		{AuditQuotas{Closed: 5, Disagree: 1}, false}, // closed places are not random for this
		{AuditQuotas{Scored: 1, Closed: 5, Disagree: 3}, false},
		{AuditQuotas{Disagree: 1}, false},
		{AuditQuotas{}, true},
	} {
		if got := c.q.RandomShareOK(); got != c.ok {
			t.Errorf("%+v: RandomShareOK %v, want %v", c.q, got, c.ok)
		}
	}
}

func TestTeacherViewOf(t *testing.T) {
	row := func(reading, value string) TeacherRow {
		return TeacherRow{Slots: map[string]TeacherSlot{"role_fit": {Reading: reading, Value: value}}}
	}
	ans := string(decide.Answered)
	for _, c := range []struct {
		name string
		row  TeacherRow
		want TeacherView
	}{
		{"strong", row(ans, "strong_fit"), TeacherWants},
		{"partial", row(ans, "partial_fit"), TeacherWants},
		{"adjacent is no disagreement", row(ans, "adjacent"), TeacherUnread},
		{"no_fit", row(ans, "no_fit"), TeacherRejects},
		{"unknown value still rejects", row(ans, "something_else"), TeacherRejects},
		{"unanswered", row("unavailable", "strong_fit"), TeacherUnread},
		{"empty value", row(ans, ""), TeacherUnread},
		{"no slot", TeacherRow{Slots: map[string]TeacherSlot{"seniority": {Reading: ans, Value: "strong_fit"}}}, TeacherUnread},
		{"nil slots", TeacherRow{}, TeacherUnread},
	} {
		if got := TeacherViewOf(c.row); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A disagreement-cell label is no sample: the relevance counts leave it out
// and count it apart.
func TestRelevanceCountsSelectedOnReadingApart(t *testing.T) {
	labels := map[posting.Key]Entry{
		"gh:x:1": {Labels: map[string]string{WouldApply: "yes"}, Source: "decile:3", PSelect: 0.2},
		"gh:x:2": {Labels: map[string]string{WouldApply: "yes"}, Source: CellTeacherWants, PSelect: 0.5},
		"gh:x:3": {Labels: map[string]string{WouldApply: "no"}, Source: CellTeacherRejects, PSelect: 0.5},
	}
	r := BuildRelevance(labels, nil, nil, nil, Names{})
	if r.Labels != 3 || r.Selected != 2 || r.Judged != 1 {
		t.Fatalf("labels %d selected %d judged %d, want 3 2 1", r.Labels, r.Selected, r.Judged)
	}
	if len(r.Deciles) != 1 || r.Deciles[0].N != 1 {
		t.Fatalf("deciles %+v, want one label in decile:3", r.Deciles)
	}
	out, n := WithoutSelectedOnReading(labels)
	if n != 2 || len(out) != 1 || out["gh:x:1"].Source != "decile:3" {
		t.Fatalf("WithoutSelectedOnReading: %v, %d", out, n)
	}
}

// Drop recall counts a disagreement-cell label as a mark: a known outcome taken
// out of the pool, a wanted one in a drop class a known miss, never a drawn
// label of its cell.
func TestDropRecallCountsCellLabelsAsKnownOutcomes(t *testing.T) {
	members := append(pool("gh:k", CellKept, 40), pool("gh:d", "dropped:a", 30)...)
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:5", 0, 20, 4, 0, 0.5)
	judge(labels, "gh:d", "dropped:a", 0, 10, 0, 0, 0.3)
	judge(labels, "gh:d", CellTeacherWants, 10, 3, 2, 0, 0.4) // 2 yes, 1 no
	d := BuildDropRecall(labels, members)
	if d.Selected != 3 {
		t.Fatalf("selected %d, want 3", d.Selected)
	}
	c := cellOf(t, d, "dropped:a")
	if c.Pool != 27 || c.Marked != 3 || c.MarkedWanted != 2 || c.KnownMisses != 2 || c.Judged != 10 {
		t.Fatalf("dropped:a %+v: want pool 27, marked 3 (2 wanted), known misses 2, judged 10", c)
	}
	// An owner mark on the posting wins: the cell label is not counted twice.
	members2 := slices.Clone(members)
	for i := range members2 {
		if members2[i].Key == "gh:d:0010" {
			members2[i].Marked, members2[i].MarkedWanted = true, false
		}
	}
	c2 := cellOf(t, BuildDropRecall(labels, members2), "dropped:a")
	if c2.Marked != 3 || c2.MarkedWanted != 1 {
		t.Fatalf("with an owner mark: %+v, want marked 3 of which 1 wanted", c2)
	}
	// A cell maybe is wanted but not a yes: the yes-only share must not count
	// it. Two kept cells' worth of identical labels, one with a cell maybe in
	// the drop class and one with nothing there, give the same yes-only share.
	withMaybe := map[posting.Key]Entry{}
	judge(withMaybe, "gh:k", "decile:5", 0, 20, 4, 0, 0.5)
	judge(withMaybe, "gh:d", "dropped:a", 0, 10, 0, 0, 0.3)
	plain := maps.Clone(withMaybe)
	judge(withMaybe, "gh:d", CellTeacherWants, 10, 1, 0, 1, 0.4) // one maybe
	dm, dp := BuildDropRecall(withMaybe, members), BuildDropRecall(plain, members)
	if dm.Status != RecallEstimated || dp.Status != RecallEstimated {
		t.Fatalf("not estimated: %s / %s", dm.Reason, dp.Reason)
	}
	if dm.MissedYes == nil || dp.MissedYes == nil {
		t.Fatalf("no yes-only share: %+v / %+v", dm.MissedYes, dp.MissedYes)
	}
	if cm := cellOf(t, dm, "dropped:a"); cm.MarkedWanted != 1 || cm.KnownMisses != 1 {
		t.Fatalf("the maybe is not a wanted known miss: %+v", cm)
	}
	if dm.MissedYes.Observed != dp.MissedYes.Observed {
		t.Errorf("a cell maybe moved the yes-only share: %v, want %v", dm.MissedYes.Observed, dp.MissedYes.Observed)
	}
	if dm.Missed.Observed <= dp.Missed.Observed {
		t.Errorf("a cell maybe did not raise the wanted share: %v vs %v", dm.Missed.Observed, dp.Missed.Observed)
	}
	if md := d.Markdown(); !strings.Contains(md, "yes-neighbour cells") {
		t.Errorf("the report does not say the cell labels were counted apart:\n%s", md)
	}
	mustJSON(t, d)
}

func TestYesYieldNamesARichStratumOnlyWhenClearlyAbove(t *testing.T) {
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:r", "decile:4", 0, 40, 2, 3, 0.1)        // 2 of 40 yes
	judge(labels, "gh:w", CellTeacherWants, 0, 10, 8, 1, 0.5)  // 8 of 10 yes
	judge(labels, "gh:j", CellTeacherRejects, 0, 4, 4, 0, 0.5) // too few to name
	judge(labels, "gh:m", SourceManual, 0, 10, 10, 0, 0)       // not drawn: not counted
	judge(labels, "gh:e", "explore:new", 0, 10, 10, 0, 0.5)    // another surface: not counted
	labels["gh:s:1"] = Entry{Labels: map[string]string{WouldApply: Skip}, Source: CellTeacherWants, PSelect: 0.5}
	y := BuildYesYield(labels)
	if y.Random.N != 40 || y.Random.Yes != 2 || y.Random.Maybe != 3 {
		t.Fatalf("random %+v, want 40 judged, 2 yes, 3 maybe", y.Random)
	}
	if len(y.Rows) != 3 {
		t.Fatalf("rows %+v, want decile:4 and the two cells", y.Rows)
	}
	if y.Richest != CellTeacherWants || !strings.Contains(y.Advice, "--disagree") {
		t.Fatalf("richest %q, advice %q", y.Richest, y.Advice)
	}
	if !strings.Contains(y.Markdown(), "no estimate of the pool") {
		t.Error("the table does not say it is no population estimate")
	}

	// A cell no better than the random strata is not named.
	flat := map[posting.Key]Entry{}
	judge(flat, "gh:r", "dropped:a", 0, 20, 4, 0, 0.1)
	judge(flat, "gh:w", CellTeacherWants, 0, 10, 3, 0, 0.5)
	if y := BuildYesYield(flat); y.Richest != "" || !strings.Contains(y.Advice, "none named") {
		t.Fatalf("named %q on no clear lead: %s", y.Richest, y.Advice)
	}
	// No random labels: nothing to compare against.
	only := map[posting.Key]Entry{}
	judge(only, "gh:w", CellTeacherWants, 0, 10, 9, 0, 0.5)
	if y := BuildYesYield(only); y.Richest != "" || !strings.HasPrefix(y.Advice, "no data") {
		t.Fatalf("with no random labels: %q %q", y.Richest, y.Advice)
	}
	if y := BuildYesYield(nil); len(y.Rows) != 0 || !strings.Contains(y.Markdown(), "no data") {
		t.Fatalf("empty: %+v", y)
	}
}

// Single deciles take months to reach the naming minimum; the groups pool
// them so the scored-versus-dropped question can be answered.
func TestYesYieldGroupsCanBeNamed(t *testing.T) {
	labels := map[posting.Key]Entry{}
	for d := 1; d <= 10; d++ {
		yes := 0
		if d >= 8 {
			yes = 2 // the yeses sit in the top deciles, 2 of 3 judged each
		}
		judge(labels, fmt.Sprintf("gh:d%02d", d), fmt.Sprintf("decile:%d", d), 0, 3, yes, 0, 0.1)
	}
	judge(labels, "gh:dr", "dropped:model:role_fit", 0, 30, 0, 0, 0.1)
	y := BuildYesYield(labels)
	for _, r := range y.Rows {
		if r.N >= MinYieldJudged && strings.HasPrefix(r.Source, "decile:") {
			t.Fatalf("a single decile reached the minimum: %+v", r)
		}
	}
	g := map[string]YieldRow{}
	for _, r := range y.Groups {
		g[r.Source] = r
	}
	if g[GroupDeciles].N != 30 || g[GroupDeciles].Yes != 6 || g[GroupTopDeciles].N != 9 || g[GroupTopDeciles].Yes != 6 || g[GroupDrops].N != 30 {
		t.Fatalf("groups %+v", y.Groups)
	}
	if y.Richest != GroupTopDeciles || !strings.Contains(y.Advice, "from --dropped to --scored") {
		t.Fatalf("richest %q advice %q", y.Richest, y.Advice)
	}
}

// The kill rule's two looks on counts (reworked 2026-11-14, before any cell
// label exists).
func TestYesYieldKillRuleLooks(t *testing.T) {
	for yes, off := range []bool{true, true, false, false} {
		if firstLook(yes) != off {
			t.Errorf("first look, %d yes in 20: off %v, want %v", yes, !off, off)
		}
	}
	for _, c := range []struct {
		yes, compN, compYes int
		want                string
	}{
		{20, 0, 0, CellCannotDecide}, // no comparator: never a keep
		{4, 100, 0, CellTurnOff},     // under the floor of 5 yes, whatever the comparator
		{5, 100, 0, CellKeep},        // a comparator with no yes: the floor decides
		{5, 80, 5, CellKeep},         // 5/40 = 12.5% against 6.25%: exactly 2x
		{5, 80, 6, CellTurnOff},      // 12.5% against 7.5%: under 2x
		{12, 120, 18, CellKeep},      // 30% against 15%: 2x
		{11, 120, 18, CellTurnOff},   // 27.5% against 15%
		{40, 1, 1, CellTurnOff},      // 100% against 100%
	} {
		if got := secondLook(c.yes, c.compN, c.compYes); got != c.want {
			t.Errorf("second look, %d yes of 40 against %d of %d: %s, want %s", c.yes, c.compYes, c.compN, got, c.want)
		}
	}
}

// The looks are latched on the first answers by time: yeses that come after
// the first 20 cannot undo a first-look turn-off, and the comparator is
// frozen at the cell's 40th answer.
func TestYesYieldKillRuleIsLatchedByAnswerTime(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	put := func(labels map[posting.Key]Entry, key, src, v string, at time.Time) {
		labels[posting.Key(key)] = Entry{Labels: map[string]string{WouldApply: v}, Source: src, PSelect: 0.5, LabeledAt: at}
	}
	labels := map[posting.Key]Entry{}
	for i := range 30 {
		v := "no"
		if i >= 20 { // ten yeses, all after the first look
			v = "yes"
		}
		// Keys sort against time, so an ordering by key would see the yeses first.
		put(labels, fmt.Sprintf("gh:w:%03d", 99-i), CellTeacherWants, v, t0.Add(time.Duration(i)*time.Hour))
	}
	c := BuildYesYield(labels).Cells[0]
	if c.Verdict != CellTurnOff || c.Yes != 10 || !strings.Contains(c.Why, "first look: 0 yes") {
		t.Fatalf("late yeses undid the first look: %+v", c)
	}

	// Second look: 8 yes in the first 40 (yes among the first 20 too). The
	// comparator has 2 of 40 by the 40th answer (5%: 20% is 4x, a keep) and
	// 38 more yeses after it, which must not count.
	labels = map[posting.Key]Entry{}
	for i := range 40 {
		v := "no"
		if i%5 == 0 {
			v = "yes"
		}
		put(labels, fmt.Sprintf("gh:w:%03d", i), CellTeacherWants, v, t0.Add(time.Duration(i)*time.Hour))
	}
	for i := range 40 {
		v := "no"
		if i < 2 {
			v = "yes"
		}
		put(labels, fmt.Sprintf("gh:r:%03d", i), "decile:4", v, t0.Add(time.Duration(i)*time.Hour-time.Minute))
	}
	for i := range 38 {
		put(labels, fmt.Sprintf("gh:s:%03d", i), "decile:6", "yes", t0.Add(time.Duration(41+i)*time.Hour))
	}
	c = BuildYesYield(labels).Cells[0]
	if c.Verdict != CellKeep || c.CompN != 40 || c.CompYes != 2 {
		t.Fatalf("second look %+v, want keep against the comparator's 2 of 40 frozen at the 40th answer", c)
	}
}

// The rejects cell has no yes rule: at 20 answers its no rate is set beside
// the top three deciles' and the owner decides. A cell of all no's, which
// the old rule turned off, is the cell working.
func TestYesYieldRejectsCellIsTheOwnersCall(t *testing.T) {
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:j", CellTeacherRejects, 0, 19, 0, 0, 0.5)
	judge(labels, "gh:t", "decile:9", 0, 10, 4, 2, 0.5)
	if c := BuildYesYield(labels).Cells[0]; c.Verdict != CellCollecting {
		t.Fatalf("19 judged: %+v", c)
	}
	judge(labels, "gh:j", CellTeacherRejects, 19, 1, 0, 0, 0.5)
	y := BuildYesYield(labels)
	c := y.Cells[0]
	if c.Cell != CellTeacherRejects || c.Verdict != CellOwnerCall || c.No != 20 || c.Comparator != GroupTopDeciles || c.CompN != 10 || c.CompNo != 4 {
		t.Fatalf("20 judged, all no: %+v", c)
	}
	if !strings.Contains(y.Markdown(), "no in 20 of 20 answers, against 4 of 10 in "+GroupTopDeciles) {
		t.Errorf("markdown lacks the no rates:\n%s", y.Markdown())
	}
}

// The verdicts reach the report, and the wants cell is held against the
// random strata pooled.
func TestYesYieldKillRule(t *testing.T) {
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:w", CellTeacherWants, 0, 20, 1, 5, 0.5)
	judge(labels, "gh:j", CellTeacherRejects, 0, 7, 0, 0, 0.5)
	judge(labels, "gh:r", "dropped:model:role_fit", 0, 12, 0, 0, 0.5)
	y := BuildYesYield(labels)
	if len(y.Cells) != 2 {
		t.Fatalf("cells %+v", y.Cells)
	}
	if w := y.Cells[0]; w.Cell != CellTeacherWants || w.Verdict != CellTurnOff || w.N != 20 || w.Yes != 1 || w.Comparator != "random strata" || w.CompN != 12 {
		t.Fatalf("wants %+v", w)
	}
	if r := y.Cells[1]; r.Cell != CellTeacherRejects || r.Verdict != CellCollecting || r.N != 7 {
		t.Fatalf("rejects %+v", r)
	}
	if !strings.Contains(y.Markdown(), "Kill rule, "+CellTeacherWants+": 1 yes of 20 judged: turn_off (first look: 1 yes in its first 20 answers") {
		t.Errorf("markdown lacks the verdict:\n%s", y.Markdown())
	}
}

// The freeze counts the exemplars that came from the cells, so a tilt toward
// the teacher's own prior is visible.
func TestFreezeCountsExemplarsFromCells(t *testing.T) {
	labels, recs := exLabels(4, 4, pairT0)
	for _, k := range []posting.Key{"y0", "y1", "n0"} {
		e := labels[k]
		e.Source, e.PSelect = CellTeacherWants, 0.5
		labels[k] = e
	}
	_, st, err := FreezeExemplars(labels, recs, pairT0.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.YesFromCells != 2 || st.NoFromCells != 1 || st.YesKept != 4 || st.NoKept != 4 {
		t.Fatalf("stats %+v, want 2 yes and 1 no from the cells of 4 and 4", st)
	}
}

// One definition of "selected" everywhere: a cell label answered yes, maybe
// or no. A skip is a skip whatever drew it.
func TestSelectedCountsJudgedCellLabelsOnly(t *testing.T) {
	labels := map[posting.Key]Entry{
		"gh:x:1": {Labels: map[string]string{WouldApply: Skip}, Source: CellTeacherWants, PSelect: 0.5},
		"gh:x:2": {Labels: map[string]string{WouldApply: "maybe"}, Source: CellTeacherWants, PSelect: 0.5},
	}
	if r := BuildRelevance(labels, nil, nil, nil, Names{}); r.Selected != 1 || r.Skipped != 1 {
		t.Errorf("relevance selected %d skipped %d, want 1 and 1", r.Selected, r.Skipped)
	}
	if md := BuildRelevance(labels, nil, nil, nil, Names{}).Markdown(); !strings.Contains(md, "yes-neighbour cells and left out of everything below: 1") {
		t.Errorf("relevance text hides the selected count:\n%s", md)
	}
}

// The reader table leaves cell labels out of every reader's figures and says
// how many.
func TestReaderAgreementLeavesCellLabelsOut(t *testing.T) {
	ann := map[string]string{"triage.role_fit": "mismatch_domain", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra"}
	recs := []ledger.Record{{Key: "gh:a:1", Annotations: ann}, {Key: "gh:a:2", Annotations: ann}, {Key: "gh:a:3", Annotations: ann}}
	labels := map[posting.Key]Entry{
		"gh:a:1": {Labels: map[string]string{WouldApply: "yes"}, Source: "decile:2", PSelect: 0.1},
		"gh:a:2": {Labels: map[string]string{WouldApply: "yes"}, Source: CellTeacherWants, PSelect: 0.5},
		"gh:a:3": {Labels: map[string]string{WouldApply: "no"}, Source: CellTeacherRejects, PSelect: 0.5},
	}
	a := BuildReaderAgreement(labels, recs, nil, TeacherProxyMissing, Names{})
	if a.Judged != 1 || a.Yes != 1 || a.Selected != 2 {
		t.Fatalf("judged %d yes %d selected %d, want 1 1 2", a.Judged, a.Yes, a.Selected)
	}
	if len(a.Readers) != 1 || a.Readers[0].Sensitivity.N != 1 {
		t.Fatalf("readers %+v: want one engine reader judged on the one random label", a.Readers)
	}
	if !strings.Contains(a.Markdown(), "left out: 2 judged from the disagreement cells") {
		t.Errorf("reader text hides the selected count:\n%s", a.Markdown())
	}
}

// The auditor is never scored on a cell label: it was chosen because a
// teacher's reading disagreed, which would tilt the verdict.
func TestEvaluationLabelsLeaveCellLabelsOut(t *testing.T) {
	labels, recs := exLabels(4, 4, pairT0)
	set, _, err := FreezeExemplars(labels, recs, pairT0.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[posting.Key]posting.Posting{}
	after := pairT0.Add(2 * time.Hour)
	for i, src := range []string{"decile:4", CellTeacherWants, CellTeacherRejects, EngineeringKept, CellYesNeighbour} {
		k := posting.Key(fmt.Sprintf("new%d", i))
		labels[k] = Entry{V: SchemaVersion, Key: k, Labels: map[string]string{WouldApply: "yes"}, Source: src, PSelect: 0.2, LabeledAt: after}
		byKey[k] = posting.Posting{Key: k, Title: fmt.Sprintf("Brand New %d", i), Company: "NewCo"}
	}
	k := posting.Key("newskip")
	labels[k] = Entry{V: SchemaVersion, Key: k, Labels: map[string]string{WouldApply: Skip}, Source: CellTeacherWants, PSelect: 0.2, LabeledAt: after}
	got, st := EvaluationLabels(labels, set, byKey)
	// The engineering label counts (drawn on no reading); the neighbour one,
	// chosen for its nearness to a yes, does not.
	if len(got) != 2 || got["new0"] != "yes" || got["new3"] != "yes" {
		t.Errorf("evaluation set %v, want the decile and engineering labels only", got)
	}
	if st.Selected != 3 || st.Skipped != 1 || st.Kept != 2 || st.Engineering != 1 {
		t.Errorf("stats %+v, want 3 selected, the cell skip as skipped, 2 kept of which 1 engineering", st)
	}
}

// Drop agreement counts the owner's labels on candidates the shadow disagreed
// with; a cell label there was drawn because a model disagreed, so it is not
// counted.
func TestDropAgreementLeavesCellLabelsOut(t *testing.T) {
	const sh = "local-d3-mini-per_slot"
	recs := []ledger.Record{
		cand("b", "mismatch_domain", "0.99", sh, "answered", "strong_fit"),
		cand("c", "mismatch_domain", "0.95", sh, "answered", "strong_fit"),
	}
	labels := map[posting.Key]Entry{
		"b": {Labels: map[string]string{WouldApply: "yes"}, Source: "dropped:model:role_fit", PSelect: 0.1},
		"c": {Labels: map[string]string{WouldApply: "yes"}, Source: CellTeacherWants, PSelect: 0.5},
	}
	rep := BuildDropAgreement(labels, recs, agreeRules, Names{OpenRouter: "d1-ultra"})
	if len(rep.Rows) != 1 || rep.Rows[0].Disagree != 2 || rep.Rows[0].DisagreeJudged != 1 || rep.Rows[0].DisagreeWanted != 1 {
		t.Fatalf("rows %+v: want 2 disagreements, 1 judged (the random one)", rep.Rows)
	}
}
