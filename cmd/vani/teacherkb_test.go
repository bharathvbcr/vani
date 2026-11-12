package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const tkSonnet = "claude-sonnet-5-5"

// tkSetup is tcSetup with a synthetic knowledge base wired the way the owner's
// is (kb.bank, kb.selections and a fake rkb, read through eligibleBank).
func tkSetup(t *testing.T) *tcEnv {
	t.Helper()
	e := tcSetup(t)
	tkBank(t, e, kbmBank)
	return e
}

// tkBank (re)writes the knowledge base and points the config at it.
func tkBank(t *testing.T, e *tcEnv, bank string) {
	t.Helper()
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	bankPath := filepath.Join(filepath.Dir(e.cfgPath), "bank.json")
	cfg.KB.Bank = bankPath
	wireEligibleKB(t, &cfg, bankPath, bank)
	if err := config.Save(e.cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

// tkDigest is the digest a run computes now, through the same path.
func tkDigest(t *testing.T, e *tcEnv) (kbmatch.Digest, string) {
	t.Helper()
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	_, d, s, err := kbmatchDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d, s
}

// tkSeed adds n postings d1-ultra read, keyed prefix:00 ..., and returns their keys.
func tkSeed(t *testing.T, state, prefix string, n int) []string {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	long := strings.Repeat("You will build and operate services that serve language models. ", 20)
	var keys []string
	for i := range n {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("%s:%02d", prefix, i)), ATS: "gh", Company: "SynthCo", Title: fmt.Sprintf("Engineer %s:%02d", prefix, i), Description: long}
		ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: posting.Explained, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(p.Key))
	}
	return keys
}

func tkReadKeys(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func tkWrite(t *testing.T, keys ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(p, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tkRows(t *testing.T, state string) []eval.TeacherRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []eval.TeacherRow
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r eval.TeacherRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestTeacherKBMatchDryRunPricesFromTheTableAndAsksNothing(t *testing.T) {
	e := tkSetup(t)
	tcNoModel(t) // the decider (and so any provider or network client) is never built
	o, _ := mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--model", tkSonnet, "--dry-run", "--json")
	var r teacherReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v:\n%s", err, o)
	}
	p := r.Plan
	if !r.DryRun || p == nil || r.Run != nil {
		t.Fatalf("reply %+v", r)
	}
	d, _ := tkDigest(t, e)
	table, ok := anthropic.PriceOf(tkSonnet)
	if !ok {
		t.Fatalf("no rate table entry for %s", tkSonnet)
	}
	if p.Task != "kb_match" || p.DigestSHA256 != d.SHA256 || p.Model != tkSonnet || p.ToAsk != 2 || p.InputTokens == nil {
		t.Fatalf("plan %+v", p)
	}
	if !strings.HasPrefix(p.PriceSource, "Anthropic's published rate table") || p.MaxUSD <= 0 || p.TypicalUSD <= 0 {
		t.Fatalf("priced from %q typical %v max %v", p.PriceSource, p.TypicalUSD, p.MaxUSD)
	}
	// The worst case is the table's rates over every prompt at its byte ceiling
	// plus every reply at its full budget: recompute it from kbmatch.Question.
	_, summary := tkDigest(t, e)
	snap, err := ledger.ReadSnapshot(filepath.Join(e.state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, rec := range snap.Records {
		if rec.Key == "gh:a:1" || rec.Key == "gh:a:2" {
			q, _ := kbmatch.Question(rec.Posting, summary, d, nil)
			want += teacherWorstNano(table, q)
		}
	}
	if got := int64(p.MaxUSD*1e9 + 0.5); got != want {
		t.Fatalf("max %d nano-USD, want %d from the table", got, want)
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Fatal("a dry run wrote the readings log")
	}
	if p.DrawFile == "" || p.DrawKeys != 2 {
		t.Fatalf("draw not saved: %+v", p)
	}
}

func TestTeacherKBMatchSendsTheFullDigestHeadFirstAndStoresItsIdentity(t *testing.T) {
	e := tkSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	o, _ := mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1")
	if !strings.Contains(o, "asked 2, stored 2") {
		t.Fatalf("output:\n%s", o)
	}
	d, summary := tkDigest(t, e)
	for _, s := range f.sent {
		ctx := strings.TrimPrefix(s, kbmatch.Prompt+"\n")
		if !strings.HasPrefix(s, kbmatch.Prompt+"\n") || !strings.HasPrefix(ctx, "JOB SEEKER'S TARGET ROLES:\n"+summary+"\n\nJOB SEEKER'S EXPERIENCE:\n"+d.Text) {
			t.Fatalf("question is not head-first with the full digest:\n%s", s)
		}
		if strings.Contains(s, "picked by an embedding model") || !strings.Contains(s, "fused attention kernels") {
			t.Fatalf("question carries the focused view or lacks the digest:\n%s", s)
		}
		if strings.Index(ctx, "JOB POSTING:") < len(d.Text) || strings.Contains(s, tcSecret) {
			t.Fatalf("posting is not after the digest, or a private note leaked:\n%s", s)
		}
	}
	rows := tkRows(t, e.state)
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		if r.Task != kbmatch.Task || r.DigestSHA != d.SHA256 || r.FixtureSHA != teacherKBMatchFixture() || r.PromptHash != teacherKBMatchPromptHash(summary) || r.Model != tcHaiku {
			t.Fatalf("row identity %+v", r)
		}
		if r.FixtureSHA == eval.FixtureSHA() || r.PromptHash == eval.PromptHash(summary) {
			t.Fatal("a kb_match row carries the jd_triage identity")
		}
		if sl := r.Slots[kbmatch.Slot]; sl.Reading != "answered" || sl.Value != "core" || len(r.Slots) != 1 {
			t.Fatalf("slots %+v", r.Slots)
		}
	}
	// Asked again: nothing is sent.
	o, _ = mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5")
	if !strings.Contains(o, "2 already stored") || len(f.sent) != 2 {
		t.Fatalf("second run sent %d:\n%s", len(f.sent), o)
	}
}

func TestTeacherKBMatchReasksWhenTheKnowledgeBaseChangesAndWithReadingsOfFollowsTheDigest(t *testing.T) {
	e := tkSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5")
	o, _ := mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--model", tkSonnet, "--with-readings-of", tcHaiku, "--dry-run")
	if !strings.Contains(o, "kept 2 of 2 postings that have a current reading by "+tcHaiku) {
		t.Fatalf("output:\n%s", o)
	}
	// The kb_match readings are not jd_triage readings.
	o, _ = mustCode(t, 0, "teacher", "--of", "d1-ultra", "--model", tkSonnet, "--with-readings-of", tcHaiku, "--dry-run")
	if !strings.Contains(o, "kept 0 of 2") {
		t.Fatalf("jd_triage saw kb_match readings:\n%s", o)
	}

	before, _ := tkDigest(t, e)
	tkBank(t, e, strings.Replace(kbmBank, "Built a request batching scheduler in Rust", "Built a request batching scheduler in Rust and Go", 1))
	after, _ := tkDigest(t, e)
	if before.SHA256 == after.SHA256 {
		t.Fatal("the bank edit did not change the digest")
	}
	o, _ = mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--model", tkSonnet, "--with-readings-of", tcHaiku, "--dry-run")
	if !strings.Contains(o, "kept 0 of 2") {
		t.Fatalf("readings under the old digest still count:\n%s", o)
	}
	o, _ = mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5")
	if !strings.Contains(o, "asked 2, stored 2") || len(f.sent) != 4 {
		t.Fatalf("a digest change did not re-ask (sent %d):\n%s", len(f.sent), o)
	}
}

func TestTeacherKBMatchFailsClosedWithoutAnEligibleKnowledgeBase(t *testing.T) {
	e := tkSetup(t)
	tcNoModel(t)
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KB.Selections = nil
	if err := config.Save(e.cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run"},
		{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--budget-usd", "5"},
	} {
		code, _, stderr := do(t, args...)
		if code == 0 || !strings.Contains(stderr, "kb.selections is empty") {
			t.Fatalf("%v: exit %d, stderr %s", args, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Fatal("a refused run wrote the readings log")
	}
	if code, _, _ := do(t, "teacher", "--task", "kbmatch", "--of", "d1-ultra", "--dry-run"); code != 2 {
		t.Fatalf("an unknown --task exited %d, want 2", code)
	}
}

func TestTeacherJDTriageIsUnchangedByTheTaskFlag(t *testing.T) {
	state := tcSetup(t).state
	f := &tcFake{}
	tcUse(t, f)
	mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1")
	plain := slices.Clone(f.sent)
	f.sent = nil
	rows := tkRows(t, state)
	// The identity captured before tasks existed (internal/eval goldens): the
	// stored rows are still current to the readers Lappi and the label queue run.
	for _, r := range rows {
		if r.Task != "" || r.DigestSHA != "" || r.FixtureSHA != "0cf04ad791d67fb04bf4e1c5be9b3206f61cf356dddf0a3054c9a8b5a13be47d" ||
			r.PromptHash != "adfed9564a844a34321fd919de1a8bb182334fc280b5de73de7d7caa0b529ea8" {
			t.Fatalf("jd_triage row identity changed: %+v", r)
		}
	}
	if err := os.Remove(filepath.Join(state, eval.TeacherFile)); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "teacher", "--task", "jd_triage", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1")
	if len(plain) != 2 || !slices.Equal(f.sent, plain) {
		t.Fatalf("--task jd_triage sent other bytes than no flag:\n%v\n%v", f.sent, plain)
	}
}

func TestTeacherKBMatchDrawIsSeededAndReproducible(t *testing.T) {
	e := tkSetup(t)
	keys := append([]string{"gh:a:1", "gh:a:2"}, tkSeed(t, e.state, "gh:b", 30)...)
	slices.Sort(keys)
	tcNoModel(t)
	dir := t.TempDir()
	draw := func(name string, extra ...string) []string {
		out := filepath.Join(dir, name)
		mustCode(t, 0, append([]string{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run", "--draw-out", out}, extra...)...)
		return tkReadKeys(t, out)
	}
	a := draw("a", "--sample", "10", "--seed", "20261109")
	b := draw("b", "--sample", "10", "--seed", "20261109")
	if want := tcDrawn(t, keys, 10, 20261109); !slices.Equal(a, want) || !slices.Equal(b, want) {
		t.Fatalf("draws %v / %v, want %v", a, b, want)
	}
	if c := draw("c", "--sample", "10", "--seed", "7"); slices.Equal(c, a) {
		t.Fatal("another seed drew the same sample")
	}
	// The default destination is a new file under the state directory.
	o, _ := mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run", "--sample", "10", "--seed", "20261109", "--json")
	var r teacherReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(r.Plan.DrawFile) != filepath.Join(e.state, teacherDrawsDir) || !slices.Equal(tkReadKeys(t, r.Plan.DrawFile), a) || r.Plan.DrawKeys != 10 {
		t.Fatalf("default draw file %q (%d keys)", r.Plan.DrawFile, r.Plan.DrawKeys)
	}
}

func TestTeacherKeysKeepTheSameDrawAfterTheCandidateListGrows(t *testing.T) {
	e := tkSetup(t)
	keys := append([]string{"gh:a:1", "gh:a:2"}, tkSeed(t, e.state, "gh:b", 30)...)
	slices.Sort(keys)
	f := &tcFake{}
	tcUse(t, f)
	dir := t.TempDir()
	run := func(name string, extra ...string) []string {
		out := filepath.Join(dir, name)
		mustCode(t, 0, append([]string{"teacher", "--task", "kb_match", "--of", "d1-ultra", "--dry-run", "--draw-out", out}, extra...)...)
		return tkReadKeys(t, out)
	}
	first := run("first", "--sample", "10", "--seed", "20261109")
	all := tkWrite(t, keys...)

	// The live list grows (keys that sort among the old ones): the bare seeded
	// draw moves, the same seed over the same --keys does not.
	tkSeed(t, e.state, "gh:ab", 40)
	if moved := run("moved", "--sample", "10", "--seed", "20261109"); slices.Equal(moved, first) {
		t.Fatal("the bare draw did not move when the list grew; the test cannot tell --keys apart")
	}
	if same := run("same", "--keys", all, "--sample", "10", "--seed", "20261109"); !slices.Equal(same, first) {
		t.Fatalf("--keys draw %v, want %v", same, first)
	}
	// The key file's line order does not change the draw.
	rev := slices.Clone(keys)
	slices.Reverse(rev)
	if same := run("rev", "--keys", tkWrite(t, rev...), "--sample", "10", "--seed", "20261109"); !slices.Equal(same, first) {
		t.Fatalf("reversed --keys draw %v, want %v", same, first)
	}

	// Resuming a capped run: the saved draw, passed back with --keys and no
	// --sample, is asked in its drawn order, the stored ones skipped.
	saved := filepath.Join(dir, "first")
	mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--keys", saved, "--budget-usd", "5", "--concurrency", "1", "--max", "4")
	mustCode(t, 0, "teacher", "--task", "kb_match", "--of", "d1-ultra", "--keys", saved, "--budget-usd", "5", "--concurrency", "1")
	if got := tcAskedKeys(f); !slices.Equal(got, first) {
		t.Fatalf("asked %v, want the saved draw in order %v", got, first)
	}
}

func TestTeacherKeysRefusesUnknownAndDuplicateKeysByName(t *testing.T) {
	e := tkSetup(t)
	tcNoModel(t)
	for name, tc := range map[string]struct {
		keys []string
		want []string
	}{
		"duplicate":  {[]string{"gh:a:1", "gh:a:2", "gh:a:1"}, []string{"duplicate", "gh:a:1 (lines 1 and 3)"}},
		"unknown":    {[]string{"gh:a:1", "gh:zz:9"}, []string{"1 not in the ledger: gh:zz:9"}},
		"not asked":  {[]string{"gh:a:3", "gh:a:4", "gh:a:5"}, []string{"3 in the ledger but not askable", "gh:a:3, gh:a:4, gh:a:5"}},
		"both kinds": {[]string{"gh:zz:1", "gh:a:3"}, []string{"names 2 posting(s)", "gh:zz:1", "gh:a:3"}},
	} {
		for _, task := range []string{"jd_triage", "kb_match"} {
			code, _, stderr := do(t, "teacher", "--task", task, "--of", "d1-ultra", "--keys", tkWrite(t, tc.keys...), "--budget-usd", "5")
			if code != 2 {
				t.Errorf("%s/%s: exit %d, want 2; stderr %s", name, task, code, stderr)
			}
			for _, w := range tc.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("%s/%s: stderr lacks %q:\n%s", name, task, w, stderr)
				}
			}
		}
	}
	if code, _, _ := do(t, "teacher", "--of", "d1-ultra", "--keys", filepath.Join(t.TempDir(), "absent"), "--dry-run"); code != 2 {
		t.Errorf("a missing --keys file exited %d, want 2", code)
	}
	if code, _, _ := do(t, "teacher", "--of", "d1-ultra", "--keys", t.TempDir(), "--dry-run"); code != 2 {
		t.Errorf("a directory as --keys exited %d, want 2", code)
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Fatal("a refused run wrote the readings log")
	}
	if _, err := os.Stat(filepath.Join(e.state, teacherDrawsDir)); err == nil {
		t.Fatal("a refused run saved a draw")
	}
}
