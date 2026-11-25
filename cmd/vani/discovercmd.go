package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/discover/freehire"
	"github.com/bharathvbcr/Vani/internal/discover/searchplan"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/site"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// discoverTimeout bounds one `watchlist discover`: at most freehire.MaxPages
// requests, spaced freehire.PageGap apart, each bounded by the client's own
// timeout and the search's retry budget.
const discoverTimeout = 3 * time.Minute

// newDiscoverDoer is the egress client `watchlist discover` asks freehire.me
// through: it may reach that host and no other, whatever allowed_hosts holds,
// so a redirect off it is refused. Tests replace it so none reaches the network.
var newDiscoverDoer = func() (egress.Doer, error) {
	return egress.New(egress.Config{AllowedHosts: []string{freehire.Host}, MaxBodyBytes: freehire.MaxBody + 1})
}

// newSiteDiscoverDoer is the egress client `watchlist discover --site` asks
// domain through: it may reach that host and no other, whatever allowed_hosts
// holds, so a redirect off it is refused. Tests replace it so none reaches the
// network.
var newSiteDiscoverDoer = func(domain string) (egress.Doer, error) {
	return egress.New(egress.Config{AllowedHosts: []string{domain}, MaxBodyBytes: site.MaxSitemapBytes + 1})
}

// discoverSleep waits between pages; nil is the real wait. Tests replace it.
var discoverSleep func(ctx context.Context, d time.Duration) error

// discoverJSON is `watchlist discover --json`.
type discoverJSON struct {
	Search     discoverSearchJSON  `json:"search"`
	Meta       discoverMetaJSON    `json:"meta"`
	Boards     []discoverBoardJSON `json:"boards"`
	Unreadable []unreadableJSON    `json:"unreadable"`
}

type discoverSearchJSON struct {
	Search    string `json:"search"`
	Country   string `json:"country,omitempty"`
	WorkMode  string `json:"work_mode,omitempty"`
	Seniority string `json:"seniority,omitempty"`
	Pages     int    `json:"pages"`
}

// discoverMetaJSON carries both numbers a capped sample needs: Total is what
// freehire holds for the search, Fetched what was read of it.
type discoverMetaJSON struct {
	Total    int `json:"total"`
	Fetched  int `json:"fetched"`
	Pages    int `json:"pages"`
	Skipped  int `json:"skipped"`
	Repeated int `json:"repeated"`
	// Capped is true when Fetched is below Total.
	Capped bool `json:"capped"`
	// Stopped is why a page after the first was not read, when one was not.
	Stopped string `json:"stopped,omitempty"`
}

type discoverBoardJSON struct {
	ATS       string `json:"ats"`
	Token     string `json:"token"`
	Company   string `json:"company"`
	Host      string `json:"host,omitempty"`
	BoardName string `json:"board_name,omitempty"`
	Region    string `json:"region,omitempty"`
	Search    string `json:"search,omitempty"`
	Postings  int    `json:"postings"`
	Watched   bool   `json:"watched"`
	// AddCommand is the `vani watchlist add` that would add the board; empty
	// for one already on the watchlist.
	AddCommand string `json:"add_command,omitempty"`
}

type unreadableJSON struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// siteDiscoverJSON is `watchlist discover --site DOMAIN --json`.
type siteDiscoverJSON struct {
	Domain           string              `json:"domain"`
	Company          string              `json:"company,omitempty"`
	IconURL          string              `json:"icon_url,omitempty"`
	RobotsFetched    bool                `json:"robots_fetched"`
	RobotsDisallowed []string            `json:"robots_disallowed"`
	SitemapsRead     int                 `json:"sitemaps_read"`
	PagesRead        int                 `json:"pages_read"`
	URLsExamined     int                 `json:"urls_examined"`
	Capped           bool                `json:"capped"`
	Candidates       []siteCandidateJSON `json:"candidates"`
	Notes            []string            `json:"notes"`
}

type siteCandidateJSON struct {
	ATS           string   `json:"ats"`
	Token         string   `json:"token"`
	Company       string   `json:"company"`
	BoardName     string   `json:"board_name,omitempty"`
	Region        string   `json:"region,omitempty"`
	Host          string   `json:"host,omitempty"`
	Search        string   `json:"search,omitempty"`
	URL           string   `json:"url,omitempty"`
	LinkPattern   string   `json:"link_pattern,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Source        string   `json:"source"`
	SampleURLs    []string `json:"sample_urls"`
	EvidenceCount int      `json:"evidence_count"`
	RobotsBlocked bool     `json:"robots_blocked"`
	Watched       bool     `json:"watched"`
	AddCommand    string   `json:"add_command,omitempty"`
}

var siteHostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// watchlistDiscover is `watchlist discover`: it asks freehire.me which boards
// have roles matching a search (or, with --site, inspects a company domain
// directly) and prints them with the `watchlist add` that adds each. It never
// edits the watchlist unless --profile --add is passed.
func (e *env) watchlistDiscover(args []string) error {
	fs := e.newFlags("watchlist discover")
	search := fs.String("search", "", "the role to look for, e.g. \"ml engineer\" (required)")
	country := fs.String("country", "", "two-letter country code(s), e.g. US or US,CA")
	workMode := fs.String("work-mode", "", "remote, hybrid or onsite")
	seniority := fs.String("seniority", "", "intern, junior, middle, senior, lead, staff, principal or c_level")
	pages := fs.Int("pages", freehire.DefaultPages, fmt.Sprintf("pages of %d results to read, 1..%d", freehire.PageSize, freehire.MaxPages))
	siteDomain := fs.String("site", "", "company domain to inspect directly (robots.txt, sitemaps and career pages), e.g. acme.com")
	asJSON := e.jsonFlag(fs, "a JSON document on stdout")
	allow := fs.Bool("allow-host", false, "add "+freehire.Host+" (or --site DOMAIN) to allowed_hosts first, when it is not there")
	profile := fs.Bool("profile", false, "search for the role words in title_signals.include and the skills the knowledge base lists most, in place of --search")
	maxSearches := fs.Int("max-searches", searchplan.DefaultSearches, fmt.Sprintf("--profile: the most searches, 1..%d", searchplan.MaxSearches))
	maxSkills := fs.Int("max-skills", searchplan.DefaultSkills, "--profile: the most knowledge-base skill searches, 0 for none")
	noKB := fs.Bool("no-kb", false, "--profile: search the role words only, without reading the knowledge base")
	dryRun := fs.Bool("dry-run", false, "--profile: print the searches and ask nothing")
	addTop := fs.Int("add", 0, fmt.Sprintf("--profile: add the top N new boards to the watchlist, 1..%d", maxDiscoverAdd))
	allowBoards := fs.Bool("allow-board-hosts", false, "--profile --add: also add a board's own host to allowed_hosts (per-tenant ATSs); without it such a board is not added")
	if err := parse(fs, args); err != nil {
		return err
	}
	profileOnly := []string{"max-searches", "max-skills", "no-kb", "dry-run", "add", "allow-board-hosts"}
	searchOnly := []string{"country", "work-mode", "seniority", "pages"}
	var setFlags []string
	fs.Visit(func(f *flag.Flag) { setFlags = append(setFlags, f.Name) })
	if slices.Contains(setFlags, "site") {
		if *profile {
			return usagef("watchlist discover: --site and --profile cannot be used together")
		}
		if slices.Contains(setFlags, "search") {
			return usagef("watchlist discover: --site and --search cannot be used together")
		}
		for _, f := range profileOnly {
			if slices.Contains(setFlags, f) {
				return usagef("watchlist discover: --%s cannot be used with --site", f)
			}
		}
		for _, f := range searchOnly {
			if slices.Contains(setFlags, f) {
				return usagef("watchlist discover: --%s cannot be used with --site", f)
			}
		}
		return e.discoverSite(*siteDomain, *allow, *asJSON)
	}
	if *profile {
		if slices.Contains(setFlags, "search") {
			return usagef("watchlist discover: --profile makes its own searches; leave out --search")
		}
		return e.discoverProfile(profileDiscoverOptions{
			filters:      freehire.Query{Country: *country, WorkMode: *workMode, Seniority: *seniority},
			pages:        *pages,
			asJSON:       *asJSON,
			allowHost:    *allow,
			maxSearches:  *maxSearches,
			maxSkills:    *maxSkills,
			maxSkillsSet: slices.Contains(setFlags, "max-skills"),
			noKB:         *noKB,
			dryRun:       *dryRun,
			add:          *addTop,
			allowBoards:  *allowBoards,
		})
	}
	for _, f := range profileOnly {
		if slices.Contains(setFlags, f) {
			return usagef("watchlist discover: --%s needs --profile", f)
		}
	}
	q := freehire.Query{Search: *search, Country: *country, WorkMode: *workMode, Seniority: *seniority}
	if err := q.Check(); err != nil {
		return usagef("watchlist discover: %v", err)
	}
	if *pages < 1 || *pages > freehire.MaxPages {
		return usagef("watchlist discover: --pages %d must be within 1..%d", *pages, freehire.MaxPages)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if err := e.ensureFreehireHost(&cfg, *allow); err != nil {
		return err
	}
	doer, err := newDiscoverDoer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()
	res, err := freehire.Client{Doer: doer, Pages: *pages, Gap: freehire.PageGap, Sleep: discoverSleep}.Search(ctx, q)
	switch {
	case errors.Is(err, srcutil.ErrRateLimited):
		return fmt.Errorf("%s rate limited the search (HTTP 429); nothing was read, try again later: %w", freehire.Host, err)
	case err != nil:
		return err
	}
	found := freehire.Boards(res.Jobs, strings.TrimSpace(q.Search))

	boards := make([]discoverBoardJSON, 0, len(found.Boards))
	for _, c := range found.Boards {
		b := c.Board
		out := discoverBoardJSON{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Host: b.Host, BoardName: b.BoardName,
			Region: b.Region, Search: b.Search, Postings: c.Postings}
		if out.Watched = slices.ContainsFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) }); !out.Watched {
			out.AddCommand = addCommand(b, cfg.AllowedHosts)
		}
		boards = append(boards, out)
	}
	unreadable := make([]unreadableJSON, 0, len(found.Unreadable))
	for _, u := range found.Unreadable {
		unreadable = append(unreadable, unreadableJSON{Source: u.Source, Reason: u.Reason, Count: u.Count})
	}
	doc := discoverJSON{
		Search: discoverSearchJSON{Search: strings.TrimSpace(q.Search), Country: q.Country, WorkMode: q.WorkMode, Seniority: q.Seniority, Pages: *pages},
		Meta: discoverMetaJSON{Total: res.Total, Fetched: res.Fetched, Pages: res.Pages, Skipped: res.Skipped, Repeated: res.Repeated,
			Capped: res.Capped, Stopped: res.Stopped},
		Boards:     boards,
		Unreadable: unreadable,
	}
	if *asJSON {
		return writeJSON(e.stdout, doc)
	}
	e.printDiscover(doc)
	return nil
}

// printDiscover is the text form of a discover result: what was read, the
// boards, and every result that is not a board.
func (e *env) printDiscover(doc discoverJSON) {
	m := doc.Meta
	fmt.Fprintf(e.stdout, "%s: read %d of %d matches for %q (%d page", freehire.Host, m.Fetched, m.Total, doc.Search.Search, m.Pages)
	if m.Pages != 1 {
		fmt.Fprint(e.stdout, "s")
	}
	fmt.Fprintln(e.stdout, ")")
	if m.Capped {
		fmt.Fprintf(e.stdout, "  not every match was read: --pages N reads more, at most %d\n", freehire.MaxPages)
	}
	if m.Stopped != "" {
		fmt.Fprintf(e.stdout, "  stopped early: %s\n", m.Stopped)
	}
	if m.Skipped > 0 || m.Repeated > 0 {
		fmt.Fprintf(e.stdout, "  %d malformed and %d repeated results were left out\n", m.Skipped, m.Repeated)
	}
	fmt.Fprintf(e.stdout, "\nboards with matching postings (%d), most first; discovery only, Vani reads postings from the boards themselves:\n", len(doc.Boards))
	for _, b := range doc.Boards {
		state := "new"
		if b.Watched {
			state = "watched"
		}
		fmt.Fprintf(e.stdout, "%4d  %s  %s  %s  [%s]\n", b.Postings, b.ATS, b.Token, b.Company, state)
		if b.AddCommand != "" {
			fmt.Fprintf(e.stdout, "      %s\n", b.AddCommand)
		}
	}
	if len(doc.Unreadable) == 0 {
		return
	}
	total := 0
	for _, u := range doc.Unreadable {
		total += u.Count
	}
	fmt.Fprintf(e.stdout, "\nresults Vani cannot add a board for (%d), by source:\n", total)
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	for _, u := range doc.Unreadable {
		fmt.Fprintf(tw, "%4d\t%s\t%s\n", u.Count, u.Source, u.Reason)
	}
	tw.Flush()
}

// discoverSite is `watchlist discover --site DOMAIN`: it inspects a company
// domain directly via site.Discover without contacting external hosts and
// reports validated board candidates without modifying the watchlist.
func (e *env) discoverSite(domain string, allow, asJSON bool) error {
	if err := validSiteDomain(domain); err != nil {
		return usagef("watchlist discover: %v", err)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if err := e.ensureSiteHost(&cfg, domain, allow); err != nil {
		return err
	}
	doer, err := newSiteDiscoverDoer(domain)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()
	res, err := site.Discover(ctx, doer, domain, site.Options{})
	if err != nil {
		return err
	}
	cands := make([]siteCandidateJSON, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		b := c.Board
		watched := slices.ContainsFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) })
		item := siteCandidateJSON{
			ATS:           string(b.ATS),
			Token:         b.Token,
			Company:       b.Company,
			BoardName:     b.BoardName,
			Region:        b.Region,
			Host:          b.Host,
			Search:        b.Search,
			URL:           b.URL,
			LinkPattern:   b.LinkPattern,
			Domain:        b.Domain,
			Icon:          b.Icon,
			Source:        c.Source,
			SampleURLs:    nonNilStrings(c.SampleURLs),
			EvidenceCount: c.EvidenceCount,
			RobotsBlocked: c.RobotsBlocked,
			Watched:       watched,
		}
		if !watched {
			item.AddCommand = addCommand(b, cfg.AllowedHosts)
		}
		cands = append(cands, item)
	}
	doc := siteDiscoverJSON{
		Domain:           res.Domain,
		Company:          res.Company,
		IconURL:          res.IconURL,
		RobotsFetched:    res.RobotsFetched,
		RobotsDisallowed: nonNilStrings(res.RobotsDisallowed),
		SitemapsRead:     res.SitemapsRead,
		PagesRead:        res.PagesRead,
		URLsExamined:     res.URLsExamined,
		Capped:           res.Capped,
		Candidates:       cands,
		Notes:            nonNilStrings(res.Notes),
	}
	if asJSON {
		return writeJSON(e.stdout, doc)
	}
	e.printSiteDiscover(doc)
	return nil
}

func validSiteDomain(domain string) error {
	if domain == "" {
		return errors.New("--site domain is empty")
	}
	if len(domain) > 253 {
		return fmt.Errorf("--site domain %q exceeds 253 bytes", domain)
	}
	if strings.ContainsFunc(domain, unicode.IsControl) {
		return errors.New("--site domain contains control characters")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return fmt.Errorf("--site domain %q must be a hostname, not an IP address", domain)
	}
	if !siteHostRE.MatchString(domain) {
		return fmt.Errorf("--site domain %q must be a lower-case bare hostname with at least two labels, e.g. acme.com", domain)
	}
	if err := egress.ValidAllowedHost(domain); err != nil {
		return fmt.Errorf("--site: %w", err)
	}
	return nil
}

// ensureSiteHost makes sure domain is in allowed_hosts, adding it only with
// allow; cfg is updated to match what was saved (same pattern as ensureFreehireHost).
func (e *env) ensureSiteHost(cfg *config.Config, domain string, allow bool) error {
	if egress.AllowsHost(cfg.AllowedHosts, domain) {
		return nil
	}
	if !allow {
		return fmt.Errorf("%s is not in allowed_hosts, so watchlist discover cannot inspect it "+
			"(robots.txt, sitemaps and career pages, at most %d sitemaps and %d career pages); "+
			"re-run with --allow-host to add it", domain, site.MaxSitemaps, site.MaxCareerPages)
	}
	if err := e.updateConfig(func(c *config.Config) error {
		c.AllowedHosts = allowHost(c.AllowedHosts, domain)
		return nil
	}); err != nil {
		return err
	}
	cfg.AllowedHosts = allowHost(cfg.AllowedHosts, domain)
	fmt.Fprintf(e.stderr, "added %s to allowed_hosts\n", domain)
	return nil
}

func nonNilStrings(ss []string) []string {
	if len(ss) == 0 {
		return []string{}
	}
	return slices.Clone(ss)
}

func (e *env) printSiteDiscover(doc siteDiscoverJSON) {
	robotsState := "robots.txt absent"
	if doc.RobotsFetched {
		robotsState = "robots.txt fetched"
	}
	fmt.Fprintf(e.stdout, "%s: read %d sitemap(s), %d page(s), examined %d URL(s) (%s)\n",
		doc.Domain, doc.SitemapsRead, doc.PagesRead, doc.URLsExamined, robotsState)
	if doc.Company != "" {
		fmt.Fprintf(e.stdout, "  company: %s\n", oneLine(doc.Company, 160))
	}
	if doc.IconURL != "" {
		fmt.Fprintf(e.stdout, "  icon: %s\n", oneLine(doc.IconURL, 300))
	}
	if doc.Capped {
		fmt.Fprintln(e.stdout, "  results were capped at bounded limits")
	}
	if len(doc.RobotsDisallowed) > 0 {
		fmt.Fprintf(e.stdout, "  robots.txt disallowed (%d): %s\n",
			len(doc.RobotsDisallowed), oneLine(strings.Join(doc.RobotsDisallowed, ", "), 300))
	}
	if len(doc.Candidates) == 0 {
		fmt.Fprintln(e.stdout, "\nno candidate boards found")
	} else {
		fmt.Fprintf(e.stdout, "\ncandidate boards (%d); discovery only, Vani reads postings from the boards themselves:\n", len(doc.Candidates))
		for _, c := range doc.Candidates {
			state := "new"
			if c.Watched {
				state = "watched"
			}
			if c.RobotsBlocked {
				state += ", robots-blocked"
			}
			fmt.Fprintf(e.stdout, "%4d  %s  %s  %s  (via %s)  [%s]\n",
				c.EvidenceCount, c.ATS, c.Token, oneLine(c.Company, 160), c.Source, state)
			for _, s := range c.SampleURLs {
				fmt.Fprintf(e.stdout, "      sample: %s\n", oneLine(s, 300))
			}
			if c.AddCommand != "" {
				fmt.Fprintf(e.stdout, "      %s\n", c.AddCommand)
			}
		}
	}
	if len(doc.Notes) > 0 {
		fmt.Fprintln(e.stdout, "\nnotes:")
		for _, n := range doc.Notes {
			fmt.Fprintf(e.stdout, "  - %s\n", oneLine(n, 300))
		}
	}
}

// addCommand is the `vani watchlist add` that adds b, for the owner to read
// and run. The company name is text from a third-party site, so every value is
// shell-quoted. --allow-host is there only when a host the board needs is not
// in allowed already, so the command widens egress by what the board needs and
// no more.
func addCommand(b sources.Board, allowed []string) string {
	parts := []string{"vani watchlist add --ats", string(b.ATS), "--token", shellQuote(b.Token), "--company", shellQuote(b.Company)}
	for _, f := range []struct{ flag, v string }{
		{"--board-name", b.BoardName},
		{"--region", b.Region},
		{"--host", b.Host},
		{"--search", b.Search},
	} {
		if f.v != "" {
			parts = append(parts, f.flag, shellQuote(f.v))
		}
	}
	for _, s := range b.Searches {
		parts = append(parts, "--search", shellQuote(s))
	}
	for _, f := range []struct{ flag, v string }{
		{"--url", b.URL},
		{"--link-pattern", b.LinkPattern},
		{"--next-pattern", b.NextPattern},
	} {
		if f.v != "" {
			parts = append(parts, f.flag, shellQuote(f.v))
		}
	}
	if b.ScrollSteps != 0 {
		parts = append(parts, "--scroll-steps", strconv.Itoa(b.ScrollSteps))
	}
	if b.WaitSelector != "" {
		parts = append(parts, "--wait-selector", shellQuote(b.WaitSelector))
	}
	if b.Extract != nil {
		for _, f := range []struct{ flag, v string }{
			{"--extract-title", b.Extract.Title},
			{"--extract-description", b.Extract.Description},
			{"--extract-location", b.Extract.Location},
			{"--extract-company", b.Extract.Company},
		} {
			if f.v != "" {
				parts = append(parts, f.flag, shellQuote(f.v))
			}
		}
	}
	for _, f := range []struct{ flag, v string }{
		{"--domain", b.Domain},
		{"--icon", b.Icon},
	} {
		if f.v != "" {
			parts = append(parts, f.flag, shellQuote(f.v))
		}
	}
	if boardNeedsAllowHost(b, allowed) {
		parts = append(parts, "--allow-host")
	}
	return strings.Join(parts, " ")
}

func boardNeedsAllowHost(b sources.Board, allowed []string) bool {
	if slices.ContainsFunc(config.BoardHosts(b), func(h string) bool { return !egress.AllowsHost(allowed, h) }) {
		return true
	}
	if b.Domain != "" && !egress.AllowsHost(allowed, b.Domain) {
		return true
	}
	if ih, ok := config.IconHost(b.Icon); ok && !egress.AllowsHost(allowed, ih) {
		return true
	}
	return false
}

// shellQuote is s as one POSIX shell word: bare when it is plain, otherwise in
// single quotes with each embedded ' written '\”. Nothing inside single quotes
// is interpreted, so a name holding $(...), backticks or quotes stays text. The
// text is already one printable line (freehire.CleanName, board tokens).
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/:@%+=", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
