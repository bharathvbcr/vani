package logo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

var (
	pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	icoBytes = []byte{0, 0, 1, 0, 1, 0, 16, 16}
	jpgBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10}
	html     = []byte("<!doctype html><title>Not found</title>")
	svg      = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
)

func TestSniff(t *testing.T) {
	for _, c := range []struct {
		name string
		b    []byte
		want string
	}{
		{"png", pngBytes, "png"},
		{"ico", icoBytes, "ico"},
		{"jpg", jpgBytes, "jpg"},
		{"svg is refused", svg, ""},
		{"html is refused", html, ""},
		{"empty", nil, ""},
		{"short png", pngBytes[:7], ""},
		{"ico with no images", []byte{0, 0, 1, 0, 0, 0}, ""},
		{"cursor, not icon", []byte{0, 0, 2, 0, 1, 0}, ""},
	} {
		got, ok := Sniff(c.b)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: Sniff = %q, %v; want %q", c.name, got, ok, c.want)
		}
	}
}

func TestFetchFallsBackToFavicon(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 404},
		"https://www.acme.com/favicon.ico":          {Status: 200, Body: icoBytes},
	})
	b, kind, err := Fetch(context.Background(), d, "www.acme.com")
	if err != nil || kind != "ico" || !bytes.Equal(b, icoBytes) {
		t.Fatalf("Fetch = %d bytes, %q, %v", len(b), kind, err)
	}
	if got := d.URLs(); len(got) != 2 || got[0] != "https://www.acme.com/apple-touch-icon.png" {
		t.Fatalf("requests %v", got)
	}
	for _, r := range d.Requests() {
		if r.Method != http.MethodGet {
			t.Fatalf("method %s", r.Method)
		}
	}
}

func TestFetchPrefersTouchIcon(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: pngBytes},
	})
	_, kind, err := Fetch(context.Background(), d, "www.acme.com")
	if err != nil || kind != "png" || len(d.URLs()) != 1 {
		t.Fatalf("kind %q err %v requests %v", kind, err, d.URLs())
	}
}

func TestFetchRefusesSoft404AndSVG(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: html, Header: http.Header{"Content-Type": {"text/html"}}},
		"https://www.acme.com/favicon.ico":          {Status: 200, Body: svg, Header: http.Header{"Content-Type": {"image/svg+xml"}}},
	})
	_, _, err := Fetch(context.Background(), d, "www.acme.com")
	if !errors.Is(err, ErrNotImage) {
		t.Fatalf("err = %v, want ErrNotImage", err)
	}
	for _, want := range []string{"/apple-touch-icon.png", "/favicon.ico", "text/html", "image/svg+xml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestFetchRefusesOversized(t *testing.T) {
	big := append(append([]byte{}, pngBytes...), make([]byte, MaxBytes)...)
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: big},
		"https://www.acme.com/favicon.ico":          {Status: 500},
	})
	_, _, err := Fetch(context.Background(), d, "www.acme.com")
	if err == nil || !strings.Contains(err.Error(), "larger than") || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchNamesTheRefusedHost(t *testing.T) {
	// The egress client refuses a redirect to a host not in allowed_hosts; the
	// error must reach the warning so the owner can fix the domain.
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`egress: host is not in the allowlist: "acme.com"`)
	})
	_, _, err := Fetch(context.Background(), d, "www.acme.com")
	if err == nil || !strings.Contains(err.Error(), `"acme.com"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchRefusesBadDomainWithoutARequest(t *testing.T) {
	d := srctest.New(nil)
	for _, dom := range []string{"", "../etc", "acme.com/x", "Acme.com", "acme..com", ".acme.com", "acme.com.", "acme.com:8443", "a b.com"} {
		if _, _, err := Fetch(context.Background(), d, dom); err == nil {
			t.Errorf("Fetch(%q) succeeded", dom)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Fatalf("%d requests sent for bad domains", n)
	}
}

func TestFetchTimeoutStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	d := doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, _, err := Fetch(ctx, d, "www.acme.com")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("err %v after %d calls; want the deadline after one", err, calls)
	}
}

func TestRefreshCachesAndSkipsFresh(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: pngBytes},
	})
	now := time.Now()
	res, err := c.Refresh(context.Background(), d, []string{"www.acme.com", "www.acme.com", ""}, now, false)
	if err != nil || res.Fetched != 1 || len(d.URLs()) != 1 {
		t.Fatalf("first refresh %+v, %v, requests %v", res, err, d.URLs())
	}
	p, ok := c.Path("www.acme.com")
	if !ok || filepath.Base(p) != "www.acme.com.png" {
		t.Fatalf("Path = %q, %v", p, ok)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, pngBytes) {
		t.Fatalf("cached bytes differ")
	}
	res, err = c.Refresh(context.Background(), d, []string{"www.acme.com"}, now.Add(time.Hour), false)
	if err != nil || res.Fresh != 1 || res.Fetched != 0 || len(d.URLs()) != 1 {
		t.Fatalf("second refresh %+v, %v; a fresh icon must not be refetched", res, err)
	}
	res, err = c.Refresh(context.Background(), d, []string{"www.acme.com"}, now.Add(time.Hour), true)
	if err != nil || res.Fetched != 1 || len(d.URLs()) != 2 {
		t.Fatalf("forced refresh %+v, %v", res, err)
	}
}

func TestRefreshFailureWaitsAndKeepsTheOldIcon(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	ok := true
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if ok && strings.HasSuffix(r.URL, ".png") {
			return srctest.Reply{Status: 200, Body: pngBytes}, true
		}
		return srctest.Reply{Status: 503}, true
	})
	now := time.Now()
	if res, err := c.Refresh(context.Background(), d, []string{"www.acme.com"}, now, false); err != nil || res.Fetched != 1 {
		t.Fatalf("seed %+v %v", res, err)
	}
	p, _ := c.Path("www.acme.com")
	old := now.Add(-RefreshAfter - time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	ok = false
	res, err := c.Refresh(context.Background(), d, []string{"www.acme.com"}, now, false)
	if err != nil || res.Failed != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "status 503") {
		t.Fatalf("stale refetch %+v %v", res, err)
	}
	if got, _ := c.Path("www.acme.com"); got != p {
		t.Fatalf("a failed refetch dropped the cached icon")
	}
	n := len(d.URLs())
	res, _ = c.Refresh(context.Background(), d, []string{"www.acme.com"}, now.Add(RetryAfter-time.Hour), false)
	if res.Waiting != 1 || len(d.URLs()) != n {
		t.Fatalf("within RetryAfter: %+v, %d new requests", res, len(d.URLs())-n)
	}
	ok = true
	res, _ = c.Refresh(context.Background(), d, []string{"www.acme.com"}, now.Add(RetryAfter+time.Hour), false)
	if res.Fetched != 1 {
		t.Fatalf("after RetryAfter: %+v", res)
	}
	st, reset := c.load()
	if reset != "" || len(st.Failures) != 0 {
		t.Fatalf("a success must clear the failure: %+v %q", st, reset)
	}
}

func TestRefreshReplacesTheOtherKind(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	png := true
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case strings.HasSuffix(r.URL, ".png") && png:
			return srctest.Reply{Status: 200, Body: pngBytes}, true
		case strings.HasSuffix(r.URL, ".ico"):
			return srctest.Reply{Status: 200, Body: icoBytes}, true
		}
		return srctest.Reply{Status: 404}, true
	})
	now := time.Now()
	c.Refresh(context.Background(), d, []string{"www.acme.com"}, now, false)
	png = false
	c.Refresh(context.Background(), d, []string{"www.acme.com"}, now, true)
	p, ok := c.Path("www.acme.com")
	if !ok || filepath.Ext(p) != ".ico" {
		t.Fatalf("Path = %q", p)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "www.acme.com.png")); !os.IsNotExist(err) {
		t.Fatalf("the old png is still cached: %v", err)
	}
}

func TestRefreshCapsFetchesPerRun(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: 200, Body: pngBytes}, true
	})
	var doms []string
	for i := range MaxPerRun + 3 {
		doms = append(doms, fmt.Sprintf("www.c%03d.com", i))
	}
	res, err := c.Refresh(context.Background(), d, doms, time.Now(), false)
	if err != nil || res.Fetched != MaxPerRun || res.Deferred != 3 || len(d.URLs()) != MaxPerRun {
		t.Fatalf("%+v %v, %d requests", res, err, len(d.URLs()))
	}
}

func TestRefreshStopsOnCancel(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	d := doerFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})
	res, err := c.Refresh(ctx, d, []string{"www.a.com", "www.b.com", "www.c.com"}, time.Now(), false)
	if !errors.Is(err, context.Canceled) || res.Deferred != 3 || res.Failed != 0 {
		t.Fatalf("%+v %v; a cancelled run is not a failure and records none", res, err)
	}
	if st, _ := c.load(); len(st.Failures) != 0 {
		t.Fatalf("cancel recorded failures %+v", st.Failures)
	}
}

func TestRefreshRefusesBadDomain(t *testing.T) {
	dir := t.TempDir()
	c := Cache{Dir: filepath.Join(dir, "logos")}
	d := srctest.New(nil)
	res, err := c.Refresh(context.Background(), d, []string{"../escape"}, time.Now(), false)
	if err != nil || res.Failed != 1 || len(d.URLs()) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, ok := c.Path("../escape"); ok {
		t.Fatal("Path answered for a bad domain")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "logos" {
			t.Fatalf("wrote %s outside the cache", e.Name())
		}
	}
}

func TestRefreshStartsAfreshFromACorruptState(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(c.Dir, stateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: pngBytes},
	})
	res, err := c.Refresh(context.Background(), d, []string{"www.acme.com"}, time.Now(), false)
	if err != nil || res.StateReset == "" || res.Fetched != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, reset := c.load(); reset != "" {
		t.Fatalf("state not rewritten: %s", reset)
	}
}

func TestPathIgnoresEmptyAndNonRegularFiles(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(c.Dir, "www.a.com.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(c.Dir, "www.b.com.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(c.Dir, "www.c.com.png")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"www.a.com", "www.b.com", "www.c.com", "www.none.com"} {
		if p, ok := c.Path(d); ok {
			t.Errorf("Path(%q) = %q", d, p)
		}
	}
	if _, ok := (Cache{}).Path("www.a.com"); ok {
		t.Error("an unset cache answered")
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	return f(r.WithContext(ctx))
}
