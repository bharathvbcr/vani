package rankfit

import (
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
// decisions). An audit judgment wins where there is one: yes above maybe above
// no, newest judgment per posting (feedback.Latest); skip labels nothing. Else
// a mark: saved or applied is a yes, hidden a no. Else a posting an impression
// shows was shown and left unmarked is a no; an unmarked posting nothing
// shows was ever shown is no label at all. Scores come from the judgment, else
// the newest impression.
func Targets(judgments []feedback.Judgment, shown []feedback.Impression, m map[posting.Key]marks.Mark) (map[posting.Key]Target, TargetCounts) {
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
	for k, j := range feedback.Latest(judgments) {
		sc := j.Scores
		t := Target{Judged: true, Scores: &sc}
		switch j.Verdict {
		case feedback.VerdictYes:
			t.Grade = GradeYes
			c.Yes++
		case feedback.VerdictMaybe:
			t.Grade = GradeMaybe
			c.Maybe++
		case feedback.VerdictNo:
			t.Grade = GradeNo
			c.No++
		default:
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
	return out, c
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
