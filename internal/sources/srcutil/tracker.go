package srcutil

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// Tracker is an egress.Doer that remembers where the last request through it
// ended: the final URL of the response, which is not the one asked for when
// the client followed a redirect, and the Location of a redirect it did not
// follow. An adapter wraps its Doer in a fresh Tracker for each request whose
// destination it must check, so a redirect away from the page asked for is
// told apart from an answer that page gave itself.
type Tracker struct {
	d egress.Doer

	mu       sync.Mutex
	final    *url.URL
	location string
}

// NewTracker wraps d.
func NewTracker(d egress.Doer) *Tracker { return &Tracker{d: d} }

// Do implements egress.Doer.
func (t *Tracker) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if t.d == nil {
		return nil, errors.New("sources: no http doer configured")
	}
	resp, err := t.d.Do(ctx, req)
	if resp != nil {
		t.mu.Lock()
		if resp.Request != nil && resp.Request.URL != nil {
			u := *resp.Request.URL
			t.final = &u
		}
		t.location = resp.Header.Get("Location")
		t.mu.Unlock()
	}
	return resp, err
}

// Final is the URL the last response came from, nil before any response.
func (t *Tracker) Final() *url.URL {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.final == nil {
		return nil
	}
	u := *t.final
	return &u
}

// Location is the Location header of the last response ("" when it had none).
func (t *Tracker) Location() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.location
}
