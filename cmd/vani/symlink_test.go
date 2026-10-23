package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestRunRecordThroughSymlinkedStateDir(t *testing.T) {
	symlinktest.RunDir(t, func(dir string) error { return writeRunRecord(dir, runRecord{}) })
}
