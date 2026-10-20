package semantic

import (
	"strings"
	"testing"
)

// Lines that every posting of a kind repeats say what the employer asks of any
// candidate, not what the job is. Against a knowledge base they are free points:
// a degree line pairs with the degree entry, a "team practices" line with any
// agent project. They are left out of the match, not out of the posting.

func TestGenericLineRecognisesCredentialAndProcessLines(t *testing.T) {
	for _, s := range []string{
		"Bachelor's degree in Computer Science, Engineering, Mathematics or a related field, or equivalent practical experience.",
		"BS/MS in Computer Science or a related technical discipline",
		"Ph.D. in Computer Science, Machine Learning or a related field.",
		"Currently pursuing a Master's degree in a technical field",
		"Equivalent practical experience.",
		"Strong written and verbal communication skills and the ability to collaborate across teams.",
		"Excellent interpersonal skills and a team player.",
		"3+ years of non-internship professional software development experience.",
		"At least 5 years of professional experience",
		"Must be authorized to work in the United States without sponsorship.",
		"Ability to pass a background check.",
	} {
		if !genericLine(s) {
			t.Errorf("%q should be generic", s)
		}
	}
}

func TestGenericLineKeepsLinesAboutTheWork(t *testing.T) {
	for _, s := range []string{
		"Optimize the latency and throughput of large language model serving on GPUs.",
		"Write and tune CUDA and Metal kernels for attention and quantized matrix multiplication.",
		"Reduce the degree of parallelism wasted in kernel launches and measure the effect.",
		"Build a degree-of-freedom solver for robot arms in Rust.",
		"5+ years of experience in building LLM inference systems.",
		"3 years of experience with CUDA kernel development",
		"Ph.D. level research in speculative decoding for LLM serving.",
		"Master the on-call rotation and improve our alerting.",
		"Collaborate with researchers to ship new model architectures to production.",
		"Work in a fast-paced environment shipping CUDA kernels and benchmarks every week.",
		"Visa processing pipelines for payments partners.", // a domain, not an immigration clause
	} {
		if genericLine(s) {
			t.Errorf("%q is about the work and must stay", s)
		}
	}
}

func TestGenericLineAdversarialInputs(t *testing.T) {
	long := strings.Repeat("Bachelor's ", 20000)
	for name, s := range map[string]string{
		"empty": "", "space": "   ", "control": "\x00\x01\x02", "invalid utf8": "\xff\xfe degree",
		"long": long, "nested": strings.Repeat("years of experience ", 5000),
	} {
		_ = genericLine(s) // must neither panic nor take noticeable time
		_ = name
	}
	if genericLine("") || genericLine("   ") {
		t.Errorf("an empty line is not generic, it is nothing")
	}
}

func TestChunkPostingLeavesGenericLinesOut(t *testing.T) {
	desc := "Requirements:\n" +
		"Optimize the latency and throughput of large language model serving on GPUs.\n" +
		"Bachelor's degree in Computer Science, Engineering or a related field, or equivalent practical experience.\n" +
		"Strong written and verbal communication skills and the ability to collaborate across teams.\n" +
		"Profile memory bandwidth and KV cache behavior and publish reproducible benchmarks.\n"
	var lines []string
	for _, c := range ChunkPosting("Software Engineer, LLM Inference", desc) {
		lines = append(lines, c.Text)
	}
	if len(lines) != 3 {
		t.Fatalf("want the title and the two work lines, got %q", lines)
	}
	for _, l := range lines {
		if genericLine(l) {
			t.Errorf("generic line chunked: %q", l)
		}
	}
	// A posting made only of them keeps its title alone, so it reads as thin and
	// the ranker takes its prior instead of a confident number.
	only := ChunkPosting("Software Engineer", "Requirements:\nBachelor's degree in Computer Science or equivalent practical experience.\nStrong communication skills and a team player.\n")
	if len(only) != 1 || only[0].Label != "title" {
		t.Errorf("a posting of generic lines alone should keep only its title, got %+v", only)
	}
}
