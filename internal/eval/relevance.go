package eval

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// DefaultPrecisionKs are the k of the precision@k tables.
var DefaultPrecisionKs = []int{5, 10, 20}

// PositiveReadings are the role_fit readings that say "worth applying to": the
// reliability table scores a backend's probability of one of them against the
// owner's would_apply. A reading outside this set says nothing about how likely
// the owner is to want the posting, so it is counted but not scored.
var PositiveReadings = map[string]bool{"strong_fit": true, "partial_fit": true}

const (
	wilsonZ = 1.959964 // two-sided 95%
	// reliabilityBins is the number of equal-width p bins of the reliability table.
	reliabilityBins = 5
)

// Wilson is the 95% Wilson score interval for k successes in n trials. n <= 0
// has no interval: ok is false, never a 0% or 0-100% stand-in.
func Wilson(k, n int) (lo, hi float64, ok bool) {
	if n <= 0 || k < 0 || k > n {
		return 0, 0, false
	}
	nf, p := float64(n), float64(k)/float64(n)
	z2 := wilsonZ * wilsonZ
	den := 1 + z2/nf
	centre := (p + z2/(2*nf)) / den
	half := wilsonZ * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / den
	return math.Max(0, centre-half), math.Min(1, centre+half), true
}

// Ordering is a ranked list of postings, best first, as one surface shows them:
// the caller builds it (the New view, the Top-open view).
type Ordering struct {
	Name string
	Keys []posting.Key
}

// Count is a raw count of wanted postings among judged ones.
type Count struct {
	Wanted int // would_apply yes or maybe
	N      int // would_apply yes, maybe or no (skip is not judged)
}

// Interval returns the Wilson interval of c; ok is false when N is 0.
func (c Count) Interval() (lo, hi float64, ok bool) { return Wilson(c.Wanted, c.N) }

func (c Count) String() string {
	lo, hi, ok := c.Interval()
	if !ok {
		return "no data (n=0)"
	}
	return fmt.Sprintf("%d/%d = %.0f%% (95%% CI %.0f-%.0f%%)", c.Wanted, c.N, 100*float64(c.Wanted)/float64(c.N), 100*lo, 100*hi)
}

// PrecisionRow is precision@K of one ordering over the labelled postings in its
// top K. Shown is how many the ordering listed there; the rest of the top K is
// unlabelled and says nothing.
type PrecisionRow struct {
	Ordering string
	K        int
	Shown    int
	Count
}

// StratumRow is one sampling stratum's judged count.
type StratumRow struct {
	Source string
	Count
}

// ReliabilityBin is one p bin of a backend's reliability table.
type ReliabilityBin struct {
	Lo, Hi float64
	Count
	SumP float64
}

// BackendRow is one backend's p against the owner's would_apply, over the
// postings where its role_fit reading was a positive one with a recorded p.
type BackendRow struct {
	Backend string
	// Judged are the owner-judged postings this backend answered role_fit for;
	// Positive of them read as PositiveReadings, and Scored of those carry a p.
	Judged, Positive, Scored int
	Count                    // would_apply among Scored
	SumP                     float64
	SumSq                    float64 // sum of (p - wanted)^2
	Bins                     [reliabilityBins]ReliabilityBin
}

// Brier is the mean squared error of p as a forecast of "wanted"; ok is false
// with nothing scored.
func (b BackendRow) Brier() (float64, bool) {
	if b.N == 0 {
		return 0, false
	}
	return b.SumSq / float64(b.N), true
}

// Relevance is what the would_apply labels measure, as raw counts.
type Relevance struct {
	Labels    int // would_apply labels in the store
	Judged    int // of those, yes/maybe/no
	Skipped   int
	Manual    int // judged outside a sample (no p_select): kept out of the strata
	Malformed int // label lines the store could not read, set by the caller
	Precision []PrecisionRow
	Deciles   []StratumRow
	Drops     []StratumRow
	Backends  []BackendRow
}

func wanted(v string) (hit, judged bool) {
	switch v {
	case "yes", "maybe":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

// BuildRelevance counts the would_apply labels in labels: precision@k of each
// ordering, the share wanted per score decile and per drop rule (strata come
// from each label's Source), and each backend's reliability against its
// recorded p. Nothing is weighted: the labels are a stratified sample, so a
// count is a count of postings the owner judged, not an estimate for the whole
// pool. recs supply backend readings; names files legacy backend names.
func BuildRelevance(labels map[posting.Key]Entry, recs []ledger.Record, orderings []Ordering, ks []int, names Names) Relevance {
	var out Relevance
	judged := map[posting.Key]bool{} // owner-judged: would_apply yes, maybe or no
	hit := map[posting.Key]bool{}
	dec, drop := map[string]*Count{}, map[string]*Count{}
	add := func(m map[string]*Count, src string, h bool) {
		c := m[src]
		if c == nil {
			c = &Count{}
			m[src] = c
		}
		c.N++
		if h {
			c.Wanted++
		}
	}
	for k, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			continue
		}
		out.Labels++
		h, j := wanted(v)
		if !j {
			out.Skipped++
			continue
		}
		out.Judged++
		judged[k], hit[k] = true, h
		switch {
		case e.Source == SourceManual:
			out.Manual++
		case strings.HasPrefix(e.Source, "decile:"):
			add(dec, e.Source, h)
		case strings.HasPrefix(e.Source, "dropped:"):
			add(drop, e.Source, h)
		}
	}
	for _, o := range orderings {
		for _, k := range ks {
			if k < 1 {
				continue
			}
			row := PrecisionRow{Ordering: o.Name, K: k, Shown: min(k, len(o.Keys))}
			for _, key := range o.Keys[:row.Shown] {
				if judged[key] {
					row.N++
					if hit[key] {
						row.Wanted++
					}
				}
			}
			out.Precision = append(out.Precision, row)
		}
	}
	out.Deciles, out.Drops = strata(dec, true), strata(drop, false)
	out.Backends = backendRows(judged, hit, recs, names)
	return out
}

// strata lists counts by source: deciles from the top (decile:10 first), drop
// rules by name.
func strata(m map[string]*Count, byDecile bool) []StratumRow {
	var out []StratumRow
	for s, c := range m {
		out = append(out, StratumRow{Source: s, Count: *c})
	}
	n := func(s string) int { v, _ := strconv.Atoi(strings.TrimPrefix(s, "decile:")); return v }
	sort.Slice(out, func(i, j int) bool {
		if byDecile {
			return n(out[i].Source) > n(out[j].Source)
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func backendRows(judged, hit map[posting.Key]bool, recs []ledger.Record, names Names) []BackendRow {
	rows := map[string]*BackendRow{}
	for _, r := range recs {
		if !judged[r.Key] {
			continue
		}
		pre := ledger.AnnTriagePrefix + "role_fit"
		if r.Annotations[pre+ledger.AnnTriageSuffixReading] != "answered" {
			continue
		}
		backend, _ := names.primary(r.Annotations[pre+ledger.AnnTriageSuffixBackend])
		if backend == "" {
			backend = UnknownBackend
		}
		row := rows[backend]
		if row == nil {
			row = &BackendRow{Backend: backend}
			for i := range row.Bins {
				row.Bins[i].Lo, row.Bins[i].Hi = float64(i)/reliabilityBins, float64(i+1)/reliabilityBins
			}
			rows[backend] = row
		}
		row.Judged++
		if !PositiveReadings[r.Annotations[pre]] {
			continue
		}
		row.Positive++
		p, err := strconv.ParseFloat(r.Annotations[pre+ledger.AnnTriageSuffixP], 64)
		if err != nil || math.IsNaN(p) || p < 0 || p > 1 {
			continue
		}
		y := 0.0
		if hit[r.Key] {
			y = 1
		}
		row.Scored++
		row.N++
		row.Wanted += int(y)
		row.SumP += p
		row.SumSq += (p - y) * (p - y)
		b := &row.Bins[min(int(p*reliabilityBins), reliabilityBins-1)]
		b.N++
		b.Wanted += int(y)
		b.SumP += p
	}
	var out []BackendRow
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Backend < out[j].Backend })
	return out
}

// Markdown renders the report. Every figure is a count over a stated n with its
// Wilson interval; a table with no judged postings says "no data", never 0%.
func (r Relevance) Markdown() string {
	var b strings.Builder
	b.WriteString("# Vani relevance (owner would_apply labels)\n\n")
	fmt.Fprintf(&b, "- would_apply labels: %d (judged yes/maybe/no: %d, skipped: %d, manual outside any sample: %d)\n", r.Labels, r.Judged, r.Skipped, r.Manual)
	if r.Malformed > 0 {
		fmt.Fprintf(&b, "- label lines skipped as unreadable: %d\n", r.Malformed)
	}
	b.WriteString("- wanted = yes or maybe. Raw counts with 95% Wilson intervals; nothing is weighted by p_select. The labelled postings are a stratified sample of the pool, so a rate here describes those postings, not the whole ledger.\n\n")
	if r.Judged == 0 {
		b.WriteString("no data: no judged would_apply labels yet (run `vani audit`)\n")
		return b.String()
	}

	b.WriteString("## Precision@k by ordering\n\nOver the judged postings in each ordering's top k; \"shown\" is how many it listed there.\n\n")
	b.WriteString("| ordering | k | shown | wanted among judged |\n|---|---|---|---|\n")
	for _, p := range r.Precision {
		fmt.Fprintf(&b, "| %s | %d | %d | %s |\n", p.Ordering, p.K, p.Shown, p.Count)
	}

	b.WriteString("\n## Wanted by score decile\n\n")
	writeStrata(&b, r.Deciles, "decile")

	b.WriteString("\n## Drop recall per rule: wanted among dropped\n\nEach wanted posting here is one the rule dropped that the owner would have applied to (a miss). Recall itself needs the kept side weighted by p_select, which this report does not do.\n\n")
	writeStrata(&b, r.Drops, "drop rule")

	b.WriteString("\n## Backend reliability: p against would_apply\n\nOnly postings the backend read as strong_fit or partial_fit with a recorded p are scored: p is the backend's probability of that reading, the event is the owner wanting the posting. Brier is the mean of (p - wanted)^2; lower is better, 0.25 is a constant guess of 0.5.\n\n")
	if len(r.Backends) == 0 {
		b.WriteString("no data: no judged posting has a backend role_fit reading\n")
		return b.String()
	}
	b.WriteString("| backend | judged | positive reading | with p | wanted among scored | mean p | Brier |\n|---|---|---|---|---|---|---|\n")
	for _, row := range r.Backends {
		meanP, brier := "no data", "no data"
		if row.N > 0 {
			meanP = fmt.Sprintf("%.2f", row.SumP/float64(row.N))
		}
		if v, ok := row.Brier(); ok {
			brier = fmt.Sprintf("%.3f", v)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %s |\n", row.Backend, row.Judged, row.Positive, row.Scored, row.Count, meanP, brier)
	}
	for _, row := range r.Backends {
		if row.N == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s, by p:\n\n| p in | mean p | wanted |\n|---|---|---|\n", row.Backend)
		for _, bin := range row.Bins {
			if bin.N == 0 {
				continue
			}
			fmt.Fprintf(&b, "| [%.1f, %.1f) | %.2f | %s |\n", bin.Lo, bin.Hi, bin.SumP/float64(bin.N), bin.Count)
		}
	}
	return b.String()
}

func writeStrata(b *strings.Builder, rows []StratumRow, label string) {
	if len(rows) == 0 {
		b.WriteString("no data: no judged postings drawn from these strata\n")
		return
	}
	fmt.Fprintf(b, "| %s | wanted among judged |\n|---|---|\n", label)
	for _, s := range rows {
		fmt.Fprintf(b, "| %s | %s |\n", s.Source, s.Count)
	}
}
