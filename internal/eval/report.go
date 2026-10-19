package eval

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// Thresholds are the OpenRouter model's probability cut-offs of the precision table.
var Thresholds = []float64{0.5, 0.6, 0.7, 0.8, 0.9, 0.95}

// UnknownBackend names slots whose backend annotation is missing.
const UnknownBackend = "unknown"

// Cell is one slot x backend measurement over labeled postings. "skip" labels
// are not counted anywhere.
type Cell struct {
	Slot     string
	Backend  string
	Labeled  int // labeled postings this backend was responsible for
	Answered int // of those, reading answered
	Correct  int // of those, value equals the label
}

// Precision is Correct/Answered; ok is false when nothing was answered.
func (c Cell) Precision() (float64, bool) { return ratio(c.Correct, c.Answered) }

// Coverage is Answered/Labeled; ok is false when nothing was labeled.
func (c Cell) Coverage() (float64, bool) { return ratio(c.Answered, c.Labeled) }

// ThresholdRow is the OpenRouter model's precision among answers with p >= P for one slot.
type ThresholdRow struct {
	P        float64
	Labeled  int // OpenRouter-owned labeled postings for the slot (the coverage denominator)
	Answered int // answered with p >= P
	Correct  int
}

func (t ThresholdRow) Precision() (float64, bool) { return ratio(t.Correct, t.Answered) }
func (t ThresholdRow) Coverage() (float64, bool)  { return ratio(t.Answered, t.Labeled) }

// Threshold is one slot's OpenRouter precision-at-threshold table.
type Threshold struct {
	Slot string
	Rows []ThresholdRow
}

// ShadowRow compares the shadow backend with the primary and the labels for
// one slot. Only postings whose shadow answered (it records nothing otherwise)
// are visible, so Labeled is carried to show how many were not.
type ShadowRow struct {
	Slot           string
	Labeled        int // labeled postings with triage for the slot
	ShadowAnswered int
	ShadowCorrect  int
	BothAnswered   int // primary and shadow both answered
	Agree          int
	Disagree       int
	PrimaryRight   int // among Disagree, primary equals the label
	ShadowRight    int // among Disagree, shadow equals the label
}

func (s ShadowRow) Precision() (float64, bool) { return ratio(s.ShadowCorrect, s.ShadowAnswered) }
func (s ShadowRow) AgreementRate() (float64, bool) {
	return ratio(s.Agree, s.BothAnswered)
}

// MinPromotionLabels is the fewest labeled postings a shadow must have been
// asked about before a verdict is given. Below it the verdict is
// "insufficient labels", never "not promotable".
const MinPromotionLabels = 30

// Promotion compares a shadow backend with one primary backend on the same
// labeled postings: those where the shadow was asked (a shadow_reading
// annotation) and that primary held the slot. Plan §4: a backend takes over a
// family only when its answered-reading precision meets the primary's at no
// worse coverage. Primaries are never pooled: when the OpenRouter model hits its daily
// cap and the LLM answers the rest, "beats d1-ultra" and "beats the LLM" are separate rows,
// each needing its own MinPromotionLabels.
type Promotion struct {
	Slot            string
	Shadow          string // shadow backend name
	Primary         string // the backend that held the slot ("d1-ultra", "llm", ...)
	Asked           int    // labeled postings the shadow was asked about
	ShadowAnswered  int
	ShadowCorrect   int
	PrimaryAnswered int // of the same postings, the primary answered
	PrimaryCorrect  int
}

// Verdict is the promotion decision and its reason, with counts.
func (p Promotion) Verdict() string {
	if p.Asked < MinPromotionLabels {
		return fmt.Sprintf("insufficient labels (%d/%d)", p.Asked, MinPromotionLabels)
	}
	pp, okP := ratio(p.PrimaryCorrect, p.PrimaryAnswered)
	sp, okS := ratio(p.ShadowCorrect, p.ShadowAnswered)
	switch {
	case !okP:
		return "no primary baseline (the chain answered none of these)"
	case !okS:
		return "not promotable: the shadow answered none of these"
	case sp < pp:
		return fmt.Sprintf("not promotable: precision %.1f%% < primary %.1f%%", sp*100, pp*100)
	case p.ShadowAnswered < p.PrimaryAnswered:
		return fmt.Sprintf("not promotable: coverage %d/%d < primary %d/%d", p.ShadowAnswered, p.Asked, p.PrimaryAnswered, p.Asked)
	}
	return "promotable"
}

// Report is the evaluation of ledger records against the label set.
type Report struct {
	Records        int // ledger records examined
	WithTriage     int // of those, with any triage annotation
	LabelEntries   int // distinct keys in the label file
	Matched        int // label entries that have a ledger record
	NoTriage       int // matched labels whose record has no triage annotations
	MalformedLines int // label-file lines skipped as unreadable
	LoadError      string
	// LegacyPrimary counts scored slot readings whose primary backend was
	// recorded as LegacyOpenRouterName and is reported under the configured
	// OpenRouter name.
	LegacyPrimary int
	// ProbabilityBackend is the backend the threshold tables are built from.
	ProbabilityBackend string

	Cells      []Cell
	Thresholds []Threshold
	Shadow     []ShadowRow
	Promotions []Promotion
}

func ratio(num, den int) (float64, bool) {
	if den <= 0 {
		return 0, false
	}
	return float64(num) / float64(den), true
}

// BuildReport scores each backend's triage annotations in recs against the labels in
// s, per slot x backend. Labeled counts postings the backend was responsible
// for; precision = correct/answered; coverage = answered/labeled. A "skip"
// label is excluded from every denominator.
//
// names maps primary readings filed under the OpenRouter model's old name to
// its current one (see Names); rep.LegacyPrimary counts how many were.
func BuildReport(recs []ledger.Record, s *Store, names Names) Report {
	rep := Report{Records: len(recs), ProbabilityBackend: names.probability()}
	labels, bad, err := s.Labels()
	rep.MalformedLines = bad
	if err != nil {
		rep.LoadError = err.Error()
	}
	rep.LabelEntries = len(labels)

	type ck struct{ slot, backend string }
	cells := map[ck]*Cell{}
	thr := map[string][]ThresholdRow{}
	shadow := map[string]*ShadowRow{}
	type pk struct{ slot, shadow, primary string }
	promo := map[pk]*Promotion{}

	for _, r := range recs {
		if hasTriage(r) {
			rep.WithTriage++
		}
		e, ok := labels[r.Key]
		if !ok {
			continue
		}
		rep.Matched++
		if !hasTriage(r) {
			rep.NoTriage++
			continue
		}
		for _, sl := range jdtriage.Slots {
			want := e.Labels[sl.Name]
			if want == "" || want == Skip {
				continue
			}
			pre := ledger.AnnTriagePrefix + sl.Name
			reading, has := r.Annotations[pre+ledger.AnnTriageSuffixReading]
			if !has {
				continue
			}
			backend, mapped := names.primary(r.Annotations[pre+ledger.AnnTriageSuffixBackend])
			if mapped {
				rep.LegacyPrimary++
			}
			if backend == "" {
				backend = UnknownBackend
			}
			c := cells[ck{sl.Name, backend}]
			if c == nil {
				c = &Cell{Slot: sl.Name, Backend: backend}
				cells[ck{sl.Name, backend}] = c
			}
			got := r.Annotations[pre]
			answered := reading == "answered"
			c.Labeled++
			if answered {
				c.Answered++
				if got == want {
					c.Correct++
				}
			}

			if backend == names.probability() {
				rows := thr[sl.Name]
				if rows == nil {
					rows = make([]ThresholdRow, len(Thresholds))
					for i, t := range Thresholds {
						rows[i].P = t
					}
				}
				p, perr := strconv.ParseFloat(r.Annotations[pre+ledger.AnnTriageSuffixP], 64)
				for i := range rows {
					rows[i].Labeled++
					if answered && perr == nil && p >= rows[i].P {
						rows[i].Answered++
						if got == want {
							rows[i].Correct++
						}
					}
				}
				thr[sl.Name] = rows
			}

			if shReading, asked := r.Annotations[pre+ledger.AnnTriageSuffixShadowReading]; asked {
				sb := r.Annotations[pre+ledger.AnnTriageSuffixShadowBackend]
				if sb == "" {
					sb = UnknownBackend
				}
				k := pk{sl.Name, sb, backend}
				p := promo[k]
				if p == nil {
					p = &Promotion{Slot: sl.Name, Shadow: sb, Primary: backend}
					promo[k] = p
				}
				p.Asked++
				if answered {
					p.PrimaryAnswered++
					if got == want {
						p.PrimaryCorrect++
					}
				}
				if shReading == "answered" {
					p.ShadowAnswered++
					if r.Annotations[pre+ledger.AnnTriageSuffixShadow] == want {
						p.ShadowCorrect++
					}
				}
			}

			sr := shadow[sl.Name]
			if sr == nil {
				sr = &ShadowRow{Slot: sl.Name}
				shadow[sl.Name] = sr
			}
			sr.Labeled++
			if sh, ok := r.Annotations[pre+ledger.AnnTriageSuffixShadow]; ok {
				sr.ShadowAnswered++
				if sh == want {
					sr.ShadowCorrect++
				}
				if answered {
					sr.BothAnswered++
					if sh == got {
						sr.Agree++
					} else {
						sr.Disagree++
						if got == want {
							sr.PrimaryRight++
						}
						if sh == want {
							sr.ShadowRight++
						}
					}
				}
			}
		}
	}

	for _, sl := range jdtriage.Slots {
		var backends []string
		for k := range cells {
			if k.slot == sl.Name {
				backends = append(backends, k.backend)
			}
		}
		sort.Strings(backends)
		for _, b := range backends {
			rep.Cells = append(rep.Cells, *cells[ck{sl.Name, b}])
		}
		if rows, ok := thr[sl.Name]; ok {
			rep.Thresholds = append(rep.Thresholds, Threshold{Slot: sl.Name, Rows: rows})
		}
		if sr, ok := shadow[sl.Name]; ok && sr.ShadowAnswered > 0 {
			rep.Shadow = append(rep.Shadow, *sr)
		}
		var rows []pk
		for k := range promo {
			if k.slot == sl.Name {
				rows = append(rows, k)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].shadow != rows[j].shadow {
				return rows[i].shadow < rows[j].shadow
			}
			return rows[i].primary < rows[j].primary
		})
		for _, k := range rows {
			rep.Promotions = append(rep.Promotions, *promo[k])
		}
	}
	return rep
}

func pct(num, den int) string {
	v, ok := ratio(num, den)
	if !ok {
		return fmt.Sprintf("n/a (%d/%d)", num, den)
	}
	return fmt.Sprintf("%.1f%% (%d/%d)", v*100, num, den)
}

// Markdown renders the report. Every rate carries its counts, and a rate with
// an empty denominator prints "n/a", never NaN.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Vani triage evaluation\n\n")
	fmt.Fprintf(&b, "- ledger records: %d, with triage annotations: %d\n", r.Records, r.WithTriage)
	fmt.Fprintf(&b, "- labeled postings: %d, matched to a ledger record: %d (%d without triage annotations, not scored)\n",
		r.LabelEntries, r.Matched, r.NoTriage)
	if r.MalformedLines > 0 {
		fmt.Fprintf(&b, "- label lines skipped as unreadable: %d\n", r.MalformedLines)
	}
	if r.LoadError != "" {
		fmt.Fprintf(&b, "- LABELS COULD NOT BE READ: %s; the figures below are not valid\n", r.LoadError)
	}
	if r.LegacyPrimary > 0 {
		fmt.Fprintf(&b, "- primary readings recorded under the old name %q and counted as %q: %d\n",
			LegacyOpenRouterName, r.ProbabilityBackend, r.LegacyPrimary)
	}
	b.WriteString("- \"skip\" labels are excluded from every denominator; the labeled set is a sample, not the whole ledger\n\n")

	if len(r.Cells) == 0 {
		b.WriteString("No labeled postings with triage annotations: n/a\n")
		return b.String()
	}

	b.WriteString("## Precision and coverage by slot and backend\n\n")
	b.WriteString("| slot | backend | labeled | answered | correct | precision | coverage |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cells {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %s | %s |\n", c.Slot, c.Backend, c.Labeled, c.Answered, c.Correct,
			pct(c.Correct, c.Answered), pct(c.Answered, c.Labeled))
	}

	for _, t := range r.Thresholds {
		fmt.Fprintf(&b, "\n## %s precision at p threshold: %s\n\n", r.ProbabilityBackend, t.Slot)
		b.WriteString("| p >= | answered | correct | precision | coverage |\n|---|---|---|---|---|\n")
		for _, row := range t.Rows {
			fmt.Fprintf(&b, "| %.2f | %d | %d | %s | %s |\n", row.P, row.Answered, row.Correct,
				pct(row.Correct, row.Answered), pct(row.Answered, row.Labeled))
		}
	}

	if len(r.Shadow) > 0 {
		b.WriteString("\n## Shadow backend\n\n")
		b.WriteString("Shadow readings are recorded only when the shadow answered, so \"labeled\" is the sample size the shadow was measured against.\n\n")
		b.WriteString("| slot | labeled | shadow answered | shadow precision | both answered | agreement | disagree | primary right | shadow right |\n|---|---|---|---|---|---|---|---|---|\n")
		for _, s := range r.Shadow {
			fmt.Fprintf(&b, "| %s | %d | %d | %s | %d | %s | %d | %d | %d |\n", s.Slot, s.Labeled, s.ShadowAnswered,
				pct(s.ShadowCorrect, s.ShadowAnswered), s.BothAnswered, pct(s.Agree, s.BothAnswered),
				s.Disagree, s.PrimaryRight, s.ShadowRight)
		}
	}

	if len(r.Promotions) > 0 {
		b.WriteString("\n## Promotion\n\n")
		fmt.Fprintf(&b, "Shadow vs each primary backend on the labeled postings the shadow was asked about and that backend held. A verdict needs at least %d of them per row; promotion is a config change you make (decide.lappi.primary), never automatic.\n\n", MinPromotionLabels)
		b.WriteString("| slot | shadow | primary | asked | shadow precision | shadow coverage | primary precision | primary coverage | verdict |\n|---|---|---|---|---|---|---|---|---|\n")
		for _, p := range r.Promotions {
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s | %s | %s | %s |\n", p.Slot, p.Shadow, p.Primary, p.Asked,
				pct(p.ShadowCorrect, p.ShadowAnswered), pct(p.ShadowAnswered, p.Asked),
				pct(p.PrimaryCorrect, p.PrimaryAnswered), pct(p.PrimaryAnswered, p.Asked), p.Verdict())
		}
	}
	return b.String()
}
