package rekey

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// The seam's production default is the real GOOS, not a test value.
func TestGoosDefaultsToRuntimeGOOS(t *testing.T) {
	if goos != runtime.GOOS {
		t.Fatalf("goos = %q, runtime.GOOS = %q", goos, runtime.GOOS)
	}
}

// Off Windows the refusal is not taken: a missing ledger is the ordinary error.
func TestNonWindowsHostDoesNotRefuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows refuses")
	}
	_, err := Run(t.TempDir(), true, time.Now())
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want the missing-ledger error", err)
	}
}

func TestWindowsRefusalReportCarriesDryRun(t *testing.T) {
	old := goos
	goos = "windows"
	defer func() { goos = old }()
	rep, err := Run(t.TempDir(), true, time.Now())
	if !errors.Is(err, ErrUnsupported) || !rep.DryRun {
		t.Fatalf("rep %+v err %v", rep, err)
	}
}
