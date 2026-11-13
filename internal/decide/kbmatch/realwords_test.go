package kbmatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/Vani/internal/track"
)

// realWords loads a words file of the (synthetic) lines through track, so these
// tests run the matcher a real send uses, not a stub.
func realWords(t *testing.T, lines ...string) track.Words {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, track.WordsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := track.LoadWords(dir)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func leakyProfile() semantic.Profile {
	return semantic.Profile{Chunks: []semantic.Chunk{
		{Label: "skills", Text: "Go, Quillbridge", Weight: 1},
		{Label: "skills", Text: "Labs", Weight: 1},
		{Label: "Inference runtime", Text: "Inference runtime", Weight: 1},
		{Label: "Inference runtime", Text: "Tuned kernels and cut decode latency in Zeph\xe2\x80\x8byrnet", Weight: 1},
		{Label: "Inference runtime", Text: "Met Jose\xcc\x81 and shipped ZephyrnetApp", Weight: 1},
		// Spacing variants of a listed hyphenated term: double space, NBSP, newline, tab.
		{Label: "Inference runtime", Text: "Cut decode  latency, decode\xc2\xa0latency, decode\nlatency and decode\tlatency", Weight: 1},
	}}
}

// The digest is redacted by the matcher a send uses: plurals, joins, invisible
// characters and a term split across two skill lines all go, in both texts.
func TestDigestOfRemovesEveryVariantOfTheOwnersTerms(t *testing.T) {
	w := realWords(t, "kernel", "decode-latency", "Zephyrnet", "Quillbridge Labs", "Jos\xc3\xa9")
	d, err := DigestOf(leakyProfile(), w)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{d.Text, d.Compact} {
		if f := w.Found(s); len(f) > 0 {
			t.Fatalf("terms survived (%v):\n%s", f, s)
		}
		if strings.Contains(strings.ToLower(s), "kernel") || strings.Contains(strings.ToLower(s), "latency") {
			t.Fatalf("a term survived in:\n%s", s)
		}
	}
	if !strings.Contains(d.Text, "Inference runtime") || !strings.Contains(d.Text, "Go") {
		t.Fatalf("harmless text was lost:\n%s", d.Text)
	}
	if len(d.Text) > MaxDigestBytes || len(d.Compact) > MaxCompactBytes {
		t.Fatalf("digest over its bound: %d, %d", len(d.Text), len(d.Compact))
	}
}

func TestPrepareRedactsFocusLinesWithTheSendMatcher(t *testing.T) {
	w := realWords(t, "kernel", "Quillbridge Labs")
	d, err := DigestOf(leakyProfile(), w)
	if err != nil {
		t.Fatal(err)
	}
	focus := []Focus{
		{Label: "Tuned Kernels", Text: "Wrote fused kernels"},
		{Label: "Quillbridge", Text: "ended a line"},
		{Label: "Labs", Text: "started the next"},
	}
	q, _, err := Prepare(posting.Posting{Title: "Engineer", Description: "kernels at an employer"}, "ml systems roles", d, focus)
	if err != nil {
		t.Fatal(err)
	}
	owner, posted, _ := strings.Cut(string(q.Context), "\nJOB POSTING:\n")
	if f := w.Found(owner); len(f) > 0 {
		t.Fatalf("%v reached the owner's sections:\n%s", f, owner)
	}
	if !strings.Contains(owner, "CLOSEST TO THIS POSTING") || !strings.Contains(owner, track.Redacted) {
		t.Fatalf("the focused view lost its redaction marker:\n%s", owner)
	}
	if !strings.Contains(posted, "kernels at an employer") {
		t.Fatalf("the employer's text was rewritten:\n%s", posted)
	}
}

// Frame is the one question that carries no knowledge base; it refuses what
// Prepare would.
func TestFrameRefusesAnUnredactedDigestAndASummaryTerm(t *testing.T) {
	w := realWords(t, "kernel")
	d, _ := DigestOf(leakyProfile(), w)
	q, err := d.Frame("early-career ml systems roles")
	if err != nil || strings.Contains(string(q.Context), "Inference runtime") {
		t.Fatalf("frame: %v\n%s", err, q.Context)
	}
	if _, err := (Digest{Text: "x"}).Frame("roles"); !errors.Is(err, ErrNoRedactor) {
		t.Fatalf("literal digest: %v", err)
	}
	var ie *IdentifyingError
	if _, err := d.Frame("kernels roles"); !errors.As(err, &ie) || ie.Summary != 1 {
		t.Fatalf("summary term: %v", err)
	}
}

// RawDigestText keeps what DigestOf cuts and strips, so a check before redaction
// sees a term that crosses the per-bullet cut whole.
func TestRawDigestTextIsUncutAndUnredacted(t *testing.T) {
	long := strings.Repeat("word ", 47) + "Zephyrnet engine"
	p := semantic.Profile{Chunks: []semantic.Chunk{
		{Label: "Runtime", Text: "Runtime", Weight: 1},
		{Label: "Runtime", Text: long, Weight: 1},
	}}
	raw, err := RawDigestText(p)
	if err != nil || !strings.Contains(raw, "Zephyrnet engine") {
		t.Fatalf("raw text lost the tail: %v\n%s", err, raw)
	}
	w := realWords(t, "zephyrnet")
	d, _ := DigestOf(p, w)
	if strings.Contains(strings.ToLower(d.Text), "zephy") && !strings.Contains(d.Text, track.Redacted) {
		t.Fatalf("the sent digest was not redacted:\n%s", d.Text)
	}
	if len(w.Found(raw)) != 1 {
		t.Fatalf("the check misses the term in the raw text")
	}
}
