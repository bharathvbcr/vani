package jobinsight

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Confidence scales a part's weight, not its value.
func TestConfidenceScalesWeightNotValue(t *testing.T) {
	others := func(in Inputs) Inputs {
		in.Coverage, in.HasCoverage = 90, true
		in.Semantic, in.HasSemantic = 0.9, true
		in.AgeKnown, in.AgeDays = true, 2
		return in
	}
	// A reading at p~0 is nearly unknown: the posting scores as if the model
	// had not read it, not as a fit leaning toward its neighbours.
	t.Run("p near zero contributes nothing", func(t *testing.T) {
		unread := Score(others(Inputs{}))
		for _, v := range []string{"strong_fit", "adjacent", "partial_fit"} {
			read := Score(others(Inputs{RoleFit: sl(v, 0.001)}))
			if math.Abs(read.Score-unread.Score) > 0.5 || math.Abs(read.Relevance-unread.Relevance) > 0.5 {
				t.Errorf("%s at p=0.001: score %.2f relevance %.2f, unread %.2f / %.2f", v, read.Score, read.Relevance, unread.Score, unread.Relevance)
			}
		}
	})
	t.Run("a low-p strong_fit does not outrank a confident partial_fit", func(t *testing.T) {
		mid := func(in Inputs) Inputs {
			in.Coverage, in.HasCoverage = 50, true
			in.Semantic, in.HasSemantic = 0.5, true
			in.AgeKnown, in.AgeDays = true, 2
			return in
		}
		lo := Score(mid(Inputs{RoleFit: sl("strong_fit", 0.2)}))
		hi := Score(mid(Inputs{RoleFit: sl("partial_fit", 0.9)}))
		if lo.Score >= hi.Score || lo.Relevance >= hi.Relevance {
			t.Errorf("strong@0.2 %.2f/%.2f >= partial@0.9 %.2f/%.2f", lo.Score, lo.Relevance, hi.Score, hi.Relevance)
		}
	})
	t.Run("when other parts are weak, more confidence in a fit never hurts", func(t *testing.T) {
		weak := func(p float64) float64 {
			return Score(Inputs{RoleFit: sl("strong_fit", p), Coverage: 20, HasCoverage: true, Semantic: 0.2, HasSemantic: true, AgeKnown: true}).Score
		}
		prev := -1.0
		for _, p := range []float64{0.05, 0.2, 0.4, 0.6, 0.8, 1} {
			if s := weak(p); s < prev-1e-9 {
				t.Fatalf("score fell from %.3f to %.3f as p rose to %v", prev, s, p)
			} else {
				prev = s
			}
		}
	})
	t.Run("an uncalibrated answer weighs AnsweredConf", func(t *testing.T) {
		// adjacent (0.3) under strong other parts: the less the answer is trusted,
		// the less it drags the posting down.
		in := others(Inputs{RoleFit: Slot{Value: "adjacent", Answered: true}})
		w := DefaultWeights()
		lowConf := w
		lowConf.AnsweredConf = 0.05
		if ScoreWith(in, lowConf).Relevance <= ScoreWith(in, w).Relevance {
			t.Errorf("trusting an adjacent answer less did not raise relevance: %.2f vs %.2f", ScoreWith(in, lowConf).Relevance, ScoreWith(in, w).Relevance)
		}
	})
}

// Mismatch is penalised once, by the gate.
func TestMismatchPenalisedOnce(t *testing.T) {
	base := baseIn()
	base.RoleFit = sl("mismatch_domain", 0.9)
	gated := Score(base)

	t.Run("a drop-rule penalty on the same reading adds nothing", func(t *testing.T) {
		dup := base
		dup.Penalties = []Penalty{{Slot: base.RoleFit}}
		if got := Score(dup); got.Gate != gated.Gate || got.Score != gated.Score {
			t.Errorf("penalty on the same slot changed gate %.3f -> %.3f", gated.Gate, got.Gate)
		}
	})
	t.Run("a penalty on a different reading still applies", func(t *testing.T) {
		other := base
		other.Penalties = []Penalty{{Slot: sl("yes", 0.9)}}
		if got := Score(other); got.Gate >= gated.Gate {
			t.Errorf("a different slot's penalty was lost: gate %.3f vs %.3f", got.Gate, gated.Gate)
		}
	})
	t.Run("the fit part no longer penalises as well as the gate", func(t *testing.T) {
		none := baseIn()
		none.RoleFit = Slot{}
		if a, b := Score(base).Match, Score(none).Match; math.Abs(a-b) > 1e-9 {
			t.Errorf("mismatch reading moved match %.4f vs unread %.4f: penalised in match and in the gate", a, b)
		}
		want := 1 - DefaultWeights().Mismatch*0.9
		if math.Abs(gated.Gate-want) > 1e-9 {
			t.Errorf("gate %.4f, want %.4f", gated.Gate, want)
		}
	})
	t.Run("repeated penalties on the mismatch reading do not stack", func(t *testing.T) {
		in := base
		in.Penalties = []Penalty{{Slot: base.RoleFit}, {Slot: base.RoleFit}}
		if got := Score(in); got.Gate != gated.Gate {
			t.Errorf("gate %.3f, want %.3f", got.Gate, gated.Gate)
		}
	})
	t.Run("seniority mismatch likewise", func(t *testing.T) {
		in := baseIn()
		in.RoleFit = sl("mismatch_seniority", 0.7)
		g := Score(in).Gate
		in.Penalties = []Penalty{{Slot: in.RoleFit}}
		if Score(in).Gate != g {
			t.Errorf("seniority mismatch penalised twice")
		}
	})
}

func TestMatchShrinkThinEvidence(t *testing.T) {
	if got := DefaultWeights().MatchShrink; got != 0.30 {
		t.Errorf("MatchShrink %v, want 0.30", got)
	}
	semOnly := Score(Inputs{Semantic: 0.9, HasSemantic: true})
	all := Score(Inputs{
		RoleFit: sl("strong_fit", 1), Coverage: 90, HasCoverage: true, Semantic: 0.9, HasSemantic: true,
		Target: true, TitleHits: 5, BodyHits: 20, TitleSignals: true, TitleRank: 3, PreferredEmployer: true,
	})
	if semOnly.Match >= all.Match-0.10 {
		t.Errorf("semantic-only match %.3f is not clearly below all-parts-known %.3f", semOnly.Match, all.Match)
	}
	if semOnly.Match <= DefaultWeights().UnknownPrior {
		t.Errorf("a strong semantic reading alone must still beat the prior: %.3f", semOnly.Match)
	}
}

// Company: saves and applies lift, hides do nothing, the cap holds.
func TestCompanyFactorUpwardOnly(t *testing.T) {
	base := baseIn()
	one := Score(base)
	if one.Company != 1 {
		t.Fatalf("no marks: company %v", one.Company)
	}
	hid := base
	hid.Company = CompanyMarks{Hidden: 40}
	if got := Score(hid); got.Company != 1 || got.Score != one.Score {
		t.Errorf("hides moved the company factor to %v (score %v vs %v)", got.Company, got.Score, one.Score)
	}
	mixed := base
	mixed.Company = CompanyMarks{Liked: 4, Hidden: 40}
	liked := base
	liked.Company = CompanyMarks{Liked: 4}
	if Score(mixed).Company != Score(liked).Company {
		t.Errorf("hides diluted saves: %v vs %v", Score(mixed).Company, Score(liked).Company)
	}
	if Score(liked).Company <= 1 || Score(liked).Company > CompanyMax {
		t.Errorf("liked company %v outside (1, %v]", Score(liked).Company, CompanyMax)
	}
	if CompanyMin != 1 {
		t.Errorf("CompanyMin %v, want 1 (no penalty)", CompanyMin)
	}
	// The cap: a near-perfect posting of a much-liked company clips at 100.
	top := Inputs{
		RoleFit: sl("strong_fit", 1), Coverage: 100, HasCoverage: true, Semantic: 1, HasSemantic: true,
		Target: true, TitleHits: 9, BodyHits: 99, TitleSignals: true, TitleRank: 3, PreferredEmployer: true,
		AgeKnown: true, AgeDays: 0, Company: CompanyMarks{Liked: 1000},
	}
	r := Score(top)
	if r.Score > 100 || r.Relevance > 100 || r.Company > CompanyMax {
		t.Errorf("score %v relevance %v company %v over the cap", r.Score, r.Relevance, r.Company)
	}
	// With no shrink and a wide span the product passes 1: Score clips at 100.
	wide := DefaultWeights()
	wide.MatchShrink, wide.CompanySpan = 0, 1
	if r := ScoreWith(top, wide); r.Score != 100 || r.Relevance != 100 || r.Company <= 1.5 {
		t.Errorf("clip: score %v relevance %v company %v, want 100 / 100 / above 1.5", r.Score, r.Relevance, r.Company)
	}
}

func TestCompareOrders(t *testing.T) {
	a := Result{Score: 50, Relevance: 60}
	b := Result{Score: 50, Relevance: 70}
	if ComparePriority(a, b, "a", "b") <= 0 {
		t.Error("equal priority: higher relevance goes first")
	}
	c := Result{Score: 50, Relevance: 60}
	if ComparePriority(a, c, "a", "b") >= 0 || ComparePriority(c, a, "b", "a") <= 0 {
		t.Error("full tie: key ascending")
	}
	if ComparePriority(a, a, "a", "a") != 0 {
		t.Error("same key compares equal")
	}
	hi := Result{Score: 60, Relevance: 10}
	if ComparePriority(hi, b, "z", "a") >= 0 {
		t.Error("priority leads in a priority order")
	}
	if CompareRelevance(hi, b, "z", "a") <= 0 {
		t.Error("relevance leads in a relevance order")
	}
	nan := Result{Score: math.NaN(), Relevance: math.NaN()}
	if ComparePriority(nan, nan, "a", "b") >= 0 {
		t.Error("NaN scores must still order by key, not panic or report unordered")
	}
}

type dv struct{ company, title string }

func diversify(rows []dv, perCompany, window int) []DiverseRow {
	return Diversify(len(rows), func(i int) string { return rows[i].company }, func(i int) string { return rows[i].title }, perCompany, window)
}

func TestDiversify(t *testing.T) {
	idx := func(rs []DiverseRow) []int {
		out := make([]int, 0, len(rs))
		for _, r := range rs {
			out = append(out, r.Index)
		}
		return out
	}
	t.Run("empty", func(t *testing.T) {
		if got := diversify(nil, 3, 50); len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("exact company+title duplicates collapse with a count", func(t *testing.T) {
		rows := []dv{{"Acme", "ML Engineer"}, {"acme ", "ml  engineer"}, {"Acme", "ML Engineer!"}, {"Acme", "Data Engineer"}, {"Beta", "ML Engineer"}}
		got := diversify(rows, 3, 50)
		if !reflect.DeepEqual(idx(got), []int{0, 3, 4}) {
			t.Fatalf("kept %v", idx(got))
		}
		if len(got[0].Similar) != 2 || got[0].Similar[0] != 1 || got[0].Similar[1] != 2 || len(got[1].Similar) != 0 {
			t.Errorf("similar %+v", got)
		}
	})
	t.Run("all one company keeps three in the window and moves the rest below it", func(t *testing.T) {
		var rows []dv
		for i := 0; i < 8; i++ {
			rows = append(rows, dv{"Acme", "Role " + string(rune('a'+i))})
		}
		got := diversify(rows, 3, 50)
		if len(got) != 8 {
			t.Fatalf("nothing is dropped: %d rows", len(got))
		}
		if !reflect.DeepEqual(idx(got), []int{0, 1, 2, 3, 4, 5, 6, 7}) {
			t.Errorf("order %v", idx(got))
		}
	})
	t.Run("the cap holds in the top window and later rows keep their order", func(t *testing.T) {
		var rows []dv
		for i := 0; i < 10; i++ {
			rows = append(rows, dv{"Acme", "Role " + string(rune('a'+i))})
		}
		rows = append(rows, dv{"Beta", "one"}, dv{"Gamma", "two"}, dv{"Delta", "three"}, dv{"Eps", "four"})
		got := diversify(rows, 3, 6)
		want := []int{0, 1, 2, 10, 11, 12, 3, 4, 5, 6, 7, 8, 9, 13}
		if !reflect.DeepEqual(idx(got), want) {
			t.Fatalf("order %v, want %v", idx(got), want)
		}
		n := 0
		for _, r := range got[:6] {
			if rows[r.Index].company == "Acme" {
				n++
			}
		}
		if n > 3 {
			t.Errorf("%d Acme rows in the top 6", n)
		}
	})
	t.Run("ties keep input order", func(t *testing.T) {
		rows := []dv{{"A", "x"}, {"B", "x"}, {"A", "y"}, {"B", "y"}}
		if got := idx(diversify(rows, 3, 50)); !reflect.DeepEqual(got, []int{0, 1, 2, 3}) {
			t.Errorf("order %v", got)
		}
	})
	t.Run("unusable limits and empty names do not panic or drop rows", func(t *testing.T) {
		rows := []dv{{"", ""}, {"", ""}, {"A", ""}}
		for _, lim := range [][2]int{{0, 0}, {-1, -1}, {1, 1}} {
			got := diversify(rows, lim[0], lim[1])
			seen := 0
			for _, r := range got {
				seen += 1 + len(r.Similar)
			}
			if seen != 3 {
				t.Errorf("limits %v account for %d of 3 rows", lim, seen)
			}
		}
	})
	t.Run("a huge title is normalised within bounds", func(t *testing.T) {
		long := strings.Repeat("x ", 1<<16)
		got := diversify([]dv{{"A", long}, {"A", long}}, 3, 50)
		if len(got) != 1 || len(got[0].Similar) != 1 {
			t.Errorf("got %+v", got)
		}
	})
}
