// Package srctest is a fake egress.Doer for adapter tests: it answers from a
// URL-to-reply table and records every URL requested. A request for a URL the
// table does not hold is an error, so a test cannot pass by hitting the wrong
// host or an unescaped path.
package srctest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Reply is one canned response.
type Reply struct {
	Status int
	Body   []byte
}

// Request is one request the Doer saw: its method, URL, headers and body.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Doer implements egress.Doer over a fixed table, or over Handler when set.
type Doer struct {
	Replies map[string]Reply
	// Handler, when non-nil, answers instead of Replies so a test can match on
	// method and request body as well as URL. ok=false is an unexpected request.
	Handler func(Request) (Reply, bool)

	mu   sync.Mutex
	urls []string
	reqs []Request
}

// New returns a Doer answering the given exact URLs.
func New(replies map[string]Reply) *Doer { return &Doer{Replies: replies} }

// NewFunc returns a Doer answering through h, which sees the method, URL,
// headers and body of every request.
func NewFunc(h func(Request) (Reply, bool)) *Doer { return &Doer{Handler: h} }

// Do implements egress.Doer.
func (d *Doer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("srctest: read request body: %w", err)
		}
		body = b
	}
	rec := Request{Method: req.Method, URL: u, Header: req.Header.Clone(), Body: body}
	d.mu.Lock()
	d.urls = append(d.urls, u)
	d.reqs = append(d.reqs, rec)
	d.mu.Unlock()
	var (
		r  Reply
		ok bool
	)
	if d.Handler != nil {
		r, ok = d.Handler(rec)
	} else {
		r, ok = d.Replies[u]
	}
	if !ok {
		return nil, fmt.Errorf("srctest: unexpected request %s %s", req.Method, u)
	}
	return &http.Response{
		StatusCode: r.Status,
		Status:     http.StatusText(r.Status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(r.Body)),
		Request:    req,
	}, nil
}

// Requests returns every request seen, in order.
func (d *Doer) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Request(nil), d.reqs...)
}

// URLs returns the requested URLs in order.
func (d *Doer) URLs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.urls...)
}
