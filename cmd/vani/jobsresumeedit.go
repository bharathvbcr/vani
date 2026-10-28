package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// `jobs resume render` builds the resume the owner edited, with no model, and
// `jobs resume revalidate` rebuilds a version without what the bank stopped
// vouching for. Both go through rkbresume.Render, which holds the owner's edit
// to the same page-safety rules as the model's choice; neither can spend money.

const (
	// maxResumeEdit caps the owner's edit read from stdin (rkbresume's own cap).
	maxResumeEdit = resumestore.MaxSelectionBytes
	// maxPreviewHTML bounds the page copied out of a build folder (rkb's own bound).
	maxPreviewHTML = 4 << 20
	// maxRevalidateReason keeps a revalidation's reason well under the store's bound.
	maxRevalidateReason = 1500
)

// previewMaxAge is how long an unfitted render's page stays in the work folder
// before the next render removes it; the app reads it at once.
var previewMaxAge = time.Hour

var previewName = regexp.MustCompile(`^preview-[0-9]+\.html$`)

// resumeRendered is the reply of a render without --fit: a page to look at, never
// a version. Saved is always false and Checks.Fit always "not_run": nothing here
// reads as a fitted or usable page.
type resumeRendered struct {
	Key      string                  `json:"key"`
	Saved    bool                    `json:"saved"`
	HTMLPath string                  `json:"html_path"`
	Checks   resumestore.Checks      `json:"checks"`
	Notes    []string                `json:"notes,omitempty"`
	Keywords []string                `json:"keywords,omitempty"`
	Bullets  []resumestore.BulletRef `json:"bullets"`
}

func (e *env) jobsResumeRender(args []string) error {
	fs := e.newFlags("jobs resume render")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout ({error,code} on failure)")
	fit := fs.Bool("fit", false, "also fit the page in Chrome and save it as a version (without it nothing is saved and the page is rkb's screen HTML, layout not measured)")
	from := fs.String("from", "", "with --fit: the version the edit was made from (v3), default the current one")
	if err := parseResumeArgs(fs, args, "render", 1); err != nil {
		return err
	}
	if *from != "" && !*fit {
		return usagef("jobs resume render: --from needs --fit (a render without it saves nothing)")
	}
	ctx, cancel := signalContext()
	defer cancel()
	var out any
	var err error
	if *fit {
		out, err = e.resumeRenderFit(ctx, fs.Arg(0), *from)
	} else {
		out, err = e.resumeRenderPreview(ctx, fs.Arg(0))
	}
	return e.resumeEmit(*asJSON, out, err, func() {
		switch v := out.(type) {
		case resumeRendered:
			fmt.Fprintf(e.stdout, "%s: rendered, layout not fitted, nothing saved\nhtml: %s\n", v.Key, v.HTMLPath)
		case resumeSaved:
			printSaved(e, v)
		}
	})
}

// readEdit reads the owner's edit from stdin, at most maxResumeEdit bytes. Its
// shape is Render's to judge (bad_edit); only its size is checked here.
func readEdit() ([]byte, *resumeError) {
	raw, err := io.ReadAll(io.LimitReader(resumeInput, maxResumeEdit+1))
	if err != nil {
		return nil, jrErr(jrBadRequest, "jobs resume: read the edit: %s", oneLine(err.Error(), 200))
	}
	if len(raw) > maxResumeEdit {
		return nil, jrErr(rkbresume.CodeEdit, "jobs resume: the edit is over %d KiB", maxResumeEdit>>10)
	}
	return raw, nil
}

// renderPrep is what both renders share: the job, the store, the edit and the
// config (no model).
func (e *env) renderPrep(ctx context.Context, rawKey string) (resumeJob, *resumestore.Store, []byte, resumeWiring, error) {
	job, jerr := e.resumeJobFor(rawKey, true)
	if jerr != nil {
		return resumeJob{}, nil, nil, resumeWiring{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeJob{}, nil, nil, resumeWiring{}, serr
	}
	edit, eerr := readEdit()
	if eerr != nil {
		return resumeJob{}, nil, nil, resumeWiring{}, eerr
	}
	w, err := e.resumeConfig(ctx, false)
	if err != nil {
		return resumeJob{}, nil, nil, resumeWiring{}, err
	}
	return job, st, edit, w, nil
}

// resumeRenderPreview renders the edit without fitting it: no version is saved,
// the page is copied to a 0600 file under the work folder and its path returned.
func (e *env) resumeRenderPreview(ctx context.Context, rawKey string) (resumeRendered, error) {
	job, _, edit, w, err := e.renderPrep(ctx, rawKey)
	if err != nil {
		return resumeRendered{}, err
	}
	res, err := rkbresume.Render(ctx, w.RC, job.req, edit, false)
	if res.BuildDir != "" {
		defer os.RemoveAll(res.BuildDir) // the page in it holds the owner's header
	}
	if err != nil {
		return resumeRendered{}, err
	}
	path, perr := e.keepPreview(res)
	if perr != nil {
		return resumeRendered{}, perr
	}
	checks := resumestore.Checks(res.Checks)
	checks.Fit = "not_run" // whatever rkb said, an unmeasured page is never "fitted"
	return resumeRendered{Key: string(job.key), HTMLPath: path, Checks: checks, Notes: res.Notes, Keywords: res.Keywords, Bullets: bulletRefs(res.Bullets)}, nil
}

func bulletRefs(in []rkbresume.BulletRef) []resumestore.BulletRef {
	out := make([]resumestore.BulletRef, len(in))
	for i, b := range in {
		out[i] = resumestore.BulletRef(b)
	}
	return out
}

// keepPreview copies rkb's page out of its build folder to preview-N.html in the
// work folder (0600), after removing previews older than previewMaxAge.
func (e *env) keepPreview(res rkbresume.Result) (string, *resumeError) {
	dir := filepath.Join(e.stateDir, "resumes", ".tmp")
	if res.BuildDir == "" || res.HTMLPath == "" || !strings.HasPrefix(res.HTMLPath, filepath.Clean(res.BuildDir)+string(filepath.Separator)) {
		return "", jrErr(rkbresume.CodeRender, "jobs resume: rkb left no page to show")
	}
	f, err := os.Open(res.HTMLPath)
	if err != nil {
		return "", jrErr(rkbresume.CodeRender, "jobs resume: the rendered page could not be read")
	}
	html, err := io.ReadAll(io.LimitReader(f, maxPreviewHTML+1))
	_ = f.Close()
	if err != nil || len(html) == 0 || len(html) > maxPreviewHTML {
		return "", jrErr(rkbresume.CodeRender, "jobs resume: the rendered page is empty or over %d MiB", maxPreviewHTML>>20)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", jrErr(jrStore, "jobs resume: the preview folder could not be made: %s", oneLine(err.Error(), 200))
	}
	sweepPreviews(dir, time.Now())
	out, err := os.CreateTemp(dir, "preview-*.html") // 0600, a random number in the name
	if err != nil {
		return "", jrErr(jrStore, "jobs resume: the preview could not be written: %s", oneLine(err.Error(), 200))
	}
	if _, err := out.Write(html); err != nil {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return "", jrErr(jrStore, "jobs resume: the preview could not be written: %s", oneLine(err.Error(), 200))
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(out.Name())
		return "", jrErr(jrStore, "jobs resume: the preview could not be written: %s", oneLine(err.Error(), 200))
	}
	return out.Name(), nil
}

// sweepPreviews removes the preview pages in dir older than previewMaxAge. It
// reads at most 1000 names and never fails a render: it only makes room.
func sweepPreviews(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for i, ent := range ents {
		if i >= 1000 {
			return
		}
		if !ent.Type().IsRegular() || !previewName.MatchString(ent.Name()) {
			continue
		}
		if info, err := ent.Info(); err == nil && now.Sub(info.ModTime()) > previewMaxAge {
			_ = os.Remove(filepath.Join(dir, ent.Name()))
		}
	}
}

// resumeRenderFit renders the edit, fits it in Chrome and saves it as a version
// made from `from` (default the current one).
func (e *env) resumeRenderFit(ctx context.Context, rawKey, from string) (resumeSaved, error) {
	job, st, edit, w, err := e.renderPrep(ctx, rawKey)
	if err != nil {
		return resumeSaved{}, err
	}
	parent, perr := e.parentFor(st, job.key, from)
	if perr != nil {
		return resumeSaved{}, perr
	}
	res, err := rkbresume.Render(ctx, w.RC, job.req, edit, true)
	if err != nil {
		return resumeSaved{}, e.paidFailure(ctx, st, job.key, res, err)
	}
	if err := checkBuilt(res); err != nil {
		return resumeSaved{}, asResumeError(err)
	}
	out, serr := e.saveVersion(ctx, st, job.key, res, parent, "", "")
	if serr != nil {
		return resumeSaved{}, serr
	}
	e.noteCurrent(st, job.key, &out, parent)
	return out, nil
}

// parentFor is the version an edit or revalidation is made from: from ("v3"),
// which must exist, else the job's current version (0 when it has none).
func (e *env) parentFor(st *resumestore.Store, key posting.Key, from string) (int, *resumeError) {
	if from == "" {
		return currentOf(st, key)
	}
	n, perr := parseVersion(from)
	if perr != nil {
		return 0, perr
	}
	if _, _, err := st.Get(string(key), n); err != nil {
		return 0, storeError(err)
	}
	return n, nil
}

// noteCurrent says why a saved version did not become current.
func (e *env) noteCurrent(st *resumestore.Store, key posting.Key, out *resumeSaved, parent int) {
	if out.CurrentMoved {
		return
	}
	if cur, err := currentOf(st, key); err == nil && cur != 0 {
		out.CurrentNote = fmt.Sprintf("v%d stays current: this version was made from v%d", cur, parent)
	}
}

// ---- revalidate ----------------------------------------------------------

func (e *env) jobsResumeRevalidate(args []string) error {
	fs := e.newFlags("jobs resume revalidate")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout ({error,code} on failure)")
	if err := parseResumeArgs(fs, args, "revalidate", 2); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeRevalidateDeadline)
	defer dcancel()
	out, err := e.resumeRevalidate(ctx, fs.Arg(0), fs.Arg(1))
	return e.resumeEmit(*asJSON, out, err, func() {
		printSaved(e, out)
		fmt.Fprintf(e.stdout, "dropped: %s\n", orDash(strings.Join(out.DroppedStale, ", ")))
	})
}

// resumeRevalidate drops what the bank stopped vouching for from version vN's
// choice and rebuilds it (no model), saving the result as a "revalidated" version
// made from vN. A version that is not stale is refused (not_stale); one whose
// freshness could not be checked is refused too (unchecked).
func (e *env) resumeRevalidate(ctx context.Context, rawKey, rawVersion string) (resumeSaved, error) {
	n, perr := parseVersion(rawVersion)
	if perr != nil {
		return resumeSaved{}, perr
	}
	job, jerr := e.resumeJobFor(rawKey, true)
	if jerr != nil {
		return resumeSaved{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeSaved{}, serr
	}
	meta, _, err := st.Get(string(job.key), n)
	if err != nil {
		return resumeSaved{}, storeError(err)
	}
	w, err := e.resumeConfig(ctx, false)
	if err != nil {
		return resumeSaved{}, err
	}
	fc, err := e.freshCheck(ctx, w, []resumestore.Meta{meta})
	if err != nil {
		return resumeSaved{}, uncheckedError(err)
	}
	pool := fc.pool
	el := fc.eligibility(0, meta)
	switch el.State {
	case eligCurrent:
		re := jrErr(jrNotStale, "jobs resume: v%d is still vouched for; there is nothing to revalidate", n)
		re.elig = &el
		return resumeSaved{}, re
	case eligUnknown: // a verdict that was not reached is not a reason to rebuild
		re := jrErr(jrUnchecked, "jobs resume: v%d could not be checked against the bank, so it is not revalidated (%s)", n, oneLine(el.Why, 200))
		re.elig = &el
		return resumeSaved{}, re
	}
	drop := el.staleIDs()
	extendWithContinuations(drop, meta, pool)
	edit, err := dropBullets(meta.Selection, drop)
	if err != nil {
		return resumeSaved{}, jrErr(jrDamaged, "jobs resume: v%d's choice cannot be read: %s", n, oneLine(err.Error(), 200))
	}
	res, err := rkbresume.Render(ctx, w.RC, job.req, edit, true)
	if err != nil {
		return resumeSaved{}, e.paidFailure(ctx, st, job.key, res, err)
	}
	if err := checkBuilt(res); err != nil {
		return resumeSaved{}, asResumeError(err)
	}
	dropped := droppedInOrder(meta, drop)
	out, serr := e.saveVersion(ctx, st, job.key, res, n, "revalidated", revalidationReason(n, dropped, el))
	if serr != nil {
		return resumeSaved{}, serr
	}
	out.DroppedStale = dropped
	e.noteCurrent(st, job.key, &out, n)
	return out, nil
}

// uncheckedError is the refusal for a version whose freshness could not be
// checked: a check that did not run is never read as a pass.
func uncheckedError(err error) *resumeError {
	re := asResumeError(err)
	switch re.code {
	case rkbresume.CodeTimeout, rkbresume.CodeCancel:
		return re
	}
	return jrErr(jrUnchecked, "jobs resume: the version could not be checked against the bank (%s: %s)", re.code, oneLine(re.msg, 200))
}

// extendWithContinuations adds to drop every bullet of the version that
// continues a dropped one (the pool says which bullet each continuation must
// directly follow): a continuation cannot stand without its lead.
func extendWithContinuations(drop map[string]bool, m resumestore.Meta, pool rkbresume.PoolView) {
	idx := poolIndex(pool)
	for changed := true; changed; {
		changed = false
		for _, b := range m.Bullets {
			if pb, ok := idx[b.ID]; ok && !drop[b.ID] && pb.Follows != "" && drop[pb.Follows] {
				drop[b.ID] = true
				changed = true
			}
		}
	}
}

// droppedInOrder lists the version's bullets that are dropped, in page order.
func droppedInOrder(m resumestore.Meta, drop map[string]bool) []string {
	var out []string
	for _, b := range m.Bullets {
		if drop[b.ID] && !slices.Contains(out, b.ID) {
			out = append(out, b.ID)
		}
	}
	return out
}

func revalidationReason(n int, dropped []string, el resumeEligibility) string {
	var s string
	if len(dropped) == 0 {
		s = fmt.Sprintf("revalidated from v%d: no bullet dropped (%s)", n, orDash(el.Why))
	} else {
		s = fmt.Sprintf("revalidated from v%d: dropped %s", n, strings.Join(dropped, ", "))
	}
	if len(s) > maxRevalidateReason {
		s = strings.ToValidUTF8(s[:maxRevalidateReason], "") + "..."
	}
	return s
}

// dropBullets is sel (a version's choice, the owner-edit shape) without the
// bullets in drop: every other key of every section and entry (titles, overrides,
// keywords) is carried through untouched, and an entry left with no bullet stays
// as a heading with an empty list. Whether the result is a page rkb can build is
// Render's to say.
func dropBullets(sel json.RawMessage, drop map[string]bool) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(sel, &top); err != nil || top == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	var secs []map[string]json.RawMessage
	if raw, ok := top["sections"]; ok {
		if err := json.Unmarshal(raw, &secs); err != nil {
			return nil, fmt.Errorf("sections are not a list of objects")
		}
	}
	keep := func(raw json.RawMessage) ([]string, error) {
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if !drop[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}
	for _, s := range secs {
		if raw, ok := s["bullets"]; ok {
			ids, err := keep(raw)
			if err != nil {
				return nil, fmt.Errorf("a section's bullets are not a list of ids")
			}
			if len(ids) == 0 {
				delete(s, "bullets")
			} else {
				s["bullets"], _ = json.Marshal(ids)
			}
		}
		if raw, ok := s["entries"]; ok {
			var ents []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &ents); err != nil {
				return nil, fmt.Errorf("a section's entries are not a list of objects")
			}
			for _, en := range ents {
				raw, ok := en["bullets"]
				if !ok {
					continue
				}
				ids, err := keep(raw)
				if err != nil {
					return nil, fmt.Errorf("an entry's bullets are not a list of ids")
				}
				en["bullets"], _ = json.Marshal(ids)
			}
			s["entries"], _ = json.Marshal(ents)
		}
	}
	if secs != nil {
		top["sections"], _ = json.Marshal(secs)
	}
	return json.Marshal(top)
}
