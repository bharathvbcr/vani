package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// ErrNoDescription means a posting has no description to score. Scoring an
// empty JD would record a coverage near zero and sink the posting below the
// ranker's unknown-coverage prior, so it is left unscored instead.
var ErrNoDescription = errors.New("pipeline: posting has no description to score")

// ErrScoringDisabled means Rescore was asked to score with scoring switched
// off (SkipScoring): there is nothing to score with, and saying "rescored"
// would be a lie.
var ErrScoringDisabled = errors.New("pipeline: scoring is disabled")

// rescorable are the statuses a record reaches past the scoring stage that
// Rescore may score in place. Digested, dropped, dead and duplicate records
// are left alone: a dropped one stays dropped (Rule 7), and a digested one was
// already shown to the owner.
var rescorable = map[posting.Status]bool{posting.Scored: true, posting.Explained: true}

// Unscored reports whether r went past the scoring stage without a score: it
// is Scored or Explained and has no rank annotation. Advance writes a rank on
// every real score, so the only way here is a pass with SkipScoring set
// (devprism.binary empty), whose Scored event said "scoring disabled". The
// ledger's folded record keeps no event reasons, so the missing rank is the
// test.
func Unscored(r ledger.Record) bool {
	return rescorable[r.Status] && r.Annotations[ledger.AnnRank] == "" && r.Annotations[AnnCoverageUnknown] == ""
}

// Described reports whether r has a description to score.
func Described(r ledger.Record) bool {
	return strings.TrimSpace(r.Posting.Description) != ""
}

// Rescore scores one Unscored record in place. It writes one event that keeps
// the record's status (an explained posting stays explained, so it is neither
// re-explained nor sent through the pipeline again) and adds the annotations
// Advance's scoring stage writes, with reason "rescored". The explanation and
// every other annotation are kept. A score never changes the status (Rule 7).
//
// It writes nothing when DevPrism fails, the call times out or ctx is
// cancelled, so a record is never marked rescored when it was not. A record
// that is no longer Unscored (an earlier pass scored it) is left alone and is
// not an error, so a repeated run does nothing. Scoring is bounded by the
// Scorer's own call timeout and by ctx.
func (r *Runner) Rescore(ctx context.Context, key posting.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.SkipScoring {
		return ErrScoringDisabled
	}
	rec, ok := r.Ledger.Get(key)
	if !ok {
		return fmt.Errorf("pipeline: unknown key %q", key)
	}
	if !Unscored(rec) {
		return nil
	}
	if !Described(rec) {
		return fmt.Errorf("%w: %s", ErrNoDescription, key)
	}
	ann, err := r.scoreAnnotations(ctx, rec)
	if err != nil {
		return err
	}
	return r.emit(ctx, key, rec.Status, "rescored", ann)
}
