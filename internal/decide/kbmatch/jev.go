package kbmatch

// Descriptions is the option descriptions a decision model (Jev) judges by, in
// the slot -> option -> description shape jev.Config takes. They say the same as
// Prompt, one option at a time.
func Descriptions() map[string]map[string]string {
	return map[string]map[string]string{Slot: {
		"core":      "The main duties of this job are work the job seeker has done or is aiming for.",
		"partial":   "Some of the main duties of this job are work the job seeker has done or is aiming for, and others are not.",
		"adjacent":  "The same field, tools or industry as the job seeker's work but different duties; a sales, marketing, operations or program role at an AI company is adjacent or unrelated, never core.",
		"unrelated": "Different work from anything the job seeker has done or is aiming for.",
	}}
}

// Instructions is the question text a decision model is sent for the slot.
func Instructions() map[string]string { return map[string]string{Slot: Prompt} }
