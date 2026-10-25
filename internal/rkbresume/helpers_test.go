package rkbresume

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
)

// The fixture's claims. clmUnv is the figure rkb has not double-verified.
var claimValues = map[string]string{"clmLat": "40%", "clmThr": "3x", "clmMem": "2 GB", "clmUnv": "9 ms"}

// bulletTexts: id -> text. {{clm...}} tokens resolve through claimValues.
var bulletTexts = map[string]string{
	"s1": "Built serving systems that cut {{clmLat}} of latency for model inference",
	"s2": "Measured tracing overhead across Go and Rust services",
	"s3": "Shipped batching for Kubernetes model serving",
	"a1": "Cut tail latency {{clmLat}} by batching requests in the serving path",
	"a2": "Raised throughput {{clmThr}} with a lock-free queue",
	"a3": "Traced slow requests across Go services with spans",
	"a4": "Reduced memory to {{clmMem}} per worker by pooling buffers",
	"a5": "Built an observability pipeline for model serving",
	"a6": "Designed a Rust parser for trace events",
	"a7": "Automated capacity planning for the serving fleet",
	"a8": "Operated the serving systems beneath those features and owned their latency",
	"p1": "Built a GPU kernel profiler for inference",
	"p2": "Raised decode throughput with fused kernels",
	"p3": "Cut inference latency in a GPU runtime",
	"p4": "Shipped a serving benchmark harness",
	"c2": "Brought the same work to a second GPU vendor",
	"c3": "Brought the same lessons to the whole fleet",
	"b1": "Published a study of batching in Python inference stacks",
	"b2": "Replicated a latency benchmark on a second machine",
	"c1": "Built a Kubernetes operator for GPU nodes",
	"u1": "Cut startup time {{clmUnv}} with lazy loading",
	"x1": "Wrote an unreviewed bullet nobody has read",
}

const (
	ownerName    = "Zed Q. Owner"
	ownerContact = "zed.owner@example.invalid"
)

func plainOf(text string) string {
	for k, v := range claimValues {
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}
	return text
}

func reviewFor(text string) map[string][]map[string]any {
	sum := sha1.Sum([]byte(plainOf(text)))
	h := hex.EncodeToString(sum[:])[:12]
	// rkb's reader gate (kbsync.Eligible): a round-1 and a round-2 pass per
	// reader, by independent reviewers, HR and hiring manager distinct.
	pass := func(reader string) []map[string]any {
		return []map[string]any{
			{"round": 1, "verdict": "pass", "text_hash": h, "by": reader + "-r1"},
			{"round": 2, "verdict": "pass", "text_hash": h, "by": reader + "-r2"},
		}
	}
	return map[string][]map[string]any{"hr": pass("hr"), "hm": pass("hm")}
}

const testJD = "We need a Kubernetes expert in Go, Rust, Python, tracing, latency, batching and observability for model serving."

var testKeywords = []string{"Kubernetes", "Go", "Rust", "Python", "tracing", "latency", "batching", "observability"}

// fixture is a bank, a base selection and a second selection on disk, a fake
// rkb and a Config that uses them.
type fixture struct {
	t        *testing.T
	dir      string
	bank     string
	base     string
	other    string
	rkb      *fakeRKB
	provider *fakeProvider
	cfg      Config
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pdftotext"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	bullets := map[string]any{}
	for id, text := range bulletTexts {
		b := map[string]any{"text": text}
		if id != "x1" {
			b["review"] = reviewFor(text)
		}
		bullets[id] = b
	}
	bankPath := filepath.Join(dir, "bank.json")
	writeJSON(t, bankPath, map[string]any{
		"headers": map[string]any{"default": map[string]any{"name": ownerName, "contact": []string{ownerContact}}},
		"entries": map[string]any{
			"exp-a": map[string]any{"title": "Engineer", "org": "Acme", "dates": "2023 to 2024", "location": "Springfield",
				"bullets": []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}},
			"exp-b": map[string]any{"title": "Researcher", "org": "Lab", "dates": "2021", "bullets": []string{"b1", "b2"}},
			"exp-c": map[string]any{"title": "Intern", "org": "Co", "dates": "2019", "bullets": []string{"c1", "c2", "c3", "x1", "u1"}},
			"edu":   map[string]any{"title": "MS Computing", "org": "Uni", "dates": "2020", "bullets": []string{}},
		},
		"bullets": bullets,
	})

	basePath := filepath.Join(dir, "base.json")
	writeJSON(t, basePath, map[string]any{
		"bank":   "../bank.json",
		"header": map[string]any{"name": ownerName, "contact": []string{ownerContact}},
		"meta": map[string]any{"target": "General", "pages": 2, "paper": "letter",
			"output_basename": "Zed_Q_Owner_Resume", "jd_keywords": []string{"Go"}},
		"sections": []any{
			map[string]any{"title": "Summary", "kind": "bullets", "bullets": []string{"s1", "s2"}},
			map[string]any{"title": "Experience", "kind": "entries", "entries": []any{
				map[string]any{"entry": "exp-a", "bullets": []string{"a1", "a2"}, "title": "Lead Engineer", "lead": "A lead line."},
				map[string]any{"entry": "exp-b", "bullets": []string{"b1"}},
			}},
			map[string]any{"title": "Skills", "kind": "lines", "lines": []any{map[string]any{"label": "Languages", "text": "Go, Rust"}}},
			map[string]any{"title": "Education", "kind": "entries", "entries": []any{
				map[string]any{"entry": "edu", "bullets": []string{}},
			}},
		},
	})
	otherPath := filepath.Join(dir, "other.json")
	writeJSON(t, otherPath, map[string]any{
		"bank":   "../bank.json",
		"header": "default",
		"meta":   map[string]any{"pages": 4},
		"sections": []any{
			map[string]any{"title": "Summary", "kind": "bullets", "bullets": []string{"s3"}},
			map[string]any{"title": "Experience", "kind": "entries", "entries": []any{
				map[string]any{"entry": "exp-a", "bullets": []string{"a3", "a4", "a5", "a6", "a7", "a8"}},
				map[string]any{"entry": "exp-b", "bullets": []string{"b2"}},
				map[string]any{"entry": "exp-c", "bullets": []string{"c2", "c1", "u1", "c3", "x1"}},
			}},
		},
	})

	f := &fixture{t: t, dir: dir, bank: bankPath, base: basePath, other: otherPath}
	f.rkb = &fakeRKB{t: t}
	f.provider = &fakeProvider{}
	f.cfg = Config{
		RKB: "/fake/rkb", CodeRoot: "/fake/code", Bank: bankPath,
		Selections: []string{basePath, otherPath}, BaseSelection: basePath,
		Model: "m", Provider: f.provider, Runner: f.rkb,
	}
	return f
}

func (f *fixture) build() (Result, error) {
	f.t.Helper()
	return Build(context.Background(), f.cfg, Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD})
}

// validReply is a choice that passes validation.
func validReply() string {
	return `{"sections":[{"title":"Summary","bullets":["s3","s1"]},{"title":"Experience","entries":[` +
		`{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1"]}]}],"jd_keywords":` + mustJSON(testKeywords) + `}`
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ---- fake rkb

type fitSpec struct {
	status   int
	advice   string
	pages    int
	pdf      []byte
	noReport bool // rkb's die(): exit 2, stderr, no report
	exit     int  // overrides the exit code when noReport
	stderr   string
	orphans  []string
}

type fakeRKB struct {
	t  *testing.T
	mu sync.Mutex

	calls    []Command
	steps    []string
	sels     []string // the selection each used-in export, lint and render saw
	selPaths []string
	dirModes []os.FileMode
	curSel   string

	// behaviours; nil means the passing one
	lint   func(sel string) (int, string)
	fit    func(sel string) fitSpec
	html   func(sel string) string
	runErr map[string]error // step -> runner error
	// unverified: claim id -> the bullet ids whose presence on the page makes
	// rkb report it as not double-verified (no ids: always).
	unverified map[string][]string
	block      string // step that waits for ctx to end
	// onStep runs at the start of every call, with the fake's lock held: it must
	// not call back into the fake.
	onStep func(step string, c Command)
	// claimValue, when set, rewrites each claim's value in an export (sel is empty
	// for the plain export, the selection's JSON for a used-in one).
	claimValue func(id, v, sel string) string
}

var testPDF = []byte("%PDF-1.7\n% fake pdf with no personal data\n%%EOF\n")

func (r *fakeRKB) Run(ctx context.Context, c Command, timeout time.Duration) (Output, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	if len(c.Args) == 0 {
		r.t.Fatalf("rkb run with no arguments")
	}
	step := c.Args[0]
	if step == "claims" {
		step = "claims-plain"
		if flag(c.Args, "--used-in") != "" {
			step = "claims-used"
		}
	}
	r.steps = append(r.steps, step)
	if r.onStep != nil {
		r.onStep(step, c)
	}
	if r.block == step {
		r.mu.Unlock()
		<-ctx.Done()
		r.mu.Lock()
		return Output{}, ctx.Err()
	}
	if err := r.runErr[step]; err != nil {
		return Output{}, err
	}
	switch step {
	case "claims-plain":
		r.writeClaims(flag(c.Args, "--out"), "")
		return Output{}, nil
	case "claims-used":
		sel := r.readSel(flag(c.Args, "--used-in"))
		r.writeClaims(flag(c.Args, "--out"), sel)
		return Output{}, nil
	case "lint":
		sel := r.readSel(c.Args[1])
		if r.lint != nil {
			exit, out := r.lint(sel)
			return Output{Exit: exit, Stdout: out}, nil
		}
		return Output{Stdout: "\n0 error(s), 0 warning(s)\n"}, nil
	case "render":
		sel := r.readSel(c.Args[1])
		dir := flag(c.Args, "--out-dir")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.t.Fatal(err)
		}
		page := "<html><body>resume</body></html>"
		if r.html != nil {
			page = r.html(sel)
		}
		if err := os.WriteFile(filepath.Join(dir, "resume.html"), []byte(page), 0o600); err != nil {
			r.t.Fatal(err)
		}
		return Output{Stdout: "wrote resume.{txt,ascii.txt,html}\n"}, nil
	case "fit":
		spec := fitSpec{pages: 2, pdf: testPDF}
		if r.fit != nil {
			spec = r.fit(r.curSel)
		}
		if spec.noReport {
			exit := spec.exit
			if exit == 0 {
				exit = 2
			}
			return Output{Exit: exit, Stderr: spec.stderr}, nil
		}
		if err := os.WriteFile(flag(c.Args, "--out"), spec.pdf, 0o600); err != nil {
			r.t.Fatal(err)
		}
		rep := map[string]any{"status": spec.status, "advice": spec.advice, "pages": spec.pages}
		var orph []map[string]string
		for _, id := range spec.orphans {
			orph = append(orph, map[string]string{"id": id})
		}
		rep["orphan_last_lines"] = orph
		b, _ := json.Marshal(rep)
		if err := os.WriteFile(flag(c.Args, "--report"), b, 0o600); err != nil {
			r.t.Fatal(err)
		}
		return Output{Exit: spec.status, Stdout: spec.advice}, nil
	}
	r.t.Fatalf("unexpected rkb step %q: %v", step, c.Args)
	return Output{}, nil
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (r *fakeRKB) readSel(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatalf("selection %s: %v", path, err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err == nil {
		r.dirModes = append(r.dirModes, st.Mode().Perm())
	}
	r.curSel = string(b)
	r.sels = append(r.sels, r.curSel)
	r.selPaths = append(r.selPaths, path)
	return r.curSel
}

// writeClaims writes the one file both `claims export` forms produce: values
// for kbsync, double_verified for this package.
func (r *fakeRKB) writeClaims(path, sel string) {
	claims := map[string]any{}
	for id, v := range claimValues {
		dv := true
		if triggers, ok := r.unverified[id]; ok && sel != "" {
			dv = false
			if len(triggers) > 0 { // unverified only while one of these bullets is on the page
				dv = true
				for _, b := range triggers {
					if strings.Contains(sel, `"`+b+`"`) {
						dv = false
					}
				}
			}
		}
		// Like rkb (claims.go evaluate), a claim that is not double-verified for a
		// reason says so in problems. (rkb also reports false, with none, for a claim
		// nothing in the probed selection uses; this fake never does.)
		if r.claimValue != nil {
			v = r.claimValue(id, v, sel)
		}
		view := map[string]any{"value": v, "double_verified": dv}
		if !dv {
			view["problems"] = []string{"pass 2 missing for: a sentence"}
		}
		claims[id] = view
	}
	b, _ := json.Marshal(map[string]any{"claims": claims})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fakeRKB) count(step string) int {
	n := 0
	for _, s := range r.steps {
		if s == step {
			n++
		}
	}
	return n
}

// selBullets lists every bullet id a selection JSON puts on the page.
func selBullets(t *testing.T, sel string) []string {
	t.Helper()
	var s struct {
		Sections []struct {
			Bullets []string `json:"bullets"`
			Entries []struct {
				Entry   string   `json:"entry"`
				Bullets []string `json:"bullets"`
			} `json:"entries"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(sel), &s); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sec := range s.Sections {
		ids = append(ids, sec.Bullets...)
		for _, e := range sec.Entries {
			ids = append(ids, e.Bullets...)
		}
	}
	return ids
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ---- fake provider

type fakeStream struct {
	text string
	done bool
}

func (s *fakeStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}

func (s *fakeStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}, StopReason: llm.StopEndTurn}, nil
}

func (s *fakeStream) Close() error { return nil }

type fakeProvider struct {
	replies  []string // one per turn; the last repeats
	err      error
	blockFor time.Duration
	reqs     []llm.Request
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 8192}, true
}

func (p *fakeProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.reqs = append(p.reqs, req)
	if p.blockFor > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.blockFor):
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	i := len(p.reqs) - 1
	if i >= len(p.replies) {
		i = len(p.replies) - 1
	}
	if i < 0 {
		return nil, fmt.Errorf("fake provider has no reply")
	}
	return &fakeStream{text: p.replies[i]}, nil
}

// userText is everything sent in user messages of one request.
func userText(r llm.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}
