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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// `vani resume` builds a resume for one job: DevPrism's resume_synthesize picks
// and orders the knowledge base's blocks for the job description and renders its
// plain Typst layout to a PDF. No model is called. The Vani Apply agent runs it
// (job kind "resume") when the owner accepts the offer to build one for a form
// that has no resume chosen; the extension then binds the PDF to the job page and
// attaches it.
//
// The header (name and contact lines) arrives in the request, from the owner's
// "Your basics" in Vani Apply, because the vault is not wired yet. It reaches
// DevPrism only through resume.HeaderSource, never in the JD text, and never
// appears in an error, a log line or the reply.

const (
	// maxResumeInput caps the request read from stdin: a long posting plus the header.
	maxResumeInput = 128 << 10
	// maxResumePDF is ResumeStore.maxBytes (macos) and MAX_RESUME_BYTES (extension):
	// a larger PDF could not cross Chrome's 1 MiB native-messaging reply.
	maxResumePDF = 512 << 10
	// resumeCallTimeout covers block selection and one Typst compile.
	resumeCallTimeout = 120 * time.Second
	maxHeaderName     = 200
	maxContactLine    = 200
	maxContactLines   = 4
	maxDiagnostic     = 300
)

// Reply codes.
const (
	codeNoName   = "no_name"
	codeDevPrism = "devprism"
	codeRender   = "render"
	codeTooLarge = "too_large"
	codePages    = "pages"
)

// resumeInput is where `vani resume` reads its request. Tests replace it.
var resumeInput io.Reader = os.Stdin

// synthesizer is what `vani resume` uses of a started DevPrism.
type synthesizer interface {
	Synthesize(ctx context.Context, src resume.HeaderSource, args resume.SynthesizeArgs) (json.RawMessage, error)
	Close() error
}

// startSynth starts DevPrism. Tests replace it.
var startSynth = func(ctx context.Context, cfg resume.Config) (synthesizer, error) { return resume.Start(ctx, cfg) }

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
	Error       string  `json:"error,omitempty"`
	Code        string  `json:"code,omitempty"`
}

// requestHeader is the header from the request, checked; it is the HeaderSource
// Synthesize reads, so the name never travels in SynthesizeArgs.
type requestHeader struct{ h resume.Header }

func (r requestHeader) Header(context.Context) (resume.Header, error) { return r.h, nil }

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
// stdout: {"pdf","name","bytes","pages","ats_coverage_pct"} or {"error","code"}.
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
	jd := jobText(req)
	if strings.TrimSpace(req.Job.Description) == "" {
		return resumeReply{}, coded("bad_request", "resume: the request has no job description")
	}
	h, err := checkHeader(req)
	if err != nil {
		return resumeReply{}, err
	}

	cfg, err := e.loadConfig()
	if err != nil {
		return resumeReply{}, coded(codeConfig, "resume: %v", err)
	}
	if cfg.DevPrism.Binary == "" {
		return resumeReply{}, coded(codeConfig, "resume: devprism.binary is not set in Vani's config, so no resume can be built (docs/PHASE0-devprism.md)")
	}
	ctx, cancel := signalContext()
	defer cancel()
	c, err := startSynth(ctx, resume.Config{BinaryPath: cfg.DevPrism.Binary, PinnedToolsHash: cfg.DevPrism.ToolsHash, CallTimeout: resumeCallTimeout})
	if err != nil {
		return resumeReply{}, coded(codeDevPrism, "resume: start DevPrism: %v", err)
	}
	defer c.Close()
	out, err := c.Synthesize(ctx, requestHeader{h}, resume.SynthesizeArgs{JD: jd})
	if err != nil {
		return resumeReply{}, coded(codeDevPrism, "resume: DevPrism: %v", err)
	}
	r, err := readSynthesis(out)
	if err != nil {
		return resumeReply{}, err
	}
	r.Name = resumeFileName(req.Job.Company)
	return r, nil
}

// jobText is what DevPrism reads: the title and company as a heading over the
// description. It holds nothing from the header.
func jobText(req resumeRequest) string {
	var b strings.Builder
	for _, s := range []string{req.Job.Title, req.Job.Company} {
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	b.WriteString(strings.TrimSpace(req.Job.Description))
	return b.String()
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

func checkHeader(req resumeRequest) (resume.Header, error) {
	name := strings.TrimSpace(req.Header.Name)
	if name == "" {
		return resume.Header{}, coded(codeNoName, "resume: no name for the header; add your first and last name in Your basics")
	}
	if !printable(name, maxHeaderName) {
		return resume.Header{}, coded("bad_request", "resume: the header name is not one printable line of at most %d characters", maxHeaderName)
	}
	if len(req.Header.ContactLines) > maxContactLines {
		return resume.Header{}, coded("bad_request", "resume: at most %d contact lines", maxContactLines)
	}
	h := resume.Header{Name: name}
	for _, l := range req.Header.ContactLines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !printable(l, maxContactLine) {
			return resume.Header{}, coded("bad_request", "resume: a contact line is not one printable line of at most %d characters", maxContactLine)
		}
		h.ContactLines = append(h.ContactLines, l)
	}
	return h, nil
}

// readSynthesis accepts resume_synthesize's reply only when it rendered a PDF
// that decodes, starts with %PDF-, matches the length DevPrism reported, fits
// the cap, and fits resume.PageBudget pages.
func readSynthesis(raw json.RawMessage) (resumeReply, error) {
	var out struct {
		Materialization *struct {
			Status         string   `json:"status"`
			PageCount      int      `json:"pageCount"`
			Errors         []string `json:"errors"`
			Reason         string   `json:"reason"`
			PDFBytesLength int      `json:"pdfBytesLength"`
			PDFBase64      string   `json:"pdfBase64"`
		} `json:"materialization"`
		MatchReport struct {
			ATS float64 `json:"atsCoveragePercentage"`
		} `json:"matchReport"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Materialization == nil {
		return resumeReply{}, coded(codeRender, "resume: DevPrism's reply has no rendered resume")
	}
	m := out.Materialization
	if m.Status != "rendered" {
		why := strings.Join(m.Errors, "; ")
		if why == "" {
			why = m.Reason
		}
		return resumeReply{}, coded(codeRender, "resume: DevPrism did not render a PDF (%s): %s", clip(m.Status, 40), clip(why, maxDiagnostic))
	}
	// A base64 string past the cap is refused before it is decoded.
	if len(m.PDFBase64) > base64.StdEncoding.EncodedLen(maxResumePDF)+4 {
		return resumeReply{}, coded(codeTooLarge, "resume: the PDF is over %d KB", maxResumePDF>>10)
	}
	pdf, err := base64.StdEncoding.DecodeString(m.PDFBase64)
	if err != nil || len(pdf) == 0 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return resumeReply{}, coded(codeRender, "resume: DevPrism's PDF is damaged")
	}
	if len(pdf) != m.PDFBytesLength {
		return resumeReply{}, coded(codeRender, "resume: DevPrism's PDF is %d bytes, not the %d it reported", len(pdf), m.PDFBytesLength)
	}
	if len(pdf) > maxResumePDF {
		return resumeReply{}, coded(codeTooLarge, "resume: the PDF is over %d KB", maxResumePDF>>10)
	}
	if m.PageCount <= 0 {
		return resumeReply{}, coded(codeRender, "resume: DevPrism did not report a page count")
	}
	if m.PageCount > resume.PageBudget {
		return resumeReply{}, coded(codePages, "resume: the PDF has %d pages, over the %d-page budget", m.PageCount, resume.PageBudget)
	}
	return resumeReply{PDF: base64.StdEncoding.EncodeToString(pdf), Bytes: len(pdf), Pages: m.PageCount, ATSCoverage: out.MatchReport.ATS}, nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
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
