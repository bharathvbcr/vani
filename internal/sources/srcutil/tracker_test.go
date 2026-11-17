package srcutil

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

type answerFunc func(*http.Request) (*http.Response, error)

func (f answerFunc) Do(_ context.Context, r *http.Request) (*http.Response, error) { return f(r) }

func TestTrackerRecordsTheFinalURLAndLocation(t *testing.T) {
	landed, _ := url.Parse("https://jobs.example.com/")
	tr := NewTracker(answerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/"}}, Request: &http.Request{URL: landed}}, nil
	}))
	if tr.Final() != nil || tr.Location() != "" {
		t.Fatal("a tracker that sent nothing reports an answer")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://jobs.example.com/job/a/b/1/2", nil)
	if _, err := tr.Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := tr.Final(); got == nil || got.String() != "https://jobs.example.com/" {
		t.Fatalf("Final %v", got)
	}
	if tr.Location() != "/" {
		t.Fatalf("Location %q", tr.Location())
	}
	// Final is a copy: changing it does not change what the tracker holds.
	tr.Final().Path = "/changed"
	if tr.Final().Path != "/" {
		t.Fatal("Final shares its URL with the caller")
	}
}

func TestTrackerOverSrctestReportsTheAddressAsked(t *testing.T) {
	const u = "https://jobs.example.com/job/a/b/1/2"
	tr := NewTracker(srctest.New(map[string]srctest.Reply{u: {Status: 200, Body: []byte("x")}}))
	if _, err := GetBody(context.Background(), tr, u); err != nil {
		t.Fatal(err)
	}
	if got := tr.Final(); got == nil || got.String() != u {
		t.Fatalf("Final %v, want %s", got, u)
	}
}

func TestTrackerWithNoDoerFails(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://jobs.example.com/", nil)
	if _, err := NewTracker(nil).Do(context.Background(), req); err == nil {
		t.Fatal("no error from a tracker with no doer")
	}
}

func TestTrackerKeepsNothingFromAFailedRequest(t *testing.T) {
	boom := errors.New("reset")
	tr := NewTracker(answerFunc(func(*http.Request) (*http.Response, error) { return nil, boom }))
	req, _ := http.NewRequest(http.MethodGet, "https://jobs.example.com/", nil)
	if _, err := tr.Do(context.Background(), req); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if tr.Final() != nil || tr.Location() != "" {
		t.Fatal("a failed request left an answer behind")
	}
}

func TestTrackerIsSafeForConcurrentUse(t *testing.T) {
	landed, _ := url.Parse("https://jobs.example.com/")
	tr := NewTracker(answerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Request: &http.Request{URL: landed}}, nil
	}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://jobs.example.com/x", nil)
			_, _ = tr.Do(context.Background(), req)
			_ = tr.Final()
			_ = tr.Location()
		}()
	}
	wg.Wait()
}
