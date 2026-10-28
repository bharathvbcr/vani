package main

import (
	"encoding/json"
	"errors"
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
		return usagef("watchlist: expected list, add, remove, domain, icon or discover")
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
	case "icon":
		return e.watchlistIcon(args[1:])
	case "discover":
		return e.watchlistDiscover(args[1:])
	}
	return usagef("watchlist: unknown subcommand %q", args[0])
}

func (e *env) watchlistList(args []string) error {
	fs := e.newFlags("watchlist list")
	asJSON := fs.Bool("json", false, "a JSON array of the boards on stdout (the Mac app's Saved searches)")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if *asJSON {
		out := make([]watchlistJSON, 0, len(cfg.Watchlist))
		for _, b := range cfg.Watchlist {
			out = append(out, watchlistJSON{ATS: b.ATS, Token: b.Token, Company: b.Company, Region: b.Region, Host: b.Host,
				Search: b.Search, Searches: b.Searches, URL: b.URL, LinkPattern: b.LinkPattern, Domain: b.Domain, Icon: b.Icon})
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
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
	ats := fs.String("ats", "", "ATS: gh, lv, ab, wd, az (Amazon), gg (Google), ap (Apple), ef (Eightfold), jb (Jibe), gm (Gem), sr (SmartRecruiters), ib (IBM; tokens ibm and confluent), av (Avature), ds (D. E. Shaw), at (Atlassian), oc (Oracle HCM), rp (Rippling), wk (Workable), rc (Recruitee), pp (Pinpoint), bh (BambooHR), ps (Personio), tt (TikTok), fd (XML feed) or pg (careers page)")
	token := fs.String("token", "", "board token (workday and oc: tenant/site, e.g. jpmc/CX_1001; ef: the company domain; jb: the client code, e.g. amd; sr: the company identifier, e.g. servicenow; av: a lower-case slug, e.g. twosigma; rp: the board slug; wk: the account slug; rc, pp, bh and ps: the board's subdomain, e.g. acme for acme.recruitee.com; gm: the Gem vanity path; tt: tiktok; pg and fd: a short slug)")
	company := fs.String("company", "", "company display name")
	boardName := fs.String("board-name", "", "the board's literal name on the ATS when it is not --token (ab only): --token hippocratic-ai --board-name \"Hippocratic AI\"")
	region := fs.String("region", "", "regional host (eu, lever only)")
	host := fs.String("host", "", "workday host (acme.wd5.myworkdayjobs.com, or wd1.myworkdaysite.com whose tenant is the token's), oc host (jpmc.fa.oraclecloud.com, or eeho.fa.us2.oraclecloud.com), or eightfold, jibe or avature careers host (careers.amd.com)")
	var searches []string
	fs.Func("search", "search term (repeatable; wd, oc, az, gg, ap, ef, jb, ib, tt); the board lists their union", func(s string) error {
		searches = append(searches, s)
		return nil
	})
	var filters map[string][]string
	fs.Func("filter", "site filter name=value (repeatable; gg, az, ap, ef, jb, sr: country=us; tt: country=Germany)", func(s string) error {
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
	pageURL := fs.String("url", "", "https URL (pg: the careers listing page; av: the portal's listing page, e.g. https://careers.twosigma.com/careers/OpenRoles; fd: the XML job feed)")
	linkPattern := fs.String("link-pattern", "", "regexp a posting link must match (pg only)")
	domain := fs.String("domain", "", "the company's website host, for its icon (www.example.com)")
	icon := fs.String("icon", "", "https URL of the company's icon, when --domain serves none at the usual paths")
	allow := fs.Bool("allow-host", false, "also add the board's API host, --domain and the --icon host to allowed_hosts")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" || *company == "" {
		return usagef("watchlist add: --ats, --token and --company are required")
	}
	b := sources.Board{ATS: posting.ATS(*ats), Token: *token, Company: *company, Region: *region, BoardName: *boardName,
		Host: *host, URL: *pageURL, LinkPattern: *linkPattern, Filters: filters, Domain: *domain, Icon: *icon}
	switch len(searches) {
	case 0:
	case 1:
		b.Search = searches[0]
	default:
		b.Searches = searches
	}
	if err := config.ValidBoard(b); err != nil { // before the config is read: a bad flag is a usage error
		return usagef("watchlist add: %v", err)
	}
	var addErr error
	if err := e.updateConfig(func(c *config.Config) error {
		addErr = addBoard(c, b, *allow)
		return addErr
	}); err != nil {
		if addErr != nil || errors.Is(err, config.ErrBusy) {
			return err
		}
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

// iconHost is the host of a board's icon URL, or "" when it has none.
func iconHost(b sources.Board) string {
	h, _ := config.IconHost(b.Icon)
	return h
}

// watchlistDomain sets (or, with an empty --domain, clears) the website host a
// board's company icon is fetched from (internal/logo).
func (e *env) watchlistDomain(args []string) error {
	return e.setBoardField(args, "domain", "domain", "the company's website host (www.example.com); empty clears it",
		func(b *sources.Board, v string) { b.Domain = v }, func(b sources.Board) string { return b.Domain })
}

// watchlistIcon sets (or, with an empty --url, clears) the owner-set URL of a
// board's company icon, for a site that serves none at internal/logo's fixed
// paths. The board needs a domain, under which the icon is cached.
func (e *env) watchlistIcon(args []string) error {
	return e.setBoardField(args, "icon", "url", "https URL of the company's icon (PNG, ICO or JPEG); empty clears it",
		func(b *sources.Board, v string) { b.Icon = v }, iconHost)
}

// setBoardField is `watchlist <label>`: it sets one field of a board already
// on the watchlist through set, from the --<field> flag, checks the board
// (config.ValidBoard), and with --allow-host adds host(board) to
// allowed_hosts. config.Save then checks every needed host is allowed.
func (e *env) setBoardField(args []string, label, field, help string, set func(*sources.Board, string), host func(sources.Board) string) error {
	name := "watchlist " + label
	fs := e.newFlags(name)
	ats := fs.String("ats", "", "ATS of the board")
	token := fs.String("token", "", "board token")
	value := fs.String(field, "", help)
	allow := fs.Bool("allow-host", false, "also add the --"+field+" host to allowed_hosts")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" {
		return usagef("%s: --ats and --token are required", name)
	}
	target := sources.Board{ATS: posting.ATS(*ats), Token: *token}
	var b sources.Board
	var editErr error
	if err := e.updateConfig(func(c *config.Config) error {
		i := slices.IndexFunc(c.Watchlist, func(x sources.Board) bool { return sameBoard(x, target) })
		if i < 0 {
			editErr = fmt.Errorf("board %s/%s is not on the watchlist", *ats, *token)
			return editErr
		}
		b = c.Watchlist[i]
		set(&b, *value)
		if err := config.ValidBoard(b); err != nil {
			editErr = usagef("%s: %v", name, err)
			return editErr
		}
		c.Watchlist[i] = b
		if *allow {
			c.AllowedHosts = allowHost(c.AllowedHosts, host(b))
		}
		return nil
	}); err != nil {
		if editErr != nil || errors.Is(err, config.ErrBusy) {
			return err
		}
		return fmt.Errorf("%w\n(use --allow-host to add the %s host to allowed_hosts)", err, field)
	}
	if *value == "" {
		fmt.Fprintf(e.stdout, "cleared the %s of %s/%s\n", label, b.ATS, b.Token)
	} else {
		fmt.Fprintf(e.stdout, "%s/%s %s %s\n", b.ATS, b.Token, label, *value)
	}
	return nil
}

func (e *env) watchlistRemove(args []string) error {
	fs := e.newFlags("watchlist remove")
	ats := fs.String("ats", "", "ATS: gh, lv, ab, wd, az, gg, ap, ef, jb, gm, sr, ib, av, ds, at, oc, rp, wk, rc, pp, bh, ps, tt, fd or pg")
	token := fs.String("token", "", "board token")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ats == "" || *token == "" {
		return usagef("watchlist remove: --ats and --token are required")
	}
	target := sources.Board{ATS: posting.ATS(*ats), Token: *token}
	if err := e.updateConfig(func(c *config.Config) error {
		n := len(c.Watchlist)
		c.Watchlist = slices.DeleteFunc(c.Watchlist, func(x sources.Board) bool { return sameBoard(x, target) })
		if len(c.Watchlist) == n {
			return fmt.Errorf("board %s/%s is not on the watchlist", *ats, *token)
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "removed %s/%s\n", *ats, *token)
	return nil
}

// watchlistJSON is one board in `watchlist list --json`.
type watchlistJSON struct {
	ATS         posting.ATS `json:"ats"`
	Token       string      `json:"token"`
	Company     string      `json:"company"`
	Region      string      `json:"region,omitempty"`
	Host        string      `json:"host,omitempty"`
	Search      string      `json:"search,omitempty"`
	Searches    []string    `json:"searches,omitempty"`
	URL         string      `json:"url,omitempty"`
	LinkPattern string      `json:"link_pattern,omitempty"`
	Domain      string      `json:"domain,omitempty"`
	Icon        string      `json:"icon,omitempty"`
}

// invalidBoardError wraps the reason config.ValidBoard refused a board.
type invalidBoardError struct{ error }

func (e invalidBoardError) Unwrap() error { return e.error }

// boardExistsError says the watchlist already holds the board (sameBoard).
type boardExistsError struct{ b sources.Board }

func (e boardExistsError) Error() string {
	return fmt.Sprintf("board %s/%s is already on the watchlist", e.b.ATS, e.b.Token)
}

// addBoard is the one way a board joins a config, for `watchlist add` and
// `jobs ingest`: it checks the board (config.ValidBoard), refuses one the
// watchlist already has (sameBoard) and, with allow, adds the board's host and
// its logo Domain and Icon host (when set) to allowed_hosts. The caller saves
// the config (config.Save validates it again, including that every board host
// is allowed). On error cfg is unchanged.
func addBoard(cfg *config.Config, b sources.Board, allow bool) error {
	if err := config.ValidBoard(b); err != nil {
		return invalidBoardError{err}
	}
	if slices.ContainsFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) }) {
		return boardExistsError{b}
	}
	cfg.Watchlist = append(cfg.Watchlist, b)
	if !allow {
		return nil
	}
	for _, host := range config.BoardHosts(b) {
		cfg.AllowedHosts = allowHost(cfg.AllowedHosts, host)
	}
	// The company icon is fetched from the board's own domain, and from its
	// owner-set icon URL (internal/logo).
	cfg.AllowedHosts = allowHost(cfg.AllowedHosts, b.Domain)
	cfg.AllowedHosts = allowHost(cfg.AllowedHosts, iconHost(b))
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
