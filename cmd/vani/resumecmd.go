package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// `vani resume` builds a resume for one job with rkb, the owner's resume tool
// (internal/rkbresume). rkb renders the owner's own template from the bank and
// the selection in kb.base_selection; DevPrism no longer builds anything, it
// only ranks postings. One bounded model turn on the configured model chooses
// which eligible bullets go on the page; with no provider, no budget or an
// invalid answer the base selection is built instead, and the reply's
// "selection" and "reason" say which. The Vani Apply agent runs it (job kind
// "resume") when the owner accepts the offer to build one for a form that has
// no resume chosen; the extension then binds the PDF to the job page and
// attaches it.
//
// The request still carries a header (name and contact lines, the owner's "Your
// basics" in Vani Apply) and it is still checked, so a bad request is refused,
// but the PDF no longer needs it: rkb prints the header of the base selection
// from the bank. The request's header is never sent to a model, written to a
// file, logged or returned.

const (
	// maxResumeInput caps the request read from stdin: a long posting plus the header.
	maxResumeInput = 128 << 10
	// maxResumePDF is ResumeStore.maxBytes (macos) and MAX_RESUME_BYTES (extension):
	// a larger PDF could not cross Chrome's 1 MiB native-messaging reply.
	maxResumePDF    = 512 << 10
	maxHeaderName   = 200
	maxContactLine  = 200
	maxContactLines = 4
)

// Reply codes of `vani resume` (rkbresume's, plus the request's).
const (
	codeNoName   = "no_name"
	codeRender   = rkbresume.CodeRender
	codeTooLarge = rkbresume.CodeTooLarge
	codePages    = rkbresume.CodePages
)

// resumeInput is where `vani resume` reads its request. Tests replace it.
var resumeInput io.Reader = os.Stdin

// resumeRunner runs rkb; nil is rkbresume's ExecRunner. Tests replace it.
var resumeRunner rkbresume.Runner

// resumeProvider builds the metered provider for the model turn; it is the
// draft path's, so decide.daily_usd.llm caps both together. Tests replace it.
var resumeProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return draftProvider(ctx, w) }

type resumeRequest struct {
	Job struct {
		Company     string `json:"company"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"job"`
	Header struct {
		Name         string   `json:"name"`
		ContactLines []string `json:"contact_lines"`
	} `json:"header"`
}

// resumeReply is the one JSON object `vani resume` writes.
type resumeReply struct {
	PDF         string  `json:"pdf,omitempty"`
	Name        string  `json:"name,omitempty"`
	Bytes       int     `json:"bytes,omitempty"`
	Pages       int     `json:"pages,omitempty"`
	ATSCoverage float64 `json:"ats_coverage_pct,omitempty"`
	// Selection is "tailored" (the model's choice was built) or "base" (the
	// owner's base selection); Reason says why base. UnverifiedDropped lists
	// bullet ids left off because rkb has not double-verified a figure in them.
	Selection         string   `json:"selection,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	UnverifiedDropped []string `json:"unverified_dropped,omitempty"`
	// Notes are layout flaws accepted on the base selection, as rkb reported them.
	Notes []string `json:"notes,omitempty"`
	Error string   `json:"error,omitempty"`
	Code  string   `json:"code,omitempty"`
}

// codedError carries a reply code with an error message that holds no personal data.
type codedError struct {
	code string
	msg  string
}

func (e *codedError) Error() string { return e.msg }

func coded(code, format string, a ...any) error {
	return &codedError{code: code, msg: fmt.Sprintf(format, a...)}
}

// cmdResume reads one request (JSON) on stdin and writes one reply (JSON) on
// stdout: {"pdf","name","bytes","pages","selection","reason"} or {"error","code"}.
func (e *env) cmdResume(args []string) error {
	fs := e.newFlags("resume")
	if err := parse(fs, args); err != nil {
		return err
	}
	reply, err := e.resumeOnce()
	if err != nil {
		code := "bad_request"
		var ce *codedError
		if errors.As(err, &ce) {
			code = ce.code
		}
		reply = resumeReply{Error: err.Error(), Code: code}
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	if werr := enc.Encode(reply); werr != nil {
		return werr
	}
	if err != nil {
		return errAlreadyReported
	}
	return nil
}

func (e *env) resumeOnce() (resumeReply, error) {
	raw, err := io.ReadAll(io.LimitReader(resumeInput, maxResumeInput+1))
	if err != nil {
		return resumeReply{}, coded("bad_request", "resume: read request: %v", err)
	}
	if len(raw) > maxResumeInput {
		return resumeReply{}, coded("bad_request", "resume: request larger than %d bytes", maxResumeInput)
	}
	var req resumeRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return resumeReply{}, coded("bad_request", "resume: request is not a resume request")
	}
	if strings.TrimSpace(req.Job.Description) == "" {
		return resumeReply{}, coded("bad_request", "resume: the request has no job description")
	}
	if err := checkHeader(req); err != nil {
		return resumeReply{}, err
	}

	cfg, err := e.loadConfig()
	if err != nil {
		return resumeReply{}, coded(codeConfig, "resume: %v", err)
	}
	if cfg.KB.Bank == "" || cfg.KB.RKB == "" {
		return resumeReply{}, coded(codeConfig, "resume: kb.bank and kb.rkb are not set in Vani's config, so rkb cannot build a resume")
	}
	vmReport, vmNote, err := resumeVMReport(cfg, time.Now())
	if err != nil {
		return resumeReply{}, coded(codeConfig, "resume: %v", err)
	}
	ctx, cancel := signalContext()
	defer cancel()

	rc := rkbresume.Config{
		RKB: cfg.KB.RKB, CodeRoot: cfg.KB.CodeRoot, Bank: cfg.KB.Bank,
		Selections: cfg.KB.Selections, BaseSelection: cfg.KB.BaseSelection,
		VMReport: vmReport,
		Runner:   resumeRunner,
		MaxPDF:   maxResumePDF,
		Model:    cfg.LLM.Model,
	}
	rc.Provider, rc.NoModel = e.resumeModel(ctx, cfg)
	if rc.Provider == nil {
		rc.Model = ""
	}
	res, err := rkbresume.Build(ctx, rc, rkbresume.Request{Company: req.Job.Company, Title: req.Job.Title, Description: req.Job.Description})
	if err != nil {
		var re *rkbresume.Error
		if errors.As(err, &re) {
			return resumeReply{}, &codedError{code: re.Code, msg: re.Msg}
		}
		return resumeReply{}, coded(codeRender, "resume: %v", err)
	}
	r, err := readBuilt(res)
	if err != nil {
		return resumeReply{}, err
	}
	r.Name = resumeFileName(req.Job.Company)
	if vmNote != "" {
		r.Notes = append(r.Notes, vmNote)
	}
	fmt.Fprintf(e.stderr, "vani: resume: %s selection (%d pages)%s\n", r.Selection, r.Pages, reasonSuffix(r.Reason))
	return r, nil
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// resumeModel is the provider for the tailoring turn, or why there is none: a
// resume is then built from the base selection. Nothing about the owner reaches
// the reason.
func (e *env) resumeModel(ctx context.Context, cfg config.Config) (llm.Provider, string) {
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return nil, "no llm provider is configured"
	}
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	p, err := resumeProvider(ctx, w)
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: resume: no model: %v\n", err)
		return nil, "the model provider is not available"
	}
	if _, ok := p.Capability(cfg.LLM.Model); !ok {
		return nil, "the provider does not serve the configured model"
	}
	return p, ""
}

// resumeVMReport is the validator report rkb's claims export reads, so figures
// the validator backs count as verified: which bullets may print depends on
// it. kb.vm_report names it; one that is set but missing is an error, never a
// silent fallback. Unset, it falls back to rkb's CV folder under kb.code_root
// (career/CurriculumVitae: vm_report_full.txt, else vm_report.txt), and note
// says which file and how old, so the reply never hides the choice. Empty
// path with a note when there is none.
func resumeVMReport(cfg config.Config, now time.Time) (path, note string, err error) {
	if p := cfg.KB.VMReport; p != "" {
		if st, serr := os.Stat(p); serr != nil || !st.Mode().IsRegular() {
			return "", "", fmt.Errorf("kb.vm_report %s is not a readable file", filepath.Base(p))
		}
		return p, "", nil
	}
	if cfg.KB.CodeRoot != "" {
		for _, name := range []string{"vm_report_full.txt", "vm_report.txt"} {
			p := filepath.Join(cfg.KB.CodeRoot, "career", "CurriculumVitae", name)
			if st, serr := os.Stat(p); serr == nil && st.Mode().IsRegular() {
				return p, fmt.Sprintf("kb.vm_report is not set: figures were checked against %s, modified %s ago", name, now.Sub(st.ModTime()).Round(time.Minute)), nil
			}
		}
	}
	return "", "kb.vm_report is not set and no validator report was found: a figure only the validator backs is left off", nil
}

func printable(s string, max int) bool {
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// checkHeader refuses a request whose header is unusable. The header is not
// used afterwards: rkb prints its own.
func checkHeader(req resumeRequest) error {
	name := strings.TrimSpace(req.Header.Name)
	if name == "" {
		return coded(codeNoName, "resume: no name for the header; add your first and last name in Your basics")
	}
	if !printable(name, maxHeaderName) {
		return coded("bad_request", "resume: the header name is not one printable line of at most %d characters", maxHeaderName)
	}
	if len(req.Header.ContactLines) > maxContactLines {
		return coded("bad_request", "resume: at most %d contact lines", maxContactLines)
	}
	for _, l := range req.Header.ContactLines {
		if l = strings.TrimSpace(l); l != "" && !printable(l, maxContactLine) {
			return coded("bad_request", "resume: a contact line is not one printable line of at most %d characters", maxContactLine)
		}
	}
	return nil
}

// readBuilt turns a built resume into the reply, re-checking what the app and
// the extension rely on: it starts with %PDF-, fits the cap, and fits
// resume.PageBudget pages.
func readBuilt(res rkbresume.Result) (resumeReply, error) {
	pdf := res.PDF
	if len(pdf) == 0 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return resumeReply{}, coded(codeRender, "resume: the built PDF is damaged")
	}
	if len(pdf) > maxResumePDF {
		return resumeReply{}, coded(codeTooLarge, "resume: the PDF is over %d KB", maxResumePDF>>10)
	}
	if res.Pages <= 0 {
		return resumeReply{}, coded(codeRender, "resume: no page count for the built PDF")
	}
	if res.Pages > resume.PageBudget {
		return resumeReply{}, coded(codePages, "resume: the PDF has %d pages, over the %d-page budget", res.Pages, resume.PageBudget)
	}
	return resumeReply{
		PDF: base64.StdEncoding.EncodeToString(pdf), Bytes: len(pdf), Pages: res.Pages,
		Selection: res.Selection, Reason: res.Reason, UnverifiedDropped: res.UnverifiedDropped, Notes: res.Notes,
	}, nil
}

// resumeFileName is the name the page shows for the built PDF: Resume_<Company>.pdf,
// letters and digits joined by underscores, at most 60 runes of company. It never
// holds the owner's name, a path separator or punctuation.
func resumeFileName(company string) string {
	words := strings.FieldsFunc(company, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	s := []rune(strings.Join(words, "_"))
	if len(s) > 60 {
		s = []rune(strings.TrimRight(string(s[:60]), "_"))
	}
	if len(s) == 0 {
		return "Resume.pdf"
	}
	return "Resume_" + string(s) + ".pdf"
}
