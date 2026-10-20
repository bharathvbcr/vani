package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The near-duplicate stage as runOnce wires it: the shingle rules decide, and
// the embedding cosine (nearDupSimilarity) only annotates candidates.

var ndT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// ndWords is a description of n distinct words; the prefix carries postMarker
// so a test can prove no description reaches a warning line.
func ndWords(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%s%d ", postMarker, prefix, i)
	}
	return b.String()
}

type ndFixture struct {
	w         *wired
	warn      *bytes.Buffer
	stateDir  string
	head      posting.Key // the earliest posting
	dup       posting.Key // same role, place and text as head: a Duplicate
	candidate posting.Key // 20 other words, then 80 of head's 100: a candidate, kept Live
}

// ndSetup writes three postings of one role at one company and returns a wired
// with semantic matching on. The caller sets openEmbedder.
func ndSetup(t *testing.T) ndFixture {
	t.Helper()
	stateDir := t.TempDir()
	led, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	add := func(id, desc string, at time.Time) posting.Key {
		t.Helper()
		k, err := posting.GreenhouseKey("acme", id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Platform Engineer", Location: "Remote", Description: desc}
		if err := led.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		return k
	}
	jd := ndWords("duty", 100)
	f := ndFixture{stateDir: stateDir, warn: &bytes.Buffer{}}
	f.head = add("1", jd, ndT0)
	f.dup = add("2", jd, ndT0.Add(time.Hour))
	// The differing words lead: the cosine reads only the first kilobyte.
	f.candidate = add("3", ndWords("other", 20)+ndWords("duty", 80), ndT0.Add(2*time.Hour))

	cfg := config.Default()
	cfg.KB.Bank = filepath.Join(stateDir, "bank.json")       // SemanticOn needs both set;
	cfg.Semantic.Snapshot = filepath.Join(stateDir, "model") // neither is read here
	f.w = &wired{cfg: cfg, stateDir: stateDir, led: led, warn: f.warn}

	old, oldLoad := openEmbedder, nearDupLoadTimeout
	t.Cleanup(func() { openEmbedder, nearDupLoadTimeout = old, oldLoad })
	return f
}

func (f ndFixture) rec(t *testing.T, k posting.Key) ledger.Record {
	t.Helper()
	r, ok := f.w.led.Get(k)
	if !ok {
		t.Fatalf("%s not in the ledger", k)
	}
	return r
}

func (f ndFixture) run(t *testing.T) pipeline.NearDupReport {
	t.Helper()
	rep, err := f.w.annotateNearDups(context.Background(), nil, ndT0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// wantLexical checks what the shingle rules decide, with or without a cosine.
func (f ndFixture) wantLexical(t *testing.T) {
	t.Helper()
	if r := f.rec(t, f.dup); r.Status != posting.Duplicate || r.Annotations[pipeline.DuplicateOfKey] != string(f.head) {
		t.Fatalf("dup: status %s, annotations %v", r.Status, r.Annotations)
	}
	r := f.rec(t, f.candidate)
	if r.Status != posting.Live || r.Annotations[pipeline.AnnDupCandidateOf] != string(f.head) || r.Annotations[pipeline.AnnDupScore] == "" {
		t.Fatalf("candidate: status %s, annotations %v", r.Status, r.Annotations)
	}
	if f.rec(t, f.head).Status != posting.Live {
		t.Fatal("the head was demoted")
	}
}

func (f ndFixture) wantNoDescriptionLogged(t *testing.T) {
	t.Helper()
	if strings.Contains(strings.ToLower(f.warn.String()), strings.ToLower(postMarker)) {
		t.Fatalf("a description reached the warnings:\n%s", f.warn.String())
	}
}

func fakeOpener(e semantic.Embedder, closed *int) func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
	return func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return e, func() error { *closed++; return nil }, nil
	}
}

func TestNearDupCosineAnnotatesTheCandidateOnly(t *testing.T) {
	f := ndSetup(t)
	emb := &countingEmbedder{Embedder: semantic.NewFakeEmbedder(256, nil)}
	closed := 0
	openEmbedder = fakeOpener(emb, &closed)

	rep := f.run(t)
	if !rep.SimilarityConfigured || rep.SimilarityErrors != 0 || rep.Duplicates != 1 || rep.Candidates != 1 || rep.Partial {
		t.Fatalf("report %+v", rep)
	}
	f.wantLexical(t)
	cos := f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine]
	if cos == "" || cos == "0.000" || cos == "1.000" {
		t.Fatalf("candidate cosine %q", cos)
	}
	// The cosine is not a status input: the duplicate carries none, the head none.
	for _, k := range []posting.Key{f.dup, f.head} {
		if c := f.rec(t, k).Annotations[pipeline.AnnDupCosine]; c != "" {
			t.Fatalf("%s carries a cosine %q", k, c)
		}
	}
	if closed != 1 {
		t.Fatalf("model closed %d times, want 1", closed)
	}
	if _, err := os.Stat(filepath.Join(semanticDir(f.stateDir), "semantic-vectors.json")); err != nil {
		t.Fatalf("vector cache not saved: %v", err)
	}
	f.wantNoDescriptionLogged(t)

	// A rerun reads both vectors from the cache and writes nothing.
	calls := emb.count()
	rep = f.run(t)
	if emb.count() != calls || rep.Written != 0 || f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine] != cos {
		t.Fatalf("rerun: %d new embed calls, report %+v", emb.count()-calls, rep)
	}
}

// An embedder that cannot be loaded leaves the stage exactly the shingle stage:
// the Duplicate is still found, the candidate still annotated, no cosine is
// written, the report says no similarity was configured, and the run goes on.
func TestNearDupFallsBackToShinglesWhenTheEmbedderIsUnavailable(t *testing.T) {
	for name, open := range map[string]func(context.Context, config.Config) (semantic.Embedder, func() error, error){
		"unavailable": func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return nil, nil, fmt.Errorf("%w: built without -tags tessl", semantic.ErrUnavailable)
		},
		"load error": func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
			return nil, nil, errors.New("snapshot unreadable")
		},
		"load hangs": func(ctx context.Context, _ config.Config) (semantic.Embedder, func() error, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := ndSetup(t)
			nearDupLoadTimeout = 50 * time.Millisecond
			openEmbedder = open
			start := time.Now()
			rep := f.run(t)
			if time.Since(start) > 5*time.Second {
				t.Fatalf("stage took %v", time.Since(start))
			}
			if rep.SimilarityConfigured || rep.SimilarityErrors != 0 || rep.Duplicates != 1 || rep.Candidates != 1 {
				t.Fatalf("report %+v", rep)
			}
			f.wantLexical(t)
			if c := f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine]; c != "" {
				t.Fatalf("cosine %q without an embedder", c)
			}
			if !strings.Contains(f.warn.String(), "near-duplicate cosine off, shingles only") ||
				!strings.Contains(f.warn.String(), "SimilarityConfigured:false") {
				t.Fatalf("warnings do not say the cosine was off:\n%s", f.warn.String())
			}
		})
	}
}

// A model that loads but fails every reading is counted, never a cosine of 0,
// and a cosine an earlier run wrote is cleared rather than left to look fresh.
func TestNearDupEmbedFailureIsCountedNotACosine(t *testing.T) {
	f := ndSetup(t)
	closed := 0
	openEmbedder = fakeOpener(semantic.NewFakeEmbedder(256, nil), &closed)
	f.run(t)
	if f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine] == "" {
		t.Fatal("setup: no cosine on the first run")
	}
	if err := semantic.ResetVectorCache(semanticDir(f.stateDir)); err != nil { // the next run must embed again
		t.Fatal(err)
	}
	broken := &countingEmbedder{Embedder: semantic.NewFakeEmbedder(256, nil),
		hook: func(context.Context, []string) ([][]float32, error, bool) { return nil, errors.New("metal device lost"), true }}
	openEmbedder = fakeOpener(broken, &closed)
	rep := f.run(t)
	if !rep.SimilarityConfigured || rep.SimilarityErrors != 1 || broken.count() == 0 {
		t.Fatalf("report %+v, %d embed calls", rep, broken.count())
	}
	f.wantLexical(t)
	if c := f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine]; c != "" {
		t.Fatalf("a failed reading left cosine %q", c)
	}
	if closed != 2 {
		t.Fatalf("model closed %d times over two runs", closed)
	}
}

// A vector cache that cannot be read is rebuilt; the cosine still annotates.
func TestNearDupRebuildsAnUnreadableVectorCache(t *testing.T) {
	f := ndSetup(t)
	closed := 0
	openEmbedder = fakeOpener(semantic.NewFakeEmbedder(256, nil), &closed)
	dir := semanticDir(f.stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "semantic-vectors.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := f.run(t)
	if !rep.SimilarityConfigured || rep.SimilarityErrors != 0 || f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine] == "" {
		t.Fatalf("report %+v, annotations %v", rep, f.rec(t, f.candidate).Annotations)
	}
	if strings.Contains(f.warn.String(), "vector cache off") {
		t.Fatalf("the cache was rebuildable:\n%s", f.warn.String())
	}
	if _, err := semantic.OpenVectorCache(dir, 0); err != nil {
		t.Fatalf("rebuilt cache does not open: %v", err)
	}
}

// With semantic matching off, or a cancelled run, no model is loaded.
func TestNearDupSimilarityNotLoadedWhenOffOrCancelled(t *testing.T) {
	f := ndSetup(t)
	loads := 0
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		loads++
		return semantic.NewFakeEmbedder(256, nil), nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sim, finish := f.w.nearDupSimilarity(ctx); sim != nil {
		t.Fatal("a cancelled run got a similarity")
	} else {
		finish()
	}
	f.w.cfg.KB.Bank = ""
	rep := f.run(t)
	if loads != 0 || rep.SimilarityConfigured {
		t.Fatalf("%d loads, report %+v", loads, rep)
	}
	f.wantLexical(t)
}
