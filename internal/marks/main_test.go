package marks

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// TestMain skips the fsyncs (atomicfile.RunWithoutSync); hardening_test.go
// counts every sync and realsync_test.go runs the real path.
func TestMain(m *testing.M) { os.Exit(atomicfile.RunWithoutSync(m)) }
