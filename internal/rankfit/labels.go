package rankfit

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Grades order the labels: a pair trains, and a held-out pair is scored, when
// one posting's grade is above the other's.
const (
	GradeNo    = 0
	GradeMaybe = 1
	GradeYes   = 2
)

// Target is one posting's training label.
type Target struct {
	Grade int
	// Judged is true when Grade is an audit judgment (feedback), the ground
	// truth the fit is evaluated on, rather than a mark or an impression.
	Judged bool
	// Scores are Score's parts as the owner saw them: when judged, or when a
	// surface last showed the posting. Nil means none were recorded and the
	// caller scores the posting now.
	Scores *feedback.Scores
	// Source and PSelect are the audit's sampling stratum and inclusion
	// probability of a judgment (eval.Entry). A judgment drawn by the audit has
	// both; a manual one, a mark and an impression have neither, and train at
	// weight 1.
	Source  string
	PSelect float64
}

// Sampled reports whether source names an audit stratum, so the label carries
// an inclusion probability. Empty and eval.SourceManual do not.
func Sampled(source string) bool { return source != "" && source != eval.SourceManual }

// The audit's strata for the per-stratum held-out report: what the ranking
// actually orders (a scored decile) against what the engine dropped, which the
// audit oversamples (eval.AuditDropped against eval.AuditScored).
const (
	StratumScored  = "scored"
	StratumDropped = "dropped"
)

// StratumOf names the stratum of an audit source ("decile:10" is scored,
// "dropped:<class>" is dropped); "" for anything else.
func StratumOf(source string) string {
	switch {
	case strings.HasPrefix(source, "decile:"):
		return StratumScored
	case strings.HasPrefix(source, "dropped:"):
		return StratumDropped
	}
	return ""
}

// checkInclusion validates a label's sampling: a sampled one needs PSelect in
// (0, 1] (false for NaN), any other carries none. It is the fit's boundary, so
// a bad probability is refused here rather than turned into an infinite or
// negative weight.
func checkInclusion(key, source string, p float64) error {
	if Sampled(source) {
		if !(p >= eval.MinInclusionProbability && p <= 1) {
			return fmt.Errorf("rankfit: %s p_select %v must be in [%g, 1]", key, p, eval.MinInclusionProbability)
		}
		return nil
	}
	if p != 0 {
		return fmt.Errorf("rankfit: %s has p_select %v but no sampling stratum", key, p)
	}
	return nil
}

// TargetCounts says what Targets rests on.
type TargetCounts struct {
	// Judged counts usable judgments by verdict (skip left out).
	Yes, Maybe, No int
	// Saved counts saved or applied marks, Hidden hidden ones, Passed shown
	// postings left unmarked; each only where no judgment overrides it.
	Saved, Hidden, Passed int
	// Skipped counts judgments of "can't tell", which label nothing.
	Skipped int
}

// Judged is the usable judgment count the weight fit's threshold is on.
func (c TargetCounts) Judged() int { return c.Yes + c.Maybe + c.No }

// Targets merges the signals the owner chose (docs/RANKING_PLAN.md, Owner
// decisions). An audit judgment (the eval store's would_apply label, which
// `vani audit answer` writes once per posting) wins where there is one: yes
// above maybe above no; skip labels nothing. Else a mark: saved or applied is
// a yes, hidden a no. Else a posting an impression shows was shown and left
// unmarked is a no; an unmarked posting nothing shows was ever shown is no
// label at all. Scores come from the newest impression; a posting no surface
// showed has none, and the caller scores it now.
//
// A judgment keeps its stratum and inclusion probability (Source, PSelect),
// which Fit turns into a Horvitz-Thompson weight; a sampled judgment whose
// probability is not in (0, 1] is an error, not a label.
func Targets(labels map[posting.Key]eval.Entry, shown []feedback.Impression, m map[posting.Key]marks.Mark) (map[posting.Key]Target, TargetCounts, error) {
	var c TargetCounts
	newest := map[posting.Key]int{} // index of each key's newest impression
	for i, im := range shown {
		if j, ok := newest[im.Key]; !ok || !im.At.Before(shown[j].At) {
			newest[im.Key] = i
		}
	}
	scores := func(k posting.Key) *feedback.Scores {
		if i, ok := newest[k]; ok {
			s := shown[i].Scores
			return &s
		}
		return nil
	}
	out := map[posting.Key]Target{}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		v, ok := labels[k].Labels[eval.WouldApply]
		if !ok {
			continue // a triage-slot label only
		}
		e := labels[k]
		if err := checkInclusion(string(k), e.Source, e.PSelect); err != nil {
			return nil, TargetCounts{}, err
		}
		t := Target{Judged: true, Scores: scores(k), Source: e.Source, PSelect: e.PSelect}
		switch v {
		case "yes":
			t.Grade = GradeYes
			c.Yes++
		case "maybe":
			t.Grade = GradeMaybe
			c.Maybe++
		case "no":
			t.Grade = GradeNo
			c.No++
		default: // eval.Skip
			c.Skipped++
			continue
		}
		out[k] = t
	}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if _, judged := out[k]; judged {
			continue
		}
		switch m[k] {
		case marks.Saved, marks.Applied:
			out[k] = Target{Grade: GradeYes, Scores: scores(k)}
			c.Saved++
		case marks.Hidden:
			out[k] = Target{Grade: GradeNo, Scores: scores(k)}
			c.Hidden++
		}
	}
	for _, k := range slices.Sorted(maps.Keys(newest)) {
		if _, labelled := out[k]; labelled || m[k] != marks.None {
			continue
		}
		out[k] = Target{Grade: GradeNo, Scores: scores(k)}
		c.Passed++
	}
	return out, c, nil
}

// PlattSamples sets each labelled triage slot (internal/eval, read only)
// against the reading the ledger holds for it: one sample per answered slot
// reading, grouped by the backend recorded for it (as written: a legacy name
// calibrates separately). A label of eval.Skip, an unanswered reading, or a
// posting not in recs gives none. missing counts the labelled postings recs
// does not hold.
func PlattSamples(recs []ledger.Record, labels map[posting.Key]eval.Entry) (byBackend map[string][]PlattSample, missing int) {
	byKey := make(map[posting.Key]*ledger.Record, len(recs))
	for i := range recs {
		byKey[recs[i].Key] = &recs[i]
	}
	byBackend = map[string][]PlattSample{}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		e := labels[k]
		r, ok := byKey[k]
		if !ok {
			missing++
			continue
		}
		for _, sl := range jdtriage.Slots {
			want := e.Labels[sl.Name]
			if want == "" || want == eval.Skip {
				continue
			}
			pre := ledger.AnnTriagePrefix + sl.Name
			a := r.Annotations
			got, backend := strings.TrimSpace(a[pre]), strings.TrimSpace(a[pre+ledger.AnnTriageSuffixBackend])
			if got == "" || backend == "" || strings.TrimSpace(a[pre+ledger.AnnTriageSuffixReading]) != "answered" {
				continue
			}
			s := PlattSample{Key: k, Correct: got == want}
			if raw := strings.TrimSpace(a[pre+ledger.AnnTriageSuffixP]); raw != "" {
				p, err := strconv.ParseFloat(raw, 64)
				s.P, s.HasP = p, err == nil
			}
			byBackend[backend] = append(byBackend[backend], s)
		}
	}
	return byBackend, missing
}

// Report is what a rankfit run rested on and found, kept in the file it wrote.
// It holds counts and numbers only: no drop class, rule value or posting text.
type Report struct {
	Labels       int                 `json:"labels"`
	Targets      TargetCounts        `json:"targets"`
	Platt        map[string]PlattFit `json:"platt,omitempty"`
	PlattRefused map[string]int      `json:"platt_refused,omitempty"`
	Weights      *FitResult          `json:"weights,omitempty"`
}
