package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/discover/freehire"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources"
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

// watchlistDiscover is `watchlist discover`: it asks freehire.me which boards
// have roles matching a search and prints them, ranked by how many matched,
// with the `watchlist add` that adds each. It never edits the watchlist.
func (e *env) watchlistDiscover(args []string) error {
	fs := e.newFlags("watchlist discover")
	search := fs.String("search", "", "the role to look for, e.g. \"ml engineer\" (required)")
	country := fs.String("country", "", "two-letter country code(s), e.g. US or US,CA")
	workMode := fs.String("work-mode", "", "remote, hybrid or onsite")
	seniority := fs.String("seniority", "", "intern, junior, middle, senior, lead, staff, principal or c_level")
	pages := fs.Int("pages", freehire.DefaultPages, fmt.Sprintf("pages of %d results to read, 1..%d", freehire.PageSize, freehire.MaxPages))
	asJSON := fs.Bool("json", false, "a JSON document on stdout")
	allow := fs.Bool("allow-host", false, "add "+freehire.Host+" to allowed_hosts first, when it is not there")
	if err := parse(fs, args); err != nil {
		return err
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
	if !egress.AllowsHost(cfg.AllowedHosts, freehire.Host) {
		if !*allow {
			return fmt.Errorf("%s is not in allowed_hosts, so watchlist discover cannot ask it for boards "+
				"(a third-party site: keyless personal use, one request a search, at most %d pages); "+
				"re-run with --allow-host to add it", freehire.Host, freehire.MaxPages)
		}
		if err := e.updateConfig(func(c *config.Config) error {
			c.AllowedHosts = allowHost(c.AllowedHosts, freehire.Host)
			return nil
		}); err != nil {
			return err
		}
		cfg.AllowedHosts = allowHost(cfg.AllowedHosts, freehire.Host)
		fmt.Fprintf(e.stderr, "added %s to allowed_hosts\n", freehire.Host)
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
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(doc)
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

// addCommand is the `vani watchlist add` that adds b, for the owner to read
// and run. The company name is text from a third-party site, so every value is
// shell-quoted. --allow-host is there only when a host the board needs is not
// in allowed already, so the command widens egress by what the board needs and
// no more.
func addCommand(b sources.Board, allowed []string) string {
	parts := []string{"vani watchlist add --ats", string(b.ATS), "--token", shellQuote(b.Token), "--company", shellQuote(b.Company)}
	for _, f := range []struct{ flag, v string }{
		{"--board-name", b.BoardName}, {"--region", b.Region}, {"--host", b.Host}, {"--search", b.Search},
	} {
		if f.v != "" {
			parts = append(parts, f.flag, shellQuote(f.v))
		}
	}
	if slices.ContainsFunc(config.BoardHosts(b), func(h string) bool { return !egress.AllowsHost(allowed, h) }) {
		parts = append(parts, "--allow-host")
	}
	return strings.Join(parts, " ")
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
