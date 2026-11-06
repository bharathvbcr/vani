package avature

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// xChunk fills a streaming body without allocating the 4 MiB cap.
var xChunk = bytes.Repeat([]byte{'x'}, 4096)

var errShortRead = errors.New("short avature body read")

// stopAfter yields n bytes and then fails the next read. GetBodyLimit stops
// after limit+1, so a cap of pageMaxBody never asks for another byte. GetBody's
// 64 MiB cap does, and that error is not ErrBodyTooLarge.
type stopAfter struct {
	allow int
	n     int
	over  bool
}

func (s *stopAfter) Read(p []byte) (int, error) {
	if s.n >= s.allow {
		s.over = true
		return 0, errors.New("read past avature body cap")
	}
	room := s.allow - s.n
	if len(p) > room {
		p = p[:room]
	}
	for i := range p {
		p[i] = 'x'
	}
	s.n += len(p)
	return len(p), nil
}

func (s *stopAfter) Close() error { return nil }

// swapBody answers through base, then replaces one response body.
type swapBody struct {
	base egress.Doer
	swap func(*http.Request, *http.Response)
}

func (s swapBody) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := s.base.Do(ctx, req)
	if err != nil || resp == nil || s.swap == nil {
		return resp, err
	}
	s.swap(req, resp)
	return resp, nil
}

func replaceBody(match string, body io.ReadCloser) func(*http.Request, *http.Response) {
	return func(req *http.Request, resp *http.Response) {
		if !strings.Contains(req.URL.Path, match) {
			return
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		resp.Body = body
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
	}
}

func TestListBodyStopsAtCap(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	body := &stopAfter{allow: pageMaxBody + 1}
	s := &source{d: swapBody{base: base.d, swap: replaceBody("/careers/OpenRoles/", body)}, now: base.now, wait: noWait}
	var reported int
	s.opt = &Options{Report: func(_ sources.Board, _ Stats) { reported++ }}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || ps != nil {
		t.Fatalf("got %d postings, err %v; want ErrBodyTooLarge and no postings", len(ps), err)
	}
	if body.over || body.n > pageMaxBody+1 {
		t.Fatalf("read %d bytes, over=%v; the cap is %d and the read must stop at cap+1", body.n, body.over, pageMaxBody)
	}
	if body.n != pageMaxBody+1 {
		t.Fatalf("read %d bytes, want exactly %d (limit+1)", body.n, pageMaxBody+1)
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after a list read failed", f.detailCount())
	}
	if reported != 0 {
		t.Fatalf("reported %d boards after a failed list", reported)
	}
}

func TestDetailBodyStopsAtCap(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	body := &stopAfter{allow: detailMaxBody + 1}
	s := &source{d: swapBody{base: base.d, swap: replaceBody("/careers/JobDetail/", body)}, now: base.now, wait: noWait}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || ps != nil {
		t.Fatalf("got %d postings, err %v; want ErrBodyTooLarge and no postings", len(ps), err)
	}
	if body.over || body.n != detailMaxBody+1 {
		t.Fatalf("read %d bytes, over=%v; want exactly %d and no further read", body.n, body.over, detailMaxBody+1)
	}
}

func TestEmptyListBodyFailsClosed(t *testing.T) {
	for name, rp := range map[string]srctest.Reply{
		"nil":   {Status: 200, Body: nil},
		"empty": {Status: 200, Body: []byte{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, 3)
			f.listAt = func(int) (srctest.Reply, bool) { return rp, true }
			ps, err := f.list()
			if err == nil || ps != nil {
				t.Fatalf("got %v, err %v; an empty list body must fail the board", ps, err)
			}
			if errors.Is(err, srcutil.ErrBodyTooLarge) {
				t.Fatalf("empty body is not over the cap: %v", err)
			}
			if f.detailCount() != 0 {
				t.Fatalf("fetched %d details from an empty list", f.detailCount())
			}
		})
	}
}

// A read that fails is the error, not an empty board.
func TestListReadErrorIsReturned(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	s := &source{
		d:    swapBody{base: base.d, swap: replaceBody("/careers/OpenRoles/", errReader{io.ErrUnexpectedEOF})},
		now:  base.now,
		wait: noWait,
	}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, io.ErrUnexpectedEOF) || ps != nil {
		t.Fatalf("got %v, err %v", ps, err)
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after a failed list read", f.detailCount())
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func (e errReader) Close() error { return nil }

// thenErr yields give bytes, then err on the next read. give stays under the cap.
type thenErr struct {
	give int
	err  error
	n    int
}

func (r *thenErr) Read(p []byte) (int, error) {
	if r.n >= r.give || len(p) == 0 {
		if len(p) == 0 && r.n < r.give {
			return 0, nil
		}
		return 0, r.err
	}
	room := r.give - r.n
	if len(p) > room {
		p = p[:room]
	}
	n := fillX(p)
	r.n += n
	return n, nil
}

func (r *thenErr) Close() error { return nil }

// eofAt streams exactly allow bytes and then io.EOF. Another read sets over.
type eofAt struct {
	allow int
	n     int
	over  bool
}

func (s *eofAt) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.n >= s.allow {
		s.over = true
		return 0, errors.New("read past exact avature body")
	}
	room := s.allow - s.n
	if len(p) > room {
		p = p[:room]
	}
	n := fillX(p)
	s.n += n
	if s.n == s.allow {
		return n, io.EOF
	}
	return n, nil
}

func (s *eofAt) Close() error { return nil }

func fillX(p []byte) int {
	for i := 0; i < len(p); {
		i += copy(p[i:], xChunk)
	}
	return len(p)
}

// blockUntilCancel reports the first read, then blocks until ctx is done.
type blockUntilCancel struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (b *blockUntilCancel) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *blockUntilCancel) Close() error { return nil }

type countRC struct {
	io.ReadCloser
	n *int
}

func (c countRC) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	*c.n += n
	return n, err
}

func TestListCancelledBeforeStart(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	var reported int
	base.opt = &Options{Report: func(sources.Board, Stats) { reported++ }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ps, err := base.List(ctx, board())
	if !errors.Is(err, context.Canceled) || ps != nil {
		t.Fatalf("got %d postings, err %v; want context.Canceled and no postings", len(ps), err)
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after cancel", f.detailCount())
	}
	for _, r := range f.reqs {
		if strings.Contains(r, "/careers/OpenRoles/") || strings.Contains(r, "/careers/JobDetail/") {
			t.Fatalf("fetched %s with a cancelled context", r)
		}
	}
	if reported != 0 {
		t.Fatalf("reported %d boards after cancel", reported)
	}
}

func TestListPartialThenError(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	body := &thenErr{give: 64, err: errShortRead}
	var reported int
	s := &source{
		d:    swapBody{base: base.d, swap: replaceBody("/careers/OpenRoles/", body)},
		now:  base.now,
		wait: noWait,
		opt:  &Options{Report: func(sources.Board, Stats) { reported++ }},
	}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, errShortRead) || ps != nil {
		t.Fatalf("got %d postings, err %v; want the read error and no postings", len(ps), err)
	}
	if errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("a short read is not over the cap: %v", err)
	}
	if body.n != 64 {
		t.Fatalf("read %d bytes, want 64", body.n)
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after a failed list read", f.detailCount())
	}
	if reported != 0 {
		t.Fatalf("reported %d boards after a failed list", reported)
	}
}

// Exactly pageMaxBody then EOF is inside GetBodyLimit's limit. The bytes are
// not HTML, so the failure is the listing parse, and the reader is not asked
// for another byte. ReadAll holds the body; this reader does not allocate it.
func TestListExactCapIsParseError(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	body := &eofAt{allow: pageMaxBody}
	var reported int
	s := &source{
		d:    swapBody{base: base.d, swap: replaceBody("/careers/OpenRoles/", body)},
		now:  base.now,
		wait: noWait,
		opt:  &Options{Report: func(sources.Board, Stats) { reported++ }},
	}
	ps, err := s.List(context.Background(), board())
	if errors.Is(err, srcutil.ErrBodyTooLarge) || err == nil || ps != nil {
		t.Fatalf("got %d postings, err %v; exact cap must not be ErrBodyTooLarge", len(ps), err)
	}
	if !errors.Is(err, ErrShape) {
		t.Fatalf("got %v; want a listing parse error", err)
	}
	if body.over || body.n != pageMaxBody {
		t.Fatalf("read %d bytes, over=%v; want exactly %d and no further read", body.n, body.over, pageMaxBody)
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after a failed list", f.detailCount())
	}
	if reported != 0 {
		t.Fatalf("reported %d boards after a failed list", reported)
	}
}

// hostRE is ASCII. board() cannot carry a unicode host, and List rejects one
// before robots, listing, or GetBodyLimit. Do not bypass that gate.
func TestUnicodeHostRejectedBeforeFetch(t *testing.T) {
	const unicodeHost = "bücher.example.com"
	if hostRE.MatchString(unicodeHost) {
		t.Fatal("hostRE accepted a unicode host; exercise GetBodyLimit instead of rejecting it")
	}
	f := newFake(t, 1)
	base, _ := f.source()
	var reported int
	base.opt = &Options{Report: func(sources.Board, Stats) { reported++ }}
	b := board()
	b.Host = unicodeHost
	b.URL = "https://" + unicodeHost + "/careers/OpenRoles"
	ps, err := base.List(context.Background(), b)
	if err == nil || ps != nil {
		t.Fatalf("got %d postings, err %v; unicode host must be rejected", len(ps), err)
	}
	if len(f.reqs) != 0 || f.detailCount() != 0 {
		t.Fatalf("fetched %v for a rejected host", f.reqs)
	}
	if reported != 0 {
		t.Fatalf("reported %d boards for a rejected host", reported)
	}
}

// The list page stays the fake's small OpenRoles body. Only the detail is capped.
func TestDetailCapLeavesShortList(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	detail := &stopAfter{allow: detailMaxBody + 1}
	var listN int
	s := &source{
		d: swapBody{base: base.d, swap: func(req *http.Request, resp *http.Response) {
			switch {
			case strings.Contains(req.URL.Path, "/careers/OpenRoles/"):
				resp.Body = countRC{ReadCloser: resp.Body, n: &listN}
			case strings.Contains(req.URL.Path, "/careers/JobDetail/"):
				replaceBody("/careers/JobDetail/", detail)(req, resp)
			}
		}},
		now:  base.now,
		wait: noWait,
	}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || ps != nil {
		t.Fatalf("got %d postings, err %v; want ErrBodyTooLarge and no postings", len(ps), err)
	}
	if detail.over || detail.n != detailMaxBody+1 {
		t.Fatalf("detail read %d bytes, over=%v; want exactly %d", detail.n, detail.over, detailMaxBody+1)
	}
	if listN == 0 || listN > 64<<10 {
		t.Fatalf("list body was %d bytes; want the fake's short page, not a swapped cap body", listN)
	}
}

func TestListCancelDuringRead(t *testing.T) {
	f := newFake(t, 1)
	base, _ := f.source()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &blockUntilCancel{ctx: ctx, started: make(chan struct{})}
	var reported int
	s := &source{
		d:    swapBody{base: base.d, swap: replaceBody("/careers/OpenRoles/", body)},
		now:  base.now,
		wait: noWait,
		opt:  &Options{Report: func(sources.Board, Stats) { reported++ }},
	}
	type result struct {
		ps  []posting.Posting
		err error
	}
	done := make(chan result, 1)
	go func() {
		ps, err := s.List(ctx, board())
		done <- result{ps, err}
	}()
	select {
	case <-body.started:
	case <-t.Context().Done():
		t.Fatal("list read did not start before the test ended")
	case <-time.After(5 * time.Second):
		t.Fatal("list read did not start")
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || got.ps != nil {
			t.Fatalf("got %d postings, err %v; want context.Canceled and no postings", len(got.ps), got.err)
		}
	case <-t.Context().Done():
		t.Fatal("List did not return before the test ended")
	case <-time.After(5 * time.Second):
		t.Fatal("List hung after cancel")
	}
	if f.detailCount() != 0 {
		t.Fatalf("fetched %d details after cancel", f.detailCount())
	}
	if reported != 0 {
		t.Fatalf("reported %d boards after cancel", reported)
	}
}
