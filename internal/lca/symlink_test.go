package lca

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return NewIndex().Save(path) })
}
