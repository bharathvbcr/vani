package eval

import (
	"fmt"
	"slices"
	"strings"
	"testing"

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
			got := AuditDraw(cands, labeled, seed, AuditQuotas{Scored: 6, Dropped: 4, Closed: 1, Disagree: n})
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
	p := AuditDraw(cands, labeled, "2026-W46", AuditQuotas{Scored: 10, Dropped: 10, Closed: 1, Disagree: 40})
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

func TestAuditQuotasRandomShareOK(t *testing.T) {
	for _, c := range []struct {
		q  AuditQuotas
		ok bool
	}{
		{AuditQuotas{Scored: 5, Dropped: 10}, true},
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 30}, true}, // a third random exactly
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 31}, false},
		{AuditQuotas{Closed: 1, Disagree: 2}, true},
		{AuditQuotas{Closed: 1, Disagree: 3}, false},
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
	if md := d.Markdown(); !strings.Contains(md, "disagreement cells") {
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
