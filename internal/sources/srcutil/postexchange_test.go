package srcutil

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

const exURL = "https://example.test/mcp"

func exchangeDoer(rs ...srctest.Reply) *srctest.Doer {
	i := 0
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != http.MethodPost || r.URL != exURL || i >= len(rs) {
			return srctest.Reply{}, false
		}
		i++
		return rs[i-1], true
	})
}

// PostExchange returns the status and headers with the body, and takes 202
// and 204 as answers; PostBodyLimit, like every other caller, still takes only
// a 200.
func TestPostExchangeAcceptsNotificationStatuses(t *testing.T) {
	for _, code := range []int{200, 202, 204} {
		d := exchangeDoer(srctest.Reply{Status: code, Body: []byte("b"), Header: http.Header{"Mcp-Session-Id": {"s1"}}})
		x, err := PostExchange(context.Background(), d, exURL, []byte("{}"), http.Header{"X-T": {"1"}}, "application/json, text/event-stream", 0)
		if err != nil || x.Status != code || x.Header.Get("Mcp-Session-Id") != "s1" || string(x.Body) != "b" {
			t.Fatalf("%d: %+v, %v", code, x, err)
		}
		reqs := d.Requests()
		if len(reqs) != 1 || reqs[0].Header.Get("Accept") != "application/json, text/event-stream" || reqs[0].Header.Get("X-T") != "1" || string(reqs[0].Body) != "{}" {
			t.Fatalf("%d: request = %+v", code, reqs)
		}
	}
	d := exchangeDoer(srctest.Reply{Status: 202})
	if _, err := PostBodyLimit(context.Background(), d, exURL, []byte("{}"), nil, "", 0); err == nil {
		t.Fatal("PostBodyLimit took a 202")
	}
}

func TestPostExchangeFailures(t *testing.T) {
	noWait(t)
	if _, err := PostExchange(context.Background(), exchangeDoer(srctest.Reply{Status: 404}), exURL, nil, nil, "", 0); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("404 err = %v", err)
	}
	if _, err := PostExchange(context.Background(), exchangeDoer(srctest.Reply{Status: 400}), exURL, nil, nil, "", 0); err == nil {
		t.Fatal("400 succeeded")
	}
	// A 429 is retried with the same body, then answered.
	d := exchangeDoer(srctest.Reply{Status: 429}, srctest.Reply{Status: 200, Body: []byte("ok")})
	x, err := PostExchange(context.Background(), d, exURL, []byte("same"), nil, "", 0)
	if err != nil || string(x.Body) != "ok" {
		t.Fatalf("after 429: %+v, %v", x, err)
	}
	for _, r := range d.Requests() {
		if string(r.Body) != "same" {
			t.Fatalf("retry body = %q", r.Body)
		}
	}
	big := make([]byte, 11)
	if _, err := PostExchange(context.Background(), exchangeDoer(srctest.Reply{Status: 200, Body: big}), exURL, nil, nil, "", 10); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("over cap err = %v", err)
	}
	if _, err := PostExchange(context.Background(), nil, exURL, nil, nil, "", 0); err == nil {
		t.Fatal("nil doer succeeded")
	}
}
