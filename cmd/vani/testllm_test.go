package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// testLLM is the llm the tests that need one configure. config.Default() names
// none, so a fresh config spends nothing on a language model.
var testLLM = config.LLM{Provider: "gemini", Model: "gemini-3.8-flash"}

// defaultWithLLM is config.Default() with testLLM.
func defaultWithLLM() config.Config {
	c := config.Default()
	c.LLM = testLLM
	return c
}

// initWithLLM runs `vani init` and gives the written config testLLM.
func initWithLLM(t *testing.T, cfgPath string) {
	t.Helper()
	mustCode(t, 0, "init")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c.LLM = testLLM
	if err := config.Save(cfgPath, c); err != nil {
		t.Fatal(err)
	}
}

// A fresh config names the decision model and no llm.
func TestDefaultNamesNoLLM(t *testing.T) {
	c := config.Default()
	if c.LLM != (config.LLM{}) {
		t.Errorf("Default llm %+v, want none", c.LLM)
	}
	if c.Decide.OpenRouterModel != "liquid/d1-ultra" || c.Decide.OpenRouterName() != "d1-ultra" {
		t.Errorf("Default decision model %q (%q), want liquid/d1-ultra", c.Decide.OpenRouterModel, c.Decide.OpenRouterName())
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Default invalid: %v", err)
	}
}
