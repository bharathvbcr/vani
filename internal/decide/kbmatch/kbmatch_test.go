package kbmatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

func testProfile() semantic.Profile {
	return semantic.Profile{Chunks: []semantic.Chunk{
		{Label: "skills", Text: "Skills: Go, Rust, CUDA, Metal", Weight: 1},
		{Label: "LLM Inference Runtime", Text: "LLM Inference Runtime", Weight: 1},
		{Label: "LLM Inference Runtime", Text: "Cut decode latency with fused attention kernels in Metal", Weight: 1},
		{Label: "LLM Inference Runtime", Text: "Built a batching scheduler in Rust", Weight: 1},
		{Label: "Agent Platform", Text: "Agent Platform", Weight: 1},
		{Label: "Agent Platform", Text: "Wrote tool-calling loops with provider fallbacks", Weight: 1},
	}}
}

func TestDigestIsDeterministicBoundedAndHashed(t *testing.T) {
	a, err := DigestOf(testProfile())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DigestOf(testProfile())
	if a != b || len(a.SHA256) != 64 {
		t.Fatalf("digest is not deterministic: %+v vs %+v", a, b)
	}
	for _, want := range []string{"Skills: Go, Rust", "- LLM Inference Runtime", "* Cut decode latency", "- Agent Platform"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("digest lacks %q:\n%s", want, a.Text)
		}
	}
	// An edit to the knowledge base changes the hash, so stored answers re-ask.
	p := testProfile()
	p.Chunks[2].Text = "Built a batching scheduler in Go"
	if c, _ := DigestOf(p); c.SHA256 == a.SHA256 {
		t.Errorf("an edited bullet kept the digest hash")
	}
}

func TestDigestCapsABigBank(t *testing.T) {
	var p semantic.Profile
	for i := 0; i < semantic.MaxProfileChunks; i++ {
		label := fmt.Sprintf("Entry %03d", i/6)
		p.Chunks = append(p.Chunks, semantic.Chunk{Label: label, Text: fmt.Sprintf("%s bullet %d %s", label, i, strings.Repeat("word ", 80)), Weight: 1})
	}
	d, err := DigestOf(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Text) > MaxDigestBytes {
		t.Errorf("digest is %d bytes, max %d", len(d.Text), MaxDigestBytes)
	}
	if !strings.HasSuffix(d.Text, "\n") || strings.Count(d.Text, "\n") < 10 {
		t.Errorf("digest keeps whole lines, got %d bytes", len(d.Text))
	}
	if !utf8.ValidString(d.Text) {
		t.Errorf("digest is not valid UTF-8")
	}
}

func TestDigestRefusesAnEmptyProfile(t *testing.T) {
	for name, p := range map[string]semantic.Profile{
		"none": {}, "blank": {Chunks: []semantic.Chunk{{Label: "x", Text: "  "}, {Label: " ", Text: "y"}}},
	} {
		if _, err := DigestOf(p); err == nil {
			t.Errorf("%s: a profile with nothing to say gave a digest", name)
		}
	}
}

func TestQuestionStaysInsideTheContextBoundAndNamesNoOne(t *testing.T) {
	d, _ := DigestOf(testProfile())
	p := posting.Posting{
		Key: "gh:acme:1", Company: "SecretCo", Title: "Software Engineer", Location: "Austin",
		Description: strings.Repeat("Design services. ", 20000),
	}
	q, truncated := Question(p, "early-career ML systems roles", d, nil)
	if err := decide.ValidateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if len(q.Context) > jdtriage.MaxContextBytes || !truncated || q.Facts[jdtriage.FactContextTruncated] != "true" {
		t.Errorf("context %d bytes (max %d), truncated %v, facts %v", len(q.Context), jdtriage.MaxContextBytes, truncated, q.Facts)
	}
	if strings.Contains(string(q.Context), "SecretCo") || strings.Contains(q.Prompt, "SecretCo") {
		t.Errorf("the employer's name reached the question")
	}
	if q.Task != Task || len(q.Slots) != 1 || q.Slots[0].Name != Slot {
		t.Errorf("question %+v", q)
	}
	if got := q.Slots[0].Options; fmt.Sprint(got) != fmt.Sprint(jobinsight.KBMatchOrder[:]) {
		t.Errorf("options %v, want the ranker's %v", got, jobinsight.KBMatchOrder)
	}
	for _, o := range q.Slots[0].Options {
		if o == "none" {
			t.Errorf("option %q is reserved by the LLM backend", o)
		}
	}
	short, _ := Question(posting.Posting{Title: "T", Description: "short"}, "s", d, nil)
	if strings.Contains(short.Facts[jdtriage.FactContextTruncated], "true") {
		t.Errorf("a short posting reported truncation")
	}
}

func TestQuestionCutsOnRuneBoundaries(t *testing.T) {
	d, _ := DigestOf(testProfile())
	q, _ := Question(posting.Posting{Title: "T", Description: strings.Repeat("é", jdtriage.MaxContextBytes)}, "s", d, nil)
	if !utf8.Valid(q.Context) {
		t.Errorf("the cut split a rune")
	}
}

// fakeDecider answers every question with one reading.
type fakeDecider struct {
	rd   decide.SlotReading
	err  error
	seen []decide.Question
}

func (f *fakeDecider) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.seen = append(f.seen, q)
	if f.err != nil {
		return decide.Answer{}, f.err
	}
	return decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{Slot: f.rd}}, nil
}

func judge(f *fakeDecider) Judge {
	d, _ := DigestOf(testProfile())
	return Judge{D: f, Target: "early-career ML systems roles", Digest: d}
}

func TestJudgeReadsOnlyAListedAnswer(t *testing.T) {
	p := posting.Posting{Key: "gh:a:1", Title: "ML Engineer", Description: "Serve models."}
	for _, v := range jobinsight.KBMatchOrder {
		f := &fakeDecider{rd: decide.SlotReading{Reading: decide.Answered, Value: v}}
		got, err := judge(f).Ask(context.Background(), p)
		if err != nil || got.Value != v || got.HasP {
			t.Errorf("%s: got %+v, %v", v, got, err)
		}
	}
	f := &fakeDecider{rd: decide.SlotReading{Reading: decide.Answered, Value: "core", Jev: &decide.JevCalib{Probability: 0.8}}}
	if got, err := judge(f).Ask(context.Background(), p); err != nil || !got.HasP || got.P != 0.8 {
		t.Errorf("a calibrated answer lost its probability: %+v, %v", got, err)
	}
	for name, rd := range map[string]decide.SlotReading{
		"abstained": {Reading: decide.Abstained}, "failed": {Reading: decide.Failed, Detail: "unparseable"},
		"unavailable": {Reading: decide.Unavailable}, "refused": {Reading: decide.Refused}, "not asked": {Reading: decide.NotAsked},
		"off-list": {Reading: decide.Answered, Value: "perfect"}, "empty": {Reading: decide.Answered},
	} {
		if _, err := judge(&fakeDecider{rd: rd}).Ask(context.Background(), p); !errors.Is(err, ErrNotRead) {
			t.Errorf("%s: want ErrNotRead, got %v", name, err)
		}
	}
}

func TestJudgeBackendErrorsAndMissingPieces(t *testing.T) {
	p := posting.Posting{Title: "T", Description: "d"}
	boom := errors.New("provider down")
	if _, err := judge(&fakeDecider{err: boom}).Ask(context.Background(), p); !errors.Is(err, boom) || errors.Is(err, ErrNotRead) {
		t.Errorf("a backend error must come back as itself, got %v", err)
	}
	if _, err := (Judge{Target: "t", Digest: Digest{Text: "x"}}).Ask(context.Background(), p); err == nil {
		t.Errorf("no Decider must be an error")
	}
	if _, err := (Judge{D: &fakeDecider{}, Target: "t"}).Ask(context.Background(), p); err == nil {
		t.Errorf("no digest must be an error, not a question with an empty knowledge base")
	}
	d, _ := DigestOf(testProfile())
	if _, err := (Judge{D: &fakeDecider{}, Target: strings.Repeat("x", jdtriage.MaxTargetSummaryBytes+1), Digest: d}).Ask(context.Background(), p); err == nil {
		t.Errorf("an oversized target summary must be refused")
	}
}

type emptyDecider struct{}

func (emptyDecider) Decide(context.Context, decide.Question) (decide.Answer, error) {
	return decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}, nil
}

func TestJudgeAnswerWithoutTheSlotIsNotRead(t *testing.T) {
	d, _ := DigestOf(testProfile())
	j := Judge{D: emptyDecider{}, Target: "t", Digest: d}
	if _, err := j.Ask(context.Background(), posting.Posting{Title: "T", Description: "d"}); !errors.Is(err, ErrNotRead) {
		t.Errorf("want ErrNotRead, got %v", err)
	}
}

func TestTextSHA256ChangesWithWhatTheModelSees(t *testing.T) {
	d, _ := DigestOf(testProfile())
	p := posting.Posting{Title: "T", Location: "L", Description: "D"}
	base := TextSHA256(p, "summary", d)
	if len(base) != 64 || base != TextSHA256(p, "summary", d) {
		t.Fatalf("hash is not stable: %q", base)
	}
	other, _ := DigestOf(semantic.Profile{Chunks: []semantic.Chunk{{Label: "x", Text: "other work", Weight: 1}}})
	for name, h := range map[string]string{
		"title":       TextSHA256(posting.Posting{Title: "T2", Location: "L", Description: "D"}, "summary", d),
		"location":    TextSHA256(posting.Posting{Title: "T", Location: "L2", Description: "D"}, "summary", d),
		"description": TextSHA256(posting.Posting{Title: "T", Location: "L", Description: "D2"}, "summary", d),
		"summary":     TextSHA256(p, "summary 2", d),
		"digest":      TextSHA256(p, "summary", other),
	} {
		if h == base {
			t.Errorf("a changed %s kept the hash", name)
		}
	}
	// Moving text between fields must not collide.
	if TextSHA256(posting.Posting{Title: "ab", Description: "c"}, "s", d) == TextSHA256(posting.Posting{Title: "a", Description: "bc"}, "s", d) {
		t.Errorf("field boundaries collide")
	}
}
