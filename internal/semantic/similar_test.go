package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var labelSynonyms = [][]string{{"linkedin", "linked"}, {"url", "link", "profile"}}

func labelSpy() *spy { return &spy{inner: NewFakeEmbedder(256, labelSynonyms)} }

func pairSet(ps []Pair) map[[2]int]float64 {
	m := map[[2]int]float64{}
	for _, p := range ps {
		m[[2]int{p.A, p.B}] = p.Cos
	}
	return m
}

func TestSimilarPairsFindsAndOrders(t *testing.T) {
	texts := []string{
		"LinkedIn profile URL", // 0
		"Years of experience",  // 1
		"LinkedIn URL",         // 2
		"",                     // 3 blank
		"LinkedIn profile URL", // 4 identical to 0
		"   ",                  // 5 blank
		"Salary expectation",   // 6
	}
	s := labelSpy()
	ps, rep, err := SimilarPairs(context.Background(), s, nil, texts, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	got := pairSet(ps)
	if got[[2]int{0, 4}] < 0.9999 || got[[2]int{0, 2}] < 0.9 || got[[2]int{2, 4}] < 0.9 {
		t.Fatalf("missed the LinkedIn wordings: %v", ps)
	}
	for p := range got {
		if p[0] >= p[1] || p[0] == 3 || p[1] == 3 || p[0] == 5 || p[1] == 5 {
			t.Errorf("bad pair %v (a < b, no blanks)", p)
		}
	}
	if _, ok := got[[2]int{1, 6}]; ok {
		t.Error("unrelated labels paired")
	}
	for i := 1; i < len(ps); i++ {
		if ps[i-1].Cos < ps[i].Cos {
			t.Errorf("not sorted by cosine: %v", ps)
		}
	}
	// Distinct texts: 5 non-blank distinct → "LinkedIn profile URL" once; blanks "" and "   " are two.
	if rep.Scored != 4 || rep.Cached != 0 || rep.Skipped != 2 || rep.Failed != 0 || rep.Unavailable {
		t.Errorf("report %+v", rep)
	}
	if ps == nil {
		t.Error("nil pairs")
	}
}

func TestSimilarPairsNoPairsIsAnEmptyListNotNil(t *testing.T) {
	ps, _, err := SimilarPairs(context.Background(), labelSpy(), nil, []string{"alpha", "bravo"}, 0.99)
	if err != nil || ps == nil || len(ps) != 0 {
		t.Fatalf("%v %v (the JSON reply needs [] not null)", ps, err)
	}
	ps, rep, err := SimilarPairs(context.Background(), labelSpy(), nil, nil, 0.5)
	if err != nil || ps == nil || len(ps) != 0 || rep.Scored != 0 {
		t.Fatalf("empty input: %v %+v %v", ps, rep, err)
	}
}

func TestSimilarPairsUsesSimilarityKindBatchesAndCache(t *testing.T) {
	var kinds []Kind
	var mu sync.Mutex
	s := labelSpy()
	s.hook = func(_ context.Context, _ []string, k Kind, _ int) ([][]float32, error, bool) {
		mu.Lock()
		kinds = append(kinds, k)
		mu.Unlock()
		return nil, nil, false
	}
	texts := make([]string, 100)
	for i := range texts {
		texts[i] = fmt.Sprintf("question number %d", i)
	}
	c := openVC(t, filepath.Join(t.TempDir(), "semantic"), 0)
	_, rep, err := SimilarPairs(context.Background(), s, c, texts, 0.99)
	if err != nil {
		t.Fatal(err)
	}
	if s.maxN > MaxBatch || s.count() != 4 || rep.Scored != 100 {
		t.Errorf("maxN=%d calls=%d rep=%+v, want batches of at most %d", s.maxN, s.count(), rep, MaxBatch)
	}
	for _, k := range kinds {
		if k != KindSimilarity {
			t.Errorf("embedded with %q", k)
		}
	}
	// A second call embeds only what is new.
	texts = append(texts, "a brand new wording")
	before := len(s.texts)
	_, rep, err = SimilarPairs(context.Background(), s, c, texts, 0.99)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Cached != 100 || rep.Scored != 1 || len(s.texts)-before != 1 || s.texts[before] != "a brand new wording" {
		t.Errorf("rep=%+v embedded=%v", rep, s.texts[before:])
	}
	// And the vectors survived to disk: a fresh process embeds nothing.
	c2 := openVC(t, filepath.Dir(c.path), 0)
	s2 := labelSpy()
	_, rep, err = SimilarPairs(context.Background(), s2, c2, texts, 0.99)
	if err != nil || s2.count() != 0 || rep.Cached != 101 {
		t.Errorf("reopened: calls=%d rep=%+v err=%v", s2.count(), rep, err)
	}
	// A model change is a different key: nothing is reused.
	s3 := labelSpy()
	s3.model = "other-model"
	_, rep, err = SimilarPairs(context.Background(), s3, c2, texts, 0.99)
	if err != nil || rep.Cached != 0 || rep.Scored != 101 {
		t.Errorf("other model: rep=%+v err=%v", rep, err)
	}
}

func TestSimilarPairsBounds(t *testing.T) {
	ctx := context.Background()
	s := labelSpy()
	if _, _, err := SimilarPairs(ctx, s, nil, make([]string, MaxSimilarTexts+1), 0.9); !errors.Is(err, ErrBounds) {
		t.Errorf("too many texts: %v", err)
	}
	if _, _, err := SimilarPairs(ctx, s, nil, []string{strings.Repeat("a", MaxSimilarTextBytes+1)}, 0.9); !errors.Is(err, ErrBounds) {
		t.Errorf("long text: %v", err)
	}
	if _, _, err := SimilarPairs(ctx, labelSpy(), nil, []string{strings.Repeat("a", MaxSimilarTextBytes)}, 0.9); err != nil {
		t.Errorf("text at the cap: %v", err)
	}
	for _, m := range []float64{math.NaN(), 1.5, -1.5, math.Inf(1)} {
		if _, _, err := SimilarPairs(ctx, s, nil, []string{"a", "b"}, m); !errors.Is(err, ErrBounds) {
			t.Errorf("min_cos %v: %v", m, err)
		}
	}
	if _, _, err := SimilarPairs(ctx, nil, nil, []string{"a"}, 0.9); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil embedder: %v", err)
	}
	if s.count() != 0 {
		t.Errorf("embedded %d times for rejected input", s.count())
	}
	// More pairs than the cap fails; it does not return a sample.
	same := make([]string, 500) // 124,750 identical pairs
	for i := range same {
		same[i] = "same wording"
	}
	if ps, _, err := SimilarPairs(ctx, s, nil, same, 0.9); !errors.Is(err, ErrBounds) || ps != nil {
		t.Errorf("pair cap: %d pairs, %v", len(ps), err)
	}
}

func TestSimilarPairsMaxTextsRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("4000 texts")
	}
	texts := make([]string, MaxSimilarTexts)
	for i := range texts {
		texts[i] = fmt.Sprintf("wording %d of %d unique", i, i*7)
	}
	start := time.Now()
	ps, rep, err := SimilarPairs(context.Background(), labelSpy(), nil, texts, 0.999)
	if err != nil || rep.Scored != MaxSimilarTexts {
		t.Fatalf("%v %+v", err, rep)
	}
	t.Logf("4000 texts, %d pairs at 0.999, %v", len(ps), time.Since(start).Round(time.Millisecond))
}

func TestSimilarPairsUnavailableIsNotNoPairs(t *testing.T) {
	s := labelSpy()
	s.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, fmt.Errorf("%w: no snapshot", ErrUnavailable), true
	}
	ps, rep, err := SimilarPairs(context.Background(), s, nil, []string{"a", "b", "c"}, 0.5)
	if !errors.Is(err, ErrUnavailable) || ps != nil || !rep.Unavailable || rep.Failed != 3 || rep.Err == "" {
		t.Fatalf("%v %+v %v", ps, rep, err)
	}
}

func TestSimilarPairsEmbedderMisbehaves(t *testing.T) {
	good := func(texts []string) [][]float32 {
		rows, _ := NewFakeEmbedder(64, labelSynonyms).Embed(context.Background(), texts, KindSimilarity)
		return rows
	}
	cases := map[string]func(x []string) ([][]float32, error){
		"too few rows":  func(x []string) ([][]float32, error) { return good(x)[:len(x)-1], nil },
		"too many rows": func(x []string) ([][]float32, error) { return append(good(x), good(x)[0]), nil },
		"no rows":       func(x []string) ([][]float32, error) { return nil, nil },
		"wrong width":   func(x []string) ([][]float32, error) { r := good(x); r[len(r)-1] = r[len(r)-1][:10]; return r, nil },
		"nan": func(x []string) ([][]float32, error) {
			r := good(x)
			r[0][3] = float32(math.NaN())
			return r, nil
		},
		"inf": func(x []string) ([][]float32, error) {
			r := good(x)
			r[0][0] = float32(math.Inf(-1))
			return r, nil
		},
		"zero vector": func(x []string) ([][]float32, error) { r := good(x); r[1] = make([]float32, 64); return r, nil },
		"error":       func(x []string) ([][]float32, error) { return nil, errors.New("boom") },
		"panic":       func(x []string) ([][]float32, error) { panic("embedder bug") },
	}
	texts := []string{"one wording", "two wording", "three wording"}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			s := labelSpy()
			s.hook = func(_ context.Context, x []string, _ Kind, _ int) ([][]float32, error, bool) {
				rows, err := bad(x)
				return rows, err, true
			}
			c := openVC(t, filepath.Join(t.TempDir(), "semantic"), 0)
			ps, rep, err := SimilarPairs(context.Background(), s, c, texts, -1)
			if err == nil || ps != nil || rep.Failed != 3 || rep.Err == "" || rep.Unavailable {
				t.Fatalf("pairs=%v rep=%+v err=%v", ps, rep, err)
			}
			if name != "error" && name != "panic" && !errors.Is(err, ErrEmbed) {
				t.Errorf("err %v is not ErrEmbed", err)
			}
			if c.Len() != 0 && name != "nan" && name != "inf" && name != "zero vector" && name != "wrong width" {
				t.Errorf("%d vectors cached from a bad answer", c.Len())
			}
			// Rows validated before the bad one in the same batch may be cached, but a
			// bad row never is.
			for _, tx := range texts {
				if v, ok := c.Get(s.Model(), KindSimilarity, tx); ok {
					if _, valid := validRow(v, 0); !valid || len(v) != 64 {
						t.Errorf("cached an unusable vector for %q", tx)
					}
				}
			}
		})
	}
}

func TestSimilarPairsTimeout(t *testing.T) {
	for name, ignoresCtx := range map[string]bool{"honors ctx": false, "ignores ctx": true} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			s := labelSpy()
			s.hook = func(ctx context.Context, _ []string, _ Kind, _ int) ([][]float32, error, bool) {
				if ignoresCtx {
					<-release
				} else {
					<-ctx.Done()
				}
				return nil, ctx.Err(), true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			ps, rep, err := SimilarPairs(ctx, s, nil, []string{"a b", "c d"}, 0.5)
			if time.Since(start) > 5*time.Second {
				t.Fatalf("took %v past a 50ms deadline", time.Since(start))
			}
			if !errors.Is(err, context.DeadlineExceeded) || ps != nil || !rep.Canceled || rep.Failed != 2 {
				t.Fatalf("%v %+v %v", ps, rep, err)
			}
		})
	}
}

func TestSimilarPairsBoundsTheDeadline(t *testing.T) {
	var got time.Time
	s := labelSpy()
	s.hook = func(ctx context.Context, _ []string, _ Kind, _ int) ([][]float32, error, bool) {
		got, _ = ctx.Deadline()
		return nil, nil, false
	}
	if _, _, err := SimilarPairs(context.Background(), s, nil, []string{"a b", "c d"}, 0.5); err != nil {
		t.Fatal(err)
	}
	if got.IsZero() || time.Until(got) > SimilarDeadline {
		t.Errorf("embedder saw deadline %v, want within %v", got, SimilarDeadline)
	}
	// A sooner caller deadline is kept.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	if _, _, err := SimilarPairs(ctx, s, nil, []string{"e f", "g h"}, 0.5); err != nil {
		t.Fatal(err)
	}
	if got.After(want) {
		t.Errorf("deadline %v is later than the caller's %v", got, want)
	}
}

func TestSimilarPairsResumesAfterPartialFailure(t *testing.T) {
	texts := make([]string, 70) // three batches: 32, 32, 6
	for i := range texts {
		texts[i] = fmt.Sprintf("wording %d", i)
	}
	dir := filepath.Join(t.TempDir(), "semantic")
	c := openVC(t, dir, 0)
	s := labelSpy()
	s.hook = func(_ context.Context, _ []string, _ Kind, call int) ([][]float32, error, bool) {
		if call == 3 {
			return nil, context.DeadlineExceeded, true
		}
		return nil, nil, false
	}
	ps, rep, err := SimilarPairs(context.Background(), s, c, texts, 0.5)
	if err == nil || ps != nil || !rep.Canceled || rep.Scored != 64 || rep.Failed != 6 {
		t.Fatalf("%v %+v %v", ps, rep, err)
	}
	c2 := openVC(t, dir, 0)
	if c2.Len() != 64 {
		t.Fatalf("%d vectors saved from the finished batches, want 64", c2.Len())
	}
	s2 := labelSpy()
	_, rep, err = SimilarPairs(context.Background(), s2, c2, texts, 0.5)
	if err != nil || rep.Cached != 64 || rep.Scored != 6 || len(s2.texts) != 6 {
		t.Fatalf("resume: %+v %v embedded=%d", rep, err, len(s2.texts))
	}
}

func TestSimilarPairsCacheSaveFailureKeepsThePairs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := filepath.Join(t.TempDir(), "semantic")
	c := openVC(t, dir, 0)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	ps, rep, err := SimilarPairs(context.Background(), labelSpy(), c, []string{"LinkedIn URL", "LinkedIn profile URL"}, 0.5)
	if err != nil || len(ps) != 1 {
		t.Fatalf("%v %v", ps, err)
	}
	if !strings.Contains(rep.Err, "semantic: cache") {
		t.Errorf("the failed save is not reported: %+v", rep)
	}
}

func TestSimilarPairsMixedWidthCache(t *testing.T) {
	c := openVC(t, t.TempDir(), 0)
	s := labelSpy()
	_ = c.Put(s.Model(), KindSimilarity, "wide", []float32{1, 0, 0, 0})
	_ = c.Put(s.Model(), KindSimilarity, "narrow", []float32{1, 0})
	ps, rep, err := SimilarPairs(context.Background(), s, c, []string{"wide", "narrow", "fresh"}, -1)
	if !errors.Is(err, ErrEmbed) || ps != nil || rep.Err == "" {
		t.Fatalf("%v %+v %v", ps, rep, err)
	}
}

func TestClipSimilarText(t *testing.T) {
	if s, cut := ClipSimilarText("short"); s != "short" || cut {
		t.Error("clipped a short text")
	}
	long := strings.Repeat("字", 200) // 600 bytes
	s, cut := ClipSimilarText(long)
	if !cut || len(s) > MaxSimilarTextBytes || len(s)%3 != 0 || !strings.HasPrefix(long, s) {
		t.Errorf("cut=%v len=%d", cut, len(s))
	}
	if _, _, err := SimilarPairs(context.Background(), labelSpy(), nil, []string{s, "x"}, 0.5); err != nil {
		t.Errorf("clipped text rejected: %v", err)
	}
	if s, cut := ClipSimilarText(strings.Repeat("a", MaxSimilarTextBytes)); cut || len(s) != MaxSimilarTextBytes {
		t.Error("clipped a text at the cap")
	}
}

func TestDupText(t *testing.T) {
	if got := DupText("", "desc"); got != "desc" {
		t.Errorf("no title: %q", got)
	}
	if got := DupText("  ", "desc"); got != "desc" {
		t.Errorf("blank title: %q", got)
	}
	if got := DupText("Engineer", "desc"); got != "Engineer\ndesc" {
		t.Errorf("title: %q", got)
	}
	long := DupText(strings.Repeat("t", 1000), strings.Repeat("字", 1000))
	if len(long) > MaxDupTitleBytes+1+MaxDupTextBytes {
		t.Errorf("%d bytes", len(long))
	}
}

func TestTitleIndex(t *testing.T) {
	x := NewTitleIndex()
	if !x.Add("Backend Engineer", "Build services in Go") || x.Title("Build services in Go") != "Backend Engineer" {
		t.Fatal("recorded title not found")
	}
	if x.Add("", "d") || x.Add("T", "  ") {
		t.Error("blank input recorded")
	}
	// The same description under another title is ambiguous for both.
	x.Add("Backend Engineer (Remote)", "Build services in Go")
	if got := x.Title("Build services in Go"); got != "" {
		t.Errorf("ambiguous description gave %q", got)
	}
	// Repeating the same title is not ambiguity.
	y := NewTitleIndex()
	y.Add("A", "d1")
	y.Add("A", "d1")
	if y.Title("d1") != "A" {
		t.Error("repeat made it ambiguous")
	}
	// The key is the embedded part: a long description matches by its first kilobyte.
	long := strings.Repeat("x", MaxDupTextBytes)
	y.Add("Long", long+" tail one")
	if y.Title(long+" a different tail") != "Long" {
		t.Error("lookup by the embedded prefix failed")
	}
	var nilIdx *TitleIndex
	if nilIdx.Title("d1") != "" {
		t.Error("nil index")
	}
	if y.Title("never added") != "" {
		t.Error("unknown description")
	}
}

func TestNewTitledSimilarity(t *testing.T) {
	ctx := context.Background()
	desc := "Build and run backend services in production"
	a, b := desc+" for payments", desc+" for payments team"
	titles := NewTitleIndex()
	titles.Add("Backend Engineer", a)
	titles.Add("Pastry Chef", b)
	s := labelSpy()
	with, ok, err := NewTitledSimilarity(s, nil, titles)(ctx, a, b)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	without, ok, err := NewSimilarity(labelSpy(), nil)(ctx, a, b)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !(with < without) {
		t.Errorf("different titles should lower the cosine: with %.3f, without %.3f", with, without)
	}
	var saw bool
	for _, tx := range s.texts {
		if strings.HasPrefix(tx, "Backend Engineer\n"+desc) {
			saw = true
		}
	}
	if !saw {
		t.Errorf("the title was not embedded: %q", s.texts)
	}
	// A title the index does not know leaves that side description-only, still a reading.
	if _, ok, err := NewTitledSimilarity(labelSpy(), nil, NewTitleIndex())(ctx, a, b); err != nil || !ok {
		t.Errorf("empty index: %v %v", ok, err)
	}
	// Same description, same title: identical text, cosine 1.
	if cos, ok, err := NewTitledSimilarity(labelSpy(), nil, titles)(ctx, a, a); err != nil || !ok || cos != 1 {
		t.Errorf("identical: %v %v %v", cos, ok, err)
	}
}

func TestNewSimilarity(t *testing.T) {
	s := labelSpy()
	c := openVC(t, t.TempDir(), 0)
	sim := NewSimilarity(s, c)
	ctx := context.Background()
	cos, ok, err := sim(ctx, "Senior engineer LinkedIn profile URL", "Senior engineer LinkedIn URL")
	if err != nil || !ok || cos < 0.8 {
		t.Fatalf("%v %v %v", cos, ok, err)
	}
	if cos, ok, err := sim(ctx, "same text", "same text"); err != nil || !ok || cos < 0.9999 {
		t.Errorf("identical: %v %v %v", cos, ok, err)
	}
	if _, ok, err := sim(ctx, "", "something"); ok || err != nil {
		t.Errorf("blank: ok=%v err=%v, want no reading and no error", ok, err)
	}
	// The second look at the same pair costs no embedding.
	n := s.count()
	if _, _, err := sim(ctx, "Senior engineer LinkedIn profile URL", "Senior engineer LinkedIn URL"); err != nil || s.count() != n {
		t.Errorf("re-embedded a cached pair: %d -> %d, %v", n, s.count(), err)
	}
	// Only the first kilobyte is embedded.
	long := strings.Repeat("x", MaxDupTextBytes) + " tail that must not matter"
	s2 := labelSpy()
	if _, _, err := NewSimilarity(s2, nil)(ctx, long, long+"more"); err != nil {
		t.Fatal(err)
	}
	for _, tx := range s2.texts {
		if len(tx) > MaxDupTextBytes {
			t.Errorf("embedded %d bytes", len(tx))
		}
	}
	// Failures are errors, never a reading.
	bad := labelSpy()
	bad.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, fmt.Errorf("%w: down", ErrUnavailable), true
	}
	if cos, ok, err := NewSimilarity(bad, nil)(ctx, "a b", "c d"); ok || cos != 0 || !errors.Is(err, ErrUnavailable) {
		t.Errorf("unavailable: %v %v %v", cos, ok, err)
	}
	if _, ok, err := NewSimilarity(nil, nil)(ctx, "a b", "c d"); ok || !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil embedder: %v %v", ok, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok, err := NewSimilarity(labelSpy(), nil)(cctx, "a b", "c d"); ok || err == nil {
		t.Errorf("canceled: %v %v", ok, err)
	}
}
