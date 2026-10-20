package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// config refuses OpenRouter backend names the decide/jev client would refuse, so a
// config that checks clean never fails at jev.New over its name.
func TestBackendNameLimitMatchesJev(t *testing.T) {
	if config.MaxBackendName != jev.MaxNameBytes {
		t.Fatalf("config.MaxBackendName %d, jev.MaxNameBytes %d", config.MaxBackendName, jev.MaxNameBytes)
	}
	name := config.BackendName("acme/" + strings.Repeat("x", config.MaxBackendName))
	d, err := egress.NewLoopback(time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jev.New(jev.Config{Name: name, Model: "acme/m", Key: "k"}, d); err != nil {
		t.Fatalf("jev refused a %d-byte derived name: %v", len(name), err)
	}
}

// The run asks the OpenRouter model under its model's name, drops on that
// name's probability, and "openrouter" as the shadow selects it.
func TestRunNamesTheOpenRouterBackendAfterItsModel(t *testing.T) {
	cfg := reasonsConfig(t) // liquid/d1-ultra, no LLM
	cfg.DropRules = []config.DropRule{{Slot: "work_mode", Values: []string{"onsite"}, MinOpenRouterProb: 0.9}}
	r, warn := runnerFor(t, cfg, t.TempDir())
	rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if got := strings.Join(rt.Backends(), ","); got != "d1-ultra" || rt.Shadow != nil {
		t.Fatalf("chain %q shadow %v\n%s", got, rt.Shadow, warn)
	}
	if r.Policy.OpenRouter != "d1-ultra" || len(r.Policy.Rules) != 1 || r.Policy.Rules[0].MinOpenRouterProb != 0.9 {
		t.Fatalf("policy %+v", r.Policy)
	}

	cfg.LLM = testLLM
	cfg.Decide.DailyUSD.LLM = 1
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg.Decide.Shadow = config.ShadowOpenRouter
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r, warn = runnerFor(t, cfg, t.TempDir())
	rt = r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if got := strings.Join(rt.Backends(), ","); got != "llm" || rt.Shadow == nil || rt.Shadow.Name() != "d1-ultra" {
		t.Fatalf("shadow openrouter: chain %q shadow %v\n%s", got, rt.Shadow, warn)
	}
}

// A config still written with the old keys runs exactly as the renamed one,
// and `vani config check` says which keys to rename.
func TestLegacyConfigWarnsAndRunsTheSame(t *testing.T) {
	cfgPath, _ := setup(t)
	cfg := config.Default()
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.NewReplacer(`"openrouter_model"`, `"jev_model"`, `"openrouter_key_env"`, `"jev_key_env"`, `"openrouter": 0.25`, `"jev": 0.25`).Replace(string(data))
	if legacy == string(data) {
		t.Fatalf("fixture did not change:\n%s", data)
	}
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustCode(t, 0, "config", "check")
	if !strings.Contains(out, "ok: ") {
		t.Fatalf("stdout %q", out)
	}
	for _, want := range []string{"decide.jev_model is now decide.openrouter_model", "decide.jev_key_env is now decide.openrouter_key_env", "decide.daily_usd.jev is now decide.daily_usd.openrouter"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decide != cfg.Decide {
		t.Fatalf("legacy decide %+v, renamed %+v", got.Decide, cfg.Decide)
	}
}
