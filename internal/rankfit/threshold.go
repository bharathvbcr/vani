package rankfit

import "fmt"

// The fit thresholds (docs/RANKING_PLAN.md). Skip does not count; below them
// a fit refuses and says it could not run.
const (
	// MinPlattPostings is the judged postings one backend needs before its
	// confidence is calibrated (Phase 4).
	MinPlattPostings = 100
	// MinJudged is the usable audit judgments (yes, maybe or no) the weight
	// fit needs (Phase 5). Marks add volume but do not count toward it.
	MinJudged = 150
)

// NotRunError is a fit that refused to run: too few labels, or labels that
// cannot support it (one class only). It is never a result.
type NotRunError struct {
	// Part names what could not run ("platt d1-ultra", "weight fit").
	Part string
	// Have and Need count what the threshold is over; Need 0 means the
	// refusal is not a count (Why says what it is).
	Have, Need int
	What       string
	Why        string
}

func (e *NotRunError) Error() string {
	if e.Need > 0 {
		return fmt.Sprintf("could not run: %s needs %d %s, has %d", e.Part, e.Need, e.What, e.Have)
	}
	return fmt.Sprintf("could not run: %s: %s", e.Part, e.Why)
}

func needAtLeast(part, what string, have, need int) error {
	if have < need {
		return &NotRunError{Part: part, Have: have, Need: need, What: what}
	}
	return nil
}
