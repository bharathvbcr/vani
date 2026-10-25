package rkbresume

import "testing"

// PromptBound is a ceiling, so a prompt built from the largest allowed posting
// and the largest allowed pool must fit under it, and a retry's allowance is
// larger than the first turn's.
func TestPromptBoundCoversTheLargestPrompt(t *testing.T) {
	if MaxTurns != 2 {
		t.Fatalf("MaxTurns = %d; tailor sends one retry at most", MaxTurns)
	}
	if TurnMaxTokens != replyMaxTokens {
		t.Fatalf("TurnMaxTokens = %d, replyMaxTokens %d", TurnMaxTokens, replyMaxTokens)
	}
	first, retry := PromptBound(1, 1<<20), PromptBound(2, 1<<20)
	// The existing prompt-size test's own ceiling is runes of posting + pool + 5000.
	if floor := MaxPostingRunes + maxPoolBytes + 5000; first < floor {
		t.Errorf("first turn bound %d is under the largest prompt %d", first, floor)
	}
	if retry <= first {
		t.Errorf("a retry also carries the first reply and feedback: %d <= %d", retry, first)
	}
	if small := PromptBound(1, 10); small >= first {
		t.Errorf("a short posting bound %d should be under a long one's %d", small, first)
	}
	if PromptBound(1, -5) < len(systemPrompt) {
		t.Error("a negative length must not shrink the bound below the instructions")
	}
}
