package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// markYesNeighbours sets NearYes on the candidates in the yes-neighbour cell's
// pool (third Fable pass on the second review): the rankfit.YesNeighbourPool
// unlabelled postings nearest the owner's yes answers, filled round-robin by
// anchor, which AuditDraw then draws from uniformly.
//
// It refuses unless the taste column, computed on the sampled labels exactly
// as `vani relevance` prints it, passes: nearness to a yes is worth the
// owner's places only when it orders their answers better than the title
// part. The anchors are the owner's yes answers made before the seed's ISO
// week opened (all of them for a free-form seed), so the pool holds still
// while the week's answers come in. A candidate the overlays may not draw
// (overlayEligible), a labelled one, and a repost (same title and company) of
// a labelled posting are not in the pool. Vectors are read from the semantic
// store as it stands, nothing is embedded; a store that cannot be read, or no
// anchor with a vector, is an error, never an empty cell that looks drawn.
//
// A taste column that does not pass, a vector store that was never written
// and no anchor with a vector are overlayUnavailable: the inputs do not exist
// yet, so a default quota draws nothing and says why. A store that exists but
// cannot be read, or a taste gate that cannot run, is a plain error.
func (e *env) markYesNeighbours(cands []eval.AuditCandidate, done map[posting.Key]eval.Entry, snap ledger.Snapshot, seed string) error {
	// The store is opened first, so one that exists but cannot be read is an
	// error even for a default quota, never a taste column that "does not pass".
	vr, err := semantic.OpenVectorReader(semanticDir(e.stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return unavailable("audit --yes-neighbours: there is no vector store yet (%v); semantic scoring writes it", err)
	}
	if err != nil {
		return fmt.Errorf("audit --yes-neighbours: the vector store could not be read: %w", err)
	}
	defer vr.Close()
	// Dropped candidates and anchors keep their description out of the
	// snapshot; postingVectors reads it back, or their vectors are never found.
	pv := e.newPostingVectors(vr, nil)
	defer pv.Close()
	sampled, _ := eval.WithoutSelectedOnReading(done)
	items, err := e.judgedItems(sampled, snap, jobinsight.DefaultWeights(), nil)
	if err != nil {
		return fmt.Errorf("audit --yes-neighbours: the taste gate could not run: %w", err)
	}
	taste := e.tasteWith(pv, items, snap)
	if pv.openErr != nil {
		return fmt.Errorf("audit --yes-neighbours: %s", taste.VectorNote)
	}
	if !taste.Passes {
		why := taste.Reason
		if taste.Status == rankfit.TasteOK {
			why = fmt.Sprintf("taste AUC %.2f does not beat the title part's %.2f", taste.AUC, taste.TitleAUC)
		}
		return unavailable("audit --yes-neighbours: refused, the taste column does not pass (%s: %s); see `vani relevance`", taste.Status, why)
	}
	vectorOf := func(r ledger.Record) []float32 {
		v, _ := pv.of(r)
		return v
	}
	cutoff, weekly := eval.WeekStart(seed)
	labelledJobs := map[string]bool{}
	var anchors []rankfit.Vectored
	for k, l := range done {
		r, ok := findRecord(snap, k)
		if !ok {
			continue
		}
		labelledJobs[eval.RepostKey(r.Posting.Title, r.Posting.Company)] = true
		if l.Labels[eval.WouldApply] != "yes" || (weekly && !l.LabeledAt.Before(cutoff)) {
			continue
		}
		if v := vectorOf(r); v != nil {
			anchors = append(anchors, rankfit.Vectored{Key: k, Vector: v})
		}
	}
	if len(anchors) == 0 {
		return unavailable("audit --yes-neighbours: no yes answer from before %s has a stored vector to be near", seed)
	}
	var pool []rankfit.Vectored
	for _, c := range cands {
		if _, labelled := done[c.Key]; labelled || !overlayEligible(c) {
			continue
		}
		r, ok := findRecord(snap, c.Key)
		if !ok || labelledJobs[eval.RepostKey(r.Posting.Title, r.Posting.Company)] {
			continue
		}
		if v := vectorOf(r); v != nil {
			pool = append(pool, rankfit.Vectored{Key: c.Key, Vector: v})
		}
	}
	near := map[posting.Key]bool{}
	for _, k := range rankfit.YesNeighbours(anchors, pool, rankfit.YesNeighbourPool) {
		near[k] = true
	}
	for i := range cands {
		cands[i].NearYes = near[cands[i].Key]
	}
	fmt.Fprintf(e.stderr, "vani: yes-neighbour pool: %d postings near %d yes answers (taste AUC %.2f against title %.2f)\n", len(near), len(anchors), taste.AUC, taste.TitleAUC)
	return nil
}
