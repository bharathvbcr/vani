package manatal

import (
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) {
	os.Exit(srcutil.RunWithoutWaits(m))
}
