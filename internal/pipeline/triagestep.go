package pipeline

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Funnel holds the spend and fail-open hooks of the triage funnel. Every
// field is optional; the zero value is the plain pipeline.
type Funnel struct {
	// FailOpen counts postings kept because a check could not run.
	FailOpen *FailOpen
	// Cache lets a posting inherit the reading of a matching one. It needs
	// ModelID: with none, nothing is inherited.
	Cache *ReadCache
	// ModelID names the model and prompt context a reading is made under
	// (backends, target summary). A reading is only reused under the same id.
	ModelID string
	// MinDescriptionRunes: a description with fewer non-space runes skips the
	// paid triage call and keeps the posting unread. Zero skips only a blank one.
	MinDescriptionRunes int
	// Notes gates the paid notes call. nil asks for every scored posting.
	Notes *NotesGate
	// ScoreFirst runs DevPrism before triage, so coverage is known by the time
	// the posting is triaged. It is never a gate: a failed score does not stop
	// triage, and a low or zero coverage does not drop or skip anything.
	ScoreFirst bool
}

// readResult is the outcome of the triage step for one live posting.
type readResult struct {
	ans    decide.Answer
	status posting.Status
	reason string
	ann    map[string]string
	// pre are the scoring annotations made before triage; kept unless dropped.
	pre map[string]string
}

// descriptionTooShort reports whether p has too little description to pay a
// model to read.
func (r *Runner) descriptionTooShort(p posting.Posting) bool {
	n := 0
	for _, c := range strings.Fields(p.Description) {
		n += utf8.RuneCountInString(c)
	}
	return n == 0 || n < r.MinDescriptionRunes
}

// read decides what triage says about a live posting, in order: a title read
// that a rule drops, a description too short to read (kept, no paid call), a
// reading inherited from a matching posting, then the backends.
func (r *Runner) read(ctx context.Context, key posting.Key, rec ledger.Record) (readResult, error) {
	p := rec.Posting
	if r.Policy.ReadTitle != nil && strings.TrimSpace(p.Title) == "" {
		r.FailOpen.Note(CauseTitleUnread)
	}
	// An exclusion, or a title reading that a drop rule drops, ends triage
	// before any backend is asked; every other title reading is discarded and
	// the backends read the posting as before.
	if ans, reason, dropped := r.Policy.EvaluatePosting(p); dropped {
		return readResult{ans: ans, status: posting.Dropped, reason: reason, ann: triageAnnotations(ans)}, nil
	}
	if r.descriptionTooShort(p) {
		r.FailOpen.Note(CauseEmptyDescription)
		reason := "description empty or too short to read; triage skipped, kept unread"
		return readResult{status: posting.Unsure, reason: reason,
			ann: map[string]string{AnnTriageSkipped: CauseEmptyDescription}}, nil
	}
	pre := r.preScore(ctx, rec)

	var (
		k      string
		leader bool
		ans    decide.Answer
		ok     bool
	)
	if r.Cache != nil && r.ModelID != "" {
		k = readKey(p, r.ModelID)
		cached, from, hit, lead, err := r.Cache.lookup(ctx, r.Ledger, key, k, r.ModelID)
		if err != nil {
			return readResult{}, err
		}
		if hit {
			// The drop rule runs again on the copied reading under today's
			// policy; an answer it would now call unsure is asked afresh.
			if st, reason := r.Policy.Evaluate(cached); st != posting.Unsure {
				ann := triageAnnotations(cached)
				ann[AnnTriageInheritedFrom] = string(from)
				ann[AnnTriageModel] = r.ModelID
				return readResult{ans: cached, status: st, reason: reason, ann: ann, pre: pre}, nil
			}
		}
		leader = lead
		if leader {
			// Release the waiters whatever happens below.
			defer func() { r.Cache.finish(k, key, ans, ok) }()
		}
	}
	var err error
	ans, err = r.Triager.Triage(ctx, p)
	if err != nil {
		return readResult{}, fmt.Errorf("pipeline: triage %s: %w", key, err)
	}
	if noReading(ans) {
		return readResult{}, fmt.Errorf("%w: %s", ErrTriageUnavailable, key)
	}
	st, reason := r.Policy.Evaluate(ans)
	ok = st != posting.Unsure
	ann := triageAnnotations(ans)
	if r.ModelID != "" {
		ann[AnnTriageModel] = r.ModelID
	}
	return readResult{ans: ans, status: st, reason: reason, ann: ann, pre: pre}, nil
}

// preScore runs DevPrism ahead of triage when ScoreFirst is set. It returns
// the scoring annotations, or nil when scoring is off or failed; a failure is
// counted and never stops triage, and the score stage tries again later.
func (r *Runner) preScore(ctx context.Context, rec ledger.Record) map[string]string {
	if !r.ScoreFirst || r.SkipScoring || r.Scorer == nil || strings.TrimSpace(rec.Posting.Description) == "" {
		return nil
	}
	ann, err := r.scoreAnnotations(ctx, rec)
	if err != nil {
		r.FailOpen.Note(CausePrescoreFailed)
		return nil
	}
	return ann
}

// scoreStage is the Triaged/Unsure stage: the annotations and reason for the
// scored event. A posting already scored before triage is not scored twice;
// one with no description has nothing to score, and is left unranked rather
// than ranked zero.
func (r *Runner) scoreStage(ctx context.Context, rec ledger.Record) (map[string]string, string, error) {
	if r.ScoreFirst && (rec.Annotations[ledger.AnnCoverage] != "" || rec.Annotations[AnnCoverageUnknown] != "") {
		return nil, "scored", nil
	}
	if strings.TrimSpace(rec.Posting.Description) == "" {
		return nil, "scoring skipped: no description", nil
	}
	ann, err := r.scoreAnnotations(ctx, rec)
	return ann, "scored", err
}
