package pipeline

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// TestMain skips the fsyncs (atomicfile.RunWithoutSync): the liveness fuzz and
// reconciler tests write tens of thousands of ledger lines, and at ~8 ms a sync
// TestDeathRuleBoundsFuzz alone spent 158.8 s of 158.8 s wall in the drive
// cache (15.0 s without). The ledger package counts its syncs and runs the
// real path.
func TestMain(m *testing.M) { os.Exit(atomicfile.RunWithoutSync(m)) }
