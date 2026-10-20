package jobinsight

import "testing"

// Relevance of the work to the owner's knowledge base (the embedding match and
// the decision model's role fit) leads the blend; target-keyword overlap is a
// small tie-breaker. A keyword is a surface string: a posting can name every
// term and describe other work, or describe the owner's work in other words.

// meaningNoKeywords reads like the owner's work but names none of the target
// terms; keywordsNoMeaning names many of them in a different kind of job.
func meaningNoKeywords() Inputs {
	in := Inputs{
		RoleFit: sl("strong_fit", 0.8), Semantic: 0.80, HasSemantic: true,
		Target: true, TitleHits: 0, BodyHits: 0, AgeKnown: true, AgeDays: 3,
	}
	return in
}

func keywordsNoMeaning() Inputs {
	in := meaningNoKeywords()
	in.Semantic = 0.55
	in.TitleHits, in.BodyHits = 2, 6
	return in
}

func TestMeaningOutranksKeywordOverlap(t *testing.T) {
	a, b := Score(meaningNoKeywords()), Score(keywordsNoMeaning())
	if a.Score <= b.Score {
		t.Errorf("close in meaning (semantic .80, no keywords) scores %.1f, not above keyword-rich but distant (semantic .55) %.1f", a.Score, b.Score)
	}
}

// Keywords still count: of two postings equal in meaning, the one naming the
// target terms leads, and the keyword share stays well below the embedding's.
func TestKeywordsStillBreakTiesInMeaning(t *testing.T) {
	a, b := meaningNoKeywords(), meaningNoKeywords()
	b.TitleHits, b.BodyHits = 2, 6
	if sa, sb := Score(a).Score, Score(b).Score; sb <= sa {
		t.Errorf("equal meaning: keyword-rich %.1f is not above keyword-free %.1f", sb, sa)
	}
	w := DefaultWeights()
	if w.Keyword <= 0 || w.Keyword*5 > w.Semantic {
		t.Errorf("keyword weight %.2f is not a small share of semantic %.2f", w.Keyword, w.Semantic)
	}
}
