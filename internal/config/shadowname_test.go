package config

import (
	"strings"
	"testing"
)

// The shadow's backend name carries the model and the question mode, so two
// candidate models, or one asked two ways, are two backends in the ledger.
func TestLocalBackendName(t *testing.T) {
	server := func(path string) *LocalServer { return &LocalServer{Binary: "/b", ModelPath: path} }
	for _, tc := range []struct {
		name string
		l    Local
		want string
	}{
		{"server mode names the file, not the model string", Local{Model: "x", Server: server("/m/d1-3B-Q8_0.gguf")}, "local-d1-3b-q8_0-combined"},
		{"upper-case extension", Local{Model: "x", Server: server("/m/D1.GGUF")}, "local-d1-combined"},
		{"two quantisations of one model differ", Local{Model: "x", Server: server("/m/d1-3B-Q4_K_M.gguf")}, "local-d1-3b-q4_k_m-combined"},
		{"per_slot is a different backend", Local{Model: "x", Server: server("/m/d1-3B-Q8_0.gguf"), Questions: "per_slot"}, "local-d1-3b-q8_0-per_slot"},
		{"endpoint mode names the model", Local{Endpoint: "http://127.0.0.1:1/x", Model: "LiquidAI/d1-3B-GGUF:Q8_0"}, "local-d1-3b-gguf-q8_0-combined"},
		{"no model", Local{Endpoint: "http://127.0.0.1:1/x"}, ""},
		{"nothing usable in the name", Local{Model: "///"}, ""},
	} {
		if got := tc.l.BackendName(); got != tc.want {
			t.Errorf("%s: BackendName() = %q, want %q", tc.name, got, tc.want)
		}
	}
	a := Local{Model: "x", Server: server("/m/a.gguf")}
	b := Local{Model: "x", Server: server("/m/b.gguf")}
	if a.BackendName() == b.BackendName() {
		t.Error("two model files share a backend name")
	}
}

func TestIsLocalShadow(t *testing.T) {
	for name, want := range map[string]bool{
		"local": true, "local-d1-combined": true, "local-": true,
		"llm": false, "lappi": false, "d1-ultra": false, "localhost": false, "": false, "unknown": false,
	} {
		if got := IsLocalShadow(name); got != want {
			t.Errorf("IsLocalShadow(%q) = %v, want %v", name, got, want)
		}
	}
}

// A name over the 32-byte limit is refused, never truncated: two truncated
// names would pool again. 32 bytes exactly is accepted.
func TestLocalShadowNameLimit(t *testing.T) {
	check := func(model string) error {
		c := valid()
		c.Decide.Shadow = "local"
		c.Decide.Local = &Local{
			Server: &LocalServer{Binary: "/b", ModelPath: "/m/" + model + ".gguf"}, Model: "m"}
		return c.Validate()
	}
	if err := check("vllm-d3-mini-Q8_0"); err != nil { // local-vllm-d3-mini-q8_0-combined: 32 bytes
		t.Fatalf("32-byte name refused: %v", err)
	}
	if n := (Local{Server: &LocalServer{Binary: "/b", ModelPath: "/m/vllm-d3-mini-Q8_0.gguf"}}).BackendName(); len(n) != MaxBackendName {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(n), MaxBackendName)
	}
	err := check("vllm-d3-mini-Q8_0-v2")
	if err == nil || !strings.Contains(err.Error(), "longer than 32 bytes") || !strings.Contains(err.Error(), "decide.local") {
		t.Fatalf("33+ byte name: err = %v", err)
	}
	if err := check("---"); err == nil || !strings.Contains(err.Error(), "no shadow backend name") {
		t.Fatalf("empty name: err = %v", err)
	}
}

// An OpenRouter model must not answer under a name the local shadow uses.
func TestOpenRouterNameCannotUseTheLocalPrefix(t *testing.T) {
	c := valid()
	c.Decide.OpenRouterModel = "someone/local-d1"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "local shadow's names") {
		t.Fatalf("err = %v", err)
	}
}
