package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/cover"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/letterstore"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// jobsCoverSaved is a build that kept a version. Body text and letterhead are
// not in the reply. PDF is false when drawing failed and the text was kept.
type jobsCoverSaved struct {
	Key          string `json:"key"`
	Version      int    `json:"version"`
	Words        int    `json:"words,omitempty"`
	CompanyCheck string `json:"company_check,omitempty"`
	Note         string `json:"note,omitempty"`
	PDF          bool   `json:"pdf"`
	Bytes        int    `json:"bytes,omitempty"`
	TextSaved    bool   `json:"text_saved,omitempty"`
	Error        string `json:"error,omitempty"`
	Code         string `json:"code,omitempty"`
}

// jobsCoverUsed is a version the extension may fetch. Paths are under the
// letters directory. The reply has no letter text and no letterhead.
type jobsCoverUsed struct {
	Key      string `json:"key"`
	Version  int    `json:"version"`
	PDF      bool   `json:"pdf"`
	Bytes    int    `json:"bytes,omitempty"`
	TextPath string `json:"text_path,omitempty"`
	PDFPath  string `json:"pdf_path,omitempty"`
	Error    string `json:"error,omitempty"`
	Code     string `json:"code,omitempty"`
}

func (e *env) jobsCover(args []string) error {
	if len(args) == 0 {
		return usagef("jobs cover: expected build or use")
	}
	switch args[0] {
	case "build":
		return e.jobsCoverBuild(args[1:])
	case "use":
		return e.jobsCoverUse(args[1:])
	default:
		return usagef("jobs cover: unknown subcommand %q (expected build or use)", args[0])
	}
}

func (e *env) jobsCoverBuild(args []string) error {
	fs := e.newFlags("jobs cover build")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("jobs cover build: expected one posting key")
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, 3*time.Minute)
	defer dcancel()
	out, err := e.coverBuild(ctx, fs.Arg(0))
	if !*asJSON {
		if err != nil && out.Version == 0 {
			return err
		}
		if err != nil {
			fmt.Fprintf(e.stdout, "saved v%d; the PDF was not drawn\n", out.Version)
			return err
		}
		fmt.Fprintf(e.stdout, "%s: v%d saved\n", out.Key, out.Version)
		return nil
	}
	if err != nil {
		out.Error = err.Error()
		if out.Code == "" {
			out.Code = cover.CodeOf(err)
		}
		if out.Code == "" {
			var de *draft.Error
			if errors.As(err, &de) {
				out.Code = de.Code
			}
		}
		if out.Code == "" {
			out.Code = "failed"
		}
		return e.writeReply(out, err)
	}
	return e.writeReply(out, nil)
}

func (e *env) coverBuild(ctx context.Context, rawKey string) (jobsCoverSaved, error) {
	job, err := e.coverPosting(rawKey)
	if err != nil {
		return jobsCoverSaved{}, err
	}
	if strings.TrimSpace(job.Description) == "" {
		return jobsCoverSaved{Code: "no_text"}, fmt.Errorf("cover: the posting has no text")
	}
	w, code, err := e.coverConfig(ctx)
	if err != nil {
		if code == "" {
			code = codeOfCover(err)
		}
		return jobsCoverSaved{Code: code}, err
	}
	head := cover.LetterheadLines(w.BaseSelection, w.Bank)
	letter, err := cover.Write(ctx, w.Drafter, draft.Job{Company: job.Company, Title: job.Title, Description: job.Description}, head)
	if err != nil && !cover.Kept(letter, err) {
		return jobsCoverSaved{}, err
	}
	st, serr := e.letterStore()
	if serr != nil {
		return jobsCoverSaved{}, serr
	}
	meta, serr := st.Save(ctx, string(job.Key), letter.Body, letter.CompanyCheck, letter.PDF)
	if serr != nil {
		return jobsCoverSaved{}, serr
	}
	out := jobsCoverSaved{
		Key:          string(job.Key),
		Version:      meta.Version,
		Words:        meta.Words,
		CompanyCheck: meta.CompanyCheck,
		PDF:          meta.PDFSHA256 != "",
		Bytes:        meta.PDFBytes,
		TextSaved:    true,
	}
	if out.CompanyCheck == draft.CompanySkipped {
		out.Note = draft.CompanySkippedNote
	}
	if err != nil {
		out.Code = cover.CodeRender
		out.Error = err.Error()
		return out, err
	}
	return out, nil
}

func (e *env) jobsCoverUse(args []string) error {
	fs := e.newFlags("jobs cover use")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return usagef("jobs cover use: expected KEY [vN]")
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	defer dcancel()
	ver := 0
	if fs.NArg() == 2 {
		n, err := parseCoverVersion(fs.Arg(1))
		if err != nil {
			return err
		}
		ver = n
	}
	out, err := e.coverUse(ctx, fs.Arg(0), ver)
	if !*asJSON {
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "%s: v%d is now the letter to apply with\n", out.Key, out.Version)
		return nil
	}
	if err != nil {
		out.Error = err.Error()
		out.Code = letterstore.CodeOf(err)
		if out.Code == "" {
			out.Code = "failed"
		}
		return e.writeReply(out, err)
	}
	return e.writeReply(out, nil)
}

func (e *env) coverUse(ctx context.Context, rawKey string, n int) (jobsCoverUsed, error) {
	job, err := e.coverPosting(rawKey)
	if err != nil {
		return jobsCoverUsed{}, err
	}
	st, err := e.letterStore()
	if err != nil {
		return jobsCoverUsed{}, err
	}
	if n == 0 {
		cur, cerr := st.Current(string(job.Key))
		if cerr != nil {
			return jobsCoverUsed{}, cerr
		}
		n = cur.Version
	}
	meta, err := st.Use(ctx, string(job.Key), n)
	if err != nil {
		return jobsCoverUsed{}, err
	}
	name, err := letterstore.Folder(string(job.Key))
	if err != nil {
		return jobsCoverUsed{}, err
	}
	dir := filepath.Join(letterstore.Dir(e.stateDir), name)
	out := jobsCoverUsed{
		Key:      string(job.Key),
		Version:  meta.Version,
		PDF:      meta.PDFSHA256 != "",
		Bytes:    meta.PDFBytes,
		TextPath: filepath.Join(dir, fmt.Sprintf("v%d.txt", meta.Version)),
	}
	if meta.PDFSHA256 != "" {
		out.PDFPath = filepath.Join(dir, fmt.Sprintf("v%d.pdf", meta.Version))
	}
	return out, nil
}

func (e *env) coverPosting(rawKey string) (posting.Posting, error) {
	if strings.TrimSpace(rawKey) == "" || strings.HasPrefix(rawKey, "-") {
		return posting.Posting{}, fmt.Errorf("cover: the posting key is not usable")
	}
	k := posting.Key(rawKey)
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), rowScope(func(ledger.Snapshot) (posting.Key, bool) { return k, true }))
	if err != nil {
		return posting.Posting{}, fmt.Errorf("cover: read the ledger: %s", oneLine(err.Error(), 200))
	}
	r, ok := findRecord(snap, k)
	if !ok {
		return posting.Posting{}, fmt.Errorf("cover: no posting %q in the ledger", k)
	}
	p := r.Posting
	p.Key = k
	return p, nil
}

func (e *env) letterStore() (*letterstore.Store, error) {
	if !filepath.IsAbs(e.stateDir) {
		return nil, fmt.Errorf("cover: the state directory must be an absolute path")
	}
	return letterstore.Open(e.stateDir)
}

func parseCoverVersion(s string) (int, error) {
	s = strings.TrimPrefix(s, "v")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > letterstore.MaxVersions {
		return 0, fmt.Errorf("cover: %q is not a version (v1 to v%d)", oneLine(s, 40), letterstore.MaxVersions)
	}
	return n, nil
}

func codeOfCover(err error) string {
	if c := letterstore.CodeOf(err); c != "" {
		return c
	}
	if c := cover.CodeOf(err); c != "" {
		return c
	}
	var de *draft.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}
