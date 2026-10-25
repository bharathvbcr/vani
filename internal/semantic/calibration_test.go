package semantic

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// calBank is a profile with eight distinct bullets under two roles, so a
// fitting posting has eight different things to match.
const calBank = `{
  "entries": {
    "e1": {"title": "Platform Engineer", "org": "Zorblax Industries", "dates": "2019 - 2022", "location": "Quuxville", "stack": "Go, Kubernetes, Postgres",
           "bullets": ["b1", "b2", "b3", "b4"]},
    "e2": {"title": "Systems Developer", "org": "Frobnitz LLC", "dates": "2016 - 2019", "location": "Wibbleton", "stack": "Rust, Prometheus, PyTorch",
           "bullets": ["b5", "b6", "b7", "b8"]}
  },
  "bullets": {
    "b1": {"text": "Operated Kubernetes clusters serving production traffic across regions"},
    "b2": {"text": "Built Golang services for internal developer tooling pipelines"},
    "b3": {"text": "Designed Postgres schemas and query optimizations for reporting workloads"},
    "b4": {"text": "Automated Terraform provisioning for cloud networking and storage"},
    "b5": {"text": "Reduced latency by profiling Rust request handlers and caching hot paths"},
    "b6": {"text": "Implemented Prometheus alerting dashboards for service reliability"},
    "b7": {"text": "Trained PyTorch ranking models and evaluated offline metrics"},
    "b8": {"text": "Led incident response rotations and wrote postmortem reviews"}
  }
}`

// calTarget is a six-sentence target summary of more than 512 bytes, as the
// owner's is, each sentence close to one bullet.
const calTarget = "Operated Kubernetes clusters serving production traffic across regions for many internal teams. " +
	"Built Golang services for internal developer tooling pipelines used every day. " +
	"Designed Postgres schemas and query optimizations for reporting workloads at scale. " +
	"Automated Terraform provisioning for cloud networking and storage across accounts. " +
	"Reduced latency by profiling Rust request handlers and caching hot paths in services. " +
	"Implemented Prometheus alerting dashboards for service reliability and led incident response."

func calProfile(t testing.TB, target string) Profile {
	t.Helper()
	b, err := kbsync.Parse([]byte(calBank))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, target)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// calibrate embeds the profile and anchors with the fake embedder and returns
// the matcher and its calibration.
func calibrateTarget(t testing.TB, target string) (*Matcher, Calibration) {
	t.Helper()
	m, err := NewMatcher(NewFakeEmbedder(256, synonyms), calProfile(t, target), filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Calibration(); ok {
		t.Fatal("a matcher with no cache reports a calibration before embedding anything")
	}
	if _, err := m.ensureProfile(context.Background()); err != nil {
		t.Fatalf("calibration: %v", err)
	}
	c, ok := m.Calibration()
	if !ok {
		t.Fatal("no calibration after the profile was embedded")
	}
	return m, c
}

// TestOnDomainAnchorUsesTheWholeTarget: two targets that read the same for the
// first 512 bytes and differ only after them must calibrate differently. The
// ceiling used to be the target cut to its first 512 bytes as one title chunk,
// so everything after them was invisible to it.
func TestOnDomainAnchorUsesTheWholeTarget(t *testing.T) {
	var head strings.Builder
	for i := 0; head.Len() < 560; i++ {
		fmt.Fprintf(&head, "Operated Kubernetes clusters serving production traffic across zone%d regions. ", i)
	}
	tailMatch := "Built Golang services for internal developer tooling pipelines."
	tailHalf := "Automated bakery provisioning for ovens and flour."
	a, b := head.String()+tailMatch, head.String()+tailHalf
	if len(a) > MaxProfileChunkBytes || len(b) > MaxProfileChunkBytes || a[:MaxPostingChunkBytes] != b[:MaxPostingChunkBytes] || a == b {
		t.Fatalf("test targets must share their first %d bytes, differ later and fit the profile bound: %d, %d", MaxPostingChunkBytes, len(a), len(b))
	}
	_, ca := calibrateTarget(t, a)
	_, cb := calibrateTarget(t, b)
	t.Logf("tail matching a bullet: ceil %.4f over %d chunks; tail half matching: ceil %.4f over %d chunks", ca.Ceil, ca.AnchorChunks, cb.Ceil, cb.AnchorChunks)
	if ca.AnchorChunks < 2 || cb.AnchorChunks < 2 {
		t.Errorf("a several-sentence target anchors on %d and %d chunk(s), want more than one", ca.AnchorChunks, cb.AnchorChunks)
	}
	if math.Abs(ca.Ceil-cb.Ceil) < 1e-3 {
		t.Errorf("targets that differ only after byte 512 calibrate to ceilings %.4f and %.4f, within 0.001: the tail of the target is ignored", ca.Ceil, cb.Ceil)
	}
	// The anchor reads its best third (3 of 9 here, after near-duplicate removal
	// collapses the repeated head sentences), so a tail change moves the ceiling
	// only if the tail reaches that cut: the matching tail is the stronger line
	// and must raise it.
	if ca.Ceil <= cb.Ceil {
		t.Errorf("the tail that matches a bullet gives ceiling %.4f, not above the half-matching tail's %.4f: the tail does not reach the top third", ca.Ceil, cb.Ceil)
	}
	if ca.Floor != cb.Floor {
		t.Errorf("the floor moved with the target: %v vs %v", ca.Floor, cb.Floor)
	}
}

// fittingLines paraphrases the eight bullets: each shares most, not all, of its
// tokens with the bullet it echoes.
var fittingLines = []string{
	"Operate Kubernetes clusters for production traffic in many regions",
	"Build Golang services and developer tooling for internal pipelines",
	"Design Postgres schemas and tune query workloads for reporting",
	"Automate Terraform provisioning for cloud networking and storage layers",
	"Profile Rust request handlers to cut latency on hot paths with caching",
	"Implement Prometheus alerting dashboards that keep services reliable",
	"Train PyTorch ranking models and evaluate their offline metrics",
	"Lead incident response rotations and write postmortem reviews",
}

var offDomainLines = []string{
	"Bake croissants and plated desserts for daily restaurant service",
	"Decorate custom wedding cakes and manage the bakery ovens",
	"Order flour butter and chocolate for the pastry kitchen",
	"Greet customers and process returns at the retail checkout",
}

// TestFittingPostingLandsInsideTheRange: a posting whose lines each paraphrase
// one thing the owner did must score high but not at the top of the scale, and
// one that fits half as well must score lower.
func TestFittingPostingLandsInsideTheRange(t *testing.T) {
	m, c := calibrateTarget(t, calTarget)
	if len(calTarget) <= MaxPostingChunkBytes || len(calTarget) > MaxProfileChunkBytes {
		t.Fatalf("calTarget is %d bytes; the test needs it past %d and within %d", len(calTarget), MaxPostingChunkBytes, MaxProfileChunkBytes)
	}
	fit := Doc{Key: "fit", Title: "Platform Engineer", Description: strings.Join(fittingLines, "\n")}
	half := Doc{Key: "half", Title: "Platform Engineer", Description: strings.Join(append(append([]string(nil), fittingLines[:4]...), offDomainLines...), "\n")}
	off := Doc{Key: "off", Title: "Pastry Chef", Description: strings.Join(offDomainLines, "\n")}
	mustWarm(t, m, fit, half, off)
	f, h, o := lookup(t, m, fit), lookup(t, m, half), lookup(t, m, off)
	t.Logf("floor %.4f ceil %.4f over %d chunks; fit raw %.4f score %.4f; half raw %.4f score %.4f; off raw %.4f score %.4f",
		c.Floor, c.Ceil, c.AnchorChunks, f.Raw, f.Score, h.Raw, h.Score, o.Raw, o.Score)
	if f.Thin || f.Score >= 0.95 || f.Score <= 0.6 {
		t.Errorf("a fitting posting scored %.4f (thin %v), want it inside (0.6, 0.95)", f.Score, f.Thin)
	}
	if f.Score >= 0.999 || h.Score >= 0.999 {
		t.Errorf("a posting saturated the scale: fit %.4f, half %.4f", f.Score, h.Score)
	}
	if !(h.Score < f.Score && o.Score < h.Score) {
		t.Errorf("fit %.4f, half off-domain %.4f, off-domain %.4f: not in order", f.Score, h.Score, o.Score)
	}
}

// TestCalibrationIsDeterministic: the same profile and embedder give the same
// floor and ceiling, to the bit, from two matchers.
func TestCalibrationIsDeterministic(t *testing.T) {
	_, a := calibrateTarget(t, calTarget)
	_, b := calibrateTarget(t, calTarget)
	if math.Float64bits(a.Floor) != math.Float64bits(b.Floor) || math.Float64bits(a.Ceil) != math.Float64bits(b.Ceil) || a.AnchorChunks != b.AnchorChunks {
		t.Fatalf("%+v vs %+v", a, b)
	}
}

// TestCalibrationReadsDuringAWarm: Calibration neither blocks on nor races a
// Warm that is embedding the profile, and reports not-ready or the final
// numbers, never a half-built pair.
func TestCalibrationReadsDuringAWarm(t *testing.T) {
	m, err := NewMatcher(NewFakeEmbedder(256, synonyms), calProfile(t, calTarget), filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			if c, ok := m.Calibration(); ok && (c.AnchorChunks != 6 || !(c.Ceil > c.Floor)) {
				t.Errorf("half-built calibration %+v", c)
				return
			}
		}
	}()
	mustWarm(t, m, k8sDoc())
	<-done
	if c, ok := m.Calibration(); !ok || c.AnchorChunks != 6 {
		t.Fatalf("%+v %v", c, ok)
	}
}

// TestShortTargetAnchorsOnOneChunk: a target too short to split (DefaultTarget,
// or a few words) still calibrates, on one chunk, and says so.
func TestShortTargetAnchorsOnOneChunk(t *testing.T) {
	for name, target := range map[string]string{
		"default":       DefaultTarget,
		"three words":   "Platform engineer Kubernetes",
		"one word":      "Kubernetes",
		"empty":         "",
		"generic only":  "Strong communication skills are required.",
		"heading only":  "Kubernetes requirements:",
		"unicode":       "Éngénieur plateforme — ‘Kubernetes’",
		"punctuation":   "!!! ??? ...",
		"whitespace":    " \t\n ",
		"markup":        "<p>Kubernetes</p>",
		"one sentence":  "Platform engineer operating Kubernetes clusters for production traffic.",
		"repeated long": strings.Repeat("kubernetes ", 400),
	} {
		t.Run(name, func(t *testing.T) {
			if got := len(onDomainAnchor(calProfile(t, target).Target)); got != 1 {
				t.Fatalf("anchor has %d chunks, want 1", got)
			}
			if _, c := calibrateTarget(t, target); c.AnchorChunks != 1 {
				t.Fatalf("Calibration reports %d anchor chunks, want 1", c.AnchorChunks)
			}
		})
	}
}

// TestAnchorIsBounded: an oversized target is cut by the profile, and a
// profile built by hand with a larger one still yields a bounded anchor.
func TestAnchorIsBounded(t *testing.T) {
	var sb strings.Builder
	for i := 0; sb.Len() < 1<<20; i++ {
		fmt.Fprintf(&sb, "Sentence number %d describes platform work with kubernetes golang postgres terraform. ", i)
	}
	chunks := onDomainAnchor(sb.String())
	if len(chunks) == 0 || len(chunks) > MaxPostingChunks {
		t.Fatalf("%d anchor chunks, want 1..%d", len(chunks), MaxPostingChunks)
	}
	for _, c := range chunks {
		if len(c.Text) > MaxPostingChunkBytes || c.Weight != 1 || c.Label == "title" {
			t.Fatalf("chunk %+v", c)
		}
	}
	if got := len(onDomainAnchor(calProfile(t, sb.String()).Target)); got < 2 || got > MaxPostingChunks {
		t.Fatalf("bounded target anchors on %d chunks", got)
	}
}

// TestAnchorHasNoTitleLine: the target is not a role title, so no chunk of the
// anchor carries the title weight; every sentence of calTarget is a line.
func TestAnchorHasNoTitleLine(t *testing.T) {
	chunks := onDomainAnchor(calTarget)
	if len(chunks) != 6 {
		t.Fatalf("%d chunks for a six-sentence target: %+v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if c.Label != "line" || c.Weight != 1 {
			t.Errorf("chunk %+v", c)
		}
	}
}
