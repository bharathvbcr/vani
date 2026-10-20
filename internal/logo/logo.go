// Package logo fetches and caches the icon of each company whose watchlist
// board names its website host (sources.Board.Domain), so the Jobs list can
// show it in place of the company's monogram.
//
// The icon is asked for at fixed, well-known paths on that host only. The
// site's HTML is never fetched and no link from it is followed, and a reply is
// kept only if its bytes are a PNG, ICO or JPEG image: an SVG (which can carry
// script) or an HTML "not found" page served with status 200 is refused.
package logo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/egress"
)

// Paths are asked for in order; the first image wins. apple-touch-icon is
// usually 180px, favicon.ico usually 16-48px.
var Paths = []string{"/apple-touch-icon.png", "/favicon.ico"}

const (
	// MaxBytes bounds an icon; a larger reply is refused, never truncated.
	MaxBytes = 256 << 10
	// RefreshAfter is how long a cached icon is kept before it is fetched again.
	RefreshAfter = 30 * 24 * time.Hour
	// RetryAfter is how long a domain whose fetch failed waits for another try.
	RetryAfter = 7 * 24 * time.Hour
	// MaxPerRun bounds the domains one Refresh fetches; the rest wait for the
	// next run and are counted in Result.Deferred.
	MaxPerRun = 50

	stateFile    = "state.json"
	stateVersion = 1
	maxStateSize = 1 << 20
	maxErrorLen  = 300
)

// exts are the cached file extensions, one per image kind Sniff accepts.
var exts = []string{"png", "ico", "jpg"}

// ErrNotImage is returned for a reply whose bytes are not an accepted image.
var ErrNotImage = errors.New("logo: not a PNG, ICO or JPEG image")

// Sniff names the image kind of b by its leading bytes: "png", "ico" or "jpg".
func Sniff(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "png", true
	// ICONDIR: reserved 0, type 1 (icon), then at least one image.
	case len(b) >= 6 && b[0] == 0 && b[1] == 0 && b[2] == 1 && b[3] == 0 && (b[4] != 0 || b[5] != 0):
		return "ico", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpg", true
	}
	return "", false
}

// validDomain is the check that keeps a domain safe as a URL host and a file
// name: lower-case letters, digits, '-' and '.', no empty label. The config
// (config.ValidBoard) has already refused anything that is not a host name;
// this is the cache's own guard, not a second definition of one.
func validDomain(d string) bool {
	if d == "" || len(d) > 253 || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return false
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// Fetch asks https://domain for each of Paths in turn and returns the first
// reply that is an accepted image, with its kind. The error names every path
// that failed and why, including a host the egress policy refused.
func Fetch(ctx context.Context, d egress.Doer, domain string) ([]byte, string, error) {
	if !validDomain(domain) {
		return nil, "", fmt.Errorf("logo: domain %q is not a host name", domain)
	}
	var errs []error
	for _, p := range Paths {
		b, kind, err := fetchOne(ctx, d, "https://"+domain+p)
		if err == nil {
			return b, kind, nil
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		errs = append(errs, fmt.Errorf("%s: %w", p, err))
	}
	return nil, "", errors.Join(errs...)
}

func fetchOne(ctx context.Context, d egress.Doer, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/png,image/x-icon,image/jpeg;q=0.9,*/*;q=0.1")
	resp, err := d.Do(ctx, req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxBytes {
		return nil, "", fmt.Errorf("larger than %d bytes", MaxBytes)
	}
	kind, ok := Sniff(b)
	if !ok {
		return nil, "", fmt.Errorf("%w (Content-Type %q)", ErrNotImage, oneLine(resp.Header.Get("Content-Type"), 80))
	}
	return b, kind, nil
}

// Cache is the icon directory: <Dir>/<domain>.<png|ico|jpg>, plus state.json,
// which remembers failed fetches so a broken domain is not asked every run.
type Cache struct {
	Dir string
}

// Path returns the cached icon of domain, if there is one.
func (c Cache) Path(domain string) (string, bool) {
	p, _, ok := c.file(domain)
	return p, ok
}

// file returns the cached icon of domain and its modification time.
func (c Cache) file(domain string) (string, time.Time, bool) {
	if c.Dir == "" || !validDomain(domain) {
		return "", time.Time{}, false
	}
	for _, ext := range exts {
		p := filepath.Join(c.Dir, domain+"."+ext)
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return p, fi.ModTime(), true
		}
	}
	return "", time.Time{}, false
}

// Failure is one domain's last failed fetch.
type Failure struct {
	At    time.Time `json:"at"`
	Error string    `json:"error"`
}

type state struct {
	Version  int                `json:"version"`
	Failures map[string]Failure `json:"failures"`
}

// Result says what one Refresh did. Every domain asked about is counted in
// exactly one of Fetched, Fresh, Waiting, Failed and Deferred.
type Result struct {
	Fetched int `json:"fetched"`
	// Fresh had a cached icon younger than RefreshAfter.
	Fresh int `json:"fresh"`
	// Waiting failed within RetryAfter and was not asked again.
	Waiting int `json:"waiting"`
	Failed  int `json:"failed"`
	// Deferred were over MaxPerRun and wait for the next run.
	Deferred int `json:"deferred"`
	// Errors names each failed domain and why, sorted by domain.
	Errors []string `json:"errors,omitempty"`
	// StateReset is why state.json could not be read and was started afresh
	// (failures are then retried at once). Empty when it was read.
	StateReset string `json:"state_reset,omitempty"`
}

// Refresh fetches the icon of every domain that has none, or one older than
// RefreshAfter, skipping domains that failed within RetryAfter; force asks
// every domain regardless. A failed refetch keeps the icon already cached. It
// stops at MaxPerRun fetches and when ctx ends (returning ctx's error with the
// counts so far). Domains are fetched one at a time.
func (c Cache) Refresh(ctx context.Context, d egress.Doer, domains []string, now time.Time, force bool) (Result, error) {
	var res Result
	if c.Dir == "" {
		return res, errors.New("logo: cache directory is not set")
	}
	st, reset := c.load()
	res.StateReset = reset
	uniq := slices.Clone(domains)
	slices.Sort(uniq)
	uniq = slices.Compact(uniq)
	var errs []string
	tried := 0
	var stop error
	for _, dom := range uniq {
		if dom == "" {
			continue
		}
		if stop != nil {
			res.Deferred++
			continue
		}
		if !validDomain(dom) {
			res.Failed++
			errs = append(errs, fmt.Sprintf("%s: not a host name", dom))
			continue
		}
		_, mod, cached := c.file(dom)
		if !force {
			if cached && now.Sub(mod) < RefreshAfter {
				res.Fresh++
				continue
			}
			if f, ok := st.Failures[dom]; ok && now.Sub(f.At) < RetryAfter {
				res.Waiting++
				continue
			}
		}
		if tried >= MaxPerRun {
			res.Deferred++
			continue
		}
		tried++
		b, kind, err := Fetch(ctx, d, dom)
		if err == nil {
			err = c.store(dom, kind, b)
		}
		if err != nil {
			if ctx.Err() != nil {
				stop = ctx.Err()
				res.Deferred++
				continue
			}
			res.Failed++
			msg := oneLine(err.Error(), maxErrorLen)
			errs = append(errs, dom+": "+msg)
			st.Failures[dom] = Failure{At: now, Error: msg}
			continue
		}
		res.Fetched++
		delete(st.Failures, dom)
	}
	res.Errors = errs
	if err := c.save(st); err != nil {
		return res, errors.Join(stop, err)
	}
	return res, stop
}

// store writes the icon and removes any cached icon of another kind.
func (c Cache) store(domain, kind string, b []byte) error {
	if err := atomicfile.Write(filepath.Join(c.Dir, domain+"."+kind), b); err != nil {
		return err
	}
	for _, ext := range exts {
		if ext == kind {
			continue
		}
		if err := os.Remove(filepath.Join(c.Dir, domain+"."+ext)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// load reads state.json. A missing file is an empty state; an unreadable one
// is started afresh and the reason returned, never a failed refresh.
func (c Cache) load() (state, string) {
	empty := state{Version: stateVersion, Failures: map[string]Failure{}}
	f, err := os.Open(filepath.Join(c.Dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return empty, ""
	}
	if err != nil {
		return empty, oneLine(err.Error(), maxErrorLen)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStateSize+1))
	switch {
	case err != nil:
		return empty, oneLine(err.Error(), maxErrorLen)
	case len(raw) > maxStateSize:
		return empty, fmt.Sprintf("larger than %d bytes", maxStateSize)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return empty, oneLine(err.Error(), maxErrorLen)
	}
	if st.Version != stateVersion {
		return empty, fmt.Sprintf("version %d, want %d", st.Version, stateVersion)
	}
	if st.Failures == nil {
		st.Failures = map[string]Failure{}
	}
	return st, ""
}

func (c Cache) save(st state) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(c.Dir, stateFile), raw)
}

// oneLine collapses s to one line of at most n bytes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}
