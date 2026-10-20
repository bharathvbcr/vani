package eval

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
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

// ThresholdRow is the OpenRouter model's precision among answers with p >= P for one slot.
type ThresholdRow struct {
	P        float64
	Labeled  int // OpenRouter-owned labeled postings for the slot (the coverage denominator)
	Answered int // answered with p >= P
	Correct  int
}

// Threshold is one slot's OpenRouter precision-at-threshold table.
type Threshold struct {
	Slot string
	Rows []ThresholdRow
}

// ShadowRow compares one shadow backend with the primary and the labels for
// one slot. Labeled counts the labeled postings that backend was asked about,
// including ones where it was unavailable.
type ShadowRow struct {
	Slot           string
	Shadow         string // shadow backend name
	Labeled        int    // labeled postings this shadow was asked about for the slot
	ShadowAnswered int
	ShadowCorrect  int
	BothAnswered   int // primary and shadow both answered
	Agree          int
	Disagree       int
	PrimaryRight   int // among Disagree, primary equals the label
	ShadowRight    int // among Disagree, shadow equals the label
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
	// ShadowUnavailable counts postings where the shadow was asked and recorded
	// as unavailable (it errored or timed out). Rows recorded before the router
	// wrote that reading cannot show their failures.
	ShadowUnavailable int
	// Paired counts postings where both the primary and the shadow answered.
	// PrimaryOnly and ShadowOnly split the paired postings where exactly one of
	// them matched the label: the only postings that separate the two.
	Paired      int
	PrimaryOnly int // primary right, shadow wrong
	ShadowOnly  int // shadow right, primary wrong
}

const (
	// MinDiscordant is the fewest paired postings on which the primary and the
	// shadow differ in correctness before a verdict is given.
	MinDiscordant = 10
	// PromotionBound is T: the shadow is promotable only when the one-sided 95%
	// exact lower bound of ShadowOnly/(PrimaryOnly+ShadowOnly) is at least this.
	// 0.5 would demand the shadow be shown at least as good; 0.4 allows it to be
	// a little worse (owner's choice, 2026-10-19).
	PromotionBound = 0.4
	// promotionAlpha is the one-sided error rate of that bound.
	promotionAlpha = 0.05
)

// Discordant is the number of paired postings where exactly one of the primary
// and the shadow matched the label.
func (p Promotion) Discordant() int { return p.PrimaryOnly + p.ShadowOnly }

// ShadowLowerBound is the one-sided 95% exact (Clopper-Pearson) lower bound of
// the share of discordant postings the shadow got right.
func (p Promotion) ShadowLowerBound() float64 {
	return lowerBound(p.ShadowOnly, p.Discordant(), promotionAlpha)
}

// Verdict is the promotion decision and its reason, with counts. It needs the
// 30-label floor, the existing no-worse-coverage rule, and then a paired
// comparison on the postings both answered: at least MinDiscordant of them where
// exactly one was right, and a lower bound on the shadow's share of those of at
// least PromotionBound. Never "promotable" on too few disagreements.
func (p Promotion) Verdict() string {
	if p.Asked < MinPromotionLabels {
		return fmt.Sprintf("insufficient labels (%d/%d)", p.Asked, MinPromotionLabels)
	}
	switch {
	case p.PrimaryAnswered == 0:
		return "no primary baseline (the chain answered none of these)"
	case p.ShadowAnswered == 0:
		return "not promotable: the shadow answered none of these"
	case p.ShadowAnswered < p.PrimaryAnswered:
		return fmt.Sprintf("not promotable: coverage %d/%d < primary %d/%d", p.ShadowAnswered, p.Asked, p.PrimaryAnswered, p.Asked)
	case p.Paired < MinPromotionLabels:
		return fmt.Sprintf("insufficient paired labels (%d/%d)", p.Paired, MinPromotionLabels)
	case p.Discordant() < MinDiscordant:
		return fmt.Sprintf("insufficient disagreements (%d/%d)", p.Discordant(), MinDiscordant)
	}
	if lb := p.ShadowLowerBound(); lb < PromotionBound {
		return fmt.Sprintf("not promotable: shadow right in %d of %d disagreements, lower bound %.2f < %.2f",
			p.ShadowOnly, p.Discordant(), lb, PromotionBound)
	}
	return "promotable"
}

// lowerBound is the one-sided exact (Clopper-Pearson) lower confidence bound at
// level 1-alpha for a binomial proportion with k successes in n trials: the p
// at which P(X >= k) = alpha. It is 0 when k is 0 or n is not positive.
func lowerBound(k, n int, alpha float64) float64 {
	if k <= 0 || n <= 0 || k > n {
		return 0
	}
	lg := func(x float64) float64 { v, _ := math.Lgamma(x); return v }
	tail := func(p float64) float64 {
		var sum float64
		for i := k; i <= n; i++ {
			sum += math.Exp(lg(float64(n)+1) - lg(float64(i)+1) - lg(float64(n-i)+1) +
				float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
		}
		return sum
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2
		if tail(mid) < alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
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

// triageLabels keeps the entries that label at least one triage slot. A
// would_apply-only entry (the audit's) says nothing about a backend's reading,
// and counting it would inflate the triage report and move the held-out split.
func triageLabels(labels map[posting.Key]Entry) map[posting.Key]Entry {
	out := make(map[posting.Key]Entry, len(labels))
	for k, e := range labels {
		for slot := range e.Labels {
			if slot != WouldApply {
				out[k] = e
				break
			}
		}
	}
	return out
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
	labels, bad, err := s.Labels()
	return buildReport(recs, labels, bad, err, names)
}

// buildReport is BuildReport over an already-read label set. BuildHeldOut
// calls it with the held-out subset, so both reports share one arithmetic.
func buildReport(recs []ledger.Record, labels map[posting.Key]Entry, bad int, err error, names Names) Report {
	labels = triageLabels(labels)
	rep := Report{Records: len(recs), ProbabilityBackend: names.probability()}
	rep.MalformedLines = bad
	if err != nil {
		rep.LoadError = err.Error()
	}
	rep.LabelEntries = len(labels)

	type ck struct{ slot, backend string }
	cells := map[ck]*Cell{}
	thr := map[string][]ThresholdRow{}
	type sk struct{ slot, shadow string }
	shadow := map[sk]*ShadowRow{}
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
				if shReading == "unavailable" {
					p.ShadowUnavailable++
				}
				if shReading == "answered" {
					p.ShadowAnswered++
					shadowRight := r.Annotations[pre+ledger.AnnTriageSuffixShadow] == want
					if shadowRight {
						p.ShadowCorrect++
					}
					if answered {
						primaryRight := got == want
						p.Paired++
						switch {
						case primaryRight && !shadowRight:
							p.PrimaryOnly++
						case shadowRight && !primaryRight:
							p.ShadowOnly++
						}
					}
				}
			}

			// One row per shadow backend: two candidate models never pool.
			_, asked := r.Annotations[pre+ledger.AnnTriageSuffixShadowReading]
			sh, answeredShadow := r.Annotations[pre+ledger.AnnTriageSuffixShadow]
			if !asked && !answeredShadow {
				continue
			}
			sb := r.Annotations[pre+ledger.AnnTriageSuffixShadowBackend]
			if sb == "" {
				sb = UnknownBackend
			}
			sr := shadow[sk{sl.Name, sb}]
			if sr == nil {
				sr = &ShadowRow{Slot: sl.Name, Shadow: sb}
				shadow[sk{sl.Name, sb}] = sr
			}
			sr.Labeled++
			if answeredShadow {
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
		var shadows []string
		for k, sr := range shadow {
			if k.slot == sl.Name && sr.ShadowAnswered > 0 {
				shadows = append(shadows, k.shadow)
			}
		}
		sort.Strings(shadows)
		for _, s := range shadows {
			rep.Shadow = append(rep.Shadow, *shadow[sk{sl.Name, s}])
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
		b.WriteString("One row per shadow backend; \"labeled\" is the labeled postings that backend was asked about, including ones where it was unavailable.\n\n")
		b.WriteString("| slot | shadow | labeled | shadow answered | shadow precision | both answered | agreement | disagree | primary right | shadow right |\n|---|---|---|---|---|---|---|---|---|---|\n")
		for _, s := range r.Shadow {
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %d | %s | %d | %d | %d |\n", s.Slot, s.Shadow, s.Labeled, s.ShadowAnswered,
				pct(s.ShadowCorrect, s.ShadowAnswered), s.BothAnswered, pct(s.Agree, s.BothAnswered),
				s.Disagree, s.PrimaryRight, s.ShadowRight)
		}
	}

	if len(r.Promotions) > 0 {
		b.WriteString("\n## Promotion\n\n")
		fmt.Fprintf(&b, "Shadow vs each primary backend on the labeled postings the shadow was asked about and that backend held. A verdict needs at least %d of them per row, a shadow that answers at least as often as the primary, and then a paired comparison on the postings both answered: at least %d where exactly one was right, and a one-sided 95%% exact lower bound on the shadow's share of those of at least %.2f. Promotion is a config change you make (decide.lappi.primary), never automatic.\n\n", MinPromotionLabels, MinDiscordant, PromotionBound)
		legacy := writePromotions(&b, r.Promotions)
		b.WriteString("\n\"primary only\" and \"shadow only\" are paired postings where only that side matched the label. Postings recorded before the router wrote an unavailable shadow reading cannot show shadow failures.\n")
		if legacy {
			fmt.Fprintf(&b, "Shadow %q is a row recorded before the model was named in the backend (it pools whichever local model ran then; see docs/FOLLOWUPS.md).\n", LegacyLocalShadow)
		}
	}
	return b.String()
}

// writePromotions writes the Promotion table rows and reports if any row has legacy local shadow.
func writePromotions(b *strings.Builder, ps []Promotion) bool {
	b.WriteString("| slot | shadow | primary | asked | shadow precision | shadow coverage | primary precision | primary coverage | shadow unavailable | paired | primary only | shadow only | verdict |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	legacy := false
	for _, p := range ps {
		if p.Shadow == LegacyLocalShadow {
			legacy = true
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %s | %s | %s | %s | %d | %d | %d | %d | %s |\n", p.Slot, p.Shadow, p.Primary, p.Asked,
			pct(p.ShadowCorrect, p.ShadowAnswered), pct(p.ShadowAnswered, p.Asked),
			pct(p.PrimaryCorrect, p.PrimaryAnswered), pct(p.PrimaryAnswered, p.Asked),
			p.ShadowUnavailable, p.Paired, p.PrimaryOnly, p.ShadowOnly, p.Verdict())
	}
	return legacy
}
