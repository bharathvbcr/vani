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

// The lines every posting of an employer repeats (a degree requirement, a team
// practice, a communication line) say nothing about the work. Against the real
// bank (SEMANTIC_REAL_BANK=1, KBSYNC_BANK) and the real model, an off-target
// posting dressed in them must not read as close to the owner's work as a posting
// about that work. The postings are synthetic; only counts and scores print.
func TestTesslRealGenericLinesDoNotCarryTheMatch(t *testing.T) {
	if os.Getenv("SEMANTIC_REAL_BANK") != "1" {
		t.Skip("set SEMANTIC_REAL_BANK=1 and KBSYNC_BANK to run against the real bank")
	}
	e := realTessl(t)
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, "Early-career applied AI and LLM engineering roles: retrieval-augmented generation, tool-calling agents, "+
		"inference serving. Early-career ML systems and performance engineering: LLM inference runtimes, GPU kernels in Metal and CUDA, "+
		"quantization, benchmark-driven optimization.")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	generic := "Bachelor's degree in Computer Science, Engineering, Mathematics or a related field, or equivalent practical experience.\n" +
		"Strong written and verbal communication skills and the ability to collaborate across teams.\n" +
		"Operate AI-native engineering practices: use AI-assisted coding, multi-agent workflows and agentic code review tools as part of the team's workflow.\n" +
		"3+ years of non-internship professional software development experience.\n"
	docs := []Doc{
		{Key: "inference", Title: "Software Engineer, LLM Inference Performance", Description: "Requirements:\n" +
			"Optimize the latency and throughput of large language model serving on GPUs.\n" +
			"Write and tune CUDA and Metal kernels for attention and quantized matrix multiplication.\n" +
			"Build the request scheduling and batching layer of our inference runtime in Rust and Go.\n" +
			"Profile memory bandwidth and KV cache behavior and publish reproducible benchmarks.\n" + generic},
		{Key: "fees", Title: "Software Development Engineer, Fees Tech", Description: "Requirements:\n" +
			"Design and build services that calculate and bill seller fees across marketplaces.\n" +
			"Own the ledger reconciliation pipelines that settle fees with payments partners.\n" +
			"Work with finance and product teams to turn fee policy into reliable software.\n" +
			"Model fee schedules and promotions in relational databases and keep them auditable.\n" + generic},
		{Key: "boilerplate-only", Title: "Software Engineer", Description: "Requirements:\n" + generic},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if rep, err := m.Warm(ctx, docs, len(docs)); err != nil {
		t.Fatalf("warm: %v (%+v)", err, rep)
	}
	got := map[string]Match{}
	for _, d := range docs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok {
			t.Fatalf("%s not scored", d.Key)
		}
		got[d.Key] = mt
		t.Logf("%-16s score=%.3f raw=%.3f thin=%v", d.Key, mt.Score, mt.Raw, mt.Thin)
		for _, ev := range mt.Evidence {
			t.Logf("    %.3f  %q <-> %s", ev.Cosine, ev.Snippet, ev.Label)
		}
	}
	if margin := got["inference"].Score - got["fees"].Score; margin < 0.25 {
		t.Errorf("a posting about the owner's work leads an off-target one by only %.3f", margin)
	}
	// A bare title and a team-practice line (which no rule can tell from work)
	// are two chunks, not a posting about the owner's work. Before the breadth
	// discount this scored 0.80 beside 0.81 for the inference posting.
	if margin := got["inference"].Score - got["boilerplate-only"].Score; margin < 0.15 {
		t.Errorf("a posting of generic lines scores %.3f, only %.3f under the on-target posting's %.3f",
			got["boilerplate-only"].Score, margin, got["inference"].Score)
	}
}
