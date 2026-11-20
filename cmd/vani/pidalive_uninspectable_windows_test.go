//go:build windows

package main

import "testing"

// requireUninspectablePidIsNotLive has no unix counterpart here: the Windows
// access-denied path is covered by TestProcessExistsAccessDeniedIsNotAlive.
func requireUninspectablePidIsNotLive(t *testing.T) { t.Helper() }
