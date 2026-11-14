package eval

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// TestMain skips the fsyncs (atomicfile.RunWithoutSync): the label, pair and
// teacher stores append a durable line per write. The package took 63.8 s wall
// on 9.2 s CPU with them.
func TestMain(m *testing.M) { os.Exit(atomicfile.RunWithoutSync(m)) }
