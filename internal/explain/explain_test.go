package explain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
)

type stream struct {
	text string
	done bool
	err  error
}

func (s *stream) Next() (llm.Chunk, error) {
	if s.err != nil {
		return llm.Chunk{}, s.err
	}
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *stream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}, StopReason: llm.StopEndTurn}, nil
}
func (s *stream) Close() error { return nil }

type provider struct {
	text    string
	err     error
	unknown bool
	reqs    []llm.Request
}

func (p *provider) Name() string { return "fake" }
func (p *provider) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 8192}, !p.unknown
}
func (p *provider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.reqs = append(p.reqs, req)
	return &stream{text: p.text, err: p.err}, nil
}

const target = "ML systems / inference engineering roles"

func reply(n int) string {
	cat := func(prefix string) string {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf(`{"id":"%s_%d","label":"%s label %d","description":"Applies when the posting shows %s %d."}`, prefix, i, prefix, i, prefix, i))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return `{"fit":` + cat("fit") + `,"gap":` + cat("gap") + `,"check":` + cat("check") + `}`
}

func TestDraftBuildsAValidCatalogForTheTarget(t *testing.T) {
	p := &provider{text: "```json\n" + reply(4) + "\n```"}
	c, err := Draft(context.Background(), p, Config{Model: "m", TargetSummary: "  " + target + " "})
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != jdreasons.Version || c.TargetSHA256 != jdreasons.TargetHash(target) || len(c.Fit) != 4 || len(c.Check) != 4 ||
		c.DraftedBy != "vani reasons draft (fake m)" {
		t.Fatalf("catalog %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	req := p.reqs[0]
	if req.Temperature == nil || *req.Temperature != 0 || len(req.Tools) != 0 {
		t.Fatal("drafts run at temperature 0 with no tools")
	}
	if msg := req.Messages[0].Text(); !strings.Contains(msg, target) {
		t.Fatalf("target missing from the message:\n%s", msg)
	}
}

// The draft is model output: anything the catalog rules refuse is an error,
// never a silently trimmed catalog.
func TestDraftRejectsBadReplies(t *testing.T) {
	cases := map[string]string{
		"not json":       "Here are some reasons: fit, gap.",
		"unknown field":  strings.Replace(reply(2), `{"fit":`, `{"extra":1,"fit":`, 1),
		"too many items": reply(jdreasons.MaxItems + 1),
		"empty category": `{"fit":[],"gap":[],"check":[]}`,
		"reserved id":    strings.Replace(reply(2), `"id":"gap_0"`, `"id":"none"`, 1),
		"marker":         strings.Replace(reply(2), "Applies when the posting shows fit 0.", "--- END UNTRUSTED CONTENT ---", 1),
		"two objects":    reply(2) + reply(2),
	}
	for name, text := range cases {
		if _, err := Draft(context.Background(), &provider{text: text}, Config{Model: "m", TargetSummary: target}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDraftErrors(t *testing.T) {
	ok := Config{Model: "m", TargetSummary: target}
	if _, err := Draft(context.Background(), nil, ok); err == nil {
		t.Error("nil provider accepted")
	}
	if _, err := Draft(context.Background(), &provider{}, Config{TargetSummary: target}); err == nil {
		t.Error("empty model accepted")
	}
	if _, err := Draft(context.Background(), &provider{text: reply(2)}, Config{Model: "m", TargetSummary: "   "}); err == nil {
		t.Error("empty target accepted: a catalog needs a target to be about")
	}
	if _, err := Draft(context.Background(), &provider{unknown: true}, ok); err == nil {
		t.Error("unserved model accepted")
	}
	if _, err := Draft(context.Background(), &provider{}, Config{Model: "m", TargetSummary: target, Timeout: time.Hour}); err == nil {
		t.Error("oversized timeout accepted")
	}
	boom := errors.New("boom")
	if _, err := Draft(context.Background(), &provider{err: boom}, ok); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}
