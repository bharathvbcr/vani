package rankeval

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	vposting "github.com/bharathvbcr/Vani/internal/posting"
)

// TestRealLedgerComposition ranks a real ledger (VANI_REAL_STATE, a Vani state
// directory; read only) with the legacy orders and with Score variants, and
// prints only aggregates: the role_fit mix and ages of the top 10/20/50, and a
// proxy NDCG@20 whose gain is the decision model's role_fit label discounted by
// age (half-life 14 days; unknown age 0.3). The proxy is partly circular (Score
// reads role_fit too); it is a sanity check of how fit and recency trade off on
// real readings, not a ground truth. No titles, companies or text are printed.
//
// REGRESSION-ONLY: this proxy shares its inputs with the ranker, so it detects
// a change in how Score trades fit against recency, not whether the ranking
// matches what the owner wants. Relevance is measured by `vani relevance`, from
// the owner's would_apply labels (internal/eval).
func TestRealLedgerComposition(t *testing.T) {
	dir := os.Getenv("VANI_REAL_STATE")
	if dir == "" {
		t.Skip("set VANI_REAL_STATE to a Vani state directory to rank a real ledger")
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []ledger.Record
	var now time.Time
	for _, r := range snap.Records {
		if r.FirstSeen.After(now) {
			now = r.FirstSeen
		}
		switch r.Status {
		case vposting.Triaged, vposting.Unsure, vposting.Scored, vposting.Explained, vposting.Digested:
			recs = append(recs, r)
		}
	}
	now = now.Add(time.Hour)
	rc := jobinsight.NewRankContext(snap.Records, nil, now)
	ins := make([]jobinsight.Inputs, len(recs))
	gain := make([]float64, len(recs))
	leg := make([]legacyRec, len(recs))
	for i, r := range recs {
		ins[i] = jobinsight.InputsFor(r, rc)
		grade := map[string]float64{"strong_fit": 3, "partial_fit": 2, "adjacent": 1}[r.Annotations[ledger.AnnTriagePrefix+"role_fit"]]
		rec := 0.3
		if ins[i].AgeKnown {
			rec = math.Exp2(-float64(max(ins[i].AgeDays, 0)) / 14)
		}
		gain[i] = (math.Exp2(grade) - 1) * rec
		leg[i] = realLegacy(i, r, now)
	}
	t.Logf("REGRESSION-ONLY proxy (circular with Score; not a relevance measure: see `vani relevance`)")
	t.Logf("real ledger: %d ranked postings (of %d), now %s", len(recs), len(snap.Records), now.Format(time.DateOnly))

	order := func(less func(a, b int) bool) []int {
		idx := make([]int, len(recs))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return less(idx[a], idx[b]) })
		return idx
	}
	report := func(name string, idx []int) {
		var parts []string
		for _, k := range []int{10, 20, 50} {
			k = min(k, len(idx))
			mix := map[string]int{}
			var ages []int
			unknown := 0
			for _, i := range idx[:k] {
				mix[shortFit(recs[i].Annotations[ledger.AnnTriagePrefix+"role_fit"])]++
				if ins[i].AgeKnown {
					ages = append(ages, ins[i].AgeDays)
				} else {
					unknown++
				}
			}
			sort.Ints(ages)
			med := "-"
			if len(ages) > 0 {
				med = strconv.Itoa(ages[len(ages)/2])
			}
			parts = append(parts, fmt.Sprintf("top%d S%d P%d A%d MS%d MD%d age~%sd unk%d", k, mix["S"], mix["P"], mix["A"], mix["MS"], mix["MD"], med, unknown))
		}
		t.Logf("%-22s ndcg@20 %.3f | %s", name, ndcgAt(idx, gain, 20), strings.Join(parts, " | "))
	}

	report("legacyDefault", order(func(a, b int) bool { return legacyDefaultLess(leg[a], leg[b]) }))
	report("legacyPriority", order(func(a, b int) bool { return legacyPriorityLess(leg[a], leg[b]) }))
	for _, v := range realVariants() {
		w := v.w
		s := make([]float64, len(recs))
		for i := range recs {
			s[i] = jobinsight.ScoreWith(ins[i], w).Score
		}
		report(v.name, order(func(a, b int) bool {
			if s[a] != s[b] {
				return s[a] > s[b]
			}
			return recs[a].Key < recs[b].Key
		}))
	}

	// A rank penalty on seniority "senior" (config rank_penalties), as the
	// owner's early-career target would set it; the proxy gain does not read
	// seniority, so the ages and fit mix are the measure, plus how many senior
	// readings remain in the top 20.
	rcPen := rc
	rcPen.DropRules = []jobinsight.DropRule{{Slot: "seniority", Values: []string{"senior"}}}
	s := make([]float64, len(recs))
	for i, r := range recs {
		s[i] = jobinsight.Score(jobinsight.InputsFor(r, rcPen)).Score
	}
	idx := order(func(a, b int) bool {
		if s[a] != s[b] {
			return s[a] > s[b]
		}
		return recs[a].Key < recs[b].Key
	})
	report("+senior penalty", idx)
	senior := func(idx []int) int {
		n := 0
		for _, i := range idx[:min(20, len(idx))] {
			if jobinsight.SlotOf(recs[i].Annotations, "seniority").Value == "senior" {
				n++
			}
		}
		return n
	}
	base := make([]float64, len(recs))
	for i := range recs {
		base[i] = jobinsight.Score(ins[i]).Score
	}
	t.Logf("senior readings in top 20: default %d, +senior penalty %d", senior(order(func(a, b int) bool {
		if base[a] != base[b] {
			return base[a] > base[b]
		}
		return recs[a].Key < recs[b].Key
	})), senior(idx))
}

type realVariant struct {
	name string
	w    jobinsight.Weights
}

func realVariants() []realVariant {
	d := jobinsight.DefaultWeights()
	with := func(f func(*jobinsight.Weights)) jobinsight.Weights { w := d; f(&w); return w }
	return []realVariant{
		{"Score(default)", d},
		{"-ordinal", with(func(w *jobinsight.Weights) { w.OrdinalFit = false })},
		{"-known", with(func(w *jobinsight.Weights) { w.KnownMatch = false })},
		{"-ordinal-known", with(func(w *jobinsight.Weights) { w.OrdinalFit, w.KnownMatch = false, false })},
		{"old fit values", with(func(w *jobinsight.Weights) { w.FitValues = [5]float64{0.85, 0.65, 0.45, 0.20, 0.10} })},
		{"half-life 14", with(func(w *jobinsight.Weights) { w.HalfLifeDays = 14 })},
	}
}

// ndcgAt is NDCG@k over real-valued gains (log2 discount).
func ndcgAt(order []int, gain []float64, k int) float64 {
	dcg := func(g []float64) float64 {
		var s float64
		for i := 0; i < k && i < len(g); i++ {
			s += g[i] / math.Log2(float64(i)+2)
		}
		return s
	}
	got := make([]float64, len(order))
	for i, j := range order {
		got[i] = gain[j]
	}
	ideal := append([]float64(nil), gain...)
	sort.Sort(sort.Reverse(sort.Float64Slice(ideal)))
	if d := dcg(ideal); d > 0 {
		return dcg(got) / d
	}
	return 0
}

func shortFit(v string) string {
	return map[string]string{"strong_fit": "S", "partial_fit": "P", "adjacent": "A", "mismatch_seniority": "MS", "mismatch_domain": "MD"}[v]
}

// realLegacy is a real record as the legacy rankers saw it: the ledger's rank,
// the role_fit value, no keyword hits (no vocabulary here), and the legacy age
// (posted date, else first seen, so a first-crawl posting looks new).
func realLegacy(id int, r ledger.Record, now time.Time) legacyRec {
	l := legacyRec{id: id, fit: r.Annotations[ledger.AnnTriagePrefix+"role_fit"], unsure: jobinsight.IsUnsure(r)}
	if v, err := strconv.ParseFloat(strings.TrimSpace(r.Annotations[ledger.AnnRank]), 64); err == nil && !math.IsNaN(v) {
		l.rank, l.hasRank = v, true
	}
	at := r.FirstSeen
	if p := r.Posting.PostedAt; !p.IsZero() && p.Before(now) {
		at = p
	}
	l.age = int(now.Sub(at) / (24 * time.Hour))
	l.firstSeen = int(now.Sub(r.FirstSeen) / (24 * time.Hour))
	return l
}
