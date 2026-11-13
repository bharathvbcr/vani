package rankfit

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// The pairwise would_apply auditor's go/no-go. Its per-posting score (a smoothed
// win rate over its pair answers, eval.ScorePairs) is set against the owner's
// gold would_apply labels made after the exemplar freeze, yes against no (maybe
// is not a class here), as an AUC with a bootstrap interval, beside the title
// part's AUC on exactly the same postings. The plan's rule: at SilverMinYes yes
// and SilverMinNo no scored postings, the auditor passes when its AUC's lower
// bound is above 0.5 and its point AUC is above the title part's. Below those
// counts the report says "insufficient" and shows no AUC: a number over a
// handful of postings would be read as a verdict.

const (
	// SilverMinYes and SilverMinNo are the fewest scored gold yes and no postings
	// a verdict is given on.
	SilverMinYes = 10
	SilverMinNo  = 30

	SilverInsufficient = "insufficient"
	SilverGo           = "go"
	SilverNoGo         = "no_go"
	SilverTooMany      = "too_many"
)

// ScoreAUC is the Mann-Whitney AUC of the wanted values over the unwanted ones
// (ties half) with a 95% percentile bootstrap interval, resampled within each
// class. resamples <= 0 is the default, and it is clamped to MaxPartAUCResamples.
// ok is false for an empty class or more than MaxPartAUCItems in one, never a NaN.
func ScoreAUC(pos, neg []float64, resamples int, seed uint64) (a, lo, hi float64, ok bool) {
	switch {
	case resamples <= 0:
		resamples = DefaultPartAUCResamples
	case resamples > MaxPartAUCResamples:
		resamples = MaxPartAUCResamples
	}
	if len(pos) == 0 || len(neg) == 0 || len(pos) > MaxPartAUCItems || len(neg) > MaxPartAUCItems {
		return 0, 0, 0, false
	}
	a = auc(pos, neg)
	lo, hi = bootstrapAUC(pos, neg, resamples, rand.New(rand.NewPCG(seed, 0x6175646974)))
	return a, lo, hi, !math.IsNaN(a)
}

// SilverRow is one score's AUC over the evaluated postings.
type SilverRow struct {
	Name string  `json:"name"`
	AUC  float64 `json:"auc"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

// SilverAUC is the report. Pos and Neg count the gold yes and no postings that
// have an auditor score; Unscored those that have none (not compared, or fewer
// comparisons than the score needs), which are left out of both rows and said
// here.
type SilverAUC struct {
	Status   string `json:"status"`
	Pos      int    `json:"pos"`
	Neg      int    `json:"neg"`
	Unscored int    `json:"unscored"`
	NeedYes  int    `json:"need_yes"`
	NeedNo   int    `json:"need_no"`
	// Auditor and Title are set when Status is go or no_go. Title is the title
	// part over the same postings, all of them (a posting whose title evidence
	// is unknown takes the prior); TitleKnown is the same over the postings
	// where the title part was evidence.
	Auditor    *SilverRow `json:"auditor,omitempty"`
	Title      *SilverRow `json:"title,omitempty"`
	TitleKnown *SilverRow `json:"title_known,omitempty"`
	Reason     string     `json:"reason"`
	Resamples  int        `json:"resamples"`
	Seed       uint64     `json:"seed"`
}

// BuildSilverAUC evaluates the auditor. items are the owner's judged postings the
// caller chose as the evaluation set (made after the freeze, outside the
// exemplars), scores the auditor's score by key. A maybe is left out.
func BuildSilverAUC(items []Item, scores map[string]float64, base jobinsight.Weights, resamples int, seed uint64) SilverAUC {
	if resamples <= 0 {
		resamples = DefaultPartAUCResamples
	}
	out := SilverAUC{NeedYes: SilverMinYes, NeedNo: SilverMinNo, Resamples: min(resamples, MaxPartAUCResamples), Seed: seed}
	var pos, neg []float64
	for i := range items {
		it := &items[i]
		if !it.Judged || it.Grade == GradeMaybe {
			continue
		}
		s, ok := scores[it.Key]
		switch {
		case !ok:
			out.Unscored++
		case it.Grade >= GradeYes:
			pos = append(pos, s)
		default:
			neg = append(neg, s)
		}
	}
	out.Pos, out.Neg = len(pos), len(neg)
	switch {
	case out.Pos > MaxPartAUCItems || out.Neg > MaxPartAUCItems:
		out.Status, out.Reason = SilverTooMany, fmt.Sprintf("over the bound of %d per class; nothing computed", MaxPartAUCItems)
		return out
	case out.Pos < SilverMinYes || out.Neg < SilverMinNo:
		out.Status = SilverInsufficient
		out.Reason = fmt.Sprintf("%d yes and %d no scored postings made after the freeze; a verdict needs at least %d and %d", out.Pos, out.Neg, SilverMinYes, SilverMinNo)
		return out
	}
	// The title part is set against the very postings the auditor scored, maybes
	// out (BuildPartAUC would count them as wanted).
	scored := func(it *Item) bool { _, ok := scores[it.Key]; return ok && it.Grade != GradeMaybe }
	parts := BuildPartAUC(items, base, "scored", scored, out.Resamples, seed)
	var title *PartAUCRow
	for i := range parts.Rows {
		if parts.Rows[i].Part == PartNames[PartTitle] {
			title = &parts.Rows[i]
		}
	}
	a, lo, hi, ok := ScoreAUC(pos, neg, out.Resamples, seed)
	if !ok || parts.Status != PartAUCOK || title == nil || !title.OK {
		out.Status, out.Reason = SilverInsufficient, "an AUC could not be computed over these postings"
		return out
	}
	out.Auditor = &SilverRow{Name: "auditor (pairwise win rate)", AUC: a, Lo: lo, Hi: hi}
	out.Title = &SilverRow{Name: "title part, all", AUC: title.AUC, Lo: title.Lo, Hi: title.Hi}
	if title.KnownOK {
		out.TitleKnown = &SilverRow{Name: "title part, known only", AUC: title.KnownAUC, Lo: title.KnownLo, Hi: title.KnownHi}
	}
	switch {
	case !(lo > 0.5):
		out.Status = SilverNoGo
		out.Reason = fmt.Sprintf("the auditor's AUC lower bound %.2f is not above 0.5", lo)
	case !(a > title.AUC):
		out.Status = SilverNoGo
		out.Reason = fmt.Sprintf("the auditor's AUC %.2f does not beat the title part's %.2f on the same postings", a, title.AUC)
	default:
		out.Status = SilverGo
		out.Reason = fmt.Sprintf("the auditor's AUC %.2f has a lower bound above 0.5 and beats the title part's %.2f", a, title.AUC)
	}
	return out
}

// Markdown renders the report.
func (r SilverAUC) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n### Pairwise auditor against the owner's later labels\n\n")
	fmt.Fprintf(&b, "%d yes and %d no gold postings made after the freeze have an auditor score (%d have none).\n", r.Pos, r.Neg, r.Unscored)
	switch r.Status {
	case SilverInsufficient, SilverTooMany:
		fmt.Fprintf(&b, "%s: %s\n", r.Status, r.Reason)
		return b.String()
	}
	fmt.Fprintf(&b, "AUC: the chance a yes posting's score is above a no posting's (ties half); 0.5 is chance. 95%% intervals from %d resamples (seed %d). Go needs a lower bound above 0.5 and a point AUC above the title part's, on at least %d yes and %d no.\n\n",
		r.Resamples, r.Seed, r.NeedYes, r.NeedNo)
	b.WriteString("| score | AUC (95%) |\n|---|---|\n")
	for _, x := range []*SilverRow{r.Auditor, r.Title, r.TitleKnown} {
		if x != nil {
			fmt.Fprintf(&b, "| %s | %.2f (%.2f-%.2f) |\n", x.Name, x.AUC, x.Lo, x.Hi)
		}
	}
	fmt.Fprintf(&b, "\nverdict: %s: %s\n", r.Status, r.Reason)
	return b.String()
}
