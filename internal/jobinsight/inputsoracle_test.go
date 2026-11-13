package jobinsight

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/expyears"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// inputsForOracle is InputsFor as it was before it was split into a blend half
// and ClosingFor (b9036fd), verbatim but for its name and the Seniority slot
// (added with Weights.SpareSeniorityMismatch, read the same way as RoleFit).
func inputsForOracle(r ledger.Record, c RankContext) Inputs {
	ann := r.Annotations
	in := Inputs{
		RoleFit:      c.SlotFor(r, "role_fit"),
		Seniority:    c.SlotFor(r, "seniority"),
		Unsure:       IsUnsure(r),
		HalfLifeDays: c.HalfLifeDays,
		AgeDays:      -1,
		AgeNeutral:   c.AgeNeutral[companyKey(r.Posting.Company)],
	}

	rank, hasRank := parseFloat(ann[ledger.AnnRank])
	if cov, ok := parseFloat(ann[ledger.AnnCoverage]); ok {
		in.Coverage, in.HasCoverage = cov, true
		in.PreferredEmployer = hasRank && rank-cov >= ledger.EmployerRankBoost-employerBoostSlack
	} else if hasRank {
		in.Coverage, in.HasCoverage = rank, true
	}

	if c.TitleRank != nil {
		in.TitleSignals, in.TitleRank = true, c.TitleRank(r.Posting.Title)
	}
	if c.Target && c.Hits != nil {
		in.Target = true
		in.TitleHits, in.BodyHits = c.Hits(r)
	}
	if c.KBMatch != nil {
		if s, ok := c.KBMatch(r); ok {
			in.KBMatch = s
		}
	}
	if c.Semantic != nil {
		in.Semantic, in.HasSemantic = c.Semantic(r)
		if !in.HasSemantic {
			in.Semantic = 0
		}
	}

	if at, _, known := FreshAge(r.Posting.PostedAt, r.FirstSeen, c.FirstCrawl[BoardOf(string(r.Key))], c.Now); known {
		in.AgeDays, in.AgeKnown = AgeDays(at, c.Now), true
	} else if !at.IsZero() {
		// Age unknown, but the first sighting bounds it from below (Score).
		in.SeenDays, in.SeenKnown = AgeDays(at, c.Now), true
	}

	seen := map[string]bool{}
	for _, rule := range c.DropRules {
		if seen[rule.Slot] {
			continue
		}
		s := c.SlotFor(r, rule.Slot)
		if !s.Answered {
			continue
		}
		if rule.Slot == "seniority" && s.Value == "staff_plus" {
			if level, ok := expyears.BelowStaff(r.Posting.Description); ok {
				s.Value = level
			}
		}
		for _, v := range rule.Values {
			if strings.TrimSpace(v) == s.Value && s.Value != "" {
				seen[rule.Slot] = true
				in.Penalties = append(in.Penalties, Penalty{Name: rule.Slot, Slot: s})
				break
			}
		}
	}

	in.Company = c.Company[companyKey(r.Posting.Company)]
	switch c.Marks[r.Key] {
	case marks.Saved, marks.Applied:
		in.Company.Liked--
	case marks.Hidden:
		in.Company.Hidden--
	}
	in.Company.Liked, in.Company.Hidden = max(in.Company.Liked, 0), max(in.Company.Hidden, 0)
	return in
}

// randomRankCase is a record set and a context over it that sets every
// optional part of RankContext, each on or off.
func randomRankCase(rng *rand.Rand) ([]ledger.Record, RankContext) {
	pick := func(s []string) string { return s[rng.IntN(len(s))] }
	fits := []string{"strong_fit", "partial_fit", "adjacent", "mismatch_domain", "", "bogus"}
	nums := []string{"", "0", "0.4", "1", " 0.9 ", "NaN", "-1", "2", "x"}
	companies := []string{"Acme", "acme ", "Globex", "Initech"}
	statuses := []posting.Status{posting.Explained, posting.Scored, posting.Unsure, posting.Digested, posting.Dead}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var recs []ledger.Record
	for i := range 30 {
		ann := map[string]string{}
		for _, slot := range []string{"role_fit", "sponsorship", "seniority"} {
			pre := ledger.AnnTriagePrefix + slot
			if slot == "role_fit" {
				ann[pre] = pick(fits)
			} else {
				ann[pre] = pick([]string{"", "yes", "no", "senior"})
			}
			ann[pre+ledger.AnnTriageSuffixReading] = pick([]string{"", "answered", "unsure"})
			ann[pre+ledger.AnnTriageSuffixP] = pick(nums)
			ann[pre+ledger.AnnTriageSuffixShadow] = pick(fits)
			ann[pre+ledger.AnnTriageSuffixShadowReading] = pick([]string{"", "answered"})
			ann[pre+ledger.AnnTriageSuffixShadowBackend] = pick([]string{"", ShadowEscalate, "lappi"})
		}
		ann[ledger.AnnRank] = pick(nums)
		ann[ledger.AnnCoverage] = pick(nums)
		if rng.IntN(4) == 0 {
			ann[ledger.AnnTriagePrefix+"unsure"] = "true"
		}
		k := posting.Key(fmt.Sprintf("gh:%s:%02d", pick([]string{"acme", "globex"}), i))
		p := posting.Posting{Key: k, Company: pick(companies), Title: pick([]string{"ML Engineer", "Sales", "Go Platform"})}
		if rng.IntN(2) == 0 {
			p.PostedAt = now.Add(-time.Duration(rng.IntN(90*24)) * time.Hour)
		}
		recs = append(recs, ledger.Record{Key: k, Status: statuses[rng.IntN(len(statuses))], Annotations: ann, Posting: p,
			FirstSeen: now.Add(-time.Duration(rng.IntN(60*24)) * time.Hour)})
	}
	m := map[posting.Key]marks.Mark{}
	for _, r := range recs {
		m[r.Key] = []marks.Mark{marks.None, marks.Saved, marks.Applied, marks.Hidden}[rng.IntN(4)]
	}
	c := NewRankContext(recs, m, now)
	c.HalfLifeDays = []float64{0, 7, 30}[rng.IntN(3)]
	if rng.IntN(2) == 0 {
		c.TitleRank = func(title string) int { return len(title) % 3 }
	}
	c.Target = rng.IntN(2) == 0
	if rng.IntN(2) == 0 {
		c.Hits = func(r ledger.Record) (int, int) { return len(r.Posting.Title) % 4, len(r.Key) % 5 }
	}
	if rng.IntN(2) == 0 {
		c.Semantic = func(r ledger.Record) (float64, bool) { return float64(len(r.Key)%10) / 10, len(r.Key)%3 != 0 }
	}
	if rng.IntN(2) == 0 {
		c.KBMatch = func(r ledger.Record) (Slot, bool) {
			return Slot{Value: fits[len(r.Posting.Title)%len(fits)], Answered: true, P: 0.7, HasP: true}, len(r.Key)%2 == 0
		}
	}
	if rng.IntN(2) == 0 {
		c.DropRules = []DropRule{{Slot: "sponsorship", Values: []string{"no"}}, {Slot: "seniority", Values: []string{" senior "}},
			{Slot: "sponsorship", Values: []string{"yes"}}}
	}
	if rng.IntN(2) == 0 {
		c.AgeNeutral = AgeNeutralSet([]string{"Acme"})
	}
	if rng.IntN(2) == 0 {
		w := DefaultWeights()
		w.FreshFloor, w.UnknownPrior = 0.3, 0.6
		c.Weights = &w
	}
	c.EscalateBelowP = []float64{0, 0.5, 0.95}[rng.IntN(3)]
	if rng.IntN(2) == 0 {
		c.Manual = func(r ledger.Record, slot string) (string, bool) {
			if slot == "role_fit" && len(r.Key)%2 == 1 {
				return "strong_fit", true
			}
			return "", false
		}
	}
	return recs, c
}

// InputsFor, its two halves and the scores built from them answer what the
// pinned InputsFor answered, to the bit, under every optional part of the
// context on and off.
func TestInputsForSplitMatchesPinnedOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(43, 47))
	bits := func(r Result) string {
		f, _ := json.Marshal(r.Factors)
		return fmt.Sprintf("%x %x %x %x %x %x %x %s %s", math.Float64bits(r.Score), math.Float64bits(r.Relevance),
			math.Float64bits(r.RelevanceKnown), math.Float64bits(r.Match), math.Float64bits(r.Gate),
			math.Float64bits(r.Fresh), math.Float64bits(r.Company), r.Basis, f)
	}
	for round := range 400 {
		recs, c := randomRankCase(rng)
		for _, r := range recs {
			got, want := InputsFor(r, c), inputsForOracle(r, c)
			if !reflect.DeepEqual(got, want) {
				gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
				for i := range gv.NumField() {
					if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
						t.Errorf("field %s: InputsFor %+v, oracle %+v", gv.Type().Field(i).Name, gv.Field(i), wv.Field(i))
					}
				}
				t.Fatalf("round %d %s: InputsFor differs from the oracle", round, r.Key)
			}
			if cl := c.ClosingFor(r); cl != ClosingOf(want) {
				t.Fatalf("round %d %s: ClosingFor %+v, oracle's %+v", round, r.Key, cl, ClosingOf(want))
			}
			w := c.weights()
			score := ScoreWith(want, w)
			b := c.BlendFor(r)
			if g, wnt := bits(b.Close(c.ClosingFor(r), w)), bits(score); g != wnt {
				t.Fatalf("round %d %s: BlendFor+Close %s, oracle %s", round, r.Key, g, wnt)
			}
			order := score
			order.Factors = nil
			if g, wnt := bits(c.CloseOrder(&b, r)), bits(order); g != wnt {
				t.Fatalf("round %d %s: CloseOrder %s, oracle %s", round, r.Key, g, wnt)
			}
			if g := bits(c.OrderOf(r)); g != bits(order) {
				t.Fatalf("round %d %s: OrderOf %s, oracle %s", round, r.Key, g, bits(order))
			}
		}
	}
}
