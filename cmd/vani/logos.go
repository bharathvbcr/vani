package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/logo"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// logoRunBudget bounds the icon refresh a daily run does, so a slow site
// cannot hold up the run. Domains it does not reach wait for the next run.
const logoRunBudget = 2 * time.Minute

// logoRequestTimeout bounds one icon request.
const logoRequestTimeout = 15 * time.Second

func logoCache(stateDir string) logo.Cache { return logo.Cache{Dir: filepath.Join(stateDir, "logos")} }

// logoDomains is every board domain in the watchlist that allowed_hosts lists
// (config.Validate refuses a config where one is missing; this is the fetch's
// own guard), and the ones it does not.
func logoDomains(cfg config.Config) (allowed, missing []string) {
	ok := map[string]bool{}
	for _, h := range cfg.AllowedHosts {
		ok[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, b := range cfg.Watchlist {
		switch {
		case b.Domain == "":
		case ok[b.Domain]:
			allowed = append(allowed, b.Domain)
		default:
			missing = append(missing, b.Domain)
		}
	}
	slices.Sort(allowed)
	slices.Sort(missing)
	return slices.Compact(allowed), slices.Compact(missing)
}

// refreshLogos fetches the icons the cache lacks or holds stale. The egress
// client allows only the board domains, so a redirect elsewhere, even to
// another allowed board host, is refused and named in the result.
func refreshLogos(ctx context.Context, cfg config.Config, stateDir string, now time.Time, force bool) (logo.Result, []string, error) {
	domains, missing := logoDomains(cfg)
	if len(domains) == 0 {
		return logo.Result{}, missing, nil
	}
	d, err := egress.New(egress.Config{AllowedHosts: domains, MaxBodyBytes: logo.MaxBytes + 1, Timeout: logoRequestTimeout})
	if err != nil {
		return logo.Result{}, missing, fmt.Errorf("logos: egress: %w", err)
	}
	res, err := logoCache(stateDir).Refresh(ctx, d, domains, now, force)
	return res, missing, err
}

// refreshLogos is the daily run's icon refresh. It only warns: a missing icon
// shows the monogram, and never fails a run.
func (w *wired) refreshLogos(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, logoRunBudget)
	defer cancel()
	res, missing, err := refreshLogos(ctx, w.cfg, w.stateDir, time.Now(), false)
	w.warnLogos(res, missing, err)
}

func (w *wired) warnLogos(res logo.Result, missing []string, err error) {
	if len(missing) > 0 {
		w.warnf("logos: not in allowed_hosts, skipped: %s", strings.Join(missing, ", "))
	}
	if res.StateReset != "" {
		w.warnf("logos: state file unreadable, started afresh: %s", res.StateReset)
	}
	for _, e := range res.Errors {
		w.warnf("logos: %s", e)
	}
	if err != nil {
		w.warnf("logos: %v", err)
	}
	w.warnf("logos: fetched %d, fresh %d, waiting %d, failed %d, deferred %d",
		res.Fetched, res.Fresh, res.Waiting, res.Failed, res.Deferred)
}

func (e *env) cmdLogos(args []string) error {
	if len(args) == 0 || args[0] != "refresh" {
		return usagef("logos: expected refresh")
	}
	fs := e.newFlags("logos refresh")
	force := fs.Bool("force", false, "refetch cached icons and domains that failed recently")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	res, missing, err := refreshLogos(ctx, cfg, e.stateDir, time.Now(), *force)
	if *asJSON {
		out := struct {
			logo.Result
			NotAllowed []string `json:"not_allowed,omitempty"`
		}{res, missing}
		enc := json.NewEncoder(e.stdout)
		if jerr := enc.Encode(out); jerr != nil {
			return jerr
		}
		return err
	}
	w := &wired{warn: e.stdout}
	w.warnLogos(res, missing, err)
	if domains, _ := logoDomains(cfg); len(domains) == 0 && len(missing) == 0 {
		fmt.Fprintln(e.stdout, "no board has a domain; set one with `vani watchlist domain`")
	}
	return err
}

// logoIndex answers which cached icon a posting's company has: the icon of
// the board the posting came from (sources.Board.KeyPrefix), else of the one
// board whose company name it carries. Postings the extension saved, or keyed
// by company and title, have no board and match by name only.
type logoIndex struct {
	cache     logo.Cache
	byPrefix  map[string]string // board key prefix -> domain
	byCompany map[string]string // folded company name -> domain; "" when two boards disagree
	memo      map[string]string // domain -> cached path, "" for none
}

func newLogoIndex(cfg config.Config, stateDir string) *logoIndex {
	ix := &logoIndex{cache: logoCache(stateDir), byPrefix: map[string]string{},
		byCompany: map[string]string{}, memo: map[string]string{}}
	for _, b := range cfg.Watchlist {
		if b.Domain == "" {
			continue
		}
		ix.byPrefix[b.KeyPrefix()] = b.Domain
		name := foldCompany(b.Company)
		if name == "" {
			continue
		}
		if d, seen := ix.byCompany[name]; seen && d != b.Domain {
			ix.byCompany[name] = ""
		} else {
			ix.byCompany[name] = b.Domain
		}
	}
	return ix
}

func foldCompany(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// path is the cached icon for a posting, or "". A nil index has none.
func (ix *logoIndex) path(k posting.Key, company string) string {
	if ix == nil {
		return ""
	}
	dom := ix.byPrefix[boardOf(k)+":"]
	if dom == "" {
		dom = ix.byCompany[foldCompany(company)]
	}
	if dom == "" {
		return ""
	}
	p, ok := ix.memo[dom]
	if !ok {
		p, _ = ix.cache.Path(dom)
		ix.memo[dom] = p
	}
	return p
}
