// Package ledger is Vani's record of every posting it has seen and every
// state change. The canonical store is append-only JSONL; any index is
// derived from it and rebuilt on start.
package ledger

import (
	"context"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Event is one immutable ledger line.
type Event struct {
	V      int            `json:"v"` // schema version, currently 1
	At     time.Time      `json:"at"`
	Key    posting.Key    `json:"key"`
	Status posting.Status `json:"status"`
	// Posting is set when the event carries a new or changed posting version.
	Posting *posting.Posting `json:"posting,omitempty"`
	// Annotations are typed results attached at this step (triage slots,
	// scores, explanation, duplicate-of key). Values are short strings.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Reason says why the status changed, for the digest and for audit.
	Reason string `json:"reason,omitempty"`
}

// Record is the folded current state of one posting.
type Record struct {
	Key         posting.Key       `json:"key"`
	Status      posting.Status    `json:"status"`
	Posting     posting.Posting   `json:"posting"`
	Annotations map[string]string `json:"annotations"`
	FirstSeen   time.Time         `json:"first_seen"`
	LastEvent   time.Time         `json:"last_event"`
	// MissedRuns counts consecutive board listings this posting was absent
	// from; two in a row marks it dead.
	MissedRuns int `json:"missed_runs"`
}

// Ledger appends events and answers from the folded state.
type Ledger interface {
	Append(ctx context.Context, e Event) error
	Get(k posting.Key) (Record, bool)
	All() []Record
	Close() error
}
