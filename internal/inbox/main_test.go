package inbox

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// TestMain skips the fsyncs of atomicfile.Write (atomicfile.RunWithoutSync):
// the spool-cap tests write a thousand batches; atomicfile counts its syncs and
// runs the real path.
func TestMain(m *testing.M) { os.Exit(atomicfile.RunWithoutSync(m)) }
