package draft

import (
	"context"
	"testing"
)

// The false-refusal check for the credential, acronym and spelled-number reads:
// honest answers written (by hand, for this test) from the evidence in
// anthropicKB and the real Anthropic posting fixture, in the way a model writes
// them, must all pass the guard. Each one uses at least one thing the extended
// guard looks at: a spelled number, a generic acronym, an acronym the posting or
// target text shows, a figure grounded by its digits. A refusal here is a false
// refusal, and the corpus is the measurement: add a draft when a real one is
// refused.
func TestHonestDraftsAreNotRefusedByTheExtendedGuard(t *testing.T) {
	const target = "Software engineer roles building tools and infrastructure for ML researchers and engineers"
	d, err := New(&provider{}, Config{Model: "m", TargetSummary: target}, anthropicKB())
	if err != nil {
		t.Fatal(err)
	}
	job := anthropicPosting(t)
	honest := []string{
		"I build tools for researchers. At Example Labs I built a React and TypeScript dashboard that they use daily to launch, monitor and compare training runs, and a labeling interface for human feedback that thirty annotators use.",
		"One of the things I enjoy most is making research workflows faster. No one should have to babysit a training run, so I built a dashboard to launch, monitor and compare them.",
		"My work at Sample Analytics was a sandboxed Python execution service for untrusted notebook code, which taught me to treat safety as a product requirement. That is why the AI safety focus here matters to me.",
		"I want to build tools for ML researchers and engineers. My labeling interface for collecting human feedback on model outputs was used by 30 annotators, and I would like to do that at the scale of AI research here.",
		"I organised a reading group on AI safety and interpretability papers, and I want to work where that thinking shapes the product. At first it was a hobby; now it is how I choose where to work.",
		"The first thing that drew me to this role is the chance to build internal tools for researchers. I have built a dashboard they use every day and I would bring that to your team.",
		"I bring Python, React and TypeScript experience, and I have worked one-on-one with researchers to learn what they need.",
	}
	for _, reply := range honest {
		d.p = &provider{text: reply}
		if _, err := d.Draft(context.Background(), Request{Question: "Why Anthropic?", Job: job}); err != nil {
			t.Errorf("an honest draft was refused: %v\n  %s", err, reply)
		}
	}
}
