package feedback

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Fit thresholds (docs/RANKING_PLAN.md): per-backend calibration needs
// CalibrationMin judged postings, the weight fit WeightFitMin. Skip does not
// count. Below them a fitter must refuse, and say it could not run.
const (
	CalibrationMin = 100
	WeightFitMin   = 150
	// MeasureMin is the fewest judged postings a stratum needs before a rate
	// over it is reported; fewer is "unmeasured", never a rate.
	MeasureMin = 5
	// MaxSample bounds one audit batch.
	MaxSample = 100
)

// Candidate is a posting an audit may sample: its key, its priority now, and,
// for a dropped posting, why it was dropped.
type Candidate struct {
	Key       posting.Key
	Priority  float64
	Dropped   bool
	DropClass string
}

// Pick is one sampled posting and the stratum it stands for.
type Pick struct {
	Key    posting.Key
	Source string
}

// Sample picks up to n postings no judgment covers, stratified so the
// owner's verdicts measure the whole ranking, not its top: about seven in ten
// from kept postings, spread round-robin over priority deciles from the top
// down ("decile:10" is the highest), and the rest from dropped postings, spread
// round-robin over drop classes ("dropped:<class>"). A short pool lends its
// places to the other. Within a stratum the order is a hash of seed and key:
// random to the ranking, the same for the same seed (an ISO week).
func Sample(cands []Candidate, judged map[posting.Key]bool, n int, seed string) []Pick {
	n = min(max(n, 0), MaxSample)
	var kept, dropped []Candidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if judged[c.Key] || seen[c.Key] || checkKey(c.Key) != nil {
			continue
		}
		seen[c.Key] = true
		if c.Dropped {
			dropped = append(dropped, c)
		} else {
			kept = append(kept, c)
		}
	}
	nDrop := min(n*3/10, len(dropped))
	nKept := min(n-nDrop, len(kept))
	nDrop = min(n-nKept, len(dropped))

	h := func(k posting.Key) string {
		s := sha256.Sum256([]byte(seed + "\x00" + string(k)))
		return string(s[:])
	}
	byHash := func(cs []Candidate) {
		sort.Slice(cs, func(i, j int) bool { return h(cs[i].Key) < h(cs[j].Key) })
	}

	// Kept: deciles by priority (ties by key, so a decile is well defined).
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Priority != kept[j].Priority {
			return kept[i].Priority < kept[j].Priority
		}
		return kept[i].Key < kept[j].Key
	})
	deciles := make([][]Candidate, 10)
	for i, c := range kept {
		d := i * 10 / len(kept)
		deciles[d] = append(deciles[d], c)
	}
	var out []Pick
	for _, d := range deciles {
		byHash(d)
	}
	for len(out) < nKept {
		for d := 9; d >= 0 && len(out) < nKept; d-- {
			if len(deciles[d]) > 0 {
				out = append(out, Pick{Key: deciles[d][0].Key, Source: fmt.Sprintf("decile:%d", d+1)})
				deciles[d] = deciles[d][1:]
			}
		}
	}

	// Dropped: by class.
	classes := map[string][]Candidate{}
	var names []string
	for _, c := range dropped {
		cl := c.DropClass
		if cl == "" {
			cl = "unknown"
		}
		if _, ok := classes[cl]; !ok {
			names = append(names, cl)
		}
		classes[cl] = append(classes[cl], c)
	}
	sort.Strings(names)
	for _, cl := range names {
		byHash(classes[cl])
	}
	for taken := 0; taken < nDrop; {
		for _, cl := range names {
			if taken == nDrop {
				break
			}
			if len(classes[cl]) > 0 {
				out = append(out, Pick{Key: classes[cl][0].Key, Source: trimSource("dropped:" + cl)})
				classes[cl] = classes[cl][1:]
				taken++
			}
		}
	}
	return out
}

func trimSource(s string) string {
	if len(s) > maxNameBytes {
		return s[:maxNameBytes]
	}
	return s
}

// WeekSeed is the ISO week of t ("2026-W43"): a batch stays the same all week.
func WeekSeed(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// Latest is each key's newest judgment: judging a posting again replaces the
// earlier verdict.
func Latest(js []Judgment) map[posting.Key]Judgment {
	out := make(map[posting.Key]Judgment, len(js))
	for _, j := range js {
		if prev, ok := out[j.Key]; !ok || !j.At.Before(prev.At) {
			out[j.Key] = j
		}
	}
	return out
}

// Rate is a share over a stratum, or unmeasured when it holds fewer than
// MeasureMin judged postings.
type Rate struct {
	Judged int      `json:"judged"`
	Hits   int      `json:"hits"`
	Rate   *float64 `json:"rate,omitempty"` // nil: unmeasured
}

func rate(judged, hits int) Rate {
	r := Rate{Judged: judged, Hits: hits}
	if judged >= MeasureMin {
		v := float64(hits) / float64(judged)
		r.Rate = &v
	}
	return r
}

// Summary is what the judgments so far measure.
type Summary struct {
	// Judged counts the latest verdict per key; Usable leaves out skip.
	Judged    int            `json:"judged"`
	Usable    int            `json:"usable"`
	ByVerdict map[string]int `json:"by_verdict"`
	ThisWeek  int            `json:"this_week"`
	// CalibrationReady and WeightFitReady say whether Usable reached the
	// thresholds; the fitters refuse below them.
	CalibrationReady bool `json:"calibration_ready"`
	WeightFitReady   bool `json:"weight_fit_ready"`
	// WouldApplyByDecile is, per priority decile at sampling, the share judged
	// yes or maybe: a good ranking makes it fall from decile 10 down.
	WouldApplyByDecile map[string]Rate `json:"would_apply_by_decile"`
	// FalseDropByClass is, per drop class, the share of dropped postings judged
	// yes or maybe: how often that rule drops a role the owner wants.
	FalseDropByClass map[string]Rate `json:"false_drop_by_class"`
	// Explore is the would-apply share of the digest's random picks.
	Explore Rate `json:"explore"`
}

// Summarize measures js (every judgment, any order) as of now.
func Summarize(js []Judgment, now time.Time) Summary {
	s := Summary{ByVerdict: map[string]int{}, WouldApplyByDecile: map[string]Rate{}, FalseDropByClass: map[string]Rate{}}
	week := WeekSeed(now)
	type tally struct{ n, hit int }
	dec, drop := map[string]*tally{}, map[string]*tally{}
	var ex tally
	add := func(m map[string]*tally, k string, hit bool) {
		t := m[k]
		if t == nil {
			t = &tally{}
			m[k] = t
		}
		t.n++
		if hit {
			t.hit++
		}
	}
	for _, j := range Latest(js) {
		s.Judged++
		s.ByVerdict[j.Verdict]++
		if WeekSeed(j.At) == week {
			s.ThisWeek++
		}
		if j.Verdict == VerdictSkip {
			continue
		}
		s.Usable++
		want := j.Verdict == VerdictYes || j.Verdict == VerdictMaybe
		switch src := j.Source; {
		case src == SourceExplore:
			ex.n++
			if want {
				ex.hit++
			}
		case len(src) > 7 && src[:7] == "decile:":
			add(dec, src[7:], want)
		case len(src) > 8 && src[:8] == "dropped:":
			add(drop, src[8:], want)
		case j.DropReason != "":
			add(drop, j.DropReason, want)
		}
	}
	for k, t := range dec {
		s.WouldApplyByDecile[k] = rate(t.n, t.hit)
	}
	for k, t := range drop {
		s.FalseDropByClass[k] = rate(t.n, t.hit)
	}
	s.Explore = rate(ex.n, ex.hit)
	s.CalibrationReady = s.Usable >= CalibrationMin
	s.WeightFitReady = s.Usable >= WeightFitMin
	return s
}

// SourceExplore marks a judgment of a role the digest's Explore section showed.
const SourceExplore = "explore"
