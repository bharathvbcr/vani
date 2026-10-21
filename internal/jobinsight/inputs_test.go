package jobinsight

import (
	"maps"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var inT0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func rec(key, company string, seenAgo time.Duration, ann map[string]string) ledger.Record {
	return ledger.Record{
		Key:         posting.Key(key),
		Status:      posting.Explained,
		FirstSeen:   inT0.Add(-seenAgo),
		Posting:     posting.Posting{Key: posting.Key(key), Company: company, Title: "Engineer"},
		Annotations: ann,
	}
}

func TestInputsForMissingAnnotations(t *testing.T) {
	r := rec("gh:a:1", "Acme", time.Hour, nil)
	c := NewRankContext([]ledger.Record{r, rec("gh:a:0", "Acme", 48*time.Hour, nil)}, nil, inT0)
	in := InputsFor(r, c)
	want := Inputs{AgeKnown: true, AgeDays: 0}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("got %+v, want %+v", in, want)
	}
}

func TestInputsForSlots(t *testing.T) {
	ann := func(kv ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		name string
		ann  map[string]string
		want Slot
	}{
		{"answered with p", ann("triage.role_fit", "strong_fit", "triage.role_fit.reading", "answered", "triage.role_fit.p", " 0.9 "),
			Slot{Value: "strong_fit", Answered: true, P: 0.9, HasP: true}},
		{"answered without p", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered"),
			Slot{Value: "adjacent", Answered: true}},
		{"abstained keeps no p", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "abstained", "triage.role_fit.p", "0.99"),
			Slot{Value: "adjacent"}},
		{"NaN p", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.p", "NaN"),
			Slot{Value: "adjacent", Answered: true}},
		{"inf p", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.p", "+Inf"),
			Slot{Value: "adjacent", Answered: true}},
		{"garbage p", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.p", "0.9x"),
			Slot{Value: "adjacent", Answered: true}},
		{"p above 1", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.p", "1.5"),
			Slot{Value: "adjacent", Answered: true}},
		{"p below 0", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.p", "-0.1"),
			Slot{Value: "adjacent", Answered: true}},
		{"disagree", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.disagree", "true"),
			Slot{Value: "adjacent", Answered: true, Disagree: true}},
		{"disagree other text", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.disagree", "yes"),
			Slot{Value: "adjacent", Answered: true}},
		{"shadow backend", ann("triage.role_fit", "adjacent", "triage.role_fit.reading", "answered", "triage.role_fit.disagree", "true", "triage.role_fit.shadow_backend", " escalate "),
			Slot{Value: "adjacent", Answered: true, Disagree: true, ShadowBackend: "escalate"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := InputsFor(rec("gh:a:1", "Acme", time.Hour, c.ann), RankContext{Now: inT0}).RoleFit
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// A Lappi shadow's disagreement halves the reading's confidence; an
// escalation's does not, since escalation runs only under an already low p.
// The records differ only in the shadow backend, and Disagree stays as the
// ledger recorded it either way.
func TestDisagreeConfSkipsEscalation(t *testing.T) {
	base := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.6"}
	with := func(kv ...string) map[string]string {
		m := maps.Clone(base)
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	w := DefaultWeights()
	conf := func(ann map[string]string) (float64, Slot) {
		s := InputsFor(rec("gh:a:1", "Acme", time.Hour, ann), RankContext{Now: inT0}).RoleFit
		return w.conf(s), s
	}
	baseline, _ := conf(base)
	lappi, ls := conf(with("triage.role_fit.disagree", "true", "triage.role_fit.shadow_backend", "lappi"))
	esc, es := conf(with("triage.role_fit.disagree", "true", "triage.role_fit.shadow_backend", "escalate"))
	if !ls.Disagree || !es.Disagree {
		t.Fatalf("disagree not read: lappi %+v, escalate %+v", ls, es)
	}
	if math.Abs(lappi-baseline*w.DisagreeConf) > 1e-12 || w.DisagreeConf != 0.5 {
		t.Fatalf("lappi disagreement conf %v, want half of %v", lappi, baseline)
	}
	if esc != baseline {
		t.Fatalf("escalation disagreement conf %v, want the baseline %v", esc, baseline)
	}
	if a, b := Score(InputsFor(rec("gh:a:1", "Acme", time.Hour, with("triage.role_fit.disagree", "true", "triage.role_fit.shadow_backend", "escalate")), RankContext{Now: inT0})).Score,
		Score(InputsFor(rec("gh:a:1", "Acme", time.Hour, base), RankContext{Now: inT0})).Score; a != b {
		t.Fatalf("escalation disagreement score %v, want the agreeing score %v", a, b)
	}
}

func TestInputsForCoverageAndPreferredEmployer(t *testing.T) {
	boost := ledger.EmployerRankBoost
	for _, c := range []struct {
		name       string
		rank, cov  string
		wantCov    float64
		hasCov     bool
		wantPrefer bool
	}{
		{"plain", "62.5", "62.5", 62.5, true, false},
		{"boosted", "72.5", "62.5", 62.5, true, true},
		{"boosted past 100", "105.2", "95.2", 95.2, true, true},
		{"smaller gap is no boost", "65", "62.5", 62.5, true, false},
		{"no coverage key falls back to rank", "70", "", 70, true, false},
		{"neither", "", "", 0, false, false},
		{"garbage coverage uses rank", "70", "lots", 70, true, false},
		{"NaN rank", "NaN", "50", 50, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ann := map[string]string{ledger.AnnRank: c.rank, ledger.AnnCoverage: c.cov}
			in := InputsFor(rec("gh:a:1", "Acme", time.Hour, ann), RankContext{Now: inT0})
			if in.HasCoverage != c.hasCov || in.Coverage != c.wantCov || in.PreferredEmployer != c.wantPrefer {
				t.Fatalf("got cov %v/%v prefer %v", in.Coverage, in.HasCoverage, in.PreferredEmployer)
			}
		})
	}
	// The derivation rests on the pipeline's constant, whatever its value.
	cov := 33.3
	ann := map[string]string{ledger.AnnCoverage: "33.3", ledger.AnnRank: formatF(cov + boost)}
	if !InputsFor(rec("gh:a:1", "Acme", time.Hour, ann), RankContext{Now: inT0}).PreferredEmployer {
		t.Fatalf("coverage %v plus EmployerRankBoost %v is not read as preferred", cov, boost)
	}
}

func formatF(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func TestInputsForUnsure(t *testing.T) {
	r := rec("gh:a:1", "Acme", time.Hour, nil)
	r.Status = posting.Unsure
	if !InputsFor(r, RankContext{Now: inT0}).Unsure {
		t.Error("status unsure")
	}
	r = rec("gh:a:1", "Acme", time.Hour, map[string]string{"triage.unsure": "role_fit abstained"})
	if !InputsFor(r, RankContext{Now: inT0}).Unsure {
		t.Error("triage.unsure annotation")
	}
	r = rec("gh:a:1", "Acme", time.Hour, map[string]string{"triage.unsure": "  "})
	if InputsFor(r, RankContext{Now: inT0}).Unsure {
		t.Error("a blank annotation is not unsure")
	}
}

func TestInputsForAge(t *testing.T) {
	first := rec("gh:a:1", "Acme", 30*24*time.Hour, nil)
	later := rec("gh:a:2", "Acme", 2*24*time.Hour, nil)
	undated := rec("gh:a:3", "Acme", 30*24*time.Hour, nil)
	dated := rec("gh:a:4", "Acme", 30*24*time.Hour, nil)
	dated.Posting.PostedAt = inT0.Add(-40 * 24 * time.Hour)
	other := rec("gh:b:1", "Beta", 5*24*time.Hour, nil)
	recs := []ledger.Record{first, later, undated, dated, other}
	c := NewRankContext(recs, nil, inT0)
	if got := c.FirstCrawl["gh:a"]; !got.Equal(inT0.Add(-30 * 24 * time.Hour)) {
		t.Fatalf("first crawl gh:a = %v", got)
	}
	if got := c.FirstCrawl["gh:b"]; !got.Equal(inT0.Add(-5 * 24 * time.Hour)) {
		t.Fatalf("first crawl gh:b = %v", got)
	}
	for _, k := range []struct {
		r    ledger.Record
		want int
	}{
		{first, -1},   // listed in the board's first crawl, undated
		{undated, -1}, // same
		{later, 2},    // first seen after the first crawl
		{dated, 40},   // the board's own date wins
		{other, -1},   // its board's only crawl
	} {
		got := InputsFor(k.r, c)
		if got.AgeDays != k.want || got.AgeKnown != (k.want >= 0) {
			t.Errorf("%s: age %d known %v, want %d known %v", k.r.Key, got.AgeDays, got.AgeKnown, k.want, k.want >= 0)
		}
	}
	zero := rec("gh:z:1", "Zed", 0, nil)
	zero.FirstSeen = time.Time{}
	if got := InputsFor(zero, NewRankContext([]ledger.Record{zero}, nil, inT0)); got.AgeDays != -1 || got.AgeKnown {
		t.Errorf("no dates: age %d known %v", got.AgeDays, got.AgeKnown)
	}
}

func TestInputsForCompanyMarks(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", "Acme", time.Hour, nil),
		rec("gh:a:2", " ACME ", time.Hour, nil),
		rec("gh:a:3", "acme", time.Hour, nil),
		rec("gh:a:4", "Acme", time.Hour, nil),
		rec("gh:a:5", "Acme", time.Hour, nil),
		rec("gh:b:1", "Beta", time.Hour, nil),
	}
	m := map[posting.Key]marks.Mark{
		"gh:a:1": marks.Saved, "gh:a:2": marks.Applied, "gh:a:3": marks.Hidden, "gh:b:1": marks.Hidden,
	}
	c := NewRankContext(recs, m, inT0)
	if got := c.Company["acme"]; got != (CompanyMarks{Liked: 2, Hidden: 1}) {
		t.Fatalf("acme counts %+v", got)
	}
	if got := c.Company["beta"]; got != (CompanyMarks{Hidden: 1}) {
		t.Fatalf("beta counts %+v", got)
	}
	// An unmarked posting sees all the marks on its company.
	if got := InputsFor(recs[3], c).Company; got != (CompanyMarks{Liked: 2, Hidden: 1}) {
		t.Errorf("unmarked: %+v", got)
	}
	// A saved posting does not count itself.
	if got := InputsFor(recs[0], c).Company; got != (CompanyMarks{Liked: 1, Hidden: 1}) {
		t.Errorf("saved: %+v", got)
	}
	if got := InputsFor(recs[2], c).Company; got != (CompanyMarks{Liked: 2}) {
		t.Errorf("hidden: %+v", got)
	}
	// A nil marks map is no marks.
	if got := InputsFor(recs[0], NewRankContext(recs, nil, inT0)).Company; got != (CompanyMarks{}) {
		t.Errorf("nil marks: %+v", got)
	}
}

func TestInputsForPenalties(t *testing.T) {
	ann := map[string]string{
		"triage.sponsorship":         "no",
		"triage.sponsorship.reading": "answered",
		"triage.sponsorship.p":       "0.7",
		"triage.work_mode":           "onsite",
		"triage.work_mode.reading":   "abstained",
	}
	r := rec("gh:a:1", "Acme", time.Hour, ann)
	rules := []DropRule{
		{Slot: "sponsorship", Values: []string{"yes", " no "}},
		{Slot: "sponsorship", Values: []string{"no"}}, // duplicate slot: once
		{Slot: "work_mode", Values: []string{"onsite"}},
		{Slot: "seniority", Values: []string{"senior"}},
		{Slot: "role_fit", Values: nil},
	}
	in := InputsFor(r, RankContext{Now: inT0, DropRules: rules})
	want := []Penalty{{Slot: Slot{Value: "no", Answered: true, P: 0.7, HasP: true}}}
	if !reflect.DeepEqual(in.Penalties, want) {
		t.Fatalf("penalties %+v, want %+v", in.Penalties, want)
	}
	if got := InputsFor(r, RankContext{Now: inT0, DropRules: []DropRule{{Slot: "sponsorship", Values: []string{"yes"}}}}).Penalties; got != nil {
		t.Errorf("value not in rule: %+v", got)
	}
	if got := InputsFor(r, RankContext{Now: inT0, DropRules: []DropRule{{Slot: "", Values: []string{""}}}}).Penalties; got != nil {
		t.Errorf("empty slot and value: %+v", got)
	}
}

func TestInputsForOptionalParts(t *testing.T) {
	r := rec("gh:a:1", "Acme", time.Hour, nil)
	r.Posting.Title = "Staff Inference Engineer"
	hits := func(ledger.Record) (int, int) { return 2, 3 }
	sem := func(ledger.Record) (float64, bool) { return 0.4, true }
	in := InputsFor(r, RankContext{Now: inT0, Target: true, Hits: hits, Semantic: sem, HalfLifeDays: 9,
		TitleRank: func(title string) int {
			if title != r.Posting.Title {
				t.Errorf("title %q", title)
			}
			return 2
		}})
	if !in.Target || in.TitleHits != 2 || in.BodyHits != 3 || !in.HasSemantic || in.Semantic != 0.4 ||
		!in.TitleSignals || in.TitleRank != 2 || in.HalfLifeDays != 9 {
		t.Fatalf("got %+v", in)
	}
	// No target, or no hits function, is no keyword part.
	for _, c := range []RankContext{{Now: inT0, Hits: hits}, {Now: inT0, Target: true}} {
		if in := InputsFor(r, c); in.Target || in.TitleHits != 0 || in.BodyHits != 0 {
			t.Errorf("keyword part set: %+v", in)
		}
	}
	// An unknown similarity is unknown, not zero.
	unknown := InputsFor(r, RankContext{Now: inT0, Semantic: func(ledger.Record) (float64, bool) { return 0.9, false }})
	if unknown.HasSemantic || unknown.Semantic != 0 {
		t.Errorf("unknown semantic: %+v", unknown)
	}
}

func TestInputsForDeterministic(t *testing.T) {
	ann := map[string]string{
		"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.6",
		"triage.sponsorship": "no", "triage.sponsorship.reading": "answered",
		ledger.AnnRank: "70", ledger.AnnCoverage: "60",
	}
	recs := []ledger.Record{rec("gh:a:1", "Acme", time.Hour, ann), rec("gh:a:2", "Acme", 40*time.Hour, nil)}
	m := map[posting.Key]marks.Mark{"gh:a:2": marks.Saved}
	rules := []DropRule{{Slot: "sponsorship", Values: []string{"no"}}}
	var first Inputs
	for i := range 20 {
		c := NewRankContext(recs, m, inT0)
		c.DropRules = rules
		in := InputsFor(recs[0], c)
		if i == 0 {
			first = in
			continue
		}
		if !reflect.DeepEqual(in, first) {
			t.Fatalf("run %d differs: %+v vs %+v", i, in, first)
		}
	}
	if a, b := Score(first), Score(first); !reflect.DeepEqual(a, b) {
		t.Fatal("Score is not deterministic")
	}
}
