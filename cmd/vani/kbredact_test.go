package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// kbTestOwnWord is the owner's identifying term the kb_match test setups list.
// It appears in no test bank, so their digests are as they were.
const kbTestOwnWord = "zzownername"

// writeKBWords writes the owner's identifying-words list where a run reads it
// (the tracks directory, <state>/tracks with no tracks_dir set).
func writeKBWords(t *testing.T, state string, terms ...string) {
	t.Helper()
	dir := filepath.Join(state, "tracks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identifying-words.txt"), []byte(strings.Join(terms, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kbOwnerPart is a question's text before the posting: the owner's sections.
// The posting is the employer's text and is neither redacted nor checked.
func kbOwnerPart(t *testing.T, sent string) string {
	t.Helper()
	owner, _, ok := strings.Cut(sent, "\nJOB POSTING:\n")
	if !ok {
		t.Fatalf("not a kb_match question:\n%s", sent)
	}
	return owner
}

// kbSecretTerms are words of the synthetic bank (kbmBank) the owner lists.
var kbSecretTerms = []string{"fused attention", "rust", "llm inference runtime"}

func assertNoTerm(t *testing.T, what string, bodies []string) {
	t.Helper()
	if len(bodies) == 0 {
		t.Fatalf("%s: nothing was sent", what)
	}
	for _, b := range bodies {
		owner := strings.ToLower(kbOwnerPart(t, b))
		for _, term := range kbSecretTerms {
			if strings.Contains(owner, term) {
				t.Errorf("%s: the identifying term %q was sent:\n%s", what, term, b)
			}
		}
		if !strings.Contains(owner, "[redacted]") {
			t.Errorf("%s: no redaction in what was sent:\n%s", what, b)
		}
	}
}

func TestKBMatchRunRemovesTheOwnersIdentifyingTerms(t *testing.T) {
	k := twoPostings(t)
	writeKBWords(t, k.state, kbSecretTerms...)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})}
	useFake(t, f)
	mustCode(t, 0, "kbmatch", "run")
	assertNoTerm(t, "kbmatch run (full digest)", f.contexts)
}

func TestKBMatchRunRemovesTermsFromTheEmbeddingPickedLines(t *testing.T) {
	k := twoPostings(t)
	writeKBWords(t, k.state, kbSecretTerms...)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "adjacent"})}
	useFake(t, f)
	k.cfg.Semantic.Snapshot = filepath.Join(filepath.Dir(k.cfgPath), "snapshot")
	k.save(t)
	kbmatchFocus = func(context.Context, config.Config, string, semantic.Profile) (func(context.Context, posting.Posting) ([]kbmatch.Focus, error), func(), error) {
		return func(context.Context, posting.Posting) ([]kbmatch.Focus, error) {
			return []kbmatch.Focus{{Label: "LLM Inference Runtime", Text: "Built a request batching scheduler in Rust"}}, nil
		}, func() {}, nil
	}
	mustCode(t, 0, "kbmatch", "run")
	for _, c := range f.contexts {
		if !strings.Contains(c, "CLOSEST TO THIS POSTING") {
			t.Fatalf("the focused view was not used:\n%s", c)
		}
	}
	assertNoTerm(t, "kbmatch run (focused)", f.contexts)
}

func TestKBMatchRunRefusesWithoutAnIdentifyingWordsFile(t *testing.T) {
	k := twoPostings(t)
	if err := os.Remove(filepath.Join(k.state, "tracks", "identifying-words.txt")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})}
	useFake(t, f)
	for _, args := range [][]string{{"kbmatch", "run"}, {"kbmatch", "run", "--dry-run"}} {
		code, _, stderr := do(t, args...)
		if code == 0 || !strings.Contains(stderr, "identifying-words.txt") {
			t.Errorf("%v: exit %d, stderr %s", args, code, stderr)
		}
	}
	if len(f.contexts) != 0 {
		t.Fatalf("a refused run sent %d question(s)", len(f.contexts))
	}
	// The flag sends with the built-in list alone, and the reply says so.
	o, _ := mustCode(t, 0, "kbmatch", "run", "--allow-missing-identifying-words", "--json")
	if !strings.Contains(o, `"identifying_words":"missing_allowed"`) || len(f.contexts) != 2 {
		t.Fatalf("allowed run (sent %d):\n%s", len(f.contexts), o)
	}
}

func TestKBMatchRefusesAnUnreadableIdentifyingWordsFileWhateverTheFlag(t *testing.T) {
	k := twoPostings(t)
	writeKBWords(t, k.state, strings.Repeat("x", 300)) // a line past the per-term bound
	f := &fakeKB{answer: byTitle(nil)}
	useFake(t, f)
	for _, args := range [][]string{{"kbmatch", "run"}, {"kbmatch", "run", "--allow-missing-identifying-words"}} {
		code, _, _ := do(t, args...)
		if code == 0 {
			t.Errorf("%v ran with an unreadable list", args)
		}
	}
	if len(f.contexts) != 0 {
		t.Fatalf("sent %d question(s)", len(f.contexts))
	}
}

func TestKBMatchRefusesATermInTheTargetSummary(t *testing.T) {
	k := twoPostings(t)
	writeKBWords(t, k.state, "ml systems")
	f := &fakeKB{answer: byTitle(nil)}
	useFake(t, f)
	code, _, stderr := do(t, "kbmatch", "run")
	if code == 0 || !strings.Contains(stderr, "target summary 1") || strings.Contains(stderr, "ml systems") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if len(f.contexts) != 0 {
		t.Fatalf("sent %d question(s)", len(f.contexts))
	}
}

func TestTeacherKBMatchRemovesTheOwnersIdentifyingTerms(t *testing.T) {
	e := tkSetup(t)
	writeKBWords(t, e.state, kbSecretTerms...)
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1")
	var bodies []string
	for _, s := range f.sent {
		bodies = append(bodies, strings.TrimPrefix(s, kbmatch.Prompt+"\n"))
	}
	assertNoTerm(t, "teacher kb_match", bodies)
}

func TestTeacherKBMatchRefusesWithoutAnIdentifyingWordsFile(t *testing.T) {
	e := tkSetup(t)
	if err := os.Remove(filepath.Join(e.state, "tracks", "identifying-words.txt")); err != nil {
		t.Fatal(err)
	}
	f := &tcFake{}
	tcUse(t, f)
	for _, args := range [][]string{
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run"},
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5"},
	} {
		code, _, stderr := do(t, args...)
		if code == 0 || !strings.Contains(stderr, "identifying-words.txt") {
			t.Errorf("%v: exit %d, stderr %s", args, code, stderr)
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("a refused run sent %d", len(f.sent))
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Fatal("a refused run wrote the readings log")
	}
	// jd_triage carries no knowledge base: it needs no list.
	code, _, stderr := do(t, "teacher", "--of", "d1-ultra", "--dry-run")
	if code != 0 {
		t.Fatalf("jd_triage refused without the list: %s", stderr)
	}
}
