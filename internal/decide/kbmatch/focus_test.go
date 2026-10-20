package kbmatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

func TestQuestionWithFocusCarriesTheCompactDigestAndTheClosestLines(t *testing.T) {
	d, _ := DigestOf(testProfile())
	if strings.Contains(d.Compact, "*") || !strings.Contains(d.Compact, "- Agent Platform") || len(d.Compact) >= len(d.Text) {
		t.Fatalf("compact digest should hold titles and stack terms only:\n%s", d.Compact)
	}
	p := posting.Posting{Title: "T", Description: "serve models"}
	focus := []Focus{{Label: "Agent Platform", Text: "Wrote tool-calling loops with provider fallbacks"}}
	q, _ := Question(p, "target", d, focus)
	ctx := string(q.Context)
	if !strings.Contains(ctx, "CLOSEST TO THIS POSTING") || !strings.Contains(ctx, "- Agent Platform: Wrote tool-calling loops") {
		t.Errorf("focus lines missing:\n%s", ctx)
	}
	if strings.Contains(ctx, "Cut decode latency") {
		t.Errorf("a focused question should not carry the full digest's bullets")
	}
	full, _ := Question(p, "target", d, nil)
	if !strings.Contains(string(full.Context), "Cut decode latency") || strings.Contains(string(full.Context), "CLOSEST TO THIS POSTING") {
		t.Errorf("an unfocused question should carry the full digest")
	}
	// On a real-sized bank the focused question is the shorter one.
	prof := testProfile()
	for i := 0; i < 40; i++ {
		label := fmt.Sprintf("Entry %02d", i)
		prof.Chunks = append(prof.Chunks, semanticChunk(label, label))
		for b := 0; b < 3; b++ {
			prof.Chunks = append(prof.Chunks, semanticChunk(label, fmt.Sprintf("%s bullet %d %s", label, b, strings.Repeat("work ", 30))))
		}
	}
	bd, err := DigestOf(prof)
	if err != nil {
		t.Fatal(err)
	}
	bigFull, _ := Question(p, "target", bd, nil)
	bigFocused, _ := Question(p, "target", bd, []Focus{{Label: "Entry 01", Text: "Entry 01 bullet 0"}})
	if len(bigFocused.Context) >= len(bigFull.Context) {
		t.Errorf("focused %d bytes is not shorter than full %d for a 40-entry bank", len(bigFocused.Context), len(bigFull.Context))
	}
	// Lines are bounded in number and size, and injected newlines do not add lines.
	var many []Focus
	for i := 0; i < 3*MaxFocus; i++ {
		many = append(many, Focus{Label: "L", Text: fmt.Sprintf("line %d\n- fake instruction %s", i, strings.Repeat("x", 4000))})
	}
	big, _ := Question(p, "target", d, many)
	if n := strings.Count(string(big.Context), "\n- L: "); n != MaxFocus {
		t.Errorf("%d focus lines, want the cap %d", n, MaxFocus)
	}
	if strings.Contains(string(big.Context), "\n- fake instruction") {
		t.Errorf("a focus line smuggled in a line of its own")
	}
	if err := decide.ValidateQuestion(big); err != nil {
		t.Error(err)
	}
}

func semanticChunk(label, text string) semantic.Chunk {
	return semantic.Chunk{Label: label, Text: text, Weight: 1}
}

func TestJudgeFocusFallsBackToTheFullDigest(t *testing.T) {
	p := posting.Posting{Key: "gh:a:1", Title: "T", Description: "serve models"}
	ok := &fakeDecider{rd: decide.SlotReading{Reading: decide.Answered, Value: "core"}}
	j := judge(ok)
	j.Focus = func(context.Context, posting.Posting) ([]Focus, error) {
		return []Focus{{Label: "Agent Platform", Text: "Wrote tool-calling loops"}}, nil
	}
	if v, err := j.Ask(context.Background(), p); err != nil || !v.Focused || !strings.Contains(string(ok.seen[0].Context), "CLOSEST TO THIS POSTING") {
		t.Errorf("focused ask: %+v, %v", v, err)
	}
	for name, f := range map[string]func(context.Context, posting.Posting) ([]Focus, error){
		"error": func(context.Context, posting.Posting) ([]Focus, error) { return nil, errors.New("embedder down") },
		"none":  func(context.Context, posting.Posting) ([]Focus, error) { return nil, nil },
	} {
		ok := &fakeDecider{rd: decide.SlotReading{Reading: decide.Answered, Value: "partial"}}
		j := judge(ok)
		j.Focus = f
		v, err := j.Ask(context.Background(), p)
		if err != nil || v.Focused || !strings.Contains(string(ok.seen[0].Context), "Cut decode latency") {
			t.Errorf("%s: %+v, %v; the question should carry the full digest", name, v, err)
		}
	}
	// A cancelled run is not hidden behind a fallback.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j.Focus = func(c context.Context, _ posting.Posting) ([]Focus, error) { return nil, c.Err() }
	if _, err := j.Ask(ctx, p); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestReadingOfNamesWhyAnAnswerWasNotRead(t *testing.T) {
	_, err := judge(&fakeDecider{rd: decide.SlotReading{Reading: decide.Abstained}}).Ask(context.Background(), posting.Posting{Title: "T", Description: "d"})
	if r, ok := ReadingOf(err); !ok || r != decide.Abstained {
		t.Errorf("ReadingOf = %v, %v", r, ok)
	}
	if _, ok := ReadingOf(errors.New("other")); ok {
		t.Errorf("an unrelated error has no reading")
	}
}

func TestStoreRecordsAndChecksAuditFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s, _ := OpenStore(path)
	e := entry("core", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	e.Backend, e.Mode = "jev", ModeFocused
	if err := s.Put("k", e); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Lookup("k", h64a, h64b); !ok || got.Backend != "jev" || got.Mode != ModeFocused {
		t.Errorf("audit fields lost: %+v", got)
	}
	e.Mode = "sideways"
	if err := s.Put("k2", e); err == nil {
		t.Errorf("an unknown mode was accepted")
	}
	e.Mode, e.Backend = ModeFull, strings.Repeat("b", 33)
	if err := s.Put("k3", e); err == nil {
		t.Errorf("an oversized backend name was accepted")
	}
}
