package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/contacts"
	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/vault"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

const providerKeySentinel = "PROVIDER-KEY-SENTINEL-never-in-a-message"

// recordingDoer answers every request with one decision reply and keeps the
// requests it saw.
type recordingDoer struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	body   string
}

func (d *recordingDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.bodies = append(d.bodies, raw)
	d.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(d.body))}, nil
}

func providerWired(t *testing.T, provider string) *wired {
	t.Helper()
	w, _ := keyWired(t)
	w.cfg.Decide.Provider = provider
	return w
}

func oneSlotQuestion() decide.Question {
	return decide.Question{Task: "t", Prompt: "p", Context: []byte("c"), Slots: []decide.Slot{{Name: "kind", Options: []string{"a", "b"}}}}
}

// Each provider's client reaches its own host, with its key in its own header,
// under the model's name, and a Keychain key is read from the provider's item.
func TestDecisionJevReachesEachProvider(t *testing.T) {
	for _, tc := range []struct {
		provider, env, host, path, header, name string
	}{
		{config.DecisionSubQ, "SUBQ_API_KEY", "api.subq.ai", "/v1/systemone", "Authorization", "subq-snap-1-2"},
		{config.DecisionGemini, "GEMINI_API_KEY", "generativelanguage.googleapis.com", "/v1beta/decisions", "x-goog-api-key", "gemini-decision-1"},
		{config.DecisionOpenRouter, "OPENROUTER_API_KEY", "openrouter.ai", "/api/alpha/decisions", "Authorization", "d1-2-ultra"},
	} {
		w := providerWired(t, tc.provider)
		t.Setenv(tc.env, providerKeySentinel)
		jc, err := w.decisionJev(jev.Config{}, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		d := &recordingDoer{body: `{"answers":{"kind":{"choice":"a","confidence":0.9}},"usage":{"input_tokens":10,"total_input_tokens":10}}`}
		jb, err := jev.New(jc, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		a, err := jb.Decide(context.Background(), oneSlotQuestion())
		if err != nil || a.Slots["kind"].Reading != decide.Answered {
			t.Fatalf("%s: %v %+v", tc.provider, err, a)
		}
		req := d.reqs[0]
		if req.URL.Host != tc.host || req.URL.Path != tc.path {
			t.Errorf("%s: sent to %s", tc.provider, req.URL)
		}
		want := providerKeySentinel
		if tc.header == "Authorization" {
			want = "Bearer " + want
		}
		if got := req.Header.Get(tc.header); got != want {
			t.Errorf("%s: %s = %q", tc.provider, tc.header, got)
		}
		if a.Backend != tc.name {
			t.Errorf("%s: backend %q", tc.provider, a.Backend)
		}
	}
}

func TestDecisionKeyComesFromTheProvidersKeychainItem(t *testing.T) {
	for provider, service := range map[string]string{config.DecisionSubQ: vault.SubQService, config.DecisionGemini: vault.GeminiService} {
		w := providerWired(t, provider)
		var asked []string
		old := keychainRead
		keychainRead = func(_ context.Context, s string) (string, error) {
			asked = append(asked, s)
			return providerKeySentinel, nil
		}
		t.Setenv(noKeychainEnv, "")
		jc, err := w.decisionJev(jev.Config{}, "")
		keychainRead = old
		if err != nil || jc.Key != providerKeySentinel || len(asked) != 1 || asked[0] != service {
			t.Errorf("%s: err=%v asked=%v", provider, err, asked)
		}
		// A key held for the process is not read again.
		if k, from, _ := w.decisionKey(); k != providerKeySentinel || from != keyFromKeychain || len(asked) != 1 {
			t.Errorf("%s: second read %q %q asked=%v", provider, k, from, asked)
		}
	}
}

func TestGeminiDecisionTakesEitherAIStudioVariable(t *testing.T) {
	w := providerWired(t, config.DecisionGemini)
	t.Setenv("GOOGLE_API_KEY", providerKeySentinel)
	if jc, err := w.decisionJev(jev.Config{}, ""); err != nil || jc.Key != providerKeySentinel {
		t.Errorf("GOOGLE_API_KEY: %v", err)
	}
}

func TestDecisionJevWithoutAKeyIsOffAndSaysWhy(t *testing.T) {
	for _, provider := range []string{config.DecisionSubQ, config.DecisionGemini} {
		w := providerWired(t, provider)
		t.Setenv(noKeychainEnv, "1")
		_, err := w.decisionJev(jev.Config{}, "")
		if !errors.Is(err, errNoDecisionKey) || !strings.Contains(err.Error(), noKeychainEnv) || !strings.Contains(err.Error(), "_API_KEY") {
			t.Errorf("%s: %v", provider, err)
		}
	}
	// The env key of one provider is never taken for another's.
	w := providerWired(t, config.DecisionSubQ)
	t.Setenv(noKeychainEnv, "1")
	t.Setenv("OPENROUTER_API_KEY", providerKeySentinel)
	t.Setenv("GEMINI_API_KEY", providerKeySentinel)
	if jc, err := w.decisionJev(jev.Config{}, ""); err == nil || jc.Key != "" {
		t.Errorf("subq took another provider's key: %v", err)
	}
}

// Every client vani builds for the decision model goes through decisionJev;
// the only jev.Config literal left is the local llama.cpp shadow, which never
// carries a key.
func TestOneConstructorForTheDecisionModel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	literal := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(string(src), "jev.New(jev.Config{")
		if n > 0 && e.Name() != "wire.go" {
			t.Errorf("%s builds a decision client by hand (%d); use decisionJev", e.Name(), n)
		}
		literal += n
		for _, banned := range []string{"Decide.OpenRouterKeyEnv,", "Getenv:       w.openRouterGetenv"} {
			if e.Name() != "decisionwire.go" && strings.Contains(string(src), banned) {
				t.Errorf("%s wires the OpenRouter key itself (%q)", e.Name(), banned)
			}
		}
	}
	if literal != 1 {
		t.Errorf("%d jev.Config literals, want the local shadow's only", literal)
	}
}

// Every question a decision-model client sends (decisionJev's callers: the
// run, suggest, contacts, notes and kbmatch; wouldapply, silver and the auditor
// ask the llm) fits both new providers'
// documented limits: SubQ takes 1..128 questions named in up to 100
// characters; Gemini takes question keys of [A-Za-z0-9_-]{1,64} and option
// keys of 1..128 characters with no leading or trailing space.
func TestEveryQuestionFitsEachProvidersLimits(t *testing.T) {
	geminiKey := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	unified := jobcategory.UnifiedQuestion(posting.Posting{Title: "Engineer", Description: "Build things."}, "")
	reasonsOpts := []string{strings.Repeat("a", jdreasons.MaxIDBytes), jdreasons.NoneID}
	questions := map[string][]decide.Slot{
		"triage (unified)": unified.Slots,
		"field triage":     fieldtriage.Question(fieldtriage.Field{ID: "f", Label: "Email", Kind: "email"}).Slots,
		"kbmatch":          {{Name: kbmatch.Slot, Options: append([]string(nil), jobinsight.KBMatchOrder[:]...)}},
		"contacts":         {{Name: contacts.Slot, Options: append(append([]string(nil), contacts.Keys...), contacts.None)}},
		"reasons":          {{Name: jdreasons.SlotFit, Options: reasonsOpts}, {Name: jdreasons.SlotGap, Options: reasonsOpts}, {Name: jdreasons.SlotCheck, Options: reasonsOpts}},
	}
	if jdreasons.MaxItems+1 > 255 {
		t.Errorf("reasons offer up to %d options; both providers take at most 255", jdreasons.MaxItems+1)
	}
	for name, slots := range questions {
		if len(slots) == 0 || len(slots) > 128 {
			t.Errorf("%s: %d questions", name, len(slots))
		}
		for _, s := range slots {
			if !geminiKey.MatchString(s.Name) || len(s.Name) > 100 {
				t.Errorf("%s: question key %q", name, s.Name)
			}
			if len(s.Options) < 2 || len(s.Options) > 255 {
				t.Errorf("%s/%s: %d options", name, s.Name, len(s.Options))
			}
			for _, o := range s.Options {
				if o == "" || utf8.RuneCountInString(o) > 128 || len(o) > 512 || o != strings.TrimSpace(o) {
					t.Errorf("%s/%s: option key %q", name, s.Name, o)
				}
			}
		}
	}
}

// The Mac app reads each provider's key from the same Keychain item and hands
// it over in the same variable vani reads first (Drafting.swift's
// Keychain/ModelKeyRoute); a rename on one side alone would launch every vani
// keyless.
func TestSwiftKeyRoutesMatchTheProviderTable(t *testing.T) {
	src, err := os.ReadFile("../../macos/Sources/VaniApplyKit/Drafting.swift")
	if err != nil {
		t.Fatalf("the Swift launcher could not be read, so the routes were not compared: %v", err)
	}
	for _, p := range config.DecisionProviders() {
		env := "OPENROUTER_API_KEY"
		if len(p.KeyEnvs) > 0 {
			env = p.KeyEnvs[0]
		}
		for _, want := range []string{`"` + p.KeychainService + `"`, `"` + env + `"`} {
			if !strings.Contains(string(src), want) {
				t.Errorf("%s: Drafting.swift does not name %s", p.ID, want)
			}
		}
	}
}

// The run asks SubQ and Gemini each slot's own question; OpenRouter keeps the
// one combined prompt. Each slot of the unified question has a distinct,
// non-empty question.
func TestTriageInstructionsPerProvider(t *testing.T) {
	if got := triageInstructions(config.Decide{OpenRouterModel: "liquid/d1.2-ultra"}); got != nil {
		t.Errorf("openrouter got per-slot questions: %v", got)
	}
	q := jobcategory.UnifiedQuestion(posting.Posting{Title: "Engineer", Description: "Build things."}, "")
	for _, provider := range []string{config.DecisionSubQ, config.DecisionGemini} {
		instr := triageInstructions(config.Decide{Provider: provider})
		seen := map[string]string{}
		for _, s := range q.Slots {
			text := instr[s.Name]
			if strings.TrimSpace(text) == "" || text == q.Prompt {
				t.Errorf("%s: slot %s asks %q", provider, s.Name, text)
			}
			if other, dup := seen[text]; dup {
				t.Errorf("%s: slots %s and %s ask the same question", provider, s.Name, other)
			}
			seen[text] = s.Name
		}
		if len(instr) != len(q.Slots) {
			t.Errorf("%s: %d questions for %d slots", provider, len(instr), len(q.Slots))
		}
	}
	// End to end through the Gemini wire: each slot carries its own instructions.
	w := providerWired(t, config.DecisionGemini)
	t.Setenv("GEMINI_API_KEY", providerKeySentinel)
	jc, err := w.decisionJev(jev.Config{Descriptions: jobcategory.UnifiedDescriptions(), Instructions: triageInstructions(w.cfg.Decide)}, "")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"answers":{},"usage":{"total_input_tokens":1}}`
	d := &recordingDoer{body: body}
	jb, err := jev.New(jc, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jb.Decide(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Questions map[string]struct {
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(d.bodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Questions) != len(q.Slots) {
		t.Fatalf("%d questions sent for %d slots", len(sent.Questions), len(q.Slots))
	}
	asked := map[string]bool{}
	for name, qq := range sent.Questions {
		if qq.Instructions == q.Prompt || asked[qq.Instructions] {
			t.Errorf("slot %s sent %q", name, qq.Instructions)
		}
		asked[qq.Instructions] = true
	}
}
