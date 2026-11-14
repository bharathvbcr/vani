package rankfit

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The taste column (second Fable plan, enhancement 5): how close a judged
// posting is to the postings the owner answered yes, as a would-be Score part,
// set beside the title part on the same postings. It is report only: nothing
// in the engine reads it, and it gates the yes-neighbour audit cell.
//
// A posting's taste is its highest cosine to a posting the owner answered yes,
// leaving the posting itself out (leave-one-out), so a yes is never scored
// against itself. The AUC is over the judged postings with a pooled vector,
// wanted (yes or maybe) above unwanted, as the part AUC reads it, and the
// title part's AUC is taken on exactly those postings.
const (
	// MinTasteYes is the fewest yes postings with a vector the column needs:
	// below it, every posting's taste rests on one or two neighbours.
	MinTasteYes = 5

	TasteOK           = "ok"
	TasteInsufficient = "insufficient"
	TasteNoVectors    = "no_vectors"
)

// TasteItem is one judged posting as the taste column reads it: its label,
// its title part, and its pooled unit vector (nil when it has none).
type TasteItem struct {
	Key    posting.Key
	Grade  int
	Title  float64
	Vector []float32
}

// Taste is the column's report. Passes is the gate the yes-neighbour cell
// asks: Status ok and the taste AUC above the title part's on the same
// postings. With fewer than MinTasteYes yes postings it is insufficient,
// never a pass.
type Taste struct {
	Status   string  `json:"status"`
	Reason   string  `json:"reason,omitempty"`
	Yes      int     `json:"yes_with_vector"`
	Pos      int     `json:"pos"`
	Neg      int     `json:"neg"`
	NoVector int     `json:"judged_without_vector"`
	AUC      float64 `json:"auc"`
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	TitleAUC float64 `json:"title_auc"`
	TitleLo  float64 `json:"title_lo"`
	TitleHi  float64 `json:"title_hi"`
	Passes   bool    `json:"passes"`
	// WithoutVector counts the judged postings without a vector by cause (the
	// caller's: never embedded, stale chunking, text unreadable, ...), so a
	// missing vector can be told from a lookup that could not run.
	WithoutVector map[string]int `json:"without_vector_by_cause,omitempty"`
	// VectorNote says what kept the causes from being read, when something did.
	VectorNote string `json:"vector_note,omitempty"`
}

// BuildTaste computes the column over items (judged postings only; the caller
// leaves out labels drawn on a model's reading or for their similarity).
func BuildTaste(items []TasteItem, resamples int, seed uint64) Taste {
	out := Taste{Status: TasteInsufficient}
	var withVec []TasteItem
	var yes []int // indexes into withVec
	for _, it := range items {
		if len(it.Vector) == 0 {
			out.NoVector++
			continue
		}
		if it.Grade == GradeYes {
			yes = append(yes, len(withVec))
		}
		withVec = append(withVec, it)
	}
	out.Yes = len(yes)
	switch {
	case len(withVec) == 0:
		out.Status, out.Reason = TasteNoVectors, "no judged posting has a stored vector"
		return out
	case out.Yes < MinTasteYes:
		out.Reason = fmt.Sprintf("%d yes postings with a vector; the column needs at least %d", out.Yes, MinTasteYes)
		return out
	}
	var pos, neg, tpos, tneg []float64
	for i, it := range withVec {
		best := math.Inf(-1)
		for _, j := range yes {
			if j == i {
				continue // leave-one-out
			}
			if c := cosine(it.Vector, withVec[j].Vector); c > best {
				best = c
			}
		}
		if it.Grade >= GradeMaybe {
			pos, tpos = append(pos, best), append(tpos, it.Title)
		} else {
			neg, tneg = append(neg, best), append(tneg, it.Title)
		}
	}
	out.Pos, out.Neg = len(pos), len(neg)
	a, lo, hi, ok := AUCWithInterval(pos, neg, resamples, seed)
	ta, tlo, thi, tok := AUCWithInterval(tpos, tneg, resamples, seed+1)
	if !ok || !tok {
		out.Reason = fmt.Sprintf("%d wanted and %d unwanted postings with a vector; an AUC needs at least %d of each", out.Pos, out.Neg, MinPartAUCClass)
		return out
	}
	out.Status = TasteOK
	out.AUC, out.Lo, out.Hi = a, lo, hi
	out.TitleAUC, out.TitleLo, out.TitleHi = ta, tlo, thi
	out.Passes = a > ta
	return out
}

// cosine is the dot product of two unit vectors (the store keeps pooled
// vectors normalised); vectors of different widths are unrelated, -1.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return -1
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Markdown renders the column.
func (t Taste) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Taste column: nearness to the owner's yes postings\n\nEach judged posting's highest cosine to another posting the owner answered yes (leave-one-out), against the title part on the same postings. Report only; it gates the yes-neighbour audit cell (`vani audit --yes-neighbours`).\n\n")
	switch t.Status {
	case TasteOK:
		fmt.Fprintf(&b, "- taste AUC %.2f (%.2f-%.2f) against title %.2f (%.2f-%.2f), over %d wanted and %d unwanted postings with a vector (%d judged without one)\n",
			t.AUC, t.Lo, t.Hi, t.TitleAUC, t.TitleLo, t.TitleHi, t.Pos, t.Neg, t.NoVector)
		if t.Passes {
			b.WriteString("- passes: the taste column orders the owner's answers better than the title part; the yes-neighbour cell may be drawn\n")
		} else {
			b.WriteString("- does not pass: the taste column does not beat the title part; the yes-neighbour cell stays closed\n")
		}
	default:
		fmt.Fprintf(&b, "%s: %s (%d judged without a vector). The yes-neighbour cell stays closed.\n", t.Status, t.Reason, t.NoVector)
	}
	if len(t.WithoutVector) > 0 {
		b.WriteString("\nJudged postings without a vector, by cause:")
		for _, c := range slices.Sorted(maps.Keys(t.WithoutVector)) {
			fmt.Fprintf(&b, " %s %d;", c, t.WithoutVector[c])
		}
		b.WriteString(" (never_embedded: the semantic warm embeds kept and marked postings only, so a posting dropped before it was reached has none)\n")
	}
	if t.VectorNote != "" {
		fmt.Fprintf(&b, "\n%s\n", t.VectorNote)
	}
	return b.String()
}
