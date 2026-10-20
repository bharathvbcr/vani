package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// `vani jobs gaps` answers "what should I learn next": across the roles a jobs
// view selects, which skills block the most (and most-wanted) roles. It is
// display-only, like the rest of the browsing aids: it never drops a role.

// jobsGapsJSON is the --json reply. The report is embedded so its fields sit
// flat beside the view.
type jobsGapsJSON struct {
	View  string `json:"view"`
	Query string `json:"query,omitempty"`
	jobinsight.GapReport
	Insight jobview.InsightInfo `json:"insight"`
	Torn    bool                `json:"torn,omitempty"`
	Ledger  string              `json:"ledger"`
	Records int                 `json:"records"`
}

// plural is "1 role", "2 roles"; a noun ending in a consonant and y takes ies.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	if len(noun) > 1 && strings.HasSuffix(noun, "y") && !strings.ContainsRune("aeiou", rune(noun[len(noun)-2])) {
		return fmt.Sprintf("%d %sies", n, noun[:len(noun)-1])
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func (e *env) jobsGapsCmd(args []string) error {
	fs := e.newFlags("jobs gaps")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	var o jobsOpts
	addSelectFlags(fs, &o)
	limit := fs.Int("n", jobinsight.DefaultGapLimit, fmt.Sprintf("skills to show (1-%d)", jobinsight.MaxGapLimit))
	examples := fs.Int("examples", jobinsight.DefaultGapExamples, fmt.Sprintf("example roles per skill (1-%d)", jobinsight.MaxGapExamples))
	if err := parse(fs, args); err != nil {
		return err
	}
	out := jobsGapsJSON{GapReport: jobinsight.GapReport{Gaps: []jobinsight.Gap{}}}
	err := func() error {
		if *limit < 1 || *limit > jobinsight.MaxGapLimit {
			return usagef("jobs gaps: -n must be 1-%d", jobinsight.MaxGapLimit)
		}
		if *examples < 1 || *examples > jobinsight.MaxGapExamples {
			return usagef("jobs gaps: --examples must be 1-%d", jobinsight.MaxGapExamples)
		}
		if err := o.check("jobs gaps", false); err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.HalfLife, snap, m)
		if err != nil {
			return err
		}
		sel, _ := jobview.Select(snap, m, &o.Opts, in, 0)
		jobs := make([]jobinsight.GapJob, 0, len(sel))
		for _, s := range sel {
			// The raw annotation, not the display copy in s.Job.Missing, which
			// is cut at jobNoteRunes.
			var missing string
			if r, ok := findRecord(snap, s.Job.Key); ok {
				missing = r.Annotations[ledger.AnnMissing]
			}
			jobs = append(jobs, jobinsight.GapJob{
				Key: string(s.Job.Key), Company: s.Job.Company, Title: s.Job.Title,
				Priority: s.Job.Priority, Hits: s.Hits, Missing: missing,
			})
		}
		out.GapReport = jobinsight.Gaps(jobs, in.vocab, in.target,
			jobinsight.GapOptions{Limit: *limit, Examples: *examples})
		if out.Gaps == nil {
			out.Gaps = []jobinsight.Gap{}
		}
		out.View, out.Query, out.Torn = o.View, o.Query, snap.Torn
		out.Insight = in.info
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: %s, %d with a DevPrism gap analysis\n", oneLine(out.View, 40), plural(out.Roles, "role"), out.WithMissing)
		switch {
		case out.Roles > 0 && !out.TargetKnown && out.WithMissing == 0:
			fmt.Fprintln(e.stdout, "no gaps could be judged: no role has a DevPrism gap analysis and the target summary was not read")
		case len(out.Gaps) == 0 && out.Roles > 0 && !out.TargetKnown:
			fmt.Fprintln(e.stdout, "no DevPrism gaps found (keyword gaps were not judged)")
		case len(out.Gaps) == 0 && out.Roles > 0:
			fmt.Fprintln(e.stdout, "no gaps found")
		case len(out.Gaps) > 0:
			fmt.Fprintln(e.stdout, "skills to build (DevPrism-found gaps first, by the priority of the roles they block):")
		}
		for i, g := range out.Gaps {
			found := "not named by your target summary"
			if g.Missing > 0 {
				found = fmt.Sprintf("%d found missing by DevPrism, weight %.1f", g.Missing, g.MissingWeight)
			}
			fmt.Fprintf(e.stdout, "%2d. %s [%s]  %s (%s), %s, weight %.1f\n",
				i+1, oneLine(g.Term, jobview.FieldRunes), oneLine(g.Category, 40), plural(g.Roles, "role"), found,
				plural(g.Companies, "company"), g.Weight)
			var ex []string
			for _, x := range g.Examples {
				ex = append(ex, oneLine(x.Company, jobview.FieldRunes)+": "+oneLine(x.Title, jobview.FieldRunes))
			}
			if len(ex) > 0 {
				fmt.Fprintf(e.stdout, "    e.g. %s\n", strings.Join(ex, "; "))
			}
		}
		if out.TotalGaps > len(out.Gaps) {
			fmt.Fprintf(e.stdout, "showing %d of %d\n", len(out.Gaps), out.TotalGaps)
		}
		if !out.TargetKnown {
			fmt.Fprintln(e.stdout, "note: only DevPrism gaps were counted because the target summary was not read")
		}
		if out.Insight.Warning != "" {
			fmt.Fprintf(e.stdout, "note: %s\n", out.Insight.Warning)
		}
	})
}
