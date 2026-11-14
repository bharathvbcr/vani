package goneparity

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// TestMain answers srcutil's retry waits at once (srcutil.RunWithoutWaits).
// With the real wait the 429/500/502/503 rows of TestTenantListFailures alone
// took over seven minutes and the package overran go test's default 10-minute
// timeout. No test here asserts on how long a retry waits.
func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }
