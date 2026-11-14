package ledger

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// TestMain skips the fsyncs (atomicfile.RunWithoutSync): the fold and evict
// equivalence tests write thousands of durable lines and spent minutes in the
// drive cache. dirsync_test.go counts every sync and runs the real path once.
func TestMain(m *testing.M) { os.Exit(atomicfile.RunWithoutSync(m)) }
