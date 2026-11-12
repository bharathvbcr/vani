package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/discover/freehire"
	"github.com/bharathvbcr/Vani/internal/discover/searchplan"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// maxDiscoverAdd bounds `watchlist discover --profile --add N`.
	maxDiscoverAdd = 50
	// profileDiscoverTimeout bounds one `watchlist discover --profile`: at most
	// searchplan.MaxSearches searches of freehire.MaxPages pages, each spaced
	// freehire.PageGap apart and bounded by the client's timeout and retry
	// budget.
	profileDiscoverTimeout = 12 * time.Minute
	// profileTextBoards is the most boards the text form lists; --json lists all.
	profileTextBoards = 40
	// maxSearchErrorRunes bounds one search's error in the output.
	maxSearchErrorRunes = 300
)

// profileStacks is the stack text of each entry of the eligible knowledge
// base (eligibleBank, Restrict), in entry id order: the only part of the bank
// profile discovery reads. Tests replace it.
var profileStacks = func(cfg config.Config) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), eligibleKBTimeout)
	defer cancel()
	b, _, el, err := eligibleBank(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r := el.Restrict(b)
	ids := make([]string, 0, len(r.Entries))
	for id := range r.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stacks := make([]string, 0, len(ids))
	for _, id := range ids {
		stacks = append(stacks, r.Entries[id].Stack)
	}
	return stacks, nil
}

type profileDiscoverOptions struct {
	filters     freehire.Query // Country, WorkMode, Seniority; Search unused
	pages       int
	asJSON      bool
	allowHost   bool
	maxSearches int
	maxSkills   int
	// maxSkillsSet is whether --max-skills was given: the default is
	// clamped to --max-searches, a value given above it is refused.
	maxSkillsSet bool
	noKB         bool
	dryRun       bool
	add          int
	allowBoards  bool
}

// profileDiscoverJSON is `watchlist discover --profile --json`.
type profileDiscoverJSON struct {
	Filters  discoverSearchJSON   `json:"filters"`
	Searches []profileSearchJSON  `json:"searches"`
	Skipped  []searchplan.Skipped `json:"skipped"`
	// SkillsBelowFloor counts skills fewer than MinEntries entries list; they
	// are never named, here or in a request.
	SkillsBelowFloor int  `json:"skills_below_floor"`
	MinEntries       int  `json:"min_entries"`
	StacksUnread     int  `json:"stacks_unread"`
	KBUsed           bool `json:"kb_used"`
	// KBError is why the knowledge base was not read, when it was asked for.
	KBError string `json:"kb_error,omitempty"`
	DryRun  bool   `json:"dry_run"`
	// Stopped is why later searches were not asked, when one ended the run.
	Stopped    string              `json:"stopped,omitempty"`
	Meta       profileMetaJSON     `json:"meta"`
	Boards     []profileBoardJSON  `json:"boards"`
	Unreadable []unreadableJSON    `json:"unreadable"`
	Added      []watchlistJSON     `json:"added"`
	NotAdded   []profileNotAddedJS `json:"not_added"`
}

type profileSearchJSON struct {
	searchplan.Search
	// Read is true when freehire answered the search.
	Read    bool `json:"read"`
	Total   int  `json:"total"`
	Fetched int  `json:"fetched"`
	// Kept is the results whose title passed the owner's title rules.
	Kept   int    `json:"kept"`
	Capped bool   `json:"capped"`
	Error  string `json:"error,omitempty"`
}

type profileMetaJSON struct {
	// Kept and OffProfile split the results read: an off-profile result's
	// title failed the owner's title rules (title_signals, exclude, drop
	// rules read from the title) and counts toward no board.
	Kept       int `json:"kept"`
	OffProfile int `json:"off_profile"`
	// Distinct is the kept results once those several searches found are
	// counted once; Repeated is how many such repeats there were.
	Distinct int `json:"distinct"`
	Repeated int `json:"repeated"`
	// Weak is the boards that are not corroborated: one search found them
	// and only that search's own word matched their titles.
	Weak int `json:"weak"`
}

type profileBoardJSON struct {
	ATS       string   `json:"ats"`
	Token     string   `json:"token"`
	Company   string   `json:"company"`
	Host      string   `json:"host,omitempty"`
	BoardName string   `json:"board_name,omitempty"`
	Region    string   `json:"region,omitempty"`
	Search    string   `json:"search,omitempty"`
	Searches  []string `json:"searches,omitempty"`
	Postings  int      `json:"postings"`
	// FoundBy is the profile searches that found an on-profile posting here.
	FoundBy []string `json:"found_by"`
	Titles  []string `json:"sample_titles"`
	// Evidence is the most role words beyond its own search word any kept
	// title on the board matched (titleEvidence).
	Evidence int `json:"evidence"`
	// Corroborated is true when two or more searches found the board, or a
	// kept title matched a role word its search did not supply. Only a
	// corroborated board is added by --add.
	Corroborated bool `json:"corroborated"`
	Watched      bool `json:"watched"`
	Added        bool `json:"added"`
	// AddCommand is the `vani watchlist add` that adds the board; empty for
	// one on the watchlist.
	AddCommand string `json:"add_command,omitempty"`
}

type profileNotAddedJS struct {
	ATS   string `json:"ats"`
	Token string `json:"token"`
	Why   string `json:"why"`
}

// errNothingAdded aborts the config save when --add found nothing to add.
var errNothingAdded = errors.New("nothing to add")

// discoverProfile is `watchlist discover --profile`: it plans searches from
// the owner's role words and knowledge-base skills (internal/discover/searchplan),
// asks freehire.me each, keeps the results whose titles pass the owner's own
// title rules, and ranks the boards they are on by how many searches reached
// them. It edits the watchlist only with --add.
func (e *env) discoverProfile(o profileDiscoverOptions) error {
	if !o.maxSkillsSet && o.maxSearches >= 1 {
		o.maxSkills = min(o.maxSkills, o.maxSearches)
	}
	switch {
	case o.pages < 1 || o.pages > freehire.MaxPages:
		return usagef("watchlist discover: --pages %d must be within 1..%d", o.pages, freehire.MaxPages)
	case o.maxSearches < 1 || o.maxSearches > searchplan.MaxSearches:
		return usagef("watchlist discover: --max-searches %d must be within 1..%d", o.maxSearches, searchplan.MaxSearches)
	case o.maxSkills < 0 || (o.maxSkillsSet && o.maxSkills > o.maxSearches):
		return usagef("watchlist discover: --max-skills %d must be within 0..--max-searches", o.maxSkills)
	case o.add < 0 || o.add > maxDiscoverAdd:
		return usagef("watchlist discover: --add %d must be within 1..%d", o.add, maxDiscoverAdd)
	case o.add > 0 && o.dryRun:
		return usagef("watchlist discover: --dry-run asks nothing, so it has nothing to --add")
	case o.allowBoards && o.add == 0:
		return usagef("watchlist discover: --allow-board-hosts needs --add")
	}
	probe := o.filters
	probe.Search = "probe"
	if err := probe.Check(); err != nil {
		return usagef("watchlist discover: %v", err)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}

	doc := profileDiscoverJSON{
		Filters: discoverSearchJSON{Country: o.filters.Country, WorkMode: o.filters.WorkMode, Seniority: o.filters.Seniority, Pages: o.pages},
		DryRun:  o.dryRun, Skipped: []searchplan.Skipped{}, Boards: []profileBoardJSON{}, Unreadable: []unreadableJSON{},
		Added: []watchlistJSON{}, NotAdded: []profileNotAddedJS{},
	}
	var stacks []string
	if !o.noKB && o.maxSkills > 0 {
		s, err := profileStacks(cfg)
		if err != nil {
			doc.KBError = oneLine(err.Error(), maxSearchErrorRunes)
			fmt.Fprintf(e.stderr, "knowledge base not read, so no skill searches: %s\n", doc.KBError)
		} else {
			stacks, doc.KBUsed = s, true
		}
	}
	skills := o.maxSkills
	if skills == 0 {
		skills = -1 // searchplan reads zero as its default
	}
	plan := searchplan.Build(searchplan.Input{Roles: cfg.TitleSignals.Include, Stacks: stacks, MaxSearches: o.maxSearches, MaxSkills: skills})
	doc.Skipped = append(doc.Skipped, plan.Skipped...)
	doc.SkillsBelowFloor, doc.MinEntries, doc.StacksUnread = plan.SkillsBelowFloor, plan.MinEntries, plan.StacksUnread
	for _, s := range plan.Searches {
		doc.Searches = append(doc.Searches, profileSearchJSON{Search: s})
	}
	if len(plan.Searches) == 0 {
		why := "title_signals.include has no usable role word"
		switch {
		case doc.KBError != "":
			why += " and the knowledge base was not read (" + doc.KBError + ")"
		case doc.KBUsed:
			why += fmt.Sprintf(" and no skill is listed by %d or more knowledge-base entries", plan.MinEntries)
		}
		return fmt.Errorf("watchlist discover --profile: no searches to make: %s", why)
	}
	if o.dryRun {
		return e.writeProfileDiscover(o.asJSON, doc)
	}

	if err := e.ensureFreehireHost(&cfg, o.allowHost); err != nil {
		return err
	}
	doer, err := newDiscoverDoer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), profileDiscoverTimeout)
	defer cancel()
	client := freehire.Client{Doer: doer, Pages: o.pages, Gap: freehire.PageGap, Sleep: discoverSleep}
	sleep := discoverSleep
	if sleep == nil {
		sleep = srcutil.Sleep
	}
	keep := profileKeep(cfg)
	var hits []freehire.Hit
	firstErr := ""
	for i := range doc.Searches {
		row := &doc.Searches[i]
		if doc.Stopped != "" {
			continue
		}
		if i > 0 {
			if err := sleep(ctx, freehire.PageGap); err != nil {
				doc.Stopped = "the run's time limit ended before search " + quoteSearch(row.Text) + " was asked"
				continue
			}
		}
		q := o.filters
		q.Search = row.Text
		res, err := client.Search(ctx, q)
		switch {
		case errors.Is(err, freehire.ErrNotFound):
			return err
		case errors.Is(err, srcutil.ErrRateLimited):
			row.Error = "rate limited (HTTP 429)"
			doc.Stopped = fmt.Sprintf("%s rate limited search %s; later searches were not asked", freehire.Host, quoteSearch(row.Text))
		case err != nil && ctx.Err() != nil:
			row.Error = oneLine(err.Error(), maxSearchErrorRunes)
			doc.Stopped = "the run's time limit ended during search " + quoteSearch(row.Text)
		case err != nil:
			row.Error = oneLine(err.Error(), maxSearchErrorRunes)
		}
		if err != nil {
			if firstErr == "" {
				firstErr = quoteSearch(row.Text) + ": " + row.Error
			}
			continue
		}
		row.Read, row.Total, row.Fetched, row.Capped = true, res.Total, res.Fetched, res.Capped
		if res.Stopped != "" {
			row.Error = oneLine(res.Stopped, maxSearchErrorRunes)
		}
		hit := freehire.Hit{Search: row.Text}
		for _, j := range res.Jobs {
			if keep(j) {
				hit.Jobs = append(hit.Jobs, j)
			} else {
				doc.Meta.OffProfile++
			}
		}
		row.Kept = len(hit.Jobs)
		doc.Meta.Kept += row.Kept
		hits = append(hits, hit)
	}
	if len(hits) == 0 {
		return fmt.Errorf("watchlist discover --profile: no search could be read (%s)", firstErr)
	}

	merged := freehire.Merge(hits, titleEvidence(cfg.TitleSignals))
	doc.Meta.Distinct, doc.Meta.Repeated = merged.Distinct, merged.Repeated
	for _, u := range merged.Unreadable {
		doc.Unreadable = append(doc.Unreadable, unreadableJSON{Source: u.Source, Reason: u.Reason, Count: u.Count})
	}
	added := map[string]bool{}
	if o.add > 0 {
		var addedBoards []sources.Board
		var notAdded []profileNotAddedJS
		err := e.updateConfig(func(c *config.Config) error {
			addedBoards, notAdded = nil, nil
			for _, mb := range merged.Boards {
				// Boards are ranked corroborated first, so the first weak
				// board ends the walk: past it only the search word matched.
				if len(addedBoards) == o.add || !corroborated(mb) {
					break
				}
				b := mb.Board
				if slices.ContainsFunc(c.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) }) {
					continue
				}
				if !o.allowBoards {
					if h := missingHosts(b, c.AllowedHosts); len(h) > 0 {
						notAdded = append(notAdded, profileNotAddedJS{ATS: string(b.ATS), Token: b.Token,
							Why: "needs " + strings.Join(h, ", ") + " in allowed_hosts (--allow-board-hosts adds it)"})
						continue
					}
				}
				if err := addBoard(c, b, o.allowBoards); err != nil {
					notAdded = append(notAdded, profileNotAddedJS{ATS: string(b.ATS), Token: b.Token, Why: oneLine(err.Error(), maxSearchErrorRunes)})
					continue
				}
				addedBoards = append(addedBoards, b)
			}
			if len(addedBoards) == 0 {
				return errNothingAdded
			}
			return nil
		})
		if err != nil && !errors.Is(err, errNothingAdded) {
			return err
		}
		if err == nil {
			for _, b := range addedBoards {
				doc.Added = append(doc.Added, boardJSON(b))
				added[string(b.ATS)+":"+strings.ToLower(b.Token)] = true
			}
			cfg, err = e.loadConfig()
			if err != nil {
				return err
			}
		}
		doc.NotAdded = append(doc.NotAdded, notAdded...)
	}
	for _, mb := range merged.Boards {
		b := mb.Board
		out := profileBoardJSON{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Host: b.Host, BoardName: b.BoardName,
			Region: b.Region, Search: b.Search, Searches: b.Searches, Postings: mb.Postings, FoundBy: mb.Searches, Titles: mb.Titles,
			Evidence: mb.Evidence, Corroborated: corroborated(mb)}
		if !out.Corroborated {
			doc.Meta.Weak++
		}
		if out.Titles == nil {
			out.Titles = []string{}
		}
		out.Added = added[string(b.ATS)+":"+strings.ToLower(b.Token)]
		out.Watched = !out.Added && slices.ContainsFunc(cfg.Watchlist, func(x sources.Board) bool { return sameBoard(x, b) })
		if !out.Watched && !out.Added {
			out.AddCommand = addCommand(b, cfg.AllowedHosts)
		}
		doc.Boards = append(doc.Boards, out)
	}
	return e.writeProfileDiscover(o.asJSON, doc)
}

// profileKeep is whether a result counts toward its board: its title passes
// the owner's own title rules. With title_signals.include set the title must
// match one of its words; a title_signals.exclude word, an exclusion (title,
// company, location as the run applies it to a listing) or a drop rule read
// from the title drops it. A result with no title passes only when include is
// empty; one with no location is kept by the location rules, as in a run.
func profileKeep(cfg config.Config) func(freehire.Job) bool {
	drops := dropPolicy(cfg, cfg.Decide.OpenRouterName()).ListedDrops()
	sig := cfg.TitleSignals
	return func(j freehire.Job) bool {
		r := sig.Rank(j.Title)
		if r < 0 || (len(sig.Include) > 0 && r == 0) {
			return false
		}
		return drops == nil || !drops(posting.Posting{Title: j.Title, Company: j.Company, Location: j.Location})
	}
}

// titleEvidence scores a kept result under the search that found it: the
// distinct title_signals.include words its title matches, less the search's
// own word when the search is one of them. A role search puts its own word in
// every title it finds, so that match says nothing; "Agent d'accueil" found by
// "Agent" scores 0, "LLM Inference Engineer" found by "Inference" scores 1. A
// skill search ("Rust") supplies no title word, so every include word counts.
func titleEvidence(sig config.TitleSignals) func(search string, j freehire.Job) int {
	return func(search string, j freehire.Job) int {
		n := sig.Rank(j.Title)
		if n <= 0 {
			return 0
		}
		key := strings.ToLower(strings.Join(strings.Fields(search), " "))
		for _, w := range sig.Include {
			if strings.ToLower(strings.Join(strings.Fields(w), " ")) == key {
				if (config.TitleSignals{Include: []string{w}}).Rank(j.Title) == 1 {
					return n - 1
				}
				break
			}
		}
		return n
	}
}

// corroborated is whether a board has evidence its search words did not
// supply: two or more searches found it, or a title matched a role word
// beyond its own search's.
func corroborated(mb freehire.MergedBoard) bool { return len(mb.Searches) >= 2 || mb.Evidence >= 1 }

// ensureFreehireHost makes sure freehire.me is in allowed_hosts, adding it
// only with allow; cfg is updated to match what was saved.
func (e *env) ensureFreehireHost(cfg *config.Config, allow bool) error {
	if egress.AllowsHost(cfg.AllowedHosts, freehire.Host) {
		return nil
	}
	if !allow {
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
	return nil
}

// missingHosts is the hosts board b needs that allowed does not hold.
func missingHosts(b sources.Board, allowed []string) []string {
	var out []string
	for _, h := range config.BoardHosts(b) {
		if !egress.AllowsHost(allowed, h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func quoteSearch(s string) string { return fmt.Sprintf("%q", s) }

func (e *env) writeProfileDiscover(asJSON bool, doc profileDiscoverJSON) error {
	if asJSON {
		return writeJSON(e.stdout, doc)
	}
	e.printProfileDiscover(doc)
	return nil
}

// printProfileDiscover is the text form: the searches first (what was sent),
// then what they found.
func (e *env) printProfileDiscover(doc profileDiscoverJSON) {
	w := e.stdout
	fmt.Fprintf(w, "profile searches (%d), sent to %s one at a time:\n", len(doc.Searches), freehire.Host)
	for _, s := range doc.Searches {
		line := fmt.Sprintf("  %-8s %s", s.From, s.Text)
		if s.Entries > 0 {
			line += fmt.Sprintf("  (%d entries)", s.Entries)
		}
		if s.Read {
			line += fmt.Sprintf("  read %d of %d, %d on-profile", s.Fetched, s.Total, s.Kept)
		}
		if s.Error != "" {
			line += "  [" + s.Error + "]"
		}
		fmt.Fprintln(w, line)
	}
	for _, s := range doc.Skipped {
		fmt.Fprintf(w, "  left out: %s %q: %s\n", s.From, s.Text, s.Why)
	}
	if doc.SkillsBelowFloor > 0 {
		fmt.Fprintf(w, "  %d skill(s) listed by fewer than %d knowledge-base entries were not searched (not named)\n", doc.SkillsBelowFloor, doc.MinEntries)
	}
	if doc.StacksUnread > 0 {
		fmt.Fprintf(w, "  %d knowledge-base stack(s) could not be read\n", doc.StacksUnread)
	}
	if doc.KBError != "" {
		fmt.Fprintf(w, "  knowledge base not read: %s\n", doc.KBError)
	}
	if doc.DryRun {
		fmt.Fprintln(w, "\n--dry-run: nothing was asked")
		return
	}
	m := doc.Meta
	fmt.Fprintf(w, "\n%d on-profile result(s), %d distinct across searches; %d off-profile title(s) left out by your title rules\n",
		m.Kept, m.Distinct, m.OffProfile)
	if m.Weak > 0 {
		fmt.Fprintf(w, "  %d board(s) are weak: one search found them and only its own word matched their titles; --add stops before them\n", m.Weak)
	}
	if doc.Stopped != "" {
		fmt.Fprintf(w, "  stopped early: %s\n", doc.Stopped)
	}
	fmt.Fprintf(w, "\nboards with on-profile postings (%d), most searches first; discovery only, Vani reads postings from the boards themselves:\n", len(doc.Boards))
	for i, b := range doc.Boards {
		if i == profileTextBoards {
			fmt.Fprintf(w, "  ... and %d more (--json lists every board)\n", len(doc.Boards)-i)
			break
		}
		state := "new"
		switch {
		case b.Added:
			state = "added"
		case b.Watched:
			state = "watched"
		}
		if !b.Corroborated {
			state += ", weak"
		}
		fmt.Fprintf(w, "%3d search(es) %4d  %s  %s  %s  [%s]\n", len(b.FoundBy), b.Postings, b.ATS, b.Token, b.Company, state)
		if len(b.Titles) > 0 {
			fmt.Fprintf(w, "      e.g. %s\n", strings.Join(b.Titles, "; "))
		}
		if b.AddCommand != "" {
			fmt.Fprintf(w, "      %s\n", b.AddCommand)
		}
	}
	if len(doc.Unreadable) > 0 {
		total := 0
		for _, u := range doc.Unreadable {
			total += u.Count
		}
		fmt.Fprintf(w, "\non-profile results Vani cannot add a board for (%d), by source (an adapter for the top ones would add boards):\n", total)
		for i, u := range doc.Unreadable {
			if i == 10 {
				fmt.Fprintf(w, "  ... and %d more groups (--json lists all)\n", len(doc.Unreadable)-i)
				break
			}
			fmt.Fprintf(w, "%4d  %s  %s\n", u.Count, u.Source, u.Reason)
		}
	}
	if len(doc.Added) > 0 {
		fmt.Fprintf(w, "\nadded %d board(s) to the watchlist; the next run reads them\n", len(doc.Added))
	}
	for _, n := range doc.NotAdded {
		fmt.Fprintf(w, "not added: %s/%s: %s\n", n.ATS, n.Token, n.Why)
	}
}
