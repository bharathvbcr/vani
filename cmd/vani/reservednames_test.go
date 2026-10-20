package main

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// llmbackend and config cannot import each other (an import cycle), so each
// keeps its own list of the names other
// backends answer under. They must stay the same set, apart from the one name
// the LLM backend owns by default: if llmbackend accepted a name config
// reserves, an LLM backend and an OpenRouter model could file readings under one
// name; if config accepted one llmbackend reserves, the reverse.
func TestReservedBackendNamesAgreeAcrossPackages(t *testing.T) {
	cfg := slices.Sorted(slices.Values(config.ReservedBackendNames()))
	cfg = slices.DeleteFunc(cfg, func(n string) bool { return n == llmbackend.DefaultName })
	llm := slices.Sorted(slices.Values(llmbackend.ReservedNames()))
	if !slices.Equal(cfg, llm) {
		t.Fatalf("config reserves %v (less %q), llmbackend reserves %v", cfg, llmbackend.DefaultName, llm)
	}
	if !slices.Contains(config.ReservedBackendNames(), llmbackend.DefaultName) {
		t.Fatalf("config does not reserve %q, the LLM backend's default name", llmbackend.DefaultName)
	}

	// Every reserved name is refused by the side that does not own it.
	for _, n := range config.ReservedBackendNames() {
		if config.CheckBackendName(n) == nil {
			t.Errorf("config accepts the reserved name %q", n)
		}
		if n != llmbackend.DefaultName && llmbackend.CheckName(n) == nil {
			t.Errorf("llmbackend accepts the reserved name %q", n)
		}
	}
	// The local shadow's prefix is refused on both sides.
	if config.CheckBackendName("local-x") == nil || llmbackend.CheckName("local-x") == nil {
		t.Error("the local- prefix is accepted by one side")
	}
	// The escalation tier's name is no one's: both sides accept it, and it is
	// not a reserved name for either.
	if err := config.CheckBackendName(escalateName); err != nil {
		t.Errorf("config refuses %q: %v", escalateName, err)
	}
	if err := llmbackend.CheckName(escalateName); err != nil {
		t.Errorf("llmbackend refuses %q: %v", escalateName, err)
	}
}
