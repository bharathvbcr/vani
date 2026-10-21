package jdreasons

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestWriteThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return Write(path, catalog(), true) })
}
