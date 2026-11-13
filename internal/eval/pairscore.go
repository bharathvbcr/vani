package eval

import (
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// From pair answers to a per-posting score.
//
// The score is a win rate with a prior, not a Bradley-Terry fit. The pairs are
// drawn at random within a stratum (and a posting's mates by a hash), so a
// posting's raw pick rate is an unbiased estimate of how often the model would
// pick it over a random peer of its stratum, which is the quantity an AUC asks
// about. Bradley-Terry would adjust for the strength of each opponent, but it
// needs a connected comparison graph and several games per posting; here most
// postings have two or three games against opponents who have two or three of
// their own, strata never meet, and the fit is then unidentified or sits at its
// regulariser. The win rate degrades gracefully (two pseudo-games pull a thin
// record to the pool's own pick rate).
//
// "Picked" counts 1 for the chosen posting; the other posting, and both after a
// "neither", count as not picked. A neither says both are clearly unwanted, so
// it is the strongest negative the model gives; a loss may still be a posting
// the owner wants. An abstention or an unusable reply counts for nothing. Scores
// are stratum-relative: a posting that beats its peers in a weak stratum scores
// the same as one that beats its peers in a strong one.

const (
	// PairPriorGames is the weight of the prior, in games.
	PairPriorGames = 2.0
	// MinPostingGames is the fewest usable comparisons a posting needs to be
	// scored at all; with fewer, its score would be mostly the prior, and the
	// report counts it as unscored rather than placing it at the prior.
	MinPostingGames = 2
)

// PostingScore is one posting's record in the pair log.
type PostingScore struct {
	Key     posting.Key
	Games   int // usable answers about pairs the posting was in
	Picked  int // answers that chose it
	Neither int // answers that said neither posting is wanted
	// Score is (Picked + PairPriorGames*PoolRate) / (Games + PairPriorGames).
	Score float64
}

// PairStats describes the answers a score rests on.
type PairStats struct {
	// Rows are the rows under the identity scored; OtherIdentity are rows of
	// another model, question or exemplar set, left out.
	Rows, OtherIdentity int
	Usable              int // answered a, b or neither (latest per pair and order)
	Abstained, Unusable int
	// ShareA is the share of a-or-b answers that said a: 0.5 means position
	// carried nothing, far from it means the model leans on the order. Zero
	// with no such answers (check Decisive).
	ShareA   float64
	Decisive int
	// NeitherShare is the share of usable answers that said neither.
	NeitherShare float64
	// BothOrders counts pairs asked in both orders; Consistent those where the
	// two orders chose the same posting (or both said neither).
	BothOrders, Consistent int
	// PoolRate is the pool's pick rate, the prior's mean.
	PoolRate float64
	// First and Last bound the rows' times.
	First, Last time.Time
}

// ScorePairs scores every posting the rows under id compare. A pair asked more
// than once in one order (a re-ask after the text changed) counts its newest
// answer. It returns the scores by key and the stats; with no usable answer the
// map is empty.
func ScorePairs(rows []PairRow, id PairIdent) (map[posting.Key]PostingScore, PairStats) {
	var st PairStats
	type slot struct {
		lo, hi  posting.Key
		swapped bool
	}
	latest := map[slot]PairRow{}
	for _, r := range rows {
		if r.ident() != id {
			st.OtherIdentity++
			continue
		}
		st.Rows++
		lo, hi := PairKeyOf(r.KeyA, r.KeyB)
		k := slot{lo, hi, r.Swapped}
		if prev, ok := latest[k]; !ok || !r.At.Before(prev.At) {
			latest[k] = r
		}
		if st.First.IsZero() || r.At.Before(st.First) {
			st.First = r.At
		}
		if r.At.After(st.Last) {
			st.Last = r.At
		}
	}
	usable := make([]PairRow, 0, len(latest))
	for _, r := range latest {
		switch decide.Reading(r.Reading) {
		case decide.Answered:
			usable = append(usable, r)
		case decide.Abstained:
			st.Abstained++
		default:
			st.Unusable++
		}
	}
	st.Usable = len(usable)
	if len(usable) == 0 {
		return map[posting.Key]PostingScore{}, st
	}

	picks := map[posting.Key]*PostingScore{}
	get := func(k posting.Key) *PostingScore {
		if p, ok := picks[k]; ok {
			return p
		}
		p := &PostingScore{Key: k}
		picks[k] = p
		return p
	}
	var games, picked, aCount, neither int
	// choice[pair][swapped] is the posting the answer chose ("" for neither).
	choice := map[[2]posting.Key]map[bool]posting.Key{}
	for _, r := range usable {
		a, b := get(r.KeyA), get(r.KeyB)
		a.Games++
		b.Games++
		games += 2
		var chosen posting.Key
		switch r.Answer {
		case wouldapply.OptA:
			a.Picked++
			picked++
			aCount++
			st.Decisive++
			chosen = r.KeyA
		case wouldapply.OptB:
			b.Picked++
			picked++
			st.Decisive++
			chosen = r.KeyB
		default: // neither
			a.Neither++
			b.Neither++
			neither++
		}
		lo, hi := PairKeyOf(r.KeyA, r.KeyB)
		if choice[[2]posting.Key{lo, hi}] == nil {
			choice[[2]posting.Key{lo, hi}] = map[bool]posting.Key{}
		}
		choice[[2]posting.Key{lo, hi}][r.Swapped] = chosen
	}
	st.PoolRate = float64(picked) / float64(games)
	if st.Decisive > 0 {
		st.ShareA = float64(aCount) / float64(st.Decisive)
	}
	st.NeitherShare = float64(neither) / float64(len(usable))
	for _, m := range choice {
		if len(m) == 2 {
			st.BothOrders++
			if m[false] == m[true] {
				st.Consistent++
			}
		}
	}
	out := make(map[posting.Key]PostingScore, len(picks))
	for k, p := range picks {
		p.Score = (float64(p.Picked) + PairPriorGames*st.PoolRate) / (float64(p.Games) + PairPriorGames)
		out[k] = *p
	}
	return out, st
}
