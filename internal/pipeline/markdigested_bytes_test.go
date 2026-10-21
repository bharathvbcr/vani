package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// A digest of 4096 roles whose keys are long (a careers-page URL key can be
// kilobytes) stays within MaxBatchEvents but exceeds MaxBatchBytes: MarkDigested
// splits by count only, so the whole batch is refused and no role is marked.
func TestAdvMarkDigestedSplitsByBytesToo(t *testing.T) {
	l, keys, at := advLedger(t, ledger.MaxBatchEvents, strings.Repeat("x", 4200))
	if err := MarkDigested(context.Background(), l, keys, at, "digest"); err != nil {
		t.Fatalf("a %d-key digest failed (marked %d): %v", len(keys), digestedCount(l, keys), err)
	}
}
