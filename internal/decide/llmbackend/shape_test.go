package llmbackend

import (
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
)

// anthropicCaps answers Capability from Manvi's real Anthropic catalogue.
type anthropicCaps struct{ fakeProvider }

func (*anthropicCaps) Name() string { return anthropic.Name }
func (*anthropicCaps) Capability(model string) (llm.Capability, bool) {
	return anthropic.Capability(model)
}

// Opus 5.5 rejects temperature with a 400 and Manvi refuses the request before
// sending; a shaped request must pass the adapter's own pre-flight check.
func TestShapePassesAnthropicPreflight(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		req := llm.Request{Model: model, MaxTokens: DefaultMaxTokens, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
		Shape(&anthropicCaps{}, &req)
		if req.Temperature != nil {
			t.Errorf("%s: temperature set", model)
		}
		if req.Effort != "low" || req.MaxTokens != DefaultMaxTokens+ReasoningHeadroom {
			t.Errorf("%s: effort %q max %d", model, req.Effort, req.MaxTokens)
		}
		for _, mode := range []anthropic.ThinkingMode{anthropic.ThinkingDefault, anthropic.ThinkingAdaptive} {
			if err := anthropic.ValidateRequest(req, mode); err != nil {
				t.Errorf("%s (%q): %v", model, mode, err)
			}
		}
	}
	// The unshaped request is what the backend used to send, and Manvi refuses it.
	zero := 0.0
	old := llm.Request{Model: "claude-opus-5-5", MaxTokens: DefaultMaxTokens, Temperature: &zero, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
	if anthropic.ValidateRequest(old, anthropic.ThinkingDefault) == nil {
		t.Error("a temperature-0 request should be refused; the test no longer proves the bug")
	}
}

// reasoningCaps is a reasoning Anthropic model with the given output ceiling.
type reasoningCaps struct {
	fakeProvider
	ceiling int
}

func (p *reasoningCaps) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "anthropic", Model: model, MaxOutputTokens: p.ceiling,
		SupportsReasoning: true, EffortLevels: []string{"low", "high"}}, true
}

// The reasoning headroom must not push a request over the model's output
// ceiling, which Manvi refuses before sending.
func TestShapeClampsHeadroomToTheOutputCeiling(t *testing.T) {
	for _, tc := range []struct{ ceiling, in, want int }{
		{2048, 1000, 1000 + ReasoningHeadroom}, // fits: full headroom
		{2048, 1500, 2048},                     // clamped to the ceiling
		{2048, 2048, 2048},                     // no room for headroom, budget kept
		{2048, 3000, 3000},                     // already over: left for Manvi to refuse, never lowered
		{0, 1500, 1500 + ReasoningHeadroom},    // no declared ceiling
	} {
		p := &reasoningCaps{ceiling: tc.ceiling}
		req := llm.Request{Model: "m", MaxTokens: tc.in}
		Shape(p, &req)
		if req.MaxTokens != tc.want {
			t.Errorf("ceiling %d, max %d: shaped to %d, want %d", tc.ceiling, tc.in, req.MaxTokens, tc.want)
		}
		c, _ := p.Capability("m")
		if tc.in <= tc.ceiling || tc.ceiling == 0 {
			if err := c.Validate(req); err != nil {
				t.Errorf("ceiling %d, max %d: Manvi refuses the shaped request: %v", tc.ceiling, tc.in, err)
			}
		}
	}
}

func TestShapeKeepsTemperatureZeroElsewhere(t *testing.T) {
	req := llm.Request{Model: "m", MaxTokens: 100}
	Shape(&fakeProvider{}, &req)
	if req.Temperature == nil || *req.Temperature != 0 || req.Effort != "" || req.MaxTokens != 100 {
		t.Errorf("non-anthropic request = %+v", req)
	}
}
