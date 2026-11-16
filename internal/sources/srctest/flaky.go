package srctest

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// FlakyOnce is a Doer for the tests of an adapter that sends real requests
// through srcutil: it fails the first request whose URL contains match with
// err (a transport failure such as a timeout, no response) and passes every
// other request, including the retry of that one, to the Doer it wraps. It is
// not used by a run.
type FlakyOnce struct {
	next  egress.Doer
	match string
	err   error

	mu       sync.Mutex
	matched  int
	failures int
}

// NewFlakyOnce wraps next so the first request for a URL containing match fails
// with err.
func NewFlakyOnce(next egress.Doer, match string, err error) *FlakyOnce {
	return &FlakyOnce{next: next, match: match, err: err}
}

// Do implements egress.Doer.
func (f *FlakyOnce) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.String(), f.match) {
		f.mu.Lock()
		f.matched++
		fail := f.failures == 0
		if fail {
			f.failures++
		}
		f.mu.Unlock()
		if fail {
			return nil, f.err
		}
	}
	return f.next.Do(ctx, req)
}

// Matched is how many requests for the URL were sent, the failed one included.
func (f *FlakyOnce) Matched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.matched
}
