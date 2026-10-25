package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/rkbcoverage"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// `jobs resume coverage` says which of the posting's keywords a version's bullets
// cover; `jobs resume gaps` hands the ones no eligible bullet covers to the
// owner's career session. Neither asks a model or spends anything; each reads the
// pool once (one read-only rkb claims export, as `show` does). The computation is
// internal/rkbcoverage; docs/RUNTIME.md ("Resume Studio", "Coverage") has the
// rules.

// resumeCoverageDeadline bounds both commands as a whole (the pool read is the
// long part), the same class as `show`. A variable so tests need not wait.
var resumeCoverageDeadline = 45 * time.Second

const (
	// maxGapTerms and maxGapTermRunes bound one hand-off line's terms.
	maxGapTerms     = 100
	maxGapTermRunes = 80
)

// resumeCoverageReply is the reply of `jobs resume coverage`. Version is the
// version asked about (the current one when none was named); Base is true, and
// Version absent, when the job has no version and the owner's base selection was
// measured instead.
type resumeCoverageReply struct {
	Key     string `json:"key"`
	Version int    `json:"version,omitempty"`
	Base    bool   `json:"base,omitempty"`
	// KeywordsSource says whose terms were judged: "posting" (the keywords `jobs
	// show` reports), "jd_keywords" (the version's own), both joined with "+", or
	// "none".
	KeywordsSource string `json:"keywords_source"`
	// KeywordsError is why the posting's keywords could not be read; the judgement
	// then holds the version's own terms alone.
	KeywordsError string `json:"keywords_error,omitempty"`
	rkbcoverage.Report
	// GapMissing are the vocabulary terms DevPrism found missing or weak for this
	// posting (the ledger's gap.missing annotation), sorted.
	GapMissing []string `json:"gap_missing"`

	pool []rkbcoverage.Bullet // every eligible bullet, for `gaps`
}

func (e *env) jobsResumeCoverage(args []string) error {
	fs := e.newFlags("jobs resume coverage")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if n := fs.NArg(); n < 1 || n > 2 {
		return usagef("jobs resume coverage: expected a posting key and, optionally, a version (v3)")
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeCoverageDeadline)
	defer dcancel()
	out, _, err := e.resumeCoverage(ctx, fs.Arg(0), fs.Arg(1))
	return e.resumeEmit(*asJSON, out, err, func() {
		what := "the base selection"
		if !out.Base {
			what = fmt.Sprintf("v%d", out.Version)
		}
		fmt.Fprintf(e.stdout, "%s: %s covers %d of %d keywords (%s)\n", out.Key, what, len(out.Hit), len(out.Hit)+len(out.Miss), out.KeywordsSource)
		for _, l := range []struct {
			what  string
			terms []string
		}{{"missing", out.Miss}, {"no eligible bullet covers", out.Uncovered}, {"could not be matched", out.Unjudged}, {"DevPrism found missing", out.GapMissing}} {
			if len(l.terms) > 0 {
				fmt.Fprintf(e.stdout, "  %s: %s\n", l.what, strings.Join(l.terms, ", "))
			}
		}
		if len(out.Unresolved) > 0 {
			fmt.Fprintf(e.stdout, "note: %d chosen bullets are no longer offered, so they cover nothing here\n", len(out.Unresolved))
		}
		if out.KeywordsError != "" {
			fmt.Fprintf(e.stdout, "note: %s\n", out.KeywordsError)
		}
	})
}

// resumeCoverage measures one version (the current one when rawVersion is empty;
// the base selection when the job has none) against the posting's keywords. It
// returns the job beside the reply for the caller that hands it over.
func (e *env) resumeCoverage(ctx context.Context, rawKey, rawVersion string) (resumeCoverageReply, resumeJob, error) {
	fail := func(err error) (resumeCoverageReply, resumeJob, error) {
		return resumeCoverageReply{}, resumeJob{}, err
	}
	job, jerr := e.resumeJobFor(rawKey, false)
	if jerr != nil {
		return fail(jerr)
	}
	want := 0
	if rawVersion != "" {
		n, perr := parseVersion(rawVersion)
		if perr != nil {
			return fail(perr)
		}
		want = n
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return fail(serr)
	}
	if want == 0 {
		cur, cerr := currentOf(st, job.key)
		if cerr != nil {
			return fail(cerr)
		}
		want = cur
	}
	out := resumeCoverageReply{Key: string(job.key), Version: want, Base: want == 0}
	var jd []string
	var selection []byte
	if want != 0 {
		meta, _, err := st.Get(string(job.key), want)
		if err != nil {
			return fail(storeError(err))
		}
		selection, jd = meta.Selection, meta.Keywords
	}

	w, err := e.resumeConfig(ctx, false)
	if err != nil {
		return fail(err)
	}
	pool, err := rkbresume.Pool(ctx, w.RC, rkbresume.Request{})
	if err != nil {
		return fail(err)
	}
	if out.Base {
		selection = pool.Base
	}

	posting, kerr := withContext(ctx, func() ([]string, error) {
		j, _, ok, err := e.showJob(job.key)
		if err != nil {
			return nil, err
		}
		if !ok { // resumeJobFor found it a moment ago; a view that cannot is not "no keywords"
			return nil, errors.New("the job view has no such posting")
		}
		terms := make([]string, 0, len(j.Keywords))
		for _, k := range j.Keywords {
			terms = append(terms, k.Term)
		}
		return terms, nil
	})
	if kerr != nil {
		out.KeywordsError = "the posting's keywords could not be read: " + oneLine(kerr.Error(), 200)
	}
	switch {
	case len(posting) > 0 && len(jd) > 0:
		out.KeywordsSource = "posting+jd_keywords"
	case len(posting) > 0:
		out.KeywordsSource = "posting"
	case len(jd) > 0:
		out.KeywordsSource = "jd_keywords"
	default:
		out.KeywordsSource = "none"
	}

	in := rkbcoverage.Input{Selection: selection, Posting: posting, JD: jd}
	for _, b := range pool.Summary {
		in.Pool = append(in.Pool, rkbcoverage.Bullet{ID: b.ID, Text: b.Text})
	}
	for _, en := range pool.Entries {
		for _, b := range en.Bullets {
			in.Pool = append(in.Pool, rkbcoverage.Bullet{ID: b.ID, Text: b.Text})
		}
	}
	if out.Report, err = rkbcoverage.Compute(in); err != nil {
		if out.Base {
			return fail(jrErr(jrFailed, "jobs resume: the base selection's coverage could not be read: %s", oneLine(err.Error(), 200)))
		}
		return fail(jrErr(jrDamaged, "jobs resume: v%d's stored choice could not be read: %s", want, oneLine(err.Error(), 200)))
	}
	out.GapMissing = gapMissingTerms(job.rec)
	out.pool = in.Pool
	return out, job, nil
}

// gapMissingTerms are the vocabulary terms in a record's gap.missing annotation,
// sorted; the annotation is JD-derived text and is never passed on whole.
func gapMissingTerms(r ledger.Record) []string {
	out := []string{}
	ann := r.Annotations[ledger.AnnMissing]
	if strings.TrimSpace(ann) == "" {
		return out
	}
	v, err := jobinsight.Default()
	if err != nil {
		return out
	}
	for t := range v.Terms(ann) {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ---- gaps ----------------------------------------------------------------------

// resumeGapsReply is the reply of `jobs resume gaps`: the terms handed over, and
// whether a line was written to File. A dry run, a job with nothing to hand over
// and a retry that adds nothing all say written:false and why.
type resumeGapsReply struct {
	Key   string   `json:"key"`
	Terms []string `json:"terms"`
	// Written is true only when a line was added to File.
	Written bool   `json:"written"`
	File    string `json:"file"`
	DryRun  bool   `json:"dry_run,omitempty"`
	// Unchanged says the file's newest line for this job already held these terms.
	Unchanged bool `json:"unchanged,omitempty"`
	// TermsTruncated says there were more terms than one line holds.
	TermsTruncated bool   `json:"terms_truncated,omitempty"`
	Note           string `json:"note,omitempty"`
}

func (e *env) jobsResumeGaps(args []string) error {
	fs := e.newFlags("jobs resume gaps")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	dry := fs.Bool("dry-run", false, "say what would be handed over and write nothing")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("jobs resume gaps: expected one posting key")
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeCoverageDeadline)
	defer dcancel()
	out, err := e.resumeGaps(ctx, fs.Arg(0), *dry)
	return e.resumeEmit(*asJSON, out, err, func() {
		switch {
		case out.Written:
			fmt.Fprintf(e.stdout, "%s: handed over %d terms to %s\n", out.Key, len(out.Terms), out.File)
		default:
			fmt.Fprintf(e.stdout, "%s: %d terms, nothing written", out.Key, len(out.Terms))
			if out.Note != "" {
				fmt.Fprintf(e.stdout, " (%s)", out.Note)
			}
			fmt.Fprintln(e.stdout)
		}
		if len(out.Terms) > 0 {
			fmt.Fprintf(e.stdout, "  %s\n", strings.Join(out.Terms, ", "))
		}
	})
}

// resumeGaps appends the job's uncovered terms and DevPrism's gap.missing terms
// as one line of <state>/resumes/gaps.jsonl. The terms are skills the bank has no
// eligible bullet for, measured on the current version's keywords (the posting's,
// when the job has none), so the owner's career session can work them into the
// bank through rkb's review.
func (e *env) resumeGaps(ctx context.Context, rawKey string, dry bool) (resumeGapsReply, error) {
	cov, job, err := e.resumeCoverage(ctx, rawKey, "")
	if err != nil {
		return resumeGapsReply{}, err
	}
	// The hand-off is "what the bank cannot cover", judged on the posting's
	// keywords: when they could not be read there is nothing to judge, and an empty
	// answer would look like a bank that covers everything.
	if cov.KeywordsError != "" {
		return resumeGapsReply{}, jrErr(jrFailed, "jobs resume: %s, so the gaps cannot be judged and nothing was written", cov.KeywordsError)
	}
	// DevPrism's must-haves are handed over only when no eligible bullet covers them
	// now (its annotation may predate a bullet the bank has since gained).
	gapOnly, gerr := rkbcoverage.PoolUncovered(cov.pool, cov.GapMissing)
	if gerr != nil {
		return resumeGapsReply{}, jrErr(jrFailed, "jobs resume: %s", oneLine(gerr.Error(), 200))
	}
	out := resumeGapsReply{Key: cov.Key, Terms: []string{}, File: filepath.Join(e.stateDir, "resumes", "gaps.jsonl"), DryRun: dry}
	seen := map[string]bool{}
	for _, t := range append(append([]string{}, cov.Uncovered...), gapOnly...) {
		t = oneLine(t, maxGapTermRunes)
		if k := strings.ToLower(t); t != "" && !seen[k] {
			seen[k] = true
			out.Terms = append(out.Terms, t)
		}
	}
	if len(out.Terms) > maxGapTerms {
		out.Terms, out.TermsTruncated = out.Terms[:maxGapTerms], true
	}
	line := rkbcoverage.Line{
		At: now().UTC().Format(time.RFC3339), Key: cov.Key,
		Company: oneLine(job.rec.Posting.Company, 120), Title: oneLine(job.rec.Posting.Title, 120), Terms: out.Terms,
	}
	line, _, dropped, ferr := line.Fit()
	if ferr != nil {
		return resumeGapsReply{}, jrErr(jrFailed, "jobs resume: %s", oneLine(ferr.Error(), 200))
	}
	out.Terms, out.TermsTruncated = line.Terms, out.TermsTruncated || dropped
	switch {
	case len(out.Terms) == 0:
		out.Note = "no eligible bullet is missing a term, and DevPrism named none: nothing to hand over"
		return out, nil
	case dry:
		out.Note = "dry run"
		return out, nil
	}
	unchanged, aerr := rkbcoverage.AppendHandoff(ctx, out.File, line)
	switch {
	case errors.Is(aerr, rkbcoverage.ErrHandoffBusy):
		return resumeGapsReply{}, jrErr(rkbresume.CodeBusy, "jobs resume: another process is writing the gaps file; try again")
	case aerr != nil && (errors.Is(aerr, context.DeadlineExceeded) || errors.Is(aerr, context.Canceled)):
		return resumeGapsReply{}, aerr
	case aerr != nil:
		return resumeGapsReply{}, jrErr(jrStore, "jobs resume: %s", oneLine(aerr.Error(), 300))
	}
	out.Written, out.Unchanged = !unchanged, unchanged
	if unchanged {
		out.Note = "the file's newest line for this job already holds these terms"
	}
	return out, nil
}
