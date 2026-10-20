package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// `vani jobs ingest` takes the jobs the extension scraped from a listing page
// (docs/EXTENSION_INGEST.md). It never writes the ledger while another process
// may own it: it validates, optionally saves the page as a watchlist board,
// spools the batch to <state dir>/inbox, and drains the spool itself only when
// the ledger lock is free. Otherwise the daemon's poll drains it.

// ingestInput is where `vani jobs ingest` reads its request. Tests replace it.
var ingestInput io.Reader = os.Stdin

// Values of the output's "search.status" and "analysis".
const (
	searchAdded        = "added"
	searchExists       = "exists"
	searchNotRequested = "not_requested"
	searchUnsupported  = "unsupported"
	searchInvalid      = "invalid"

	analysisDaemon  = "daemon"
	analysisNextRun = "next_run"
)

type ingestSearchJSON struct {
	Status  string      `json:"status"`
	ATS     posting.ATS `json:"ats,omitempty"`
	Token   string      `json:"token,omitempty"`
	Company string      `json:"company,omitempty"`
	URL     string      `json:"url,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

type ingestJSON struct {
	Queued     int               `json:"queued"`
	Duplicates int               `json:"duplicates"`
	Rejected   []inbox.Rejection `json:"rejected"`
	Search     ingestSearchJSON  `json:"search"`
	Analysis   string            `json:"analysis"`
	// Error is set on an exit-2 reply (the Swift host shows only "error" on a
	// non-zero exit); the other fields then say why.
	Error string `json:"error,omitempty"`
}

// scrapeToken is the token of a batch that is not a saved pg board's own page.
// config.ValidBoard refuses its suffix, so no board's absence pass can reach
// those records.
func scrapeToken(host string) string {
	return hostSlugN(host, inbox.MaxTokenLen-len(config.ScrapeSuffix)) + config.ScrapeSuffix
}

func (e *env) jobsIngest(args []string) error {
	fs := e.newFlags("jobs ingest")
	asJSON := fs.Bool("json", false, "request JSON on stdin, one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	out, err := e.ingest()
	if *asJSON {
		var v any = out
		if out == nil {
			v = struct {
				Error string `json:"error"`
			}{err.Error()}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetEscapeHTML(false)
		if werr := enc.Encode(v); werr != nil {
			return werr
		}
		if err != nil && !errors.As(err, new(usageError)) {
			return errAlreadyReported
		}
		return err
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "queued %d, %d duplicate, %d rejected; search %s; analysis: %s\n",
		out.Queued, out.Duplicates, len(out.Rejected), out.Search.Status, out.Analysis)
	return nil
}

// ingest does the work. A non-nil result is printed even beside an error, so a
// caller that was refused for having no valid job still sees every rejection.
func (e *env) ingest() (*ingestJSON, error) {
	data, err := io.ReadAll(io.LimitReader(ingestInput, inbox.MaxStdinBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jobs ingest: read stdin: %w", err)
	}
	req, err := inbox.ParseRequest(data)
	if err != nil {
		return nil, usagef("jobs ingest: %v", err)
	}
	jobs, dups, rejected := req.Validate()
	out := &ingestJSON{Queued: len(jobs), Duplicates: dups, Rejected: rejected, Search: ingestSearchJSON{Status: searchNotRequested}, Analysis: analysisNextRun}
	if out.Rejected == nil {
		out.Rejected = []inbox.Rejection{}
	}
	company := ingestCompany(req.Page, jobs)
	// A batch folds under a token no board can have, unless its own page is the
	// saved pg board: a board's full listing ages every record under its token
	// that it does not list, so other scrapes must never share it.
	token := scrapeToken(pageOrJobHost(req.Page, jobs))

	if req.SaveSearch {
		res, board, err := e.saveSearch(req.Page, jobs, company)
		if err != nil {
			return nil, err
		}
		out.Search = res
		if board.ATS == posting.Page && (res.Status == searchAdded || res.Status == searchExists) &&
			board.URL != "" && board.URL == scrubPageURL(req.Page.URL) {
			token = strings.ToLower(board.Token)
		}
	}
	if len(jobs) == 0 {
		if out.Search.Status != searchAdded {
			reason := "and no search was saved"
			if len(out.Rejected) > 0 {
				reason = fmt.Sprintf("job %d: %s", out.Rejected[0].Index, out.Rejected[0].Reason)
			}
			out.Error = "no job in the batch was valid: " + reason
			return out, usagef("jobs ingest: %s", out.Error)
		}
		return out, nil
	}

	b := inbox.Batch{Token: token, Company: company, Page: req.Page, SaveSearch: req.SaveSearch, Jobs: jobs}
	if _, err := inbox.Write(filepath.Join(e.stateDir, inbox.Dir), b); err != nil {
		return nil, fmt.Errorf("jobs ingest: %w", err)
	}
	// Drain now only if no one else owns the ledger. A held lock means a daemon
	// (or a run) is up, and its inbox poll or next run folds the batch.
	led, err := ledger.Open(filepath.Join(e.stateDir, "ledger"))
	switch {
	case errors.Is(err, ledger.ErrLocked):
		out.Analysis = analysisDaemon
		return out, nil
	case err != nil:
		return nil, fmt.Errorf("jobs ingest: the jobs are queued in the inbox, but the ledger could not be opened to fold them: %w", err)
	}
	defer led.Close()
	ctx, stop := signalContext()
	defer stop()
	res, err := drainInbox(ctx, led, e.stateDir, protectMarked(e.stateDir, func(f string, a ...any) { fmt.Fprintf(e.stderr, "vani: "+f+"\n", a...) }))
	if res.Report.Bad != nil {
		fmt.Fprintf(e.stderr, "vani: %d unreadable inbox file(s) moved to %s\n", len(res.Report.Bad), filepath.Join(e.stateDir, inbox.Dir, inbox.BadDir))
	}
	if err != nil {
		return nil, fmt.Errorf("jobs ingest: the jobs are queued in the inbox, but folding them failed: %w", err)
	}
	return out, nil
}

// saveSearch turns the listing page into a watchlist board and saves it. A
// page that cannot be a board is reported in the result and is not an error;
// only a config that cannot be read or written is.
func (e *env) saveSearch(page inbox.Page, jobs []inbox.Job, company string) (ingestSearchJSON, sources.Board, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return ingestSearchJSON{}, sources.Board{}, fmt.Errorf("jobs ingest: save search: %w", err)
	}
	board, res := deriveBoard(page.URL, jobs, company, cfg.Watchlist)
	if res.Status != "" {
		return res, sources.Board{}, nil
	}
	res = ingestSearchJSON{ATS: board.ATS, Token: board.Token, Company: board.Company, URL: scrubPageURL(page.URL)}
	// The user clicked "Save search", so the board's host joins allowed_hosts
	// (watchlist add --allow-host): without it config.Save refuses the board.
	switch err := addBoard(&cfg, board, true); {
	case errors.As(err, new(boardExistsError)):
		res.Status = searchExists
		return res, board, nil
	case errors.As(err, new(invalidBoardError)):
		return ingestSearchJSON{Status: searchInvalid, URL: res.URL, Reason: errors.Unwrap(err).Error()}, sources.Board{}, nil
	case err != nil:
		return ingestSearchJSON{}, sources.Board{}, err
	}
	if board.ATS == posting.Workday {
		// A board the adapter cannot list fails every daily run, so it is not saved.
		// The jobs are still queued: only the search is refused.
		if reason := workdayRefusal(board); reason != "" {
			return ingestSearchJSON{Status: searchInvalid, URL: res.URL, Reason: reason}, sources.Board{}, nil
		}
	}
	if err := config.Save(e.configPath, cfg); err != nil {
		return ingestSearchJSON{}, sources.Board{}, fmt.Errorf("jobs ingest: save search: %w", err)
	}
	res.Status = searchAdded
	return res, board, nil
}

// workdayCheckTimeout bounds the one size check a Workday save makes. The Swift
// host kills `vani jobs ingest` at 30 s (JobIngest.swift, ingestTimeout), so the
// check must end well before that for the CLI to still reply with its reason.
const workdayCheckTimeout = 10 * time.Second

// checkWorkdaySize asks the board's Workday host how big the search is. Tests
// replace it so no test reaches the network.
var checkWorkdaySize = func(ctx context.Context, b sources.Board) error {
	// The user clicked "Save search", so the board's host may be asked, the same
	// host addBoard puts in allowed_hosts.
	c, err := egress.New(egress.Config{AllowedHosts: []string{b.Host}})
	if err != nil {
		return err
	}
	return workday.CheckSize(ctx, c, b)
}

// workdayRefusal is why a Workday search cannot be saved, or "" when it can. A
// search over the adapter's posting cap is refused with the way out; a check
// that could not run is refused too, as unchecked, never as fine.
func workdayRefusal(b sources.Board) string {
	ctx, cancel := context.WithTimeout(context.Background(), workdayCheckTimeout)
	defer cancel()
	err := checkWorkdaySize(ctx, b)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, srcutil.ErrTooManyPostings):
		return fmt.Sprintf("this Workday search lists more postings than Vani can follow (%v); narrow it with search terms and save it again", err)
	}
	return fmt.Sprintf("could not check how many postings this Workday search lists, so it was not saved; try again: %v", err)
}

// aggregators are job sites that need a signed-in browser. Their subdomains too.
var aggregators = []string{"linkedin.com", "indeed.com", "glassdoor.com", "ziprecruiter.com", "wellfound.com", "dice.com", "monster.com", "simplyhired.com"}

func isAggregator(host string) bool {
	for _, a := range aggregators {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

var (
	workdayHostRE   = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)\.wd[0-9]{1,2}\.myworkdayjobs\.com$`)
	workdayLocaleRE = regexp.MustCompile(`^[a-z]{2}([-_][A-Za-z]{2,4})?$`)
)

// deriveBoard maps a listing page to the board that searches it. When the page
// cannot be a board it returns a result whose Status is set (unsupported or
// invalid); otherwise Status is "" and the caller adds the board. wl is the
// current watchlist, for a page board's unique token.
func deriveBoard(pageURL string, jobs []inbox.Job, company string, wl []sources.Board) (sources.Board, ingestSearchJSON) {
	scrubbed := scrubPageURL(pageURL)
	fail := func(status, format string, a ...any) (sources.Board, ingestSearchJSON) {
		return sources.Board{}, ingestSearchJSON{Status: status, URL: scrubbed, Reason: fmt.Sprintf(format, a...)}
	}
	u, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fail(searchInvalid, "the page URL is not an https URL")
	}
	host := strings.ToLower(u.Hostname())
	segs := pathSegments(u.Path)
	first := ""
	if len(segs) > 0 {
		first = segs[0]
	}
	switch {
	case isAggregator(host):
		return fail(searchUnsupported, "%s needs a signed-in browser; Vani cannot search it on its own", host)
	case host == "boards.greenhouse.io" || host == "job-boards.greenhouse.io":
		if first == "" {
			return fail(searchInvalid, "the page URL names no Greenhouse board")
		}
		return sources.Board{ATS: posting.Greenhouse, Token: first, Company: company}, ingestSearchJSON{}
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		if first == "" {
			return fail(searchInvalid, "the page URL names no Lever board")
		}
		b := sources.Board{ATS: posting.Lever, Token: first, Company: company}
		if host == "jobs.eu.lever.co" {
			b.Region = "eu"
		}
		return b, ingestSearchJSON{}
	case host == "jobs.ashbyhq.com":
		if first == "" {
			return fail(searchInvalid, "the page URL names no Ashby board")
		}
		return sources.Board{ATS: posting.Ashby, Token: first, Company: company}, ingestSearchJSON{}
	}
	if m := workdayHostRE.FindStringSubmatch(host); m != nil {
		if len(segs) > 0 && workdayLocaleRE.MatchString(segs[0]) {
			segs = segs[1:]
		}
		if len(segs) == 0 {
			return fail(searchInvalid, "the page URL names no Workday site")
		}
		return sources.Board{ATS: posting.Workday, Token: m[1] + "/" + segs[0], Company: company, Host: host,
			Search: strings.TrimSpace(u.Query().Get("q"))}, ingestSearchJSON{}
	}

	// Any other https page is a careers page, read by the pg adapter.
	for _, x := range wl {
		if x.ATS == posting.Page && x.URL == scrubbed {
			return x, ingestSearchJSON{}
		}
	}
	pattern, reason := linkPattern(jobs)
	if reason != "" {
		return fail(searchInvalid, "%s", reason)
	}
	token := hostSlug(host)
	if slicesContainsBoard(wl, posting.Page, token) {
		sum := sha256.Sum256([]byte(u.EscapedPath() + "?" + sortedQuery(scrubbed)))
		token = hostSlugN(host, inbox.MaxTokenLen-9) + "-" + hex.EncodeToString(sum[:4])
		if slicesContainsBoard(wl, posting.Page, token) {
			return fail(searchInvalid, "a different saved search already uses this page's name")
		}
	}
	return sources.Board{ATS: posting.Page, Token: token, Company: company, URL: scrubbed, LinkPattern: pattern}, ingestSearchJSON{}
}

func slicesContainsBoard(wl []sources.Board, ats posting.ATS, token string) bool {
	for _, x := range wl {
		if sameBoard(x, sources.Board{ATS: ats, Token: token}) {
			return true
		}
	}
	return false
}

func sortedQuery(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Query().Encode()
	}
	return ""
}

func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// linkPattern derives a page board's link_pattern from the scraped job URLs:
// ^https://{host}/{longest common leading directory path}. Every job URL must
// match it and there must be at least two, on one host. The prefix ends at a
// whole path segment, so /jobs/123 and /jobs/124 give /jobs/, never /jobs/12.
func linkPattern(jobs []inbox.Job) (pattern, reason string) {
	if len(jobs) < 2 {
		return "", "need at least two different job links to derive a link pattern"
	}
	var host string
	var common []string
	for i, j := range jobs {
		u, err := url.Parse(j.URL)
		if err != nil || u.Scheme != "https" {
			return "", "job links are not all https"
		}
		h := strings.ToLower(u.Host)
		dirs := pathSegments(u.Path)
		if len(dirs) > 0 {
			dirs = dirs[:len(dirs)-1] // the last segment is the job itself
		}
		if i == 0 {
			host, common = h, dirs
			continue
		}
		if h != host {
			return "", "job links span more than one host"
		}
		n := 0
		for n < len(common) && n < len(dirs) && common[n] == dirs[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return "", fmt.Sprintf("the job links share no path on %s; add the board with `vani watchlist add --ats pg` and a link pattern", host)
	}
	var sb strings.Builder
	sb.WriteString("^https://" + regexp.QuoteMeta(host) + "/")
	for _, s := range common {
		sb.WriteString(regexp.QuoteMeta(s) + "/")
	}
	pattern = sb.String()
	if len(pattern) > 256 {
		return "", "the job links' common path is too long for a link pattern"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", "the job links' common path is not a valid link pattern"
	}
	for _, j := range jobs {
		if !re.MatchString(j.URL) {
			return "", "a job link does not match the derived link pattern"
		}
	}
	return pattern, ""
}

// trackingParams are dropped from a saved listing URL. Search parameters
// (q, location, department) are the search and stay.
func trackingParam(k string) bool {
	k = strings.ToLower(k)
	switch k {
	case "gclid", "fbclid", "msclkid", "ref", "refid", "trk", "trackingid", "sid", "token":
		return true
	}
	return strings.HasPrefix(k, "utm_") || strings.HasPrefix(k, "session")
}

// scrubPageURL is the page URL without tracking parameters, fragment or
// userinfo, or "" when it does not parse.
func scrubPageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.Fragment, u.RawFragment, u.User = "", "", nil
	q := u.Query()
	for k := range q {
		if trackingParam(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func pageOrJobHost(page inbox.Page, jobs []inbox.Job) string {
	if u, err := url.Parse(strings.TrimSpace(page.URL)); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	for _, j := range jobs {
		if u, err := url.Parse(j.URL); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}

// hostSlug is a host as a board token: lower-case letters and digits, runs of
// anything else one "-".
func hostSlug(host string) string { return hostSlugN(host, inbox.MaxTokenLen) }

func hostSlugN(host string, limit int) string {
	var sb strings.Builder
	dash := true
	for _, r := range strings.ToLower(host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			dash = false
		} else if !dash {
			sb.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > limit {
		s = strings.Trim(s[:limit], "-")
	}
	if s == "" {
		return "page"
	}
	return s
}

// ingestCompany is the batch's fallback company: the majority company of the
// scraped jobs (the first to reach the top count wins a tie), else the page
// title, else the host.
func ingestCompany(page inbox.Page, jobs []inbox.Job) string {
	count := map[string]int{}
	var order []string
	for _, j := range jobs {
		if j.Company == "" {
			continue
		}
		k := strings.ToLower(j.Company)
		if count[k] == 0 {
			order = append(order, j.Company)
		}
		count[k]++
	}
	best, n := "", 0
	for _, c := range order {
		if count[strings.ToLower(c)] > n {
			best, n = c, count[strings.ToLower(c)]
		}
	}
	if best == "" {
		best = strings.TrimSpace(page.Title)
	}
	if best == "" {
		best = pageOrJobHost(page, jobs)
	}
	if len(best) > inbox.MaxCompany {
		best = best[:inbox.MaxCompany]
		for !utf8.ValidString(best) {
			best = best[:len(best)-1]
		}
	}
	return best
}

// drainResult is what one drainInbox did.
type drainResult struct {
	Report inbox.DrainReport
	// New and Duplicate sum the batches' fold summaries.
	New, Duplicate int
	// Live lists the postings the folded batches name that are Live now, in
	// fold order: the ones that still wait for triage.
	Live []posting.Key
}

// inboxDrainLimit is how many spooled files one drain takes.
const inboxDrainLimit = inbox.MaxDrainFiles

// drainInbox folds the spooled batches into led with ReconcilePartial (no
// absence pass). The caller owns led and must be its only writer meanwhile.
func drainInbox(ctx context.Context, led ledger.Ledger, stateDir string, protect func(posting.Key) bool) (drainResult, error) {
	var res drainResult
	rec := pipeline.NewReconciler(led)
	rec.Protect(protect)
	seen := map[posting.Key]bool{}
	var keys []posting.Key
	rep, err := inbox.Drain(ctx, filepath.Join(stateDir, inbox.Dir), inboxDrainLimit, func(b inbox.Batch) error {
		ps := b.Postings()
		board := sources.Board{ATS: posting.Page, Token: b.Token, Company: b.Company}
		sum, err := rec.ReconcilePartial(ctx, board, ps, now())
		if err != nil {
			return fmt.Errorf("fold batch: %w", err)
		}
		res.New += sum.New
		res.Duplicate += sum.Duplicate
		for _, p := range ps {
			if !seen[p.Key] {
				seen[p.Key] = true
				keys = append(keys, p.Key)
			}
		}
		return nil
	})
	res.Report = rep
	for _, k := range keys {
		if r, ok := led.Get(k); ok && r.Status == posting.Live {
			res.Live = append(res.Live, k)
		}
	}
	return res, err
}

// protectMarked says which postings the user marked, which dedupe must never
// demote to Duplicate. Only the marks are read, never the notes. If the marks
// cannot be read every posting counts as marked: a check that could not run
// must not let a mark be demoted.
func protectMarked(stateDir string, warnf func(string, ...any)) func(posting.Key) bool {
	m, err := marks.ReadMarks(marks.Dir(stateDir))
	if err != nil {
		warnf("marks unreadable; dedupe will not demote any existing posting: %v", err)
		return func(posting.Key) bool { return true }
	}
	return func(k posting.Key) bool { _, ok := m[k]; return ok }
}
