package doctor

import (
	"fmt"
	"sort"
	"strings"
)

// StratumTally is one drop stratum's verdicts among the answered sample.
type StratumTally struct {
	Stratum  string
	Pool     int
	Asked    int
	Wrong    int
	Correct  int
	Unclear  int
	Answered int
}

// Tally counts verdicts per stratum. Strata with nothing asked are kept, so
// the table shows every stratum the sample was drawn from.
func Tally(r Report, drops []DropCase) []StratumTally {
	by := map[string]*StratumTally{}
	for _, s := range r.Strata {
		by[s.Name] = &StratumTally{Stratum: s.Name, Pool: s.Pool}
	}
	stratumOf := map[string]string{}
	for _, d := range sentDrops(drops) {
		stratumOf[d.Key] = d.Stratum
		t := by[d.Stratum]
		if t == nil {
			t = &StratumTally{Stratum: d.Stratum}
			by[d.Stratum] = t
		}
		t.Asked++
	}
	for _, f := range r.DropFinds {
		t := by[stratumOf[f.Key]]
		if t == nil {
			continue
		}
		t.Answered++
		switch f.Verdict {
		case DropWrong:
			t.Wrong++
		case DropCorrect:
			t.Correct++
		default:
			t.Unclear++
		}
	}
	out := make([]StratumTally, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stratum < out[j].Stratum })
	return out
}

// Markdown renders the report for a person. Every count of what was examined
// sits beside the findings, so a short or failed part is visible as one.
func Markdown(r Report, drops []DropCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pipeline doctor, %s\n\n", r.At.UTC().Format("2006-01-02 15:04Z"))
	fmt.Fprintf(&b, "Model: %s. Runs read through %s. The model's verdicts are a report only: nothing in the ledger, the drop rules or the watchlist was changed.\n\n", r.Model, stamp(r.Through))

	b.WriteString("## Boards\n\n")
	fmt.Fprintf(&b, "%d boards tripped a deterministic signal; %d were sent, %d judged.", len(r.Signals), r.Boards.Asked, r.Boards.Answered)
	if r.Boards.Error != "" {
		fmt.Fprintf(&b, " **The board review did not complete: %s** Nothing below is a verdict.", r.Boards.Error)
	}
	b.WriteString("\n\n")
	order := map[string]int{VerdictSilentFailure: 0, VerdictTransientEmpty: 1, VerdictUnclear: 2, VerdictLoudFailure: 3, VerdictTrulyEmpty: 4, VerdictHealthy: 5}
	finds := append([]BoardFinding(nil), r.BoardFinds...)
	sort.SliceStable(finds, func(i, j int) bool { return order[finds[i].Verdict] < order[finds[j].Verdict] })
	if len(finds) > 0 {
		b.WriteString("| Board | Verdict | Confidence | Evidence | Next step |\n|---|---|---|---|---|\n")
		for _, f := range finds {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(f.Board), f.Verdict, f.Confidence, cell(f.Evidence), cell(f.NextStep))
		}
		b.WriteString("\n")
	}
	if len(r.NotExamined) > 0 {
		fmt.Fprintf(&b, "Not examined (signalled, but not judged; not a clean bill): %s\n\n", strings.Join(r.NotExamined, ", "))
	}
	if r.Recovered > 0 {
		fmt.Fprintf(&b, "%d board reads that the run records left unnamed were recovered from the runs' own logs.\n\n", r.Recovered)
	}
	if r.LogMissing > 0 {
		fmt.Fprintf(&b, "Not examined: the unnamed boards of %d runs that left no log. Their history here is partial; a board missing from the table above is not thereby healthy.\n\n", r.LogMissing)
	}
	patterns(&b, r.Boards.Patterns)

	b.WriteString("## Dropped postings\n\n")
	fmt.Fprintf(&b, "%d sampled drops were sent, %d judged (seed %q).", r.Drops.Asked, r.Drops.Answered, r.Seed)
	if r.Drops.Error != "" {
		fmt.Fprintf(&b, " **The drop review did not complete: %s** Nothing below is a verdict.", r.Drops.Error)
	}
	b.WriteString("\n\n")
	if t := Tally(r, drops); len(t) > 0 {
		b.WriteString("| Stratum | Pool | Asked | Judged | Wrong | Correct | Unclear |\n|---|---|---|---|---|---|---|\n")
		for _, s := range t {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d |\n", cell(s.Stratum), s.Pool, s.Asked, s.Answered, s.Wrong, s.Correct, s.Unclear)
		}
		b.WriteString("\nA stratum's wrong share is a small-sample estimate (n = Judged); it says where to look, not a rate to quote.\n\n")
	}
	titles := map[string]string{}
	for _, d := range drops {
		titles[d.Key] = d.Company + " - " + d.Title
	}
	var wrong []DropFinding
	for _, f := range r.DropFinds {
		if f.Verdict == DropWrong {
			wrong = append(wrong, f)
		}
	}
	if len(wrong) > 0 {
		b.WriteString("Drops judged wrong:\n\n| Posting | Cause | Why |\n|---|---|---|\n")
		for _, f := range wrong {
			fmt.Fprintf(&b, "| %s (%s) | %s | %s |\n", cell(titles[f.Key]), cell(f.Key), f.Cause, cell(f.Why))
		}
		b.WriteString("\n")
	}
	if len(r.DropsLeft) > 0 {
		fmt.Fprintf(&b, "Not examined: %d sampled drops.\n\n", len(r.DropsLeft))
	}
	patterns(&b, r.Drops.Patterns)

	b.WriteString("## Logs read\n\n")
	fmt.Fprintf(&b, "%d logs read, %d trouble lines, %d distinct; %d distinct lines past the cap were not sent.", r.Logs.LogsRead, r.Logs.LinesSeen, r.Logs.Distinct, r.Logs.LeftOut)
	if len(r.Logs.Unreadable) > 0 {
		fmt.Fprintf(&b, " Unreadable: %s.", strings.Join(r.Logs.Unreadable, ", "))
	}
	if len(r.Logs.TailOnlyFor) > 0 {
		fmt.Fprintf(&b, " Read from the tail only: %s.", strings.Join(r.Logs.TailOnlyFor, ", "))
	}
	b.WriteString("\n")
	return b.String()
}

func patterns(b *strings.Builder, ps []Pattern) {
	if len(ps) == 0 {
		return
	}
	b.WriteString("Patterns:\n\n")
	for _, p := range ps {
		fmt.Fprintf(b, "- **%s.** %s", oneLineMD(p.Title), oneLineMD(p.Detail))
		if p.Fix != "" {
			fmt.Fprintf(b, " Fix: %s", oneLineMD(p.Fix))
		}
		if len(p.Refs) > 0 {
			fmt.Fprintf(b, " (%s)", oneLineMD(strings.Join(p.Refs, ", ")))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// cell is s safe inside one Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(oneLineMD(s), "|", "\\|")
}

func oneLineMD(s string) string { return strings.Join(strings.Fields(s), " ") }
