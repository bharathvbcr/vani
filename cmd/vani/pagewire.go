package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/page"
)

// lightpandaTimeout bounds one page load, a process start included.
const lightpandaTimeout = 60 * time.Second

func (w *wired) hasPageBoard() bool {
	for _, b := range w.cfg.Watchlist {
		if b.ATS == posting.Page {
			return true
		}
	}
	return false
}

// pageProxyConfig is the egress proxy for careers pages. One lightpanda
// process serves a whole board and keeps its CONNECT tunnel to a host open
// across the board's pages, so a tunnel may live as long as a board does; at
// the 5 min default a large board's tunnel is cut, and lightpanda re-sends
// the request in flight unpaced (measured 2026-10-19).
var pageProxyConfig = egress.ProxyConfig{MaxLifetime: page.BoardTimeout}

// pageSource starts the per-run egress proxy that lightpanda must tunnel
// through, so a careers page gets the same allowlist, https-only and
// private-address rules as an API board, and the browser that runs through
// it. Both live for this run only; stop ends the browser, then the proxy. No
// lightpanda process starts until a page board is listed.
func (w *wired) pageSource(ctx context.Context, c *egress.Client) (sources.Source, func(), error) {
	bin, err := lightpandaBinary(w.cfg.LightpandaBinary)
	if err != nil {
		return nil, nil, err
	}
	p, err := egress.NewProxy(c, pageProxyConfig)
	if err != nil {
		return nil, nil, err
	}
	addr, token, err := p.Start(ctx)
	if err != nil {
		_ = p.Close()
		return nil, nil, fmt.Errorf("egress proxy: %w", err)
	}
	proxy := page.ProxyInfo{Addr: addr, Token: token}
	run, browser, err := pageBrowser(bin, w.cfg.PageInterval(), c, proxy)
	if err != nil {
		_ = p.Close()
		return nil, nil, err
	}
	stop := func() {
		if err := browser.Close(); err != nil {
			w.warnf("lightpanda close: %v", err)
		}
		if err := p.Close(); err != nil {
			w.warnf("egress proxy close: %v", err)
		}
	}
	return page.New(run, proxy), stop, nil
}

// pageBrowser is lightpanda at bin, one process per board, with every page
// load spaced per host by the longer of interval (config page_interval_ms)
// and the Crawl-delay in that host's robots.txt, which is read through d.
// The *MCPBrowser is returned for Close.
func pageBrowser(bin string, interval time.Duration, d egress.Doer, proxy page.ProxyInfo) (*page.Paced, *page.MCPBrowser, error) {
	flags, err := page.Flags(proxy)
	if err != nil {
		return nil, nil, err
	}
	browser, err := page.NewMCPBrowser(bin, flags, lightpandaTimeout)
	if err != nil {
		return nil, nil, err
	}
	paced, err := page.NewRobotsPaced(browser, d, interval)
	if err != nil {
		return nil, nil, fmt.Errorf("page_interval_ms: %w", err)
	}
	return paced, browser, nil
}

// lightpandaBinary resolves the configured binary, or "lightpanda" on PATH,
// to an absolute path. MCPBrowser refuses a relative one.
func lightpandaBinary(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	p, err := exec.LookPath("lightpanda")
	if err != nil {
		return "", fmt.Errorf("lightpanda not found on PATH (set lightpanda_binary): %w", err)
	}
	return filepath.Abs(p)
}
