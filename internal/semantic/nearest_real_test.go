//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Against the real bank and model (SEMANTIC_REAL_BANK=1, KBSYNC_BANK), the lines
// Nearest picks for an inference posting are closer to it than the ones it picks
// for a nursing posting, and the two sets differ. Only counts and cosines print.
func TestTesslRealNearestFollowsThePosting(t *testing.T) {
	if os.Getenv("SEMANTIC_REAL_BANK") != "1" {
		t.Skip("set SEMANTIC_REAL_BANK=1 and KBSYNC_BANK to run against the real bank")
	}
	e := realTessl(t)
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, "Early-career applied AI and LLM engineering and ML systems roles")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inf, err := m.Nearest(ctx, Doc{Title: "Software Engineer, LLM Inference Performance", Description: "Requirements:\n" +
		"Optimize the latency and throughput of large language model serving on GPUs.\n" +
		"Write and tune CUDA and Metal kernels for attention and quantized matrix multiplication.\n" +
		"Build the request scheduling and batching layer of our inference runtime in Rust and Go.\n"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	nurse, err := m.Nearest(ctx, Doc{Title: "Registered Nurse", Description: "Requirements:\n" +
		"Provide patient care on a medical-surgical unit and administer medications.\n" +
		"Document assessments and coordinate discharge planning with physicians.\n"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(inf) != 5 || len(nurse) != 5 {
		t.Fatalf("want 5 neighbours each, got %d and %d", len(inf), len(nurse))
	}
	t.Logf("inference: top cosine %.3f, fifth %.3f; nurse: top %.3f, fifth %.3f", inf[0].Cosine, inf[4].Cosine, nurse[0].Cosine, nurse[4].Cosine)
	if !(inf[0].Cosine > nurse[0].Cosine+0.05) {
		t.Errorf("the closest line to an inference posting (%.3f) is not clearly closer than for a nursing one (%.3f)", inf[0].Cosine, nurse[0].Cosine)
	}
	same := 0
	for _, a := range inf {
		for _, n := range nurse {
			if a.Text == n.Text {
				same++
			}
		}
	}
	if same > 2 {
		t.Errorf("%d of 5 lines are the same for an inference and a nursing posting; retrieval is not following the posting", same)
	}
}
