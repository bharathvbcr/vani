package draft

// split builds a grounding from what the seeker owns and what the model was only
// shown (a posting, a question, a company, a title).
func split(seeker, shown []string) grounding { return newGrounding(seeker, shown) }

// seekerOnly is a grounding whose every source is seeker-owned: the figure and
// format tests, which are about how text is read and not about whose it is.
func seekerOnly(sources ...string) grounding { return split(sources, nil) }
