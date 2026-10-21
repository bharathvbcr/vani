package kbsync

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return NewSyncState(path).Save() })
}
