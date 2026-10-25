package rkbresume

// What a caller needs to say, before anything is spent, the most a Build can
// cost. The numbers are this package's own limits, exported so a dry run states
// them instead of copying them.

// MaxTurns is the most model turns one Build sends: the first, and at most one
// retry after a lint or fit failure or a refused choice.
const MaxTurns = 2

// TurnMaxTokens is the output ceiling a turn asks for before the provider
// shapes it (an effort level adds thinking room on top).
const TurnMaxTokens = replyMaxTokens

// PromptBound is an upper bound on the bytes the turn-th model turn (1-based)
// sends for a posting of descRunes runes (more are cut to MaxPostingRunes): the
// instructions, the posting and the evidence pool, each at its own cap. A retry
// also carries the first reply (at most maxReplyBytes) and the feedback about
// it. It is a ceiling, not a prediction: the pool is usually well under its cap.
func PromptBound(turn, descRunes int) int {
	posting := max(min(descRunes, MaxPostingRunes), 0) * 4 // a rune is at most 4 bytes
	n := len(systemPrompt) + posting + maxPoolBytes + 5000
	if turn > 1 {
		n += maxReplyBytes + maxFeedbackProblems*maxFeedbackRunes*4
	}
	return n
}
