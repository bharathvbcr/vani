package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
)

// strings returns every string leaf of a decoded JSON value.
func jsonStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			jsonStrings(e, out)
		}
	case map[string]any:
		for _, e := range x {
			jsonStrings(e, out)
		}
	}
}

// The test that matters: what leaves the machine. A fake egress target (a local
// server the decision model's real client posts to) records the HTTP request
// bodies, so this reads the bytes the provider would receive, after the
// backend has framed them, not what a stub decider was handed. The owner's list
// holds terms the synthetic bank spells in other forms (a plural, a space for a
// hyphen); the owner's part of every body must hold none, and must still hold
// the harmless knowledge-base text and the redaction marker, so an empty or
// unrelated body cannot pass.
func TestKBMatchProviderBytesCarryNoOwnerTerm(t *testing.T) {
	k := twoPostings(t)
	clearKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	k.cfg.LLM = config.LLM{}
	k.cfg.Decide.DailyUSD.LLM = 0
	k.cfg.Decide.OpenRouterModel = "liquid/d1-ultra"
	k.cfg.Decide.DailyUSD.OpenRouter = 5
	k.cfg.KBMatch.Backend = config.KBMatchOpenRouter
	k.save(t)
	writeKBWords(t, k.state, "kernel", "decode-latency", "request-batching", "Zephyrnet")

	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		_, _ = io.WriteString(w, `{"answers":{"kb_match":{"choice":"core","confidence":0.9}}}`)
	}))
	defer srv.Close()
	oldE, oldD, oldF := kbmatchEndpoint, kbmatchDoer, kbmatchFocus
	t.Cleanup(func() { kbmatchEndpoint, kbmatchDoer, kbmatchFocus = oldE, oldD, oldF })
	kbmatchEndpoint = srv.URL
	kbmatchDoer = func(config.Config) (egress.Doer, error) { return egress.NewLoopback(5*time.Second, 0) }

	mustCode(t, 0, "kbmatch", "run")
	if len(bodies) != 2 {
		t.Fatalf("%d requests reached the provider, want 2", len(bodies))
	}
	for i, raw := range bodies {
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("request %d is not JSON: %v", i, err)
		}
		var leaves []string
		jsonStrings(doc, &leaves)
		var owner, posted string
		for _, s := range leaves {
			if o, p, ok := strings.Cut(s, "\nJOB POSTING:\n"); ok {
				owner, posted = o, p
			}
		}
		if owner == "" {
			t.Fatalf("request %d carries no kb_match question:\n%s", i, raw)
		}
		lower := strings.ToLower(owner)
		for _, term := range []string{"kernel", "decode latency", "decode-latency", "decodelatency", "request batching", "request-batching", "zephyrnet"} {
			if strings.Contains(lower, term) {
				t.Errorf("request %d: %q reached the provider (owner part):\n%s", i, term, owner)
			}
		}
		for _, want := range []string{"[redacted]", "LLM Inference Runtime", "CUDA", "scheduler"} {
			if !strings.Contains(owner, want) {
				t.Errorf("request %d: owner part lacks %q (an empty or unrelated body would pass without this):\n%s", i, want, owner)
			}
		}
		// The employer's posting text is not the owner's: it is neither redacted nor checked.
		if !strings.Contains(posted, "GPU kernels") {
			t.Errorf("request %d: the posting section was rewritten:\n%s", i, posted)
		}
	}
}

// An identifying-words file that exists but holds no terms is as good as none:
// it sends nothing unless the flag is passed (F4).
func TestKBMatchRunRefusesAnEmptyIdentifyingWordsFile(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})}
	useFake(t, f)
	for name, lines := range map[string][]string{
		"blank":        {""},
		"comment only": {"# my names go here", "", "   "},
	} {
		writeKBWords(t, k.state, lines...)
		for _, args := range [][]string{{"kbmatch", "run"}, {"kbmatch", "run", "--dry-run"}} {
			code, _, stderr := do(t, args...)
			if code == 0 || !strings.Contains(stderr, "identifying-words.txt") || !strings.Contains(stderr, "no terms") {
				t.Errorf("%s %v: exit %d, stderr %s", name, args, code, stderr)
			}
		}
	}
	if len(f.contexts) != 0 {
		t.Fatalf("a refused run sent %d question(s)", len(f.contexts))
	}
	o, _ := mustCode(t, 0, "kbmatch", "run", "--allow-missing-identifying-words", "--json")
	if !strings.Contains(o, `"identifying_words":"empty_allowed"`) || len(f.contexts) != 2 {
		t.Fatalf("allowed run (sent %d):\n%s", len(f.contexts), o)
	}
	// Status reads and sends nothing: an empty file is reported, not refused.
	o, _ = mustCode(t, 0, "kbmatch", "status", "--json")
	if !strings.Contains(o, `"identifying_words":"empty"`) {
		t.Fatalf("status:\n%s", o)
	}
}

func TestTeacherKBMatchRefusesAnEmptyIdentifyingWordsFile(t *testing.T) {
	e := tkSetup(t)
	writeKBWords(t, e.state, "# nothing yet")
	f := &tcFake{}
	tcUse(t, f)
	for _, args := range [][]string{
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run"},
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5"},
	} {
		code, _, stderr := do(t, args...)
		if code == 0 || !strings.Contains(stderr, "no terms") {
			t.Errorf("%v: exit %d, stderr %s", args, code, stderr)
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("a refused run sent %d", len(f.sent))
	}
}

// The words-file error names a line, never its content: it reaches --json and
// the ranking lookup's reason (F5).
func TestKBMatchWordsErrorsNeverQuoteTheFile(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(nil)}
	useFake(t, f)
	f.answer = byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})
	mustCode(t, 0, "kbmatch", "run") // so the store holds answers and the ranking lookup reads the list
	secret := "zephyrnetquillbridgelabsandmore"
	if err := os.WriteFile(filepath.Join(k.state, "tracks", "identifying-words.txt"), []byte("fine\n"+secret+strings.Repeat("x", 300)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"kbmatch", "run", "--json"}, {"kbmatch", "run"}, {"kbmatch", "status", "--json"}} {
		code, stdout, stderr := do(t, args...)
		if code == 0 {
			t.Errorf("%v accepted an over-long line", args)
		}
		for _, out := range []string{stdout, stderr} {
			if strings.Contains(out, "zephyrnet") || strings.Contains(out, "xxxx") {
				t.Errorf("%v quotes the file:\n%s", args, out)
			}
		}
		if !strings.Contains(stdout+stderr, "line 2") {
			t.Errorf("%v does not name the line:\n%s%s", args, stdout, stderr)
		}
	}
	cfg, err := config.Load(k.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, reason := kbmatchLookupBy(cfg, k.state, nil); strings.Contains(reason, "zephyrnet") || strings.Contains(reason, "xxxx") {
		t.Errorf("the ranking lookup's reason quotes the file: %s", reason)
	}
}
