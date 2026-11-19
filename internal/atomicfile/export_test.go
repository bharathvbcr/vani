package atomicfile

// SetAfterOpenHookForTest installs fn to run between OpenRead's open and its
// identity check, and returns a restore func. Tests only; not parallel-safe.
func SetAfterOpenHookForTest(fn func()) (restore func()) {
	prev := afterOpenHook
	afterOpenHook = fn
	return func() { afterOpenHook = prev }
}

// OpenReadAttemptsForTest exposes the retry bound.
const OpenReadAttemptsForTest = openReadAttempts
