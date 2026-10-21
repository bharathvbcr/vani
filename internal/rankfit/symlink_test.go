package rankfit

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
)

func TestWriteThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return Write(path, goodFile()) })
}
