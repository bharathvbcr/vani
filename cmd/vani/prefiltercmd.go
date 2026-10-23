package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// prefilterReport is what the prefilter (exclusions and title readings) would
// do to a ledger: which live postings it would drop before triage, which kept
// ones the next run's exclusion pass would drop or reopen, and how the
// decision model read the postings it would drop that a model has already
// read.
type prefilterReport struct {
	Live, Dropped int
	ByReason      map[string]int
	Rows          []prefilterRow // the live postings it would drop, in ledger order
	// Kept are the triaged, scored, explained or digested postings the
	// exclusions would drop at the next run (marked ones never are); Reopen
	// are the excluded postings they no longer match, which return to live.
	Kept, Reopen []prefilterRow
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

// prefilterOf applies p's exclusions and title reading to every record;
// protect says which postings are marked. It reads only.
func prefilterOf(recs []ledger.Record, p pipeline.DropPolicy, protect func(posting.Key) bool) prefilterReport {
	rep := prefilterReport{ByReason: map[string]int{}, RoleFit: map[string]int{}}
	ch := pipeline.ExclusionChanges(recs, p, protect)
	for i, r := range ch.Drop {
		if r.Status != posting.Live { // a live one is counted below, with the title drops
			rep.Kept = append(rep.Kept, prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: ch.Reasons[i], Stage: string(r.Status)})
		}
	}
	for _, r := range ch.Reopen {
		rep.Reopen = append(rep.Reopen, prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: r.Annotations[pipeline.AnnExcluded], Stage: string(r.Status)})
	}
	const rf = ledger.AnnTriagePrefix + "role_fit"
	for _, r := range recs {
		if r.Status == posting.Live {
			rep.Live++
		}
		_, reason, drop := p.EvaluatePosting(r.Posting)
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

// cmdPrefilter reports what the prefilter (exclude, and title_signals.read
// with the drop rules) would drop. It never writes the ledger and calls no
// backend.
func (e *env) cmdPrefilter(args []string) error {
	fs := e.newFlags("prefilter")
	list := fs.Bool("list", false, "also list each posting it would drop or return to live")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	p := dropPolicy(cfg, cfg.Decide.OpenRouterName())
	if !p.Prefilters() {
		fmt.Fprintln(e.stdout, "prefilter is off: the config has no exclude entries and no title_signals.read entries; nothing was evaluated")
		return nil
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return fmt.Errorf("prefilter: read the ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: prefilter: the ledger ends in a torn line; it was left out")
	}
	protect := protectMarked(e.stateDir, func(f string, a ...any) { fmt.Fprintf(e.stderr, "vani: prefilter: "+f+"\n", a...) })
	writePrefilter(e.stdout, prefilterOf(snap.Records, p, protect), len(snap.Records), *list)
	return nil
}

func writePrefilter(w io.Writer, rep prefilterReport, total int, list bool) {
	fmt.Fprintf(w, "prefilter (dry run, nothing written): %d of %d live posting(s) would be dropped before triage (%d in the ledger)\n",
		rep.Dropped, rep.Live, total)
	fmt.Fprintf(w, "exclusions at the next run: %d kept posting(s) would be dropped (marked ones are kept), %d excluded posting(s) would return to live\n",
		len(rep.Kept), len(rep.Reopen))
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
		fmt.Fprintf(w, "    %s  %s [%s, %s] %s\n", f.Key, jobview.CleanText(f.Title, 120, false), f.RoleFit, f.Stage, f.Reason)
	}
	if rep.Read == 0 {
		fmt.Fprintln(w, "  no model reading to compare against: precision is unmeasured, not perfect")
	}
	if list {
		for _, r := range rep.Rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason)
		}
		for _, r := range rep.Kept {
			fmt.Fprintf(w, "%s\t%s\t%s (now %s)\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason, r.Stage)
		}
		for _, r := range rep.Reopen {
			fmt.Fprintf(w, "%s\t%s\treturns to live (was %s)\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason)
		}
	}
}
