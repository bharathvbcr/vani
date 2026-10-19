package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// prefilterReport is what the title prefilter would do to a ledger: which
// live postings it would drop before triage, and how the decision model read
// the postings it would drop that a model has already read.
type prefilterReport struct {
	Live, Dropped int
	ByReason      map[string]int
	Rows          []prefilterRow // the live postings it would drop, in ledger order
	// Read counts the postings it would drop whose role_fit a decision
	// backend (not the title) answered; RoleFit is how, and Fits lists the
	// ones read strong_fit or partial_fit: the prefilter's false drops.
	Read    int
	RoleFit map[string]int
	Fits    []prefilterRow
}

type prefilterRow struct {
	Key            posting.Key
	Title, Reason  string
	RoleFit, Stage string
}

// prefilterOf applies p's title reading to every record. It reads only.
func prefilterOf(recs []ledger.Record, p pipeline.DropPolicy) prefilterReport {
	rep := prefilterReport{ByReason: map[string]int{}, RoleFit: map[string]int{}}
	const rf = ledger.AnnTriagePrefix + "role_fit"
	for _, r := range recs {
		if r.Status == posting.Live {
			rep.Live++
		}
		_, reason, drop := p.EvaluateTitle(r.Posting.Title)
		if !drop {
			continue
		}
		row := prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: reason, Stage: string(r.Status)}
		if r.Status == posting.Live {
			rep.Dropped++
			rep.ByReason[reason]++
			rep.Rows = append(rep.Rows, row)
		}
		a := r.Annotations
		if a[rf+ledger.AnnTriageSuffixReading] != "answered" || a[rf] == "" || a[rf+ledger.AnnTriageSuffixBackend] == pipeline.TitleBackend {
			continue
		}
		rep.Read++
		rep.RoleFit[a[rf]]++
		if v := a[rf]; v == "strong_fit" || v == "partial_fit" {
			row.RoleFit = v
			rep.Fits = append(rep.Fits, row)
		}
	}
	return rep
}

// cmdPrefilter reports what the title prefilter (title_signals.read and the
// drop rules) would drop. It never writes the ledger and calls no backend.
func (e *env) cmdPrefilter(args []string) error {
	fs := e.newFlags("prefilter")
	list := fs.Bool("list", false, "also list each live posting it would drop")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if len(cfg.TitleSignals.Read) == 0 {
		fmt.Fprintln(e.stdout, "title prefilter is off: the config has no title_signals.read entries; nothing was evaluated")
		return nil
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return fmt.Errorf("prefilter: read the ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: prefilter: the ledger ends in a torn line; it was left out")
	}
	writePrefilter(e.stdout, prefilterOf(snap.Records, dropPolicy(cfg, cfg.Decide.OpenRouterName())), len(snap.Records), *list)
	return nil
}

func writePrefilter(w io.Writer, rep prefilterReport, total int, list bool) {
	fmt.Fprintf(w, "title prefilter (dry run, nothing written): %d of %d live posting(s) would be dropped before triage (%d in the ledger)\n",
		rep.Dropped, rep.Live, total)
	reasons := make([]string, 0, len(rep.ByReason))
	for r := range rep.ByReason {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if rep.ByReason[reasons[i]] != rep.ByReason[reasons[j]] {
			return rep.ByReason[reasons[i]] > rep.ByReason[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	for _, r := range reasons {
		fmt.Fprintf(w, "  %5d  %s\n", rep.ByReason[r], r)
	}
	vals := make([]string, 0, len(rep.RoleFit))
	for v := range rep.RoleFit {
		vals = append(vals, fmt.Sprintf("%s %d", v, rep.RoleFit[v]))
	}
	slices.Sort(vals)
	fmt.Fprintf(w, "precision: a decision model read role_fit on %d posting(s) it would drop (any status): %s\n", rep.Read, strings.Join(vals, ", "))
	fmt.Fprintf(w, "  read strong_fit or partial_fit (false drops): %d\n", len(rep.Fits))
	for _, f := range rep.Fits {
		fmt.Fprintf(w, "    %s  %s [%s, %s] %s\n", f.Key, cleanText(f.Title, 120, false), f.RoleFit, f.Stage, f.Reason)
	}
	if rep.Read == 0 {
		fmt.Fprintln(w, "  no model reading to compare against: precision is unmeasured, not perfect")
	}
	if list {
		for _, r := range rep.Rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Key, cleanText(r.Title, 120, false), r.Reason)
		}
	}
}
