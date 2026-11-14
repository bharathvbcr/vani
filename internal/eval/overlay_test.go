package eval

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// overlayPool is disagreePool with the engineering postings (every fourth
// scored one and the first five of drop class b) and the yes neighbours
// (scored 50..59, two of them also engineering and one also in no cell) marked.
func overlayPool() []AuditCandidate {
	cands := disagreePool()
	for i := range cands {
		k := string(cands[i].Key)
		if strings.HasPrefix(k, "gh:s:") {
			var n int
			fmt.Sscanf(k, "gh:s:%03d", &n)
			cands[i].Engineering = n%4 == 0
			cands[i].NearYes = (n >= 50 && n < 60) || n == 3 // 3 is in the wants cell too
		}
		if strings.HasPrefix(k, "gh:d:b:") && k < "gh:d:b:005" {
			cands[i].Engineering = true
		}
		if k == "gh:c:000" {
			cands[i].Engineering, cands[i].NearYes = true, true // closed: never drawn by an overlay
		}
	}
	return cands
}

// onlyRandom is the picks and strata from the random groups (deciles, drop
// classes, closed): what the estimates read.
func onlyRandom(p AuditPlan) ([]AuditPick, []AuditStratum) {
	return slices.DeleteFunc(slices.Clone(p.Picks), func(pk AuditPick) bool { return !auditStratum(pk.Source) }),
		slices.DeleteFunc(slices.Clone(p.Strata), func(s AuditStratum) bool { return !auditStratum(s.Source) })
}

// The engineering group and the yes-neighbour cell are overlays like the
// disagreement cells: whatever their quotas, the random groups draw the same
// postings with the same p_select.
func TestEngineeringAndNeighbourLeaveTheRandomDrawUnchanged(t *testing.T) {
	cands := overlayPool()
	plain := slices.Clone(cands)
	for i := range plain {
		plain[i].Engineering, plain[i].NearYes, plain[i].Teacher = false, false, TeacherUnread
	}
	labeled := map[posting.Key]Entry{"gh:s:004": {}, "gh:d:b:001": {}}
	for _, seed := range []string{"2026-W46", "2026-W47", "x"} {
		base := AuditDraw(plain, labeled, seed, AuditQuotas{Scored: 6, Dropped: 4, Closed: 1})
		for _, n := range []int{0, 1, 3, 9, 40} {
			got := AuditDraw(cands, labeled, seed, AuditQuotas{Scored: 6, Dropped: 4, Closed: 1, Engineering: n, Neighbour: n / 2, Disagree: n / 3})
			picks, strata := onlyRandom(got)
			if !slices.Equal(picks, base.Picks) || !slices.Equal(strata, base.Strata) {
				t.Errorf("%s, overlays %d: random draw changed:\n got %v %v\nwant %v %v", seed, n, picks, strata, base.Picks, base.Strata)
			}
		}
	}
}

// Order is random, engineering, cells, neighbours: a posting in several
// overlays is drawn by the first, no key is drawn twice, a closed or labelled
// posting is in none, and each overlay draws only its own members.
func TestOverlaysDrawInOrderAndNoKeyTwice(t *testing.T) {
	cands := overlayPool()
	labeled := map[posting.Key]Entry{"gh:s:052": {}}
	p := AuditDraw(cands, labeled, "2026-W46", AuditQuotas{Engineering: 200, Disagree: 200, DisagreeRejects: 200, Neighbour: 200})
	seen := map[posting.Key]string{}
	for _, pk := range p.Picks {
		if prev, dup := seen[pk.Key]; dup {
			t.Fatalf("%s drawn twice: %s and %s", pk.Key, prev, pk.Source)
		}
		seen[pk.Key] = pk.Source
		if !(pk.PSelect >= MinInclusionProbability && pk.PSelect <= 1) {
			t.Errorf("%s p_select %v", pk.Key, pk.PSelect)
		}
		if e := (Entry{Key: pk.Key, Labels: map[string]string{WouldApply: "no"}, Source: pk.Source, PSelect: pk.PSelect}); e.Validate() != nil {
			t.Errorf("a label from %s does not validate: %v", pk.Source, e.Validate())
		}
	}
	for k, want := range map[posting.Key]string{
		"gh:s:000":   EngineeringKept,    // engineering and in the wants cell: engineering first
		"gh:s:056":   EngineeringKept,    // engineering and a yes neighbour
		"gh:d:b:000": EngineeringDropped, // dropped engineering
		"gh:s:001":   CellTeacherWants,
		"gh:s:003":   CellTeacherWants, // a wants member and a yes neighbour: the cells draw first
		"gh:s:096":   EngineeringKept,  // engineering and in the rejects cell
		"gh:s:097":   CellTeacherRejects,
		"gh:s:051":   CellYesNeighbour,
		"gh:s:030":   "", // in no overlay
		"gh:s:052":   "", // labelled
		"gh:c:000":   "", // closed
	} {
		if seen[k] != want {
			t.Errorf("%s drawn as %q, want %q", k, seen[k], want)
		}
	}
	count := map[string]int{}
	for _, src := range seen {
		count[src]++
	}
	// Engineering: scored 0,4,..,96 (25) less the labelled 52, plus b:000..004.
	if count[EngineeringKept] != 24 || count[EngineeringDropped] != 5 {
		t.Errorf("engineering drew %d kept and %d dropped, want 24 and 5", count[EngineeringKept], count[EngineeringDropped])
	}
	// Neighbours 50..59 less the engineering 52 and 56 (52 is also labelled).
	if count[CellYesNeighbour] != 8 {
		t.Errorf("yes neighbours drew %d, want 8", count[CellYesNeighbour])
	}
	// Each overlay's quota is its own: none lends to another.
	q := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Neighbour: 3})
	if len(q.Picks) != 3 || !SelectedOnReading(q.Picks[0].Source) || q.Picks[0].Source != CellYesNeighbour {
		t.Errorf("neighbour 3 alone drew %v", q.Picks)
	}
	if e := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Engineering: 4}); len(e.Picks) != 4 {
		t.Errorf("engineering 4 alone drew %v", e.Picks)
	}
}

// The cap counts the yes-neighbour cell with the disagreement cells and the
// engineering group on neither side.
func TestRandomShareCountsNeighboursNotEngineering(t *testing.T) {
	for _, c := range []struct {
		q  AuditQuotas
		ok bool
	}{
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 20, Neighbour: 10}, true},
		{AuditQuotas{Scored: 5, Dropped: 10, Disagree: 20, Neighbour: 11}, false},
		{AuditQuotas{Scored: 5, Dropped: 10, Engineering: 500}, true},
		{AuditQuotas{Engineering: 5, Neighbour: 1}, false},
	} {
		if got := c.q.RandomShareOK(); got != c.ok {
			t.Errorf("%+v: RandomShareOK %v, want %v", c.q, got, c.ok)
		}
	}
	if !SelectedOnReading(CellYesNeighbour) || SelectedOnReading(EngineeringKept) || EngineeringStratum(CellYesNeighbour) || !EngineeringStratum(EngineeringDropped) {
		t.Fatal("the stratum predicates mix the neighbour cell and the engineering group")
	}
}

// Engineering labels are a random sample of the engineering postings, not of
// a decile or drop class: relevance, drop recall and the reader table count
// them apart; the yes yield and the auditor's evaluation keep them.
func TestEngineeringLabelsCountedApart(t *testing.T) {
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:5", 0, 20, 4, 0, 0.5)
	judge(labels, "gh:d", "dropped:a", 0, 10, 0, 0, 0.3)
	judge(labels, "gh:e", EngineeringKept, 0, 6, 5, 0, 0.2)
	judge(labels, "gh:f", EngineeringDropped, 0, 2, 1, 0, 0.2)

	r := BuildRelevance(labels, nil, nil, nil, Names{})
	if r.Engineering != 8 || r.Judged != 38 {
		t.Errorf("relevance engineering %d judged %d, want 8 and 38 (in precision@k like a manual label)", r.Engineering, r.Judged)
	}
	if !strings.Contains(r.Markdown(), "engineering") {
		t.Errorf("relevance text hides the engineering count:\n%s", r.Markdown())
	}
	members := append(pool("gh:k", CellKept, 40), pool("gh:d", "dropped:a", 30)...)
	members = append(members, pool("gh:f", "dropped:a", 2)...)
	d := BuildDropRecall(labels, members)
	if d.Engineering != 8 {
		t.Errorf("drop recall engineering %d, want 8", d.Engineering)
	}
	if c := cellOf(t, d, "dropped:a"); c.KnownMisses != 0 || c.Marked != 0 {
		t.Errorf("an engineering yes became a known miss: %+v", c)
	}
	ra := BuildReaderAgreement(labels, nil, nil, TeacherProxyMissing, Names{})
	if ra.Engineering != 8 {
		t.Errorf("reader table engineering %d, want 8", ra.Engineering)
	}

	y := BuildYesYield(labels)
	var eng *YieldRow
	for i := range y.Rows {
		if y.Rows[i].Source == EngineeringKept {
			eng = &y.Rows[i]
		}
	}
	if eng == nil || eng.N != 6 || eng.Yes != 5 {
		t.Fatalf("yes yield engineering row %+v, want 5 of 6", eng)
	}
	if y.Random.N != 30 {
		t.Errorf("random strata pooled %d, want 30 (engineering is not random strata)", y.Random.N)
	}
	if y.Richest != EngineeringKept || !strings.Contains(y.Advice, "--engineering") {
		t.Errorf("richest %q advice %q", y.Richest, y.Advice)
	}
}

// The yes-neighbour cell is under the kill rule like the wants cell, held
// against the score deciles rather than every random stratum: its pool is
// mostly kept postings.
func TestYesNeighbourKillRule(t *testing.T) {
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:n", CellYesNeighbour, 0, 40, 6, 0, 0.5)
	judge(labels, "gh:d", "decile:5", 0, 20, 1, 0, 0.5)
	judge(labels, "gh:x", "dropped:title:role_fit", 0, 30, 0, 0, 0.5) // would halve the comparator's rate if counted
	y := BuildYesYield(labels)
	if len(y.Cells) != 1 {
		t.Fatalf("cells %+v", y.Cells)
	}
	c := y.Cells[0]
	if c.Cell != CellYesNeighbour || c.Verdict != CellKeep || c.Comparator != GroupDeciles || c.CompN != 20 || c.CompYes != 1 {
		t.Fatalf("neighbour verdict %+v, want keep against the deciles' 1 of 20", c)
	}

	// The same cell with no decile answers cannot decide, and is not kept.
	labels = map[posting.Key]Entry{}
	judge(labels, "gh:n", CellYesNeighbour, 0, 40, 6, 0, 0.5)
	judge(labels, "gh:x", "dropped:title:role_fit", 0, 30, 0, 0, 0.5)
	if c := BuildYesYield(labels).Cells[0]; c.Verdict != CellCannotDecide {
		t.Fatalf("no comparator: %+v, want %s", c, CellCannotDecide)
	}
}
