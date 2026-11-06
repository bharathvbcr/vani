package avature

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

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
