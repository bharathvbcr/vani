package rankfit

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The weekly audit's judgement (docs/RANKING_PLAN.md, Phase 1) is read from an
// eval label entry's Labels under WouldApplyKey, "yes" or "no"; eval.Skip and
// anything else is not a judgement. This is the one place that format is read.
const (
	WouldApplyKey = "would_apply"
	WouldApplyYes = "yes"
	WouldApplyNo  = "no"
)

// WouldApply reads e's would-apply judgement; ok is false when it has none.
func WouldApply(e eval.Entry) (yes, ok bool) {
	switch strings.TrimSpace(e.Labels[WouldApplyKey]) {
	case WouldApplyYes:
		return true, true
	case WouldApplyNo:
		return false, true
	}
	return false, false
}

// Target is one posting's training label.
type Target struct {
	Positive bool
	// Judged is true when Positive is a would-apply judgement.
	Judged bool
}

// Targets merges the two signals the owner chose (docs/RANKING_PLAN.md,
// Owner decisions): a would-apply judgement where there is one, which wins
// over a mark on the same posting, else a mark: saved or applied is a
// positive, hidden a negative. An unmarked, unjudged posting is no label at
// all: nothing records that it was shown, so it is not a negative.
func Targets(labels map[posting.Key]eval.Entry, m map[posting.Key]marks.Mark) map[posting.Key]Target {
	out := map[posting.Key]Target{}
	for k, mk := range m {
		switch mk {
		case marks.Saved, marks.Applied:
			out[k] = Target{Positive: true}
		case marks.Hidden:
			out[k] = Target{}
		}
	}
	for k, e := range labels {
		if yes, ok := WouldApply(e); ok {
			out[k] = Target{Positive: yes, Judged: true}
		}
	}
	return out
}

// PlattSamples sets each labelled triage slot against the reading the ledger
// holds for it: one sample per answered slot reading, grouped by the backend
// recorded for it (as written: a legacy name calibrates separately). A label
// of eval.Skip, an unanswered reading, or a posting not in recs gives none.
// missing counts the labelled postings recs does not hold.
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
			s := PlattSample{Correct: got == want}
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
type Report struct {
	Labels       int                 `json:"labels"`
	Judged       int                 `json:"judged"`
	MarkedPos    int                 `json:"marked_positive"`
	MarkedNeg    int                 `json:"marked_negative"`
	Platt        map[string]PlattFit `json:"platt,omitempty"`
	PlattRefused map[string]int      `json:"platt_refused,omitempty"`
	Weights      *FitResult          `json:"weights,omitempty"`
}
