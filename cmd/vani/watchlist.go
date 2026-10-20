package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func (e *env) cmdWatchlist(args []string) error {
	if len(args) == 0 {
		return usagef("watchlist: expected list, add, remove or domain")
	}
	switch args[0] {
	case "list":
		return e.watchlistList(args[1:])
	case "add":
		return e.watchlistAdd(args[1:])
	case "remove":
		return e.watchlistRemove(args[1:])
	case "domain":
		return e.watchlistDomain(args[1:])
	}
	return usagef("watchlist: unknown subcommand %q", args[0])
}

func (e *env) watchlistList(args []string) error {
	if err := parse(e.newFlags("watchlist list"), args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	for _, b := range cfg.Watchlist {
		region := b.Region
		if region == "" {
			region = "-"
		}
		fmt.Fprintf(e.stdout, "%s\t%s\t%s\t%s\n", b.ATS, b.Token, region, b.Company)
	}
	return nil
}

func (e *env) watchlistAdd(args []string) error {
	fs := e.newFlags("watchlist add")
	ats := fs.String("ats", "", "ATS: gh, lv, ab, wd, az (Amazon), gg (Google), ap (Apple), ef (Eightfold) or pg (careers page)")
	token := fs.String("token", "", "board token (workday: tenant/site; ef: the company domain; pg: a short slug)")
	company := fs.String("company", "", "company display name")
	region := fs.String("region", "", "regional host (eu, lever only)")
	host := fs.String("host", "", "workday tenant host (acme.wd5.myworkdayjobs.com) or eightfold careers host")
	var searches []string
	fs.Func("search", "search term (repeatable; wd, az, gg, ap, ef); the board lists their union", func(s string) error {
		searches = append(searches, s)
		return nil
	})
	var filters map[string][]string
	fs.Func("filter", "site filter name=value (repeatable; gg, az, ap)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return fmt.Errorf("filter %q is not name=value", s)
		}
		if filters == nil {
			filters = map[string][]string{}
		}
		filters[k] = append(filters[k], v)
		return nil
	})
	pageURL := fs.String("url", "", "careers listing page (pg only, https)")
	linkPattern := fs.String("link-pattern", "", "regexp a posting link must match (pg only)")
	domain := fs.String("domain", "", "the company's website host, for its icon (www.example.com)")
	allow := fs.Bool("allow-host", false, "also add the board's API host and --domain to allowed_hosts")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" || *company == "" {
		return usagef("watchlist add: --ats, --token and --company are required")
	}
	b := sources.Board{ATS: posting.ATS(*ats), Token: *token, Company: *company, Region: *region,
		Host: *host, URL: *pageURL, LinkPattern: *linkPattern, Filters: filters, Domain: *domain}
	switch len(searches) {
	case 0:
	case 1:
		b.Search = searches[0]
	default:
		b.Searches = searches
	}
	if err := config.ValidBoard(b); err != nil {
		return usagef("watchlist add: %v", err)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) }) {
		return fmt.Errorf("board %s/%s is already on the watchlist", b.ATS, b.Token)
	}
	cfg.Watchlist = append(cfg.Watchlist, b)
	if *allow {
		if host, ok := config.BoardHost(b); ok {
			cfg.AllowedHosts = allowHost(cfg.AllowedHosts, host)
		}
		cfg.AllowedHosts = allowHost(cfg.AllowedHosts, b.Domain)
	}
	if err := config.Save(e.configPath, cfg); err != nil {
		return fmt.Errorf("%w\n(use --allow-host to add the board's API host to allowed_hosts)", err)
	}
	fmt.Fprintf(e.stdout, "added %s/%s (%s)\n", b.ATS, b.Token, b.Company)
	return nil
}

// allowHost adds host to allowed unless it is empty or already there.
func allowHost(allowed []string, host string) []string {
	if host == "" || slices.Contains(allowed, host) {
		return allowed
	}
	return append(allowed, host)
}

// watchlistDomain sets (or, with an empty --domain, clears) the website host a
// board's company icon is fetched from (internal/logo).
func (e *env) watchlistDomain(args []string) error {
	fs := e.newFlags("watchlist domain")
	ats := fs.String("ats", "", "ATS of the board")
	token := fs.String("token", "", "board token")
	domain := fs.String("domain", "", "the company's website host (www.example.com); empty clears it")
	allow := fs.Bool("allow-host", false, "also add --domain to allowed_hosts")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" {
		return usagef("watchlist domain: --ats and --token are required")
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	target := sources.Board{ATS: posting.ATS(*ats), Token: *token}
	i := slices.IndexFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, target) })
	if i < 0 {
		return fmt.Errorf("board %s/%s is not on the watchlist", *ats, *token)
	}
	b := cfg.Watchlist[i]
	b.Domain = *domain
	if err := config.ValidBoard(b); err != nil {
		return usagef("watchlist domain: %v", err)
	}
	cfg.Watchlist[i] = b
	if *allow {
		cfg.AllowedHosts = allowHost(cfg.AllowedHosts, b.Domain)
	}
	if err := config.Save(e.configPath, cfg); err != nil {
		return fmt.Errorf("%w\n(use --allow-host to add the domain to allowed_hosts)", err)
	}
	if b.Domain == "" {
		fmt.Fprintf(e.stdout, "cleared the domain of %s/%s\n", b.ATS, b.Token)
	} else {
		fmt.Fprintf(e.stdout, "%s/%s domain %s\n", b.ATS, b.Token, b.Domain)
	}
	return nil
}

func (e *env) watchlistRemove(args []string) error {
	fs := e.newFlags("watchlist remove")
	ats := fs.String("ats", "", "ATS: gh, lv, ab, wd, az, gg, ap, ef or pg")
	token := fs.String("token", "", "board token")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" {
		return usagef("watchlist remove: --ats and --token are required")
	}
	target := sources.Board{ATS: posting.ATS(*ats), Token: *token}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	n := len(cfg.Watchlist)
	cfg.Watchlist = slices.DeleteFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, target) })
	if len(cfg.Watchlist) == n {
		return fmt.Errorf("board %s/%s is not on the watchlist", *ats, *token)
	}
	if err := config.Save(e.configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "removed %s/%s\n", *ats, *token)
	return nil
}

func sameBoard(a, b sources.Board) bool {
	return a.ATS == b.ATS && strings.EqualFold(a.Token, b.Token)
}

func (e *env) cmdLaunchd(args []string) error {
	if len(args) == 0 || args[0] != "plist" {
		return usagef("launchd: expected subcommand \"plist\"")
	}
	fs := e.newFlags("launchd plist")
	binary := fs.String("binary", "", "path to the vani binary (default: this executable)")
	label := fs.String("label", "dev.vani.daemon", "launchd label")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	bin := *binary
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		bin = exe
	}
	if !filepath.IsAbs(bin) {
		return usagef("--binary must be an absolute path, got %q", bin)
	}
	// The agent runs against the config and state this process resolved
	// (VANI_CONFIG / VANI_STATE_DIR, else the defaults). launchd starts it
	// from / with no shell environment, so a relative path would point
	// somewhere else.
	for _, p := range []struct{ name, path string }{
		{"config path (VANI_CONFIG)", e.configPath},
		{"state dir (VANI_STATE_DIR)", e.stateDir},
	} {
		if !filepath.IsAbs(p.path) {
			return usagef("%s must be an absolute path for a launchd agent, got %q", p.name, p.path)
		}
	}
	_, err := e.stdout.Write(daemon.PlistXML(*label, bin, []string{"daemon"}, e.configPath, e.stateDir))
	return err
}
