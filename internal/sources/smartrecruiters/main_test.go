package smartrecruiters

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// TestMain answers srcutil's retry waits at once (srcutil.RunWithoutWaits).
func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }
