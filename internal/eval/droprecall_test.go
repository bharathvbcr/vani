package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// pool lists n members of cell, keyed prefix:i.
func pool(prefix, cell string, n int) []RecallMember {
	out := make([]RecallMember, n)
	for i := range out {
		out[i] = RecallMember{Key: posting.Key(fmt.Sprintf("%s:%04d", prefix, i)), Cell: cell}
	}
	return out
}

// judge labels members from+0 .. from+n-1 of prefix: the first y say yes, the
// next m maybe, the rest no, each drawn from src with probability p.
func judge(labels map[posting.Key]Entry, prefix, src string, from, n, y, m int, p float64) {
	for i := range n {
		v := "no"
		switch {
		case i < y:
			v = "yes"
		case i < y+m:
			v = "maybe"
		}
		labels[posting.Key(fmt.Sprintf("%s:%04d", prefix, from+i))] = Entry{Labels: map[string]string{WouldApply: v}, Source: src, PSelect: p}
	}
}

func mustJSON(t *testing.T, d DropRecall) {
	t.Helper()
	if _, err := json.Marshal(d); err != nil {
		t.Fatalf("the estimate does not encode: %v", err)
	}
}

func cellOf(t *testing.T, d DropRecall, name string) RecallCell {
	t.Helper()
	for _, c := range d.Cells {
		if c.Cell == name {
			return c
		}
	}
	t.Fatalf("no cell %q in %+v", name, d.Cells)
	return RecallCell{}
}

func TestDropRecallEstimatesTheDroppedShareOfWantedPostings(t *testing.T) {
	var members []RecallMember
	members = append(members, pool("gh:k", CellKept, 1000)...)
	members = append(members, pool("gh:m", "dropped:model:role_fit", 1000)...)
	members = append(members, pool("gh:t", "dropped:title:role_fit", 500)...)
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:5", 0, 20, 4, 8, 0.01)               // 12 wanted, 4 yes of 20
	judge(labels, "gh:m", "dropped:model:role_fit", 0, 20, 0, 2, 0.01) // 2 wanted, 0 yes of 20
	judge(labels, "gh:t", "dropped:title:role_fit", 0, 5, 0, 0, 0.01)  // 0 of 5

	d := BuildDropRecall(labels, members)
	mustJSON(t, d)
	if d.Status != RecallEstimated || d.Missed == nil || d.MissedYes == nil {
		t.Fatalf("status %s: %s", d.Status, d.Reason)
	}
	// Raw: kept 1000 × 12/20 = 600 wanted; model 1000 × 2/20 = 100; title 0.
	if want := 100.0 / 700; math.Abs(d.Missed.Observed-want) > 1e-9 {
		t.Errorf("observed missed share %v, want %v", d.Missed.Observed, want)
	}
	// Yes only: kept 200, drops 0. The two readings differ.
	if d.MissedYes.Observed != 0 {
		t.Errorf("observed yes-only missed share %v, want 0", d.MissedYes.Observed)
	}
	for _, s := range []*RecallShare{d.Missed, d.MissedYes} {
		if !(s.Lo <= s.Share && s.Share <= s.Hi && s.Lo < s.Hi) {
			t.Errorf("median %v outside or on a collapsed interval %v-%v", s.Share, s.Lo, s.Hi)
		}
	}
	// 0 of 20 yes, and 0 of 5 wanted, are not measured zeros.
	if d.MissedYes.Hi < 0.05 {
		t.Errorf("yes-only interval %v-%v reads as a measured zero", d.MissedYes.Lo, d.MissedYes.Hi)
	}
	title := cellOf(t, d, "dropped:title:role_fit")
	if title.Wanted.Rate != 0 || title.Wanted.Hi < 0.25 || title.Wanted.EstHi < 100 || title.Wanted.Est <= 0 {
		t.Errorf("0/5 cell %+v; want a wide upper end and a median above 0", title.Wanted)
	}
	// Clopper-Pearson for 0 of 5 is 1-0.05^(1/5); 59 not-wanted labels in all
	// reach a 5% bound (ln 0.05 / ln 0.95 = 58.4), so 54 more.
	if want := 1 - math.Pow(0.05, 0.2); math.Abs(title.UpperBound-want) > 1e-12 || title.LabelsFor5pct != 54 {
		t.Errorf("title bound %v need %d, want %v 54", title.UpperBound, title.LabelsFor5pct, want)
	}
	model := cellOf(t, d, "dropped:model:role_fit")
	if model.LabelsFor5pct <= 0 || model.LabelsFor5pct != labelsForBound(2, 20) || model.Wanted.K != 2 || model.Yes.K != 0 || !(model.UpperBound > 0.1 && model.UpperBound < 0.35) {
		t.Errorf("model cell %+v", model)
	}
	if len(d.ByClass) != 2 || len(d.ByClassYes) != 2 {
		t.Errorf("by class %+v / %+v", d.ByClass, d.ByClassYes)
	}
	if again := BuildDropRecall(labels, members); !reflect.DeepEqual(d, again) {
		t.Error("the estimate is not deterministic")
	}
	md := d.Markdown()
	for _, frag := range []string{"## Drop recall estimate", "Share of wanted (yes or maybe) postings dropped: median", "Share of yes only postings dropped",
		"observed 14.3%", "| dropped:title:role_fit | 5 | 0 | 45.1% | 54 |", fmt.Sprintf("| dropped:model:role_fit | 20 | 2 | %.1f%% | %d |", 100*model.UpperBound, model.LabelsFor5pct)} {
		if !strings.Contains(md, frag) {
			t.Errorf("markdown lacks %q:\n%s", frag, md)
		}
	}
}

// p_select is not a weight here: as the pool grows a stratum's p_select falls
// week on week, and 1/p would count late labels far more than early ones.
// Labels in one stratum count alike whatever their p_select; a label drawn
// from another stratum than its posting sits in now is left out as moved.
func TestDropRecallIgnoresPSelectAndLeavesMovedLabelsOut(t *testing.T) {
	members := append(pool("gh:k", CellKept, 100), pool("gh:m", "dropped:model:role_fit", 100)...)
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:9", 0, 2, 2, 0, 0.0017)                                                                     // two early yes
	judge(labels, "gh:k", "decile:2", 2, 2, 0, 0, 0.00029)                                                                    // two late no
	labels["gh:k:0004"] = Entry{Labels: map[string]string{WouldApply: "no"}, Source: "dropped:model:role_fit", PSelect: 0.1}  // drawn dropped, kept now
	labels["gh:m:0050"] = Entry{Labels: map[string]string{WouldApply: "yes"}, Source: "decile:3", PSelect: 0.1}               // drawn kept, dropped now
	labels["gh:m:0051"] = Entry{Labels: map[string]string{WouldApply: "yes"}, Source: "dropped:title:role_fit", PSelect: 0.1} // another drop class
	judge(labels, "gh:m", "dropped:model:role_fit", 0, 3, 0, 0, 0.03)
	d := BuildDropRecall(labels, members)
	k := cellOf(t, d, CellKept)
	if k.Judged != 4 || k.Wanted.K != 2 || k.Wanted.Rate != 0.5 {
		t.Errorf("kept %+v: want 2 of 4, p_select ignored", k)
	}
	if m := cellOf(t, d, "dropped:model:role_fit"); m.Judged != 3 || m.Wanted.K != 0 {
		t.Errorf("model %+v", m)
	}
	if d.Moved != 3 {
		t.Errorf("moved %d, want 3", d.Moved)
	}
}

func TestDropRecallRefusesWhatItCannotEstimate(t *testing.T) {
	kept, drop := pool("gh:k", CellKept, 50), pool("gh:m", "dropped:model:seniority", 50)
	both := append(append([]RecallMember{}, kept...), drop...)
	full := map[posting.Key]Entry{}
	judge(full, "gh:k", "decile:3", 0, 5, 3, 0, 0.1)
	judge(full, "gh:m", "dropped:model:seniority", 0, 5, 1, 0, 0.1)

	cases := []struct {
		name    string
		labels  map[posting.Key]Entry
		members []RecallMember
		reason  string
	}{
		{"no labels", nil, both, "short are"},
		{"no members", full, nil, "no kept or dropped postings"},
		{"nothing dropped", full, kept, "nothing is dropped"},
		{"a dropped cell with no labels", func() map[posting.Key]Entry {
			m := map[posting.Key]Entry{}
			judge(m, "gh:k", "decile:3", 0, 5, 3, 0, 0.1)
			return m
		}(), both, "dropped:model:seniority (0 of 3 judged, pool 50)"},
		{"a cell with too few", func() map[posting.Key]Entry {
			m := map[posting.Key]Entry{}
			judge(m, "gh:k", "decile:3", 0, 5, 3, 0, 0.1)
			judge(m, "gh:m", "dropped:model:seniority", 0, 2, 0, 0, 0.1)
			return m
		}(), both, "dropped:model:seniority (2 of 3 judged, pool 50)"},
		{"all skips", func() map[posting.Key]Entry {
			m := map[posting.Key]Entry{}
			for k := range full {
				m[k] = Entry{Labels: map[string]string{WouldApply: Skip}, Source: "decile:3", PSelect: 0.1}
			}
			return m
		}(), both, "short are"},
		{"closed members only", full, pool("gh:x", "closed:dead", 10), "no kept or dropped postings"},
	}
	for _, c := range cases {
		d := BuildDropRecall(c.labels, c.members)
		mustJSON(t, d)
		if d.Status != RecallInsufficient || d.Missed != nil || d.MissedYes != nil || d.ByClass != nil {
			t.Errorf("%s: status %s missed %v", c.name, d.Status, d.Missed)
		}
		if !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s: reason %q lacks %q", c.name, d.Reason, c.reason)
		}
		md := d.Markdown()
		if !strings.Contains(md, "Recall: insufficient") || strings.Contains(md, "postings dropped: median") {
			t.Errorf("%s: an insufficient estimate printed a share:\n%s", c.name, md)
		}
	}
	// A cell with no labels is unknown, never 0: no rate, no count.
	d := BuildDropRecall(cases[3].labels, both)
	u := cellOf(t, d, "dropped:model:seniority")
	if u.Known || u.Wanted != (RecallRate{}) || u.UpperBound != 1 || u.LabelsFor5pct != 59 {
		t.Errorf("unlabelled cell %+v", u)
	}
	if md := d.Markdown(); !strings.Contains(md, "| dropped:model:seniority | 50 | 0 (0) | 0 | unknown | unknown | unknown | unknown |") {
		t.Errorf("unlabelled cell not shown as unknown:\n%s", md)
	}
	if d := BuildDropRecall(full, both); d.Status != RecallEstimated {
		t.Errorf("full: %s", d.Reason)
	}
}

func TestDropRecallAdversarialLabelsAndMembers(t *testing.T) {
	members := append(pool("gh:k", CellKept, 20), pool("gh:m", "dropped:model:role_fit", 2)...)
	members = append(members, members[0], members[20]) // listed twice: counted once
	members = append(members, RecallMember{Key: "", Cell: CellKept}, RecallMember{Key: "gh:x:1", Cell: "closed:dead"})
	members = append(members, RecallMember{Key: "gh:s:1", Cell: "dropped:model:role_fit", Marked: true, MarkedWanted: true})
	members = append(members, RecallMember{Key: "gh:h:1", Cell: CellKept, Marked: true})
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:2", 0, 3, 2, 0, 0.2)
	judge(labels, "gh:m", "dropped:model:role_fit", 0, 2, 0, 0, 0.5) // a pool of 2 needs 2, not 3
	yes := func(p float64, src string) Entry {
		return Entry{Labels: map[string]string{WouldApply: "yes"}, Source: src, PSelect: p}
	}
	labels["gh:k:0013"] = yes(0, SourceManual)
	labels["gh:k:0015"] = yes(0, "")
	labels["gh:gone:1"] = yes(0.1, "decile:2")                                       // not in the ledger any more
	labels["gh:x:1"] = yes(0.1, "closed:dead")                                       // closed: out of the pool
	labels["gh:s:1"] = yes(0.1, "dropped:model:role_fit")                            // marked: counted by the mark
	labels["gh:k:0014"] = Entry{Labels: map[string]string{"role_fit": "strong_fit"}} // a slot label only

	d := BuildDropRecall(labels, members)
	mustJSON(t, d)
	if d.Status != RecallEstimated {
		t.Fatalf("status %s: %s", d.Status, d.Reason)
	}
	k, m := cellOf(t, d, CellKept), cellOf(t, d, "dropped:model:role_fit")
	if k.Pool != 20 || k.Marked != 1 || k.MarkedWanted != 0 || k.Judged != 3 {
		t.Errorf("kept %+v", k)
	}
	if m.Pool != 2 || m.Marked != 1 || m.MarkedWanted != 1 || m.Judged != 2 || m.Wanted.K != 0 {
		t.Errorf("dropped %+v", m)
	}
	if d.Manual != 2 || d.OutOfPool != 2 || d.MarkedOut != 1 || d.Moved != 0 {
		t.Errorf("left out: manual %d out of pool %d marked %d moved %d; want 2 2 1 0", d.Manual, d.OutOfPool, d.MarkedOut, d.Moved)
	}
	// The saved drop is a known miss: the raw share is 1 / (20 × 2/3 + 1).
	if want := 1 / (20*2.0/3 + 1); math.Abs(d.Missed.Observed-want) > 1e-9 {
		t.Errorf("observed missed %v, want %v", d.Missed.Observed, want)
	}
	// A pool whose only posting is marked wanted: all dropped, no labels needed.
	only := BuildDropRecall(nil, []RecallMember{{Key: "a", Cell: "dropped:model:role_fit", Marked: true, MarkedWanted: true}})
	mustJSON(t, only)
	if only.Status != RecallEstimated || only.Missed.Share != 1 || only.Missed.Lo != 1 {
		t.Errorf("census only: %+v", only)
	}
	// Nothing wanted anywhere and nothing to sample: no ratio exists.
	none := BuildDropRecall(nil, []RecallMember{{Key: "a", Cell: "dropped:model:role_fit", Marked: true}, {Key: "b", Cell: CellKept, Marked: true}})
	mustJSON(t, none)
	if none.Status != RecallInsufficient || !strings.Contains(none.Reason, "no wanted posting is possible") {
		t.Errorf("all marked unwanted: %+v", none)
	}
	// Nothing says yes anywhere, but maybes exist: wanted estimates, yes-only
	// has no ratio and says so rather than printing 0%.
	may := map[posting.Key]Entry{}
	judge(may, "gh:k", "decile:2", 0, 3, 0, 3, 0.2)
	judge(may, "gh:m", "dropped:model:role_fit", 0, 2, 0, 0, 0.5)
	mm := BuildDropRecall(may, members[:22])
	mustJSON(t, mm)
	if mm.Status != RecallEstimated || mm.MissedYes == nil {
		// Jeffreys draws of 0 yes are still positive, so a ratio exists.
		t.Errorf("maybes only: %+v", mm)
	}
}

func TestLabelsForBound(t *testing.T) {
	// A class with a miss still has a reachable bound (TestLabelsForBoundWithAMiss).
	for _, c := range []struct{ k, n, want int }{{0, 0, 59}, {0, 5, 54}, {0, 59, 0}, {0, 200, 0}, {1, 93, 0}} {
		if got := labelsForBound(c.k, c.n); got != c.want {
			t.Errorf("labelsForBound(%d, %d) = %d, want %d", c.k, c.n, got, c.want)
		}
	}
	// The exact bound agrees with the zero-miss closed form, and grows with k.
	if a, b := upperBound(0, 59, 0.05), 1-math.Pow(0.05, 1.0/59); math.Abs(a-b) > 1e-12 || a > RecallBoundTarget {
		t.Errorf("0 of 59: %v vs %v", a, b)
	}
	if !(upperBound(1, 20, 0.05) > upperBound(0, 20, 0.05)) || upperBound(3, 3, 0.05) < 0.99 {
		t.Errorf("bounds out of order")
	}
}

// betaCDF integrates the Beta(a, b) density, a >= 0.5, by Simpson's rule
// after x = u² (which makes the a = ½ singularity at 0 smooth): an oracle
// independent of the sampler.
func betaCDF(a, b, x float64) float64 {
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	ld, _ := math.Lgamma(a + b)
	norm := math.Exp(ld - lb - lc)
	f := func(u float64) float64 {
		return 2 * norm * math.Pow(u, 2*a-1) * math.Pow(1-u*u, b-1)
	}
	const n = 20000
	hi := math.Sqrt(x)
	h := hi / n
	s := f(0) + f(hi)
	for i := 1; i < n; i++ {
		w := 2.0
		if i%2 == 1 {
			w = 4
		}
		s += w * f(float64(i)*h)
	}
	return s * h / 3
}

func betaQuantile(a, b, q float64) float64 {
	lo, hi := 0.0, 1.0
	for range 50 {
		mid := (lo + hi) / 2
		if betaCDF(a, b, mid) < q {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func TestBetaSamplerMatchesTheDensity(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, c := range []struct{ a, b float64 }{{0.5, 5.5}, {0.5, 0.5}, {2, 5}, {6.5, 4.5}, {0.5, 300.5}} {
		xs := make([]float64, 100000)
		for i := range xs {
			xs[i] = betaSample(r, c.a, c.b)
			if !(xs[i] >= 0 && xs[i] <= 1) {
				t.Fatalf("Beta(%v,%v) drew %v", c.a, c.b, xs[i])
			}
		}
		for _, q := range []float64{0.025, 0.5, 0.975} {
			got, want := quantile(xs, q), betaQuantile(c.a, c.b, q)
			if math.Abs(got-want) > 0.01+0.02*want {
				t.Errorf("Beta(%v,%v) q%.3f: sampled %.4f, density %.4f", c.a, c.b, q, got, want)
			}
		}
	}
	// Jeffreys for 0 of 5: the upper end is near 0.38, far from a zero.
	if hi := betaQuantile(0.5, 5.5, 0.975); hi < 0.3 || hi > 0.45 {
		t.Errorf("Jeffreys 0/5 upper %v", hi)
	}
	if !math.IsNaN(quantile(nil, 0.5)) {
		t.Error("the quantile of nothing is a number")
	}
}
