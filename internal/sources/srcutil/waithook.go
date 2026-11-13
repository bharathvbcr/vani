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
