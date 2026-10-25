package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// `jobs resume show` is what the Studio opens a job with; `jobs resume use`
// binds a version for applying. Neither asks a model or spends anything.

// resumeShown is the reply of `jobs resume show`.
type resumeShown struct {
	Key string `json:"key"`
	// EligibilityChecked is false under --no-check: the versions are read from the
	// store alone, each version's own checks.eligibility is what was true when it
	// was built, and no pool is given. When true, each version's top-level
	// "eligibility" is the fresh verdict (current, stale or unknown).
	EligibilityChecked bool `json:"eligibility_checked"`
	Current            int  `json:"current,omitempty"`
	Used               int  `json:"used,omitempty"`
	// Version is the version whose full choice is in Versions (the one asked for,
	// else the current one); absent when the job has none.
	Version  int             `json:"version,omitempty"`
	Versions []resumeVersion `json:"versions"`
	// VersionsError is set when the store could not be read whole: Versions then
	// holds what read, never a claim that it is everything.
	VersionsError string `json:"versions_error,omitempty"`
	// CheckError is why the fresh check could not run; each version is then
	// "unknown", and there is no pool.
	CheckError string `json:"check_error,omitempty"`
	// Pool is what the editor may place, as the bank stands now.
	Pool *rkbresume.PoolView `json:"pool,omitempty"`
	// Keywords are the posting's, from the same data `jobs show` reports.
	Keywords      []jobinsight.Keyword `json:"keywords,omitempty"`
	KeywordsError string               `json:"keywords_error,omitempty"`
}

func (e *env) jobsResumeShow(args []string) error {
	fs := e.newFlags("jobs resume show")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	noCheck := fs.Bool("no-check", false, "read the stored versions only: no fresh staleness check and no pool (eligibility_checked is false)")
	if err := parseResumeArgs(fs, args, "show", 1, 2); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	d := resumeShowDeadline
	if *noCheck {
		d = resumeShowStoredDeadline
	}
	ctx, dcancel := context.WithTimeout(ctx, d)
	defer dcancel()
	out, err := e.resumeShow(ctx, fs.Arg(0), fs.Arg(1), !*noCheck)
	return e.resumeEmit(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: %d versions", out.Key, len(out.Versions))
		if out.Current != 0 {
			fmt.Fprintf(e.stdout, ", current v%d", out.Current)
		}
		if out.Used != 0 {
			fmt.Fprintf(e.stdout, ", used v%d", out.Used)
		}
		fmt.Fprintln(e.stdout)
		for _, v := range out.Versions {
			el := ""
			if v.Eligibility != nil {
				el = ", " + v.Eligibility.State
			}
			fmt.Fprintf(e.stdout, "  v%d  %s  fit %s%s\n", v.Version, v.Source, v.Checks.Fit, el)
		}
		for _, w := range []string{out.VersionsError, out.CheckError, out.KeywordsError} {
			if w != "" {
				fmt.Fprintf(e.stdout, "note: %s\n", w)
			}
		}
	})
}

func (e *env) resumeShow(ctx context.Context, rawKey, rawVersion string, check bool) (resumeShown, error) {
	job, jerr := e.resumeJobFor(rawKey, false)
	if jerr != nil {
		return resumeShown{}, jerr
	}
	want := 0
	if rawVersion != "" {
		n, perr := parseVersion(rawVersion)
		if perr != nil {
			return resumeShown{}, perr
		}
		want = n
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeShown{}, serr
	}
	out := resumeShown{Key: string(job.key), EligibilityChecked: check, Versions: []resumeVersion{}}

	var damage []string
	metas, lerr := st.List(string(job.key))
	if lerr != nil && !errors.Is(lerr, resumestore.ErrNotFound) {
		damage = append(damage, oneLine(lerr.Error(), 300))
	}
	sum, err := st.Summary(string(job.key))
	if err != nil {
		damage = append(damage, oneLine(err.Error(), 300))
	} else {
		if sum.Current != nil {
			out.Current = sum.Current.Version
		}
		if sum.Used != nil {
			out.Used = sum.Used.Version
		}
	}
	out.VersionsError = strings.Join(damage, "; ")
	if want == 0 {
		want = out.Current
	}
	found := false
	for _, m := range metas {
		found = found || m.Version == want
	}
	if rawVersion != "" && !found { // say why, as the store does (gone, or damaged)
		if _, _, err := st.Get(string(job.key), want); err != nil {
			return resumeShown{}, storeError(err)
		}
		return resumeShown{}, jrErr(jrNotFound, "jobs resume: %s has no v%d", job.key, want)
	}

	var pool rkbresume.PoolView
	var bank map[string]bool
	if check {
		w, err := e.resumeConfig(ctx, false)
		if err == nil {
			pool, bank, err = e.freshPool(ctx, w)
		}
		if err != nil {
			out.CheckError = oneLine(asResumeError(err).msg, 300)
		} else {
			out.Pool = &pool
		}
	}
	for _, m := range metas {
		v := resumeVersion{Meta: m, Current: m.Version == out.Current, Used: m.Version == out.Used}
		v.Selection = nil
		if m.Version == want && found {
			v.Selection = m.Selection
			out.Version = m.Version
		}
		if check {
			el := unknownEligibility("the bank could not be read: " + out.CheckError)
			if out.Pool != nil {
				el = checkEligibility(m, pool, bank)
			}
			v.Eligibility = &el
		}
		out.Versions = append(out.Versions, v)
	}

	kw, kerr := withContext(ctx, func() ([]jobinsight.Keyword, error) {
		j, _, ok, err := e.showJob(job.key)
		if err != nil || !ok {
			return nil, err
		}
		return j.Keywords, nil
	})
	if kerr != nil {
		out.KeywordsError = "the posting's keywords could not be read: " + oneLine(kerr.Error(), 200)
	}
	out.Keywords = kw
	return out, nil
}

// withContext runs f and returns its result, or ctx's error when ctx ends first
// (f is not interruptible; the process ends soon after, and its result is dropped).
func withContext[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// ---- use -----------------------------------------------------------------

// resumeUsed is the reply of `jobs resume use`: the version now bound for
// applying and the absolute path of its PDF (0600), under the name the page shows.
type resumeUsed struct {
	Key       string `json:"key"`
	Version   int    `json:"version"`
	PDFPath   string `json:"pdf_path"`
	Name      string `json:"name"`
	PDFSHA256 string `json:"pdf_sha256"`
}

func (e *env) jobsResumeUse(args []string) error {
	fs := e.newFlags("jobs resume use")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	if err := parseResumeArgs(fs, args, "use", 2); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeUseDeadline)
	defer dcancel()
	out, err := e.resumeUse(ctx, fs.Arg(0), fs.Arg(1))
	return e.resumeEmit(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: v%d is now the version to apply with\npdf: %s\n", out.Key, out.Version, out.PDFPath)
	})
}

// resumeUse binds version vN for applying, after checking it afresh: a stale
// version is refused (stale), one that could not be checked is refused too
// (unchecked), and nothing is bound on a guess.
func (e *env) resumeUse(ctx context.Context, rawKey, rawVersion string) (resumeUsed, error) {
	n, perr := parseVersion(rawVersion)
	if perr != nil {
		return resumeUsed{}, perr
	}
	job, jerr := e.resumeJobFor(rawKey, false)
	if jerr != nil {
		return resumeUsed{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeUsed{}, serr
	}
	meta, _, err := st.Get(string(job.key), n)
	if err != nil {
		return resumeUsed{}, storeError(err)
	}
	w, err := e.resumeConfig(ctx, false)
	if err != nil {
		return resumeUsed{}, err
	}
	pool, bank, err := e.freshPool(ctx, w)
	if err != nil {
		return resumeUsed{}, uncheckedError(err)
	}
	el := checkEligibility(meta, pool, bank)
	switch el.State {
	case eligCurrent:
	case eligStale:
		re := jrErr(jrStale, "jobs resume: v%d is stale (%s); revalidate it, or build again", n, staleSummary(el))
		re.elig = &el
		return resumeUsed{}, re
	default:
		re := jrErr(jrUnchecked, "jobs resume: v%d could not be checked against the bank, so it is not used", n)
		re.elig = &el
		return resumeUsed{}, re
	}
	used, err := st.Use(ctx, string(job.key), n)
	if err != nil {
		return resumeUsed{}, storeError(err)
	}
	path, err := st.PDFPath(string(job.key), used.Version)
	if err != nil {
		return resumeUsed{}, storeError(err)
	}
	return resumeUsed{Key: string(job.key), Version: used.Version, PDFPath: path, Name: resumeFileName(job.rec.Posting.Company), PDFSHA256: used.PDFSHA256}, nil
}

// staleSummary is a stale verdict in a few words (ids and counts, no text).
func staleSummary(el resumeEligibility) string {
	var parts []string
	for _, p := range []struct {
		what string
		ids  []string
	}{{"reworded", el.Reworded}, {"gone", el.Gone}, {"ineligible", el.Ineligible}} {
		if len(p.ids) > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", len(p.ids), p.what))
		}
	}
	if len(parts) == 0 {
		return el.Why
	}
	return strings.Join(parts, ", ")
}
