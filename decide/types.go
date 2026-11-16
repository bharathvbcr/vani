// Package decide asks typed multiple-choice questions of decision backends
// (Lappi locally, Jev in the cloud, an LLM last) and returns typed readings.
//
// Rule 7 (Lappi-decision docs/caller-contract.md): an answer may only narrow,
// rank or annotate among options the caller already considers valid. Nothing
// in this package means "allow", "pass" or "approve", and packages that make
// those decisions must not import it. A Value is always one of the caller's
// options or empty; parsers reject anything else.
package decide

import "context"

// MaxOptions is the per-slot option cap Lappi enforces (16 options plus a
// reserved abstain row). Every backend is held to it so a question works on
// all of them.
const MaxOptions = 16

// Slot is one multiple-choice question part. Options are caller-supplied and
// are the only legal answers.
type Slot struct {
	Name    string   `json:"name"`
	Options []string `json:"options"`
}

// Question is one decision request. Context is untrusted input (e.g. a job
// description) and is capped by the caller.
type Question struct {
	Task     string            `json:"task"`
	Prompt   string            `json:"question"`
	Context  []byte            `json:"-"`
	Slots    []Slot            `json:"slots"`
	Facts    map[string]string `json:"facts,omitempty"` // structure-only record of any narrowing the caller did
	RecordID string            `json:"record_id,omitempty"`
}

// Reading is how a slot's outcome must be read. It is closed: a backend that
// cannot map its result onto one of these is a bug.
type Reading string

const (
	Answered    Reading = "answered"    // a value was chosen
	Abstained   Reading = "abstained"   // the model declined (Lappi noul/degraded, LLM "none")
	Refused     Reading = "refused"     // the backend will not take this question (e.g. Lappi task_not_trained)
	Failed      Reading = "failed"      // the backend answered something unusable
	Unavailable Reading = "unavailable" // not reachable within the deadline
	NotAsked    Reading = "not_asked"   // skipped by policy (disabled, family not served, budget exhausted)
)

// LappiCalib is Lappi's calibration: Score is the top-two probability margin,
// ConformalSet the split-conformal prediction set.
type LappiCalib struct {
	Score        float64  `json:"score"`
	ConformalSet []string `json:"conformal_set"`
}

// JevCalib is Jev's calibration: the probability of the chosen label.
type JevCalib struct {
	Probability float64 `json:"probability"`
}

// SlotReading is one slot's outcome. Exactly one calibration pointer is set
// when Reading is Answered and the backend reports calibration; there is
// deliberately no shared "confidence" number, because the backends' numbers
// mean different things.
type SlotReading struct {
	Reading Reading     `json:"reading"`
	Value   string      `json:"value,omitempty"`
	Lappi   *LappiCalib `json:"lappi,omitempty"`
	Jev     *JevCalib   `json:"jev,omitempty"`
	Detail  string      `json:"detail,omitempty"` // refusal kind, error class; never model text
}

// Answer is a backend's reply to a whole Question.
type Answer struct {
	Backend string                 `json:"backend"` // "lappi" | "jev" | "llm"
	Slots   map[string]SlotReading `json:"slots"`
	CostUSD float64                `json:"cost_usd,omitempty"`
	// Shadow holds the shadow backend's per-slot readings when a Router has one
	// and asked it. A shadow that errored or timed out is recorded too, as
	// Unavailable on every slot, so Shadow non-nil means "was asked", not
	// "answered". They are informational and never authoritative: nothing may
	// act on them, and DropPolicy must ignore them.
	Shadow map[string]SlotReading `json:"shadow,omitempty"`
}

// Backend is one decision model.
type Backend interface {
	Name() string
	// Serves reports whether this backend can take the task at all, so the
	// router can skip it without a round-trip.
	Serves(task string) bool
	Decide(ctx context.Context, q Question) (Answer, error)
}

// Decider is what callers use: a router over backends.
type Decider interface {
	Decide(ctx context.Context, q Question) (Answer, error)
}
