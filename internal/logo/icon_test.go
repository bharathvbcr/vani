package logo

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// doms is a Source per domain, with no icon URL.
func doms(ds ...string) []Source {
	out := make([]Source, 0, len(ds))
	for _, d := range ds {
		out = append(out, Source{Domain: d})
	}
	return out
}

const iconURL = "https://cdn.acme-assets.com/brand/icon.png?v=3"

func TestFetchAsksTheIconURLFirst(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		iconURL: {Status: 200, Body: jpgBytes},
		"https://www.acme.com/apple-touch-icon.png": {Status: 200, Body: pngBytes},
	})
	b, kind, err := Fetch(context.Background(), d, "www.acme.com", iconURL)
	if err != nil || kind != "jpg" || string(b) != string(jpgBytes) {
		t.Fatalf("Fetch = %q, %v", kind, err)
	}
	if got := d.URLs(); !slices.Equal(got, []string{iconURL}) {
		t.Fatalf("requests %v; the icon URL answers, so no fixed path is asked", got)
	}
}

func TestFetchFallsBackFromABrokenIconURL(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		iconURL:                            {Status: 404},
		"https://www.acme.com/favicon.ico": {Status: 200, Body: icoBytes},
	})
	_, kind, err := Fetch(context.Background(), d, "www.acme.com", iconURL)
	if err != nil || kind != "ico" {
		t.Fatalf("Fetch = %q, %v", kind, err)
	}
	if got := d.URLs(); len(got) == 0 || got[0] != iconURL {
		t.Fatalf("requests %v; the icon URL is asked first", got)
	}
}

func TestFetchNamesTheIconURLWhenAllFail(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{iconURL: {Status: 200, Body: svg}})
	_, _, err := Fetch(context.Background(), d, "www.acme.com", iconURL)
	if err == nil || !strings.Contains(err.Error(), "icon "+iconURL) || !strings.Contains(err.Error(), "/favicon.png") {
		t.Fatalf("err = %v; it must name the icon URL and every fixed path", err)
	}
}

func TestFetchFindsFaviconPNG(t *testing.T) {
	// Some sites serve only /favicon.png at the root (Broadcom, 2026-10).
	d := srctest.New(map[string]srctest.Reply{
		"https://www.acme.com/favicon.png": {Status: 200, Body: pngBytes},
	})
	_, kind, err := Fetch(context.Background(), d, "www.acme.com", "")
	if err != nil || kind != "png" {
		t.Fatalf("Fetch = %q, %v", kind, err)
	}
}

func TestFetchRefusesANonHTTPSIconWithoutARequest(t *testing.T) {
	d := srctest.New(nil)
	for _, icon := range []string{"http://www.acme.com/i.png", "file:///etc/passwd", "//www.acme.com/i.png", "data:image/png;base64,AA"} {
		if _, _, err := Fetch(context.Background(), d, "www.acme.com", icon); err == nil {
			t.Errorf("Fetch(icon %q) succeeded", icon)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Fatalf("%d requests sent for refused icon URLs", n)
	}
}

func TestRefreshRefetchesWhenTheIconURLChanges(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case strings.HasPrefix(r.URL, "https://cdn.acme-assets.com/"):
			return srctest.Reply{Status: 200, Body: jpgBytes}, true
		case strings.HasSuffix(r.URL, "/favicon.ico"):
			return srctest.Reply{Status: 200, Body: icoBytes}, true
		}
		return srctest.Reply{Status: 404}, true
	})
	now := time.Now()
	if res, err := c.Refresh(context.Background(), d, doms("www.acme.com"), now, false); err != nil || res.Fetched != 1 {
		t.Fatalf("seed %+v %v", res, err)
	}
	withIcon := []Source{{Domain: "www.acme.com", Icon: iconURL}}
	res, err := c.Refresh(context.Background(), d, withIcon, now.Add(time.Hour), false)
	if err != nil || res.Fetched != 1 {
		t.Fatalf("a newly set icon URL must be fetched at once, though the cached icon is fresh: %+v %v", res, err)
	}
	if p, _ := c.Path("www.acme.com"); filepath.Ext(p) != ".jpg" {
		t.Fatalf("Path = %q, want the icon URL's jpg", p)
	}
	n := len(d.URLs())
	if res, _ := c.Refresh(context.Background(), d, withIcon, now.Add(2*time.Hour), false); res.Fresh != 1 || len(d.URLs()) != n {
		t.Fatalf("an unchanged icon URL is fresh: %+v, %d new requests", res, len(d.URLs())-n)
	}
	if res, _ := c.Refresh(context.Background(), d, doms("www.acme.com"), now.Add(3*time.Hour), false); res.Fetched != 1 {
		t.Fatalf("clearing the icon URL refetches from the fixed paths: %+v", res)
	}
	if st, _ := c.load(); len(st.Icons) != 0 {
		t.Fatalf("a domain fetched with no icon URL is not recorded: %v", st.Icons)
	}
}

func TestRefreshRetriesAFailedDomainOnceItsIconURLIsSet(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == iconURL {
			return srctest.Reply{Status: 200, Body: pngBytes}, true
		}
		return srctest.Reply{Status: 404}, true
	})
	now := time.Now()
	if res, _ := c.Refresh(context.Background(), d, doms("www.acme.com"), now, false); res.Failed != 1 {
		t.Fatalf("seed %+v", res)
	}
	if res, _ := c.Refresh(context.Background(), d, doms("www.acme.com"), now.Add(time.Hour), false); res.Waiting != 1 {
		t.Fatalf("an unchanged failed domain waits: %+v", res)
	}
	res, err := c.Refresh(context.Background(), d, []Source{{Domain: "www.acme.com", Icon: iconURL}}, now.Add(time.Hour), false)
	if err != nil || res.Fetched != 1 {
		t.Fatalf("setting an icon URL must not wait out RetryAfter: %+v %v", res, err)
	}
	if st, _ := c.load(); len(st.Failures) != 0 || st.Icons["www.acme.com"] != iconURL {
		t.Fatalf("state %+v", st)
	}
}

func TestRefreshAsksADuplicatedDomainOnceWithItsIcon(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	d := srctest.New(map[string]srctest.Reply{iconURL: {Status: 200, Body: pngBytes}})
	res, err := c.Refresh(context.Background(), d,
		[]Source{{Domain: "www.acme.com"}, {Domain: "www.acme.com", Icon: iconURL}, {Domain: "www.acme.com"}}, time.Now(), false)
	if err != nil || res.Fetched != 1 || !slices.Equal(d.URLs(), []string{iconURL}) {
		t.Fatalf("%+v %v, requests %v", res, err, d.URLs())
	}
}
