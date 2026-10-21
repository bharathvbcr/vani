//go:build windows

package rekey

import (
	"errors"
	"testing"
	"time"
)

// On a real Windows host Run refuses with no seam involved.
func TestRunIsUnsupportedOnRealWindows(t *testing.T) {
	if _, err := Run(t.TempDir(), false, time.Now()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err %v, want ErrUnsupported", err)
	}
}
