package eval

import (
	"math"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScorePairsIsAShrunkWinRate(t *testing.T) {
	// k:1 beats k:2 and k:3 (it is shown A, then B); k:4 and k:5 say neither.
	rows := []PairRow{
		pairRowOf("k:1", "k:2", "a", pairT0),
		pairRowOf("k:3", "k:1", "b", pairT0),
		pairRowOf("k:4", "k:5", "neither", pairT0),
	}
	scores, st := ScorePairs(rows, pairIdent())
	// 3 usable answers, 6 postings-in-a-pair games, 2 picks: pool rate 1/3.
	if st.Usable != 3 || st.Decisive != 2 || !near(st.PoolRate, 2.0/6) {
		t.Fatalf("stats %+v", st)
	}
	prior := PairPriorGames * st.PoolRate
	want := map[posting.Key]float64{
		"k:1": (2 + prior) / (2 + PairPriorGames),
		"k:2": (0 + prior) / (1 + PairPriorGames),
		"k:3": (0 + prior) / (1 + PairPriorGames),
		"k:4": (0 + prior) / (1 + PairPriorGames),
		"k:5": (0 + prior) / (1 + PairPriorGames),
	}
	for k, w := range want {
		if got := scores[k].Score; !near(got, w) {
			t.Errorf("%s scored %v, want %v", k, got, w)
		}
	}
	if scores["k:4"].Neither != 1 || scores["k:1"].Games != 2 || scores["k:1"].Picked != 2 {
		t.Errorf("counts %+v %+v", scores["k:4"], scores["k:1"])
	}
	if !(scores["k:1"].Score > scores["k:2"].Score) {
		t.Error("a posting that won twice does not outscore one that lost")
	}
	if st.NeitherShare < 0.33 || st.NeitherShare > 0.34 {
		t.Errorf("neither share %v", st.NeitherShare)
	}
	// k:1 was shown first once and second once, and picked both times; k:3 is B once.
	if !near(st.ShareA, 0.5) {
		t.Errorf("share of A %v, want 0.5", st.ShareA)
	}
}

func TestScorePairsCountsNothingForAbstentionsAndOtherQuestions(t *testing.T) {
	other := pairIdent()
	other.Model = "model-y"
	foreign := pairRowOf("k:7", "k:8", "a", pairT0)
	foreign.Model = other.Model
	rows := []PairRow{pairRowOf("k:1", "k:2", "a", pairT0), pairRowOf("k:3", "k:4", "", pairT0), foreign}
	failed := pairRowOf("k:5", "k:6", "a", pairT0)
	failed.Reading, failed.Answer, failed.Detail = "failed", "", "invalid_value"
	rows = append(rows, failed)
	scores, st := ScorePairs(rows, pairIdent())
	if st.Usable != 1 || st.Abstained != 1 || st.Unusable != 1 || st.OtherIdentity != 1 || st.Rows != 3 {
		t.Errorf("stats %+v", st)
	}
	for _, k := range []posting.Key{"k:3", "k:4", "k:5", "k:6", "k:7", "k:8"} {
		if _, ok := scores[k]; ok {
			t.Errorf("%s has a score from an abstention, a failure or another model", k)
		}
	}
	if len(scores) != 2 {
		t.Errorf("%d scored postings, want the 2 of the one usable answer", len(scores))
	}
}

func TestScorePairsWithNoUsableAnswerIsEmptyNotZero(t *testing.T) {
	if scores, st := ScorePairs(nil, pairIdent()); len(scores) != 0 || st.Usable != 0 {
		t.Errorf("no rows: %v %+v", scores, st)
	}
	scores, st := ScorePairs([]PairRow{pairRowOf("k:1", "k:2", "", pairT0)}, pairIdent())
	if len(scores) != 0 || st.Abstained != 1 || st.PoolRate != 0 {
		t.Errorf("only an abstention: %v %+v", scores, st)
	}
}

func TestScorePairsReadsPositionBiasAndBothOrderConsistency(t *testing.T) {
	// The model always says A: position, not content. Asked in both orders every
	// pair flips its chosen posting.
	var rows []PairRow
	for _, p := range [][2]string{{"k:1", "k:2"}, {"k:3", "k:4"}} {
		rows = append(rows, pairRowOf(p[0], p[1], "a", pairT0), pairRowOf(p[1], p[0], "a", pairT0))
	}
	// A third pair is consistent: k:5 wins whichever way it is shown.
	rows = append(rows, pairRowOf("k:5", "k:6", "a", pairT0), pairRowOf("k:6", "k:5", "b", pairT0))
	_, st := ScorePairs(rows, pairIdent())
	if st.Decisive != 6 || !near(st.ShareA, 5.0/6) {
		t.Errorf("share of A %v over %d decisive answers, want 5/6 over 6", st.ShareA, st.Decisive)
	}
	if st.BothOrders != 3 || st.Consistent != 1 {
		t.Errorf("both orders %d consistent %d, want 3 and 1", st.BothOrders, st.Consistent)
	}
}

func TestScorePairsKeepsTheNewestAnswerPerPairAndOrder(t *testing.T) {
	old := pairRowOf("k:1", "k:2", "a", pairT0)
	newer := pairRowOf("k:1", "k:2", "b", pairT0.Add(time.Hour))
	scores, st := ScorePairs([]PairRow{newer, old}, pairIdent()) // file order is not time order
	if st.Usable != 1 || scores["k:2"].Picked != 1 || scores["k:1"].Picked != 0 {
		t.Errorf("a re-ask did not supersede the old answer: %+v %+v", st, scores)
	}
}

func TestScorePairsIsIdenticalWhateverTheRowOrder(t *testing.T) {
	rows := []PairRow{pairRowOf("k:1", "k:2", "a", pairT0), pairRowOf("k:2", "k:3", "neither", pairT0), pairRowOf("k:3", "k:1", "b", pairT0)}
	a, sa := ScorePairs(rows, pairIdent())
	rev := []PairRow{rows[2], rows[1], rows[0]}
	b, sb := ScorePairs(rev, pairIdent())
	if sa.PoolRate != sb.PoolRate || len(a) != len(b) {
		t.Fatal("the order of the rows changed the score")
	}
	for k, s := range a {
		if b[k] != s {
			t.Errorf("%s: %+v against %+v", k, s, b[k])
		}
	}
}
