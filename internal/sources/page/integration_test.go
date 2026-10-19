package page

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// localOnlyRunner is TEST-ONLY. The httptest server lives on 127.0.0.1 with a
// self-signed certificate, which production flags forbid by design, so this
// wrapper drops the proxy and private-network flags the source adds and trusts
// the test certificate instead. Production code never builds a runner like
// this: it is defined in a _test file on purpose.
type localOnlyRunner struct {
	inner  Runner
	caPath string
}

func (l localOnlyRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--block-private-networks":
		case "--http-proxy", "--proxy-bearer-token", "--block-cidrs":
			i++ // drop the flag and its value
		default:
			out = append(out, args[i])
		}
	}
	out = append([]string{out[0], "--ca-cert", l.caPath}, out[1:]...)
	return l.inner.Run(ctx, out)
}

// TestLightpandaIntegration runs the real binary against a local HTTPS server.
// It is skipped unless VANI_LIGHTPANDA_IT=1; VANI_LIGHTPANDA_BIN overrides the
// default /opt/homebrew/bin/lightpanda.
func TestLightpandaIntegration(t *testing.T) {
	if os.Getenv("VANI_LIGHTPANDA_IT") != "1" {
		t.Skip("set VANI_LIGHTPANDA_IT=1 to run the real lightpanda binary")
	}
	bin := os.Getenv("VANI_LIGHTPANDA_BIN")
	if bin == "" {
		bin = "/opt/homebrew/bin/lightpanda"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("VANI_LIGHTPANDA_IT=1 but no binary: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/jobs/p1">Platform Engineer</a><a href="/jobs/p2">Plain Page</a>
			<a href="/jobs/p1#apply">again</a><a href="mailto:x@example.com">mail</a><a href="/about">About</a></body></html>`))
	})
	mux.HandleFunc("/jobs/p1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Platform Engineer - Testco</title>
			<script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Platform Engineer",
			"description":"<p>Run the <b>platform</b>.</p>","datePosted":"2026-10-01","validThrough":"2099-01-01",
			"hiringOrganization":{"@type":"Organization","name":"Testco Ltd"},"jobLocationType":"TELECOMMUTE",
			"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"Lisbon","addressCountry":"PT"}}}</script>
			</head><body><h1>Platform Engineer</h1></body></html>`))
	})
	mux.HandleFunc("/jobs/p2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Plain Page Role</title></head><body><p>No structured data here.</p></body></html>`))
	})
	srv := httptest.NewTLSServer(mux) // unknown paths (robots.txt) answer 404
	defer srv.Close()

	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	run := localOnlyRunner{inner: ExecRunner{Binary: bin, Timeout: 60 * time.Second}, caPath: ca}
	src := New(run, ProxyInfo{Addr: "127.0.0.1:1", Token: testTok})
	b := sources.Board{ATS: posting.Page, Token: "testco", Company: "Fallback Co", URL: srv.URL + "/jobs", LinkPattern: `/jobs/p\d$`}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got, err := src.List(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2: %+v", len(got), got)
	}
	p1, p2 := got[0], got[1]
	if p1.Title != "Platform Engineer" || p1.Company != "Testco Ltd" || p1.Location != "Lisbon, PT" || !p1.Remote ||
		!strings.Contains(p1.Description, "Run the platform.") {
		t.Errorf("JSON-LD posting: %+v", p1)
	}
	if p2.Title != "Plain Page Role" || p2.Company != "Fallback Co" || !strings.Contains(p2.Description, "No structured data here.") {
		t.Errorf("fallback posting: %+v", p2)
	}
	for _, p := range got {
		if !strings.HasPrefix(string(p.Key), b.KeyPrefix()) {
			t.Errorf("key %q lacks prefix %q", p.Key, b.KeyPrefix())
		}
	}

	// A page that fails to load fails the board.
	// (/about is linked from the listing but the server answers 404 for it.)
	b.LinkPattern = `/jobs/p1$|/about$`
	if got, err := src.List(ctx, b); err == nil {
		t.Fatalf("a 404 posting page must fail the whole board, got %d postings", len(got))
	}
}
