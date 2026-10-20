package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// productionOpenEmbedder is the opener before any test replaces it.
var productionOpenEmbedder = openEmbedder

var labelSyn = [][]string{{"linkedin", "linked"}, {"url", "link", "profile"}}

type simReply struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Model     string `json:"model"`
	Pairs     []struct {
		A    int     `json:"a"`
		B    int     `json:"b"`
		Cos  float64 `json:"cos"`
		Tier string  `json:"tier"`
	} `json:"pairs"`
	Truncated int    `json:"truncated"`
	Note      string `json:"note"`
	Error     string `json:"error"`
}

// countingEmbedder counts Embed calls on a fake.
type countingEmbedder struct {
	semantic.Embedder
	mu    sync.Mutex
	calls int
	hook  func(ctx context.Context, texts []string) ([][]float32, error, bool)
}

func (c *countingEmbedder) Embed(ctx context.Context, texts []string, k semantic.Kind) ([][]float32, error) {
	c.mu.Lock()
	c.calls++
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		if rows, err, ok := hook(ctx, texts); ok {
			return rows, err
		}
	}
	return c.Embedder.Embed(ctx, texts, k)
}

func (c *countingEmbedder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// similarSetup is semanticSetup with a counting label embedder.
func similarSetup(t *testing.T) (stateDir string, emb *countingEmbedder) {
	t.Helper()
	_, stateDir = semanticSetup(t)
	emb = &countingEmbedder{Embedder: semantic.NewFakeEmbedder(256, labelSyn)}
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return emb, func() error { return nil }, nil
	}
	return stateDir, emb
}

func askSimilar(t *testing.T, stdin string, wantCode int, args ...string) (simReply, string, string) {
	t.Helper()
	old := answersInput
	answersInput = strings.NewReader(stdin)
	t.Cleanup(func() { answersInput = old })
	out, errOut := mustCode(t, wantCode, append([]string{"answers", "similar", "--json"}, args...)...)
	var r simReply
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, out)
	}
	return r, out, errOut
}

func reqJSON(t *testing.T, texts []string, minCos *float64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"texts": texts, "min_cos": minCos})
	if err != nil {
		t.Fatal(err)
	}
	if minCos == nil {
		b, _ = json.Marshal(map[string]any{"texts": texts})
	}
	return string(b)
}

func TestAnswersSimilarFindsPairs(t *testing.T) {
	_, emb := similarSetup(t)
	texts := []string{"LinkedIn profile URL", "Years of experience", "LinkedIn URL", "", "LinkedIn profile URL"}
	r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
	if !r.Available || r.Model == "" || r.Reason != "" {
		t.Fatalf("%s", out)
	}
	got := map[[2]int]string{}
	for _, p := range r.Pairs {
		if p.A >= p.B || p.A < 0 || p.B >= len(texts) {
			t.Errorf("bad pair %+v", p)
		}
		if p.Tier != "likely" && p.Tier != "possible" {
			t.Errorf("tier %q", p.Tier)
		}
		if want := semantic.TierPair(texts[p.A], texts[p.B], p.Cos); want != "" && p.Tier != want {
			t.Errorf("tier %q at cos %v, want %q", p.Tier, p.Cos, want)
		}
		got[[2]int{p.A, p.B}] = p.Tier
	}
	if got[[2]int{0, 4}] != "likely" || got[[2]int{0, 2}] == "" || got[[2]int{2, 4}] == "" {
		t.Errorf("pairs %v", r.Pairs)
	}
	if _, ok := got[[2]int{0, 1}]; ok {
		t.Error("unrelated wordings paired")
	}
	// The second request is served from the vector cache.
	n := emb.count()
	r2, _, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
	if !r2.Available || emb.count() != n || len(r2.Pairs) != len(r.Pairs) {
		t.Errorf("second run embedded again (%d -> %d) or changed the pairs", n, emb.count())
	}
}

func TestAnswersSimilarMinCosAndEmptyPairsAreAnArray(t *testing.T) {
	similarSetup(t)
	texts := []string{"alpha beta", "gamma delta"}
	_, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
	if !strings.Contains(out, `"pairs":[]`) {
		t.Errorf("no pairs must be [] not null: %s", out)
	}
	zero := -1.0
	r, _, _ := askSimilar(t, reqJSON(t, texts, &zero), 0)
	if len(r.Pairs) != 1 || r.Pairs[0].Tier != "possible" {
		t.Errorf("min_cos -1 should list every pair as possible: %+v", r.Pairs)
	}
	one := 1.0
	r, _, _ = askSimilar(t, reqJSON(t, []string{"same words", "same words"}, &one), 0)
	if len(r.Pairs) != 1 || r.Pairs[0].Tier != "likely" {
		t.Errorf("identical wordings at min_cos 1: %+v", r.Pairs)
	}
}

// A model that scores First name and Last name as the same vector must not make
// them "likely": the lexical veto holds them at "possible", while a true pair at
// the same cosine stays "likely".
func TestAnswersSimilarVetoHoldsNearMissesAtPossible(t *testing.T) {
	_, emb := similarSetup(t)
	emb.Embedder = semantic.NewFakeEmbedder(256, [][]string{{"first", "last"}, {"linkedin", "linked"}})
	texts := []string{"First name", "Last name", "LinkedIn", "LINKEDIN *"}
	r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
	tier := map[[2]int]string{}
	for _, p := range r.Pairs {
		tier[[2]int{p.A, p.B}] = p.Tier
		if p.Cos < 0.999 {
			t.Errorf("setup: %+v should be a perfect cosine", p)
		}
	}
	if tier[[2]int{0, 1}] != "possible" || tier[[2]int{2, 3}] != "likely" {
		t.Fatalf("%s", out)
	}
}

func TestAnswersSimilarFewTextsNeedNoModel(t *testing.T) {
	_, emb := similarSetup(t)
	for _, texts := range [][]string{nil, {}, {"only one"}} {
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		if !r.Available || len(r.Pairs) != 0 || !strings.Contains(out, `"pairs":[]`) {
			t.Errorf("%v: %s", texts, out)
		}
	}
	if emb.count() != 0 {
		t.Errorf("embedded %d times for fewer than two texts", emb.count())
	}
}

func TestAnswersSimilarUnavailableIsNeverNoPairs(t *testing.T) {
	texts := []string{"LinkedIn URL", "LinkedIn profile URL"}
	check := func(t *testing.T, r simReply, out, wantInReason string) {
		t.Helper()
		if r.Available || r.Reason == "" || !strings.Contains(r.Reason, wantInReason) || !strings.Contains(out, `"pairs":[]`) {
			t.Fatalf("want available:false with %q in the reason, pairs []: %s", wantInReason, out)
		}
	}
	t.Run("embedder unavailable", func(t *testing.T) {
		similarSetup(t)
		openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return nil, nil, fmt.Errorf("%w: this vani was built without -tags tessl", semantic.ErrUnavailable)
		}
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		check(t, r, out, "built without")
	})
	t.Run("no snapshot", func(t *testing.T) {
		cfgPath, _ := semanticSetup(t)
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Semantic.Snapshot = ""
		if err := config.Save(cfgPath, cfg); err != nil {
			t.Fatal(err)
		}
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		check(t, r, out, "semantic.snapshot")
	})
	t.Run("the real opener with a snapshot that is not there", func(t *testing.T) {
		semanticSetup(t)
		// semanticSetup replaced openEmbedder; put the production one back.
		openEmbedder = productionOpenEmbedder
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		check(t, r, out, "embedding model")
	})
	t.Run("embedder answers badly", func(t *testing.T) {
		_, emb := similarSetup(t)
		emb.hook = func(_ context.Context, x []string) ([][]float32, error, bool) { return nil, nil, true } // no rows
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		check(t, r, out, "answered badly")
	})
	t.Run("embedder fails", func(t *testing.T) {
		_, emb := similarSetup(t)
		emb.hook = func(context.Context, []string) ([][]float32, error, bool) { return nil, errors.New("boom"), true }
		r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0)
		check(t, r, out, "did not run")
	})
	t.Run("too many texts", func(t *testing.T) {
		similarSetup(t)
		many := make([]string, semantic.MaxSimilarTexts+1)
		for i := range many {
			many[i] = fmt.Sprint("w", i)
		}
		r, out, _ := askSimilar(t, reqJSON(t, many, nil), 0)
		check(t, r, out, "at most")
	})
}

func TestAnswersSimilarTimesOutWithAReason(t *testing.T) {
	_, emb := similarSetup(t)
	emb.hook = func(ctx context.Context, _ []string) ([][]float32, error, bool) {
		<-ctx.Done()
		return nil, ctx.Err(), true
	}
	start := time.Now()
	r, out, _ := askSimilar(t, reqJSON(t, []string{"LinkedIn URL", "LinkedIn profile URL"}, nil), 0, "--timeout", "1s")
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v with --timeout 1s", d)
	}
	if r.Available || !strings.Contains(r.Reason, "ran out of time") || !strings.Contains(out, `"pairs":[]`) {
		t.Fatalf("%s", out)
	}
}

func TestAnswersSimilarKeepsFinishedBatchesWhenItRunsOutOfTime(t *testing.T) {
	_, emb := similarSetup(t)
	texts := make([]string, 70)
	for i := range texts {
		texts[i] = fmt.Sprintf("wording %d here", i)
	}
	emb.hook = func(ctx context.Context, x []string) ([][]float32, error, bool) {
		if emb.count() == 3 {
			<-ctx.Done()
			return nil, ctx.Err(), true
		}
		return nil, nil, false
	}
	r, out, _ := askSimilar(t, reqJSON(t, texts, nil), 0, "--timeout", "1s")
	if r.Available || !strings.Contains(r.Reason, "64 of its wordings") {
		t.Fatalf("%s", out)
	}
	emb.mu.Lock()
	emb.hook, emb.calls = nil, 0
	emb.mu.Unlock()
	if r, out, _ = askSimilar(t, reqJSON(t, texts, nil), 0); !r.Available {
		t.Fatalf("%s", out)
	}
	if emb.count() != 1 {
		t.Errorf("the retry made %d embed calls, want 1 for the 6 left", emb.count())
	}
}

func TestAnswersSimilarTruncatesLongWordings(t *testing.T) {
	similarSetup(t)
	long := strings.Repeat("字", 300) // 900 bytes
	r, out, _ := askSimilar(t, reqJSON(t, []string{long, long + "x", "other"}, nil), 0)
	if !r.Available || r.Truncated != 2 {
		t.Fatalf("%s", out)
	}
}

func TestAnswersSimilarMalformedRequests(t *testing.T) {
	similarSetup(t)
	huge := `{"texts":["` + strings.Repeat("a", maxSimilarInput) + `"]}`
	for name, in := range map[string]string{
		"empty":        "",
		"not json":     "{",
		"wrong shape":  `["a","b"]`,
		"texts string": `{"texts":"abc"}`,
		"nan-ish":      `{"texts":["a","b"],"min_cos":"x"}`,
		"min_cos high": `{"texts":["a","b"],"min_cos":1.5}`,
		"min_cos low":  `{"texts":["a","b"],"min_cos":-2}`,
		"oversized":    huge,
		"number texts": `{"texts":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, out, _ := askSimilar(t, in, 1)
			if r.Error == "" || r.Available {
				t.Errorf("want {\"error\":...}: %s", out)
			}
		})
	}
	// A bad flag is a usage error, exit 2.
	old := answersInput
	answersInput = strings.NewReader("{}")
	defer func() { answersInput = old }()
	mustCode(t, 2, "answers", "similar", "--nope")
	mustCode(t, 2, "answers", "similar", "--timeout", "0s")
	mustCode(t, 2, "answers")
	mustCode(t, 2, "answers", "other")
}

func TestAnswersSimilarRebuildsACorruptVectorCache(t *testing.T) {
	state, _ := similarSetup(t)
	dir := semanticDir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "semantic-vectors.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, out, _ := askSimilar(t, reqJSON(t, []string{"LinkedIn URL", "LinkedIn profile URL"}, nil), 0)
	if !r.Available || len(r.Pairs) != 1 || !strings.Contains(r.Note, "started again") {
		t.Fatalf("%s", out)
	}
	if c, err := semantic.OpenVectorCache(dir, 0); err != nil || c.Len() != 2 {
		t.Errorf("the rebuilt cache: %v %v", c, err)
	}
}

// The wordings go to the embedder and the cache; they never come back out in a
// reply or on stderr, even when something fails.
func TestAnswersSimilarNeverEchoesWordings(t *testing.T) {
	marker := "ZQXWORDINGMARKER"
	texts := []string{"LinkedIn " + marker, "LinkedIn profile " + marker}
	_, emb := similarSetup(t)
	r, out, errOut := askSimilar(t, reqJSON(t, texts, nil), 0)
	if !r.Available || strings.Contains(out, marker) || strings.Contains(errOut, marker) {
		t.Errorf("success leaked: %s | %s", out, errOut)
	}
	emb.hook = func(context.Context, []string) ([][]float32, error, bool) {
		return nil, errors.New("embedder failed"), true
	}
	_, out, errOut = askSimilar(t, reqJSON(t, []string{texts[0] + " 2", texts[1] + " 2"}, nil), 0)
	if strings.Contains(out, marker) || strings.Contains(errOut, marker) {
		t.Errorf("failure leaked: %s | %s", out, errOut)
	}
}
