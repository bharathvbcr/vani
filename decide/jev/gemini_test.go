package jev

import (
	"context"
	"encoding/json/v2"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/decide"
)

// geminiResp is a Gemini Decisions reply in the documented shape
// (aistudio.google.com/docs/decisions-api, read 2026-11-17): a choice answer
// carries choice, probabilities and confidence; usage counts
// total_input_tokens and bills nothing else.
const geminiResp = `{"answers":{
"mode":{"type":"choice","choice":"remote","probabilities":{"remote":0.99,"onsite":0.01},"confidence":0.99},
"level":{"type":"choice","choice":"senior","probabilities":{"junior":0.3,"senior":0.7},"confidence":0.7}},
"usage":{"total_input_tokens":1000000,"input_tokens_by_modality":[{"modality":"text","tokens":1000000}],"total_output_tokens":0,"total_tokens":1000000}}`

func geminiConfig(sp decide.Spend) Config {
	return Config{Endpoint: "https://generativelanguage.googleapis.com/v1beta/decisions", Model: "gemini-decision-1",
		Key: testKey, Name: "gemini-decision-1", Wire: WireGemini, Spend: sp, InputUSDPerMTok: 0.10}
}

func TestGeminiRequestShapeAndAuth(t *testing.T) {
	sp := &fakeSpend{}
	b, d, _ := newBackend(t, geminiConfig(sp), ok(geminiResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil {
		t.Fatal(err)
	}
	req := d.reqs[0]
	if req.URL.Host != "generativelanguage.googleapis.com" || req.URL.Path != "/v1beta/decisions" {
		t.Errorf("url = %s", req.URL)
	}
	if got := req.Header.Get("x-goog-api-key"); got != testKey {
		t.Errorf("x-goog-api-key = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization must not be sent to Google: %q", got)
	}
	want := `{"input":"hello world","model":"gemini-decision-1","questions":{` +
		`"level":{"criteria":{"junior":"junior","senior":"senior"},"instructions":"Classify it.","type":"choice"},` +
		`"mode":{"criteria":{"onsite":"onsite","remote":"remote"},"instructions":"Classify it.","type":"choice"}}}`
	if d.bodies[0] != want {
		t.Errorf("body\n got %s\nwant %s", d.bodies[0], want)
	}
	if strings.Contains(d.bodies[0], `"state"`) {
		t.Error("gemini body carries state")
	}
	if got := a.Slots["mode"]; got.Reading != decide.Answered || got.Value != "remote" || got.Jev.Probability != 0.99 {
		t.Errorf("mode = %+v", got)
	}
	// 1M input tokens at $0.10 per million.
	if math.Abs(a.CostUSD-0.10) > 1e-12 || len(sp.charged) != 1 {
		t.Errorf("cost = %v charged = %v", a.CostUSD, sp.charged)
	}
}

func TestGeminiNonStringStateIsSentAsJSONText(t *testing.T) {
	cfg := geminiConfig(nil)
	cfg.State = func(q decide.Question) any { return map[string]string{"text": string(q.Context)} }
	b, d, _ := newBackend(t, cfg, ok(geminiResp))
	if _, err := b.Decide(context.Background(), question()); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(d.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Input != `{"text":"hello world"}` {
		t.Errorf("input = %q", body.Input)
	}
}

func TestGeminiSafetyBlockIsRefusedNotAnOutage(t *testing.T) {
	sp := &fakeSpend{}
	b, d, _ := newBackend(t, geminiConfig(sp), step{status: 400,
		body: `{"error":{"code":"safety","message":"Request blocked due to safety violations (harmful content). Please modify your input and retry."}}`})
	a, err := b.Decide(context.Background(), question())
	if err != nil {
		t.Fatalf("a refusal is an answer, not an error: %v", err)
	}
	for n, rd := range a.Slots {
		if rd.Reading != decide.Refused || rd.Detail != "safety:safety" {
			t.Errorf("%s = %+v", n, rd)
		}
	}
	if len(d.reqs) != 1 || len(sp.charged) != 0 || !b.Serves("x") {
		t.Errorf("reqs=%d charged=%v serves=%v", len(d.reqs), sp.charged, b.Serves("x"))
	}
}

func TestGeminiOtherBadRequestStaysAnError(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"invalid_argument","message":"question key too long"}}`,
		`{"error":{"code":400,"message":"x"}}`,
		`not json`,
		``,
	} {
		b, d, _ := newBackend(t, geminiConfig(nil), step{status: 400, body: body})
		a, err := b.Decide(context.Background(), question())
		if err == nil || a.Slots["mode"].Reading != decide.Unavailable || len(d.reqs) != 1 {
			t.Errorf("%q: err=%v mode=%+v reqs=%d", body, err, a.Slots["mode"], len(d.reqs))
		}
	}
	// The safety code means nothing to a systemone endpoint: there a 400 is a fault.
	cfg := subqConfig(nil, 0.04)
	b, _, _ := newBackend(t, cfg, step{status: 400, body: `{"error":{"code":"safety"}}`})
	if _, err := b.Decide(context.Background(), question()); err == nil {
		t.Error("systemone 400 read as a refusal")
	}
}

func TestGeminiRefusesBadKeysBeforeNetworkOrBudget(t *testing.T) {
	reached := 0
	defer func() {
		if reached == 0 {
			t.Error("every case was refused by decide.ValidateQuestion; the gemini key check was never exercised")
		}
	}()
	for name, q := range map[string]decide.Question{
		"long question key": {Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: strings.Repeat("k", 65), Options: []string{"a", "b"}}}},
		"dotted key":        {Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: "a.b", Options: []string{"a", "b"}}}},
		"padded option":     {Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: "k", Options: []string{" a", "b"}}}},
		"long option":       {Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: "k", Options: []string{strings.Repeat("o", 129), "b"}}}},
		"tab option":        {Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: "k", Options: []string{"a\tb", "b"}}}},
	} {
		if decide.ValidateQuestion(q) != nil {
			continue // decide refuses it already; nothing for the wire to add
		}
		reached++
		t.Logf("%s reaches the gemini key check", name)
		sp := &fakeSpend{}
		b, d, _ := newBackend(t, geminiConfig(sp), ok(geminiResp))
		a, err := b.Decide(context.Background(), q)
		if err == nil || len(d.reqs) != 0 || len(sp.charged) != 0 {
			t.Errorf("%s: err=%v reqs=%d", name, err, len(d.reqs))
		}
		for _, rd := range a.Slots {
			if rd.Reading != decide.Refused {
				t.Errorf("%s: reading %+v", name, rd)
			}
		}
	}
	// The documented boundaries are accepted.
	ok64 := decide.Question{Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: strings.Repeat("K", 64), Options: []string{strings.Repeat("o", 128), "two words"}}}}
	if err := checkGeminiKeys(ok64); err != nil {
		t.Errorf("boundary refused: %v", err)
	}
}

func TestGeminiEmptyInputIsNeverSent(t *testing.T) {
	q := question()
	q.Context = []byte("  \n ")
	b, d, _ := newBackend(t, geminiConfig(nil), ok(geminiResp))
	a, err := b.Decide(context.Background(), q)
	if err != nil || len(d.reqs) != 0 || a.Slots["mode"].Reading != decide.Refused {
		t.Errorf("err=%v reqs=%d mode=%+v", err, len(d.reqs), a.Slots["mode"])
	}
}

func TestUnknownWireRefused(t *testing.T) {
	c := geminiConfig(nil)
	c.Wire = "anthropic"
	if err := c.Validate(); err == nil {
		t.Error("unknown wire accepted")
	}
}
