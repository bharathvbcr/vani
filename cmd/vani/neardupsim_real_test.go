//go:build tessl && darwin && arm64

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The near-duplicate stage as runOnce wires it, through the production
// openEmbedder and EmbeddingGemma 2. Without the snapshot the test is skipped
// and says so; under VANI_EMBED_REQUIRE=1 that is a failure.
func TestRealNearDupCosineThroughTheDailyRunWiring(t *testing.T) {
	snap := os.Getenv("VANI_TESSL_SNAPSHOT")
	if snap == "" {
		if home, err := os.UserHomeDir(); err == nil {
			m, _ := filepath.Glob(filepath.Join(home, ".cache", "huggingface", "hub", "models--google--embeddinggemma-2", "snapshots", "*"))
			if len(m) == 1 {
				snap = m[0]
			}
		}
	}
	if snap == "" {
		if os.Getenv("VANI_EMBED_REQUIRE") == "1" {
			t.Fatal("VANI_EMBED_REQUIRE=1 but no embeddinggemma-2 snapshot: set VANI_TESSL_SNAPSHOT")
		}
		t.Skip("SKIPPED, NOT MEASURED: no embeddinggemma-2 snapshot (set VANI_TESSL_SNAPSHOT); the near-dup cosine was not run")
	}
	f := ndSetup(t)
	openEmbedder = productionOpenEmbedder
	f.w.cfg.Semantic.Snapshot = snap

	rep := f.run(t)
	if !rep.SimilarityConfigured || rep.SimilarityErrors != 0 || rep.Partial || rep.Duplicates != 1 || rep.Candidates != 1 {
		t.Fatalf("report %+v\nwarnings:\n%s", rep, f.warn.String())
	}
	f.wantLexical(t)
	c := f.rec(t, f.candidate).Annotations[pipeline.AnnDupCosine]
	cos, err := strconv.ParseFloat(c, 64)
	if err != nil || cos <= 0 || cos >= 1 {
		t.Fatalf("candidate cosine %q", c)
	}
	for _, k := range []posting.Key{f.dup, f.head} {
		if c := f.rec(t, k).Annotations[pipeline.AnnDupCosine]; c != "" {
			t.Fatalf("%s carries a cosine %q", k, c)
		}
	}
	t.Logf("candidate cosine %s", c)
}
