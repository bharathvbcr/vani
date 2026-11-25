package adzuna

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// hostsDoer is a real net/http client, following redirects as egress's does,
// whose dialer sends each host name to a local TLS server.
type hostsDoer struct{ c *http.Client }

func (d hostsDoer) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	return d.c.Do(r.WithContext(ctx))
}

// A redirect from the API to another host is never answered as Adzuna's
// listing, and the request the client sends there carries no key, not even in
// a Referer.
func TestRedirectIsRefusedAndCarriesNoKey(t *testing.T) {
	var mu sync.Mutex
	var otherReferer, otherQuery string
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherReferer, otherQuery = r.Header.Get("Referer"), r.URL.RawQuery
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer other.Close()
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/landing", http.StatusFound)
	}))
	defer api.Close()
	addr := map[string]string{Host + ":443": api.Listener.Addr().String(), "elsewhere.example:443": other.Listener.Addr().String()}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local test servers
		DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			if to, ok := addr[a]; ok {
				a = to
			}
			return (&net.Dialer{}).DialContext(ctx, network, a)
		},
	}
	d := hostsDoer{&http.Client{Transport: tr}}
	ps, err := newSource(t, d, Options{}).List(context.Background(), board)
	if !errors.Is(err, ErrRedirected) {
		t.Fatalf("got %d postings, err %v; want ErrRedirected, never another host's listing", len(ps), err)
	}
	assertNoLeak(t, "error", err.Error())
	mu.Lock()
	defer mu.Unlock()
	if otherReferer == "" && otherQuery == "" {
		t.Fatal("the redirect was not followed; the test proves nothing")
	}
	assertNoLeak(t, "referer sent to the redirect target", otherReferer)
	assertNoLeak(t, "query sent to the redirect target", otherQuery)
	if strings.Contains(otherReferer, "app_") {
		t.Fatalf("referer %q names a credential parameter", otherReferer)
	}
}
