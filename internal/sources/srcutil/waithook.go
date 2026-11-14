package srcutil

import (
	"context"
	"time"
)

// SetWaitForTest replaces the wait a request takes before a retry, and the one
// a board takes for a throttled host to cool (HostThrottle), with wait, and
// returns the function that puts the real one back. It is for the tests of a
// caller that runs real requests through this package and must not sleep in
// real time; nothing in a run calls it. wait must return ctx.Err() when ctx is
// done, as Sleep does.
func SetWaitForTest(wait func(ctx context.Context, d time.Duration) error) (restore func()) {
	old := sleep
	sleep = wait
	return func() { sleep = old }
}

// RunWithoutWaits runs a test binary (a *testing.M) with every retry and
// cool wait answered at once, unless ctx is already done, and returns
// m.Run's exit code for os.Exit. It is for the TestMain of a package whose
// tests send an adapter 429s and 5xx: with the real wait (backoffBase
// doubling to maxBackoff) each such test sleeps for tens of seconds, which
// pushed packages past go test's default 10-minute timeout. A test that needs
// to see the waits still installs its own with SetWaitForTest.
func RunWithoutWaits(m interface{ Run() int }) int {
	restore := SetWaitForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	defer restore()
	return m.Run()
}
