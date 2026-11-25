// Package site discovers candidate watchlist boards for a company domain from
// its robots.txt, sitemaps, and HTML landing pages without contacting external
// hosts or third-party archives.
package site

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/page"
)

// Exported bounds on site discovery requests, sizes, and outputs.
const (
	MaxRobotsBytes    = 512 << 10
	MaxSitemapBytes   = 8 << 20
	MaxHTMLBytes      = 2 << 20
	MaxSitemaps       = 5
	MaxSitemapEntries = 10_000
	MaxCareerPages    = 3
	MaxCandidates     = 50
	RequestTimeout    = 15 * time.Second
)

const (
	maxURLLen          = 2048
	maxCompanyRunes    = 120
	maxSampleURLs      = 3
	defaultEightfoldQ  = "engineer"
	slugMaxLen         = 63
	careerRootLinkCap  = 256
)

var (
	// hostRE matches a lower-case bare hostname of at least two labels (no IP
	// literal, port, scheme, path, or userinfo), matching config.hostname.
	hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

	// talentBrewJobPath matches TalentBrew job detail URLs on a site's own host,
	// mirroring internal/sources/talentbrew.jobPath.
	talentBrewJobPath = regexp.MustCompile(`^/job/([A-Za-z0-9%._~-]{1,1000})/([A-Za-z0-9%._~-]{1,1000})/([0-9]{1,10})/([0-9]{1,20})$`)

	errBodyTooLarge = errors.New("site: response body exceeds size limit")
)

// Options tunes bounded site discovery. Zero fields use the package's exported
// bounds; positive fields above those bounds are capped at them.
type Options struct {
	MaxSitemaps    int
	MaxCareerPages int
	RequestTimeout time.Duration
}

// Candidate is one validated board discovered for the domain.
type Candidate struct {
	Board         sources.Board
	Source        string
	SampleURLs    []string
	EvidenceCount int
	RobotsBlocked bool
}

// Result reports everything observed and discovered for a domain.
type Result struct {
	Domain           string
	Company          string
	IconURL          string
	RobotsFetched    bool
	RobotsDisallowed []string
	SitemapsRead     int
	PagesRead        int
	URLsExamined     int
	Capped           bool
	Candidates       []Candidate
	Notes            []string
}

type resolvedOptions struct {
	maxSitemaps    int
	maxCareerPages int
	requestTimeout time.Duration
}

type discoveredURL struct {
	raw        string
	parsed     *url.URL
	source     string // "sitemap", "homepage", "careers", "iframe"
	listingURL string // page URL where this link was found, if HTML
}

type fetchResponse struct {
	status int
	header http.Header
	body   []byte
}

// Discover inspects domain via d (fetching only URLs whose host equals domain),
// obeying robots.txt, reading sitemaps and HTML career landing pages, and
// resolving candidate watchlist boards validated with config.ValidBoard.
func Discover(
	ctx context.Context,
	d egress.Doer,
	domain string,
	opts Options,
) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("site: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if d == nil {
		return Result{}, errors.New("site: no egress doer configured")
	}
	if err := validateDomain(domain); err != nil {
		return Result{}, err
	}
	ro, err := resolveOptions(opts)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		Domain:           domain,
		RobotsDisallowed: []string{},
		Candidates:       []Candidate{},
		Notes:            []string{},
	}

	disallowedSeen := map[string]bool{}
	recordDisallowed := func(rawPath string) {
		if rawPath == "" || disallowedSeen[rawPath] {
			return
		}
		disallowedSeen[rawPath] = true
		res.RobotsDisallowed = append(res.RobotsDisallowed, rawPath)
	}

	// Step 1: robots.txt (fail-closed on 5xx, network error, timeout, or oversize).
	policy, notes, err := fetchRobots(ctx, d, domain, ro.requestTimeout)
	if err != nil {
		return Result{}, err
	}
	res.RobotsFetched = policy.fetched
	res.Notes = append(res.Notes, notes...)

	var allURLs []discoveredURL
	seenURLs := map[string]int{}
	addURL := func(item discoveredURL) {
		if item.raw == "" || item.parsed == nil {
			return
		}
		if idx, exists := seenURLs[item.raw]; exists {
			if allURLs[idx].listingURL == "" && item.listingURL != "" {
				allURLs[idx].listingURL = item.listingURL
			}
			return
		}
		seenURLs[item.raw] = len(allURLs)
		allURLs = append(allURLs, item)
	}

	// Step 2: Sitemaps.
	smRes, err := readSitemaps(
		ctx,
		d,
		domain,
		ro.requestTimeout,
		ro.maxSitemaps,
		policy,
		recordDisallowed,
	)
	if err != nil {
		return Result{}, err
	}
	res.SitemapsRead = smRes.sitemapsRead
	if smRes.capped {
		res.Capped = true
	}
	res.Notes = append(res.Notes, smRes.notes...)
	for _, u := range smRes.urls {
		addURL(u)
	}

	// Step 3: Homepage + up to MaxCareerPages career landing pages.
	scanRes, err := scanSitePages(
		ctx,
		d,
		domain,
		ro.requestTimeout,
		ro.maxCareerPages,
		policy,
		smRes.urls,
		recordDisallowed,
	)
	if err != nil {
		return Result{}, err
	}
	res.PagesRead = scanRes.pagesRead
	res.Company = scanRes.company
	res.IconURL = scanRes.iconURL
	res.Notes = append(res.Notes, scanRes.notes...)
	for _, u := range scanRes.urls {
		addURL(u)
	}

	res.URLsExamined = len(allURLs)

	// Step 4: Board resolution & pg synthesis.
	cands, capped, candNotes := resolveCandidates(domain, res.Company, res.IconURL, policy, allURLs)
	res.Candidates = cands
	if capped {
		res.Capped = true
	}
	res.Notes = append(res.Notes, candNotes...)

	return res, nil
}

func validateDomain(domain string) error {
	if domain == "" {
		return errors.New("site: domain is empty")
	}
	if len(domain) > 253 {
		return fmt.Errorf("site: domain %q exceeds 253 bytes", domain)
	}
	if strings.ContainsFunc(domain, unicode.IsControl) {
		return errors.New("site: domain contains control characters")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return fmt.Errorf("site: domain %q must be a hostname, not an IP address", domain)
	}
	if !hostRE.MatchString(domain) {
		return fmt.Errorf("site: domain %q must be a lower-case bare hostname with at least two labels", domain)
	}
	return nil
}

func resolveOptions(opts Options) (resolvedOptions, error) {
	if opts.MaxSitemaps < 0 || opts.MaxCareerPages < 0 || opts.RequestTimeout < 0 {
		return resolvedOptions{}, errors.New("site: options must not be negative")
	}
	ro := resolvedOptions{
		maxSitemaps:    opts.MaxSitemaps,
		maxCareerPages: opts.MaxCareerPages,
		requestTimeout: opts.RequestTimeout,
	}
	if ro.maxSitemaps == 0 {
		ro.maxSitemaps = MaxSitemaps
	} else if ro.maxSitemaps > MaxSitemaps {
		ro.maxSitemaps = MaxSitemaps
	}
	if ro.maxCareerPages == 0 {
		ro.maxCareerPages = MaxCareerPages
	} else if ro.maxCareerPages > MaxCareerPages {
		ro.maxCareerPages = MaxCareerPages
	}
	if ro.requestTimeout == 0 {
		ro.requestTimeout = RequestTimeout
	} else if ro.requestTimeout > RequestTimeout {
		ro.requestTimeout = RequestTimeout
	}
	return ro, nil
}

// fetchSameHost fetches rawURL through d under per-request timeout and maxBytes
// limit, refusing any request or redirect off domain and any body exceeding maxBytes.
func fetchSameHost(
	ctx context.Context,
	d egress.Doer,
	rawURL string,
	domain string,
	accept string,
	timeout time.Duration,
	maxBytes int64,
) (fetchResponse, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" ||
		u.User != nil || u.Port() != "" || !strings.EqualFold(u.Hostname(), domain) {
		return fetchResponse{}, fmt.Errorf("site: refused non-same-host https URL %q", rawURL)
	}

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchResponse{}, fmt.Errorf("site: build request for %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", page.UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := d.Do(rctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return fetchResponse{}, ctx.Err()
		}
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			return fetchResponse{}, fmt.Errorf("site: %s timed out after %s: %w", rawURL, timeout, context.DeadlineExceeded)
		}
		return fetchResponse{}, fmt.Errorf("site: get %s: %w", rawURL, err)
	}
	if resp == nil || resp.Body == nil {
		return fetchResponse{}, fmt.Errorf("site: get %s: empty response", rawURL)
	}
	defer resp.Body.Close()

	if resp.Request != nil && resp.Request.URL != nil && !strings.EqualFold(resp.Request.URL.Hostname(), domain) {
		return fetchResponse{}, fmt.Errorf("site: %s redirected off %s to %s", rawURL, domain, resp.Request.URL.Hostname())
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return fetchResponse{}, ctx.Err()
		}
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			return fetchResponse{}, fmt.Errorf("site: reading %s timed out after %s: %w", rawURL, timeout, context.DeadlineExceeded)
		}
		return fetchResponse{}, fmt.Errorf("site: read %s: %w", rawURL, err)
	}
	if int64(len(body)) > maxBytes {
		return fetchResponse{}, fmt.Errorf("%w (%d bytes limit) for %s", errBodyTooLarge, maxBytes, rawURL)
	}

	return fetchResponse{
		status: resp.StatusCode,
		header: resp.Header,
		body:   body,
	}, nil
}

func resolveCandidates(
	domain string,
	company string,
	iconURL string,
	policy robotsPolicy,
	urls []discoveredURL,
) ([]Candidate, bool, []string) {
	type agg struct {
		cand Candidate
		seen map[string]bool
	}
	byKey := map[string]*agg{}
	var order []string
	var notes []string

	companyName := strings.TrimSpace(company)
	if companyName == "" {
		companyName = domain
	}
	_, iconValid := config.IconHost(iconURL)

	attachBoard := func(b sources.Board) (sources.Board, bool) {
		b.Domain = domain
		if iconValid {
			b.Icon = iconURL
		}
		if strings.TrimSpace(b.Company) == "" {
			b.Company = companyName
		}
		if err := config.ValidBoard(b); err != nil {
			notes = append(notes, fmt.Sprintf("skipped invalid candidate %s/%s: %v", b.ATS, b.Token, err))
			return sources.Board{}, false
		}
		return b, true
	}

	for _, item := range urls {
		b, ok := matchStructuredBoard(item.parsed, domain)
		if !ok {
			continue
		}
		b, valid := attachBoard(b)
		if !valid {
			continue
		}
		key := string(b.ATS) + ":" + strings.ToLower(b.Token)
		entry := byKey[key]
		if entry == nil {
			entry = &agg{
				cand: Candidate{
					Board:      b,
					Source:     item.source,
					SampleURLs: []string{},
				},
				seen: map[string]bool{},
			}
			byKey[key] = entry
			order = append(order, key)
		}
		if !entry.seen[item.raw] {
			entry.seen[item.raw] = true
			entry.cand.EvidenceCount++
			if len(entry.cand.SampleURLs) < maxSampleURLs {
				entry.cand.SampleURLs = append(entry.cand.SampleURLs, item.raw)
			}
		}
	}

	// Only synthesize a pg board if no structured ATS board covers domain.
	if len(order) == 0 {
		if pgCand, ok := synthesizePageBoard(domain, policy, urls, attachBoard); ok {
			key := string(pgCand.Board.ATS) + ":" + strings.ToLower(pgCand.Board.Token)
			byKey[key] = &agg{cand: pgCand}
			order = append(order, key)
		}
	}

	cands := make([]Candidate, 0, len(order))
	for _, k := range order {
		cands = append(cands, byKey[k].cand)
	}
	slices.SortStableFunc(cands, func(a, b Candidate) int {
		if a.EvidenceCount != b.EvidenceCount {
			if a.EvidenceCount > b.EvidenceCount {
				return -1
			}
			return 1
		}
		if a.Board.ATS != b.Board.ATS {
			return strings.Compare(string(a.Board.ATS), string(b.Board.ATS))
		}
		return strings.Compare(strings.ToLower(a.Board.Token), strings.ToLower(b.Board.Token))
	})

	capped := false
	if len(cands) > MaxCandidates {
		cands = cands[:MaxCandidates]
		capped = true
		notes = append(notes, fmt.Sprintf("candidates capped at %d", MaxCandidates))
	}

	return cands, capped, notes
}

func matchStructuredBoard(u *url.URL, domain string) (sources.Board, bool) {
	if u == nil {
		return sources.Board{}, false
	}
	host := strings.ToLower(u.Hostname())
	segs := splitPathSegments(u.EscapedPath())

	// Check Greenhouse embed URLs BEFORE sources.BoardFromURL, because
	// BoardFromURL treats the first segment ("embed") as the board token.
	if host == "boards.greenhouse.io" || host == "job-boards.greenhouse.io" {
		if len(segs) > 0 && strings.EqualFold(segs[0], "embed") {
			if len(segs) >= 2 && (strings.EqualFold(segs[1], "job_board") || strings.EqualFold(segs[1], "job_app")) {
				tok := strings.TrimSpace(u.Query().Get("for"))
				if tok == "" {
					return sources.Board{}, false
				}
				return sources.Board{ATS: posting.Greenhouse, Token: tok}, true
			}
			return sources.Board{}, false
		}
	}

	if b, known, err := sources.BoardFromURL(u); known {
		if err != nil {
			return sources.Board{}, false
		}
		return b, true
	}

	// TalentBrew job URLs on domain: /job/{city}/{title}/{company_id}/{job_id}.
	if strings.EqualFold(host, domain) {
		if m := talentBrewJobPath.FindStringSubmatch(u.EscapedPath()); m != nil {
			if strings.Trim(m[1], ".") != "" && strings.Trim(m[2], ".") != "" {
				return sources.Board{
					ATS:   posting.TalentBrew,
					Token: hostSlug(domain),
					Host:  domain,
				}, true
			}
		}
	}

	// Avature listing URLs on domain (or *.avature.net): /{portal}/SearchJobs or /{portal}/OpenRoles.
	if b, ok := matchAvatureURL(u, host, domain); ok {
		return b, true
	}

	// Eightfold hosts: *.eightfold.ai or careers.* with /careers?domain=<d>.
	if b, ok := matchEightfoldURL(u, host, segs, domain); ok {
		return b, true
	}

	return sources.Board{}, false
}

func matchAvatureURL(u *url.URL, host, domain string) (sources.Board, bool) {
	isSameHost := strings.EqualFold(host, domain)
	isAvatureHost := strings.HasSuffix(host, ".avature.net")
	if !isSameHost && !isAvatureHost {
		return sources.Board{}, false
	}
	if isAvatureHost {
		label := strings.TrimSuffix(host, ".avature.net")
		if label == "" || label == "www" || label == "api" || label == "app" || strings.Contains(label, ".") {
			return sources.Board{}, false
		}
	}
	cleanTarget := "https://" + host + u.EscapedPath()
	lp, err := avature.ParseListURL(cleanTarget, host)
	if err != nil {
		return sources.Board{}, false
	}
	suffix, _, _ := strings.Cut(strings.TrimPrefix(lp.Path, "/"+lp.Portal+"/"), "/")
	if !strings.EqualFold(suffix, "SearchJobs") && !strings.EqualFold(suffix, "OpenRoles") {
		return sources.Board{}, false
	}
	tok := hostSlug(domain)
	if !isSameHost {
		tok = hostSlug(strings.TrimSuffix(host, ".avature.net"))
	}
	return sources.Board{
		ATS:   posting.Avature,
		Token: tok,
		Host:  host,
		URL:   "https://" + host + lp.Path,
	}, true
}

var reservedEightfoldSubdomains = map[string]bool{
	"www": true, "api": true, "app": true, "docs": true, "support": true,
}

func matchEightfoldURL(u *url.URL, host string, segs []string, domain string) (sources.Board, bool) {
	q := u.Query()
	queryDomain := strings.ToLower(strings.TrimSpace(q.Get("domain")))

	var token string
	switch {
	case strings.HasSuffix(host, ".eightfold.ai"):
		prefix := strings.TrimSuffix(host, ".eightfold.ai")
		if prefix == "" || reservedEightfoldSubdomains[prefix] || !hostRE.MatchString(host) {
			return sources.Board{}, false
		}
		if hostRE.MatchString(queryDomain) {
			token = queryDomain
		} else {
			token = domain
		}
	case strings.HasPrefix(host, "careers.") && len(segs) > 0 && strings.EqualFold(segs[0], "careers"):
		if !hostRE.MatchString(queryDomain) {
			return sources.Board{}, false
		}
		token = queryDomain
	default:
		return sources.Board{}, false
	}

	search := strings.TrimSpace(q.Get("query"))
	if search == "" {
		search = strings.TrimSpace(q.Get("q"))
	}
	if search == "" || len(search) > sources.MaxSearchLen || strings.ContainsFunc(search, unicode.IsControl) {
		search = defaultEightfoldQ
	}
	return sources.Board{
		ATS:    posting.Eightfold,
		Token:  token,
		Host:   host,
		Search: search,
	}, true
}

var pgJobRoots = map[string]bool{
	"jobs":          true,
	"careers":       true,
	"positions":     true,
	"openings":      true,
	"roles":         true,
	"opportunities": true,
	"vacancies":     true,
}

var pgAssetExts = []string{
	".css", ".js", ".mjs", ".png", ".jpg", ".jpeg", ".gif", ".svg",
	".ico", ".webp", ".woff", ".woff2", ".ttf", ".eot", ".pdf", ".xml", ".json",
}

func synthesizePageBoard(
	domain string,
	policy robotsPolicy,
	urls []discoveredURL,
	attachBoard func(sources.Board) (sources.Board, bool),
) (Candidate, bool) {
	type dirBucket struct {
		dirPrefix   string
		items       []discoveredURL
		dirSegs     [][]string
		listingURLs map[string]int
		source      string
	}
	type rootGroup struct {
		rootPrefix string
		dirs       map[string]*dirBucket
		dirOrder   []string
		allItems   []discoveredURL
		allSegs    [][]string
		listings   map[string]int
		source     string
	}
	roots := map[string]*rootGroup{}
	var rootOrder []string

	for _, item := range urls {
		if item.parsed == nil || !strings.EqualFold(item.parsed.Hostname(), domain) {
			continue
		}
		segs := splitPathSegments(item.parsed.EscapedPath())
		if len(segs) < 2 {
			continue
		}
		lastLower := strings.ToLower(segs[len(segs)-1])
		if isNonPostingTail(lastLower) {
			continue
		}
		rootIdx := -1
		for i := 0; i < len(segs)-1; i++ {
			if pgJobRoots[strings.ToLower(segs[i])] {
				rootIdx = i
				break
			}
		}
		if rootIdx < 0 {
			continue
		}
		rootPrefix := "/" + strings.Join(segs[:rootIdx+1], "/") + "/"
		dirPrefix := "/" + strings.Join(segs[:len(segs)-1], "/") + "/"

		rg := roots[rootPrefix]
		if rg == nil {
			rg = &rootGroup{
				rootPrefix: rootPrefix,
				dirs:       map[string]*dirBucket{},
				listings:   map[string]int{},
				source:     item.source,
			}
			roots[rootPrefix] = rg
			rootOrder = append(rootOrder, rootPrefix)
		}
		rg.allItems = append(rg.allItems, item)
		rg.allSegs = append(rg.allSegs, segs[:len(segs)-1])
		if item.listingURL != "" {
			rg.listings[item.listingURL]++
		}

		db := rg.dirs[dirPrefix]
		if db == nil {
			db = &dirBucket{
				dirPrefix:   dirPrefix,
				listingURLs: map[string]int{},
				source:      item.source,
			}
			rg.dirs[dirPrefix] = db
			rg.dirOrder = append(rg.dirOrder, dirPrefix)
		}
		db.items = append(db.items, item)
		db.dirSegs = append(db.dirSegs, segs[:len(segs)-1])
		if item.listingURL != "" {
			db.listingURLs[item.listingURL]++
		}
	}

	var bestCand Candidate
	bestCount := 0

	evaluate := func(items []discoveredURL, dirSegs [][]string, listingURLs map[string]int, source string) {
		if len(items) < 2 {
			return
		}
		common := commonDirPrefix(dirSegs)
		if len(common) == 0 {
			return
		}
		prefix := "/" + strings.Join(common, "/") + "/"
		pattern := "^https://" + regexp.QuoteMeta(domain) + regexp.QuoteMeta(prefix) + ".+"
		if len(pattern) > careerRootLinkCap {
			return
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return
		}

		matched := make([]discoveredURL, 0, len(items))
		for _, item := range items {
			if re.MatchString(item.raw) {
				matched = append(matched, item)
			}
		}
		if len(matched) < 2 {
			return
		}

		listingURL := pickPGListingURL(domain, prefix, listingURLs, urls)
		b, valid := attachBoard(sources.Board{
			ATS:         posting.Page,
			Token:       hostSlug(domain),
			URL:         listingURL,
			LinkPattern: pattern,
		})
		if !valid {
			return
		}

		samples := make([]string, 0, maxSampleURLs)
		for i := 0; i < len(matched) && len(samples) < maxSampleURLs; i++ {
			samples = append(samples, matched[i].raw)
		}

		robotsBlocked := false
		if lu, err := url.Parse(listingURL); err == nil {
			if !policy.allowed(requestPath(lu)) {
				robotsBlocked = true
			}
		}
		if !robotsBlocked {
			for _, m := range matched {
				if !policy.allowed(requestPath(m.parsed)) {
					robotsBlocked = true
					break
				}
			}
		}

		if len(matched) > bestCount {
			bestCount = len(matched)
			bestCand = Candidate{
				Board:         b,
				Source:        source,
				SampleURLs:    samples,
				EvidenceCount: len(matched),
				RobotsBlocked: robotsBlocked,
			}
		}
	}

	for _, rk := range rootOrder {
		rg := roots[rk]
		var qualified []*dirBucket
		for _, dk := range rg.dirOrder {
			if db := rg.dirs[dk]; len(db.items) >= 2 {
				qualified = append(qualified, db)
			}
		}
		switch len(qualified) {
		case 0:
			// No single immediate directory has >= 2 postings (e.g. /careers/eng/1
			// and /careers/pm/2); fall back to the shared root directory.
			evaluate(rg.allItems, rg.allSegs, rg.listings, rg.source)
		case 1:
			db := qualified[0]
			evaluate(db.items, db.dirSegs, db.listingURLs, db.source)
		default:
			var mergedItems []discoveredURL
			var mergedSegs [][]string
			mergedListings := map[string]int{}
			for _, db := range qualified {
				mergedItems = append(mergedItems, db.items...)
				mergedSegs = append(mergedSegs, db.dirSegs...)
				for k, v := range db.listingURLs {
					mergedListings[k] += v
				}
			}
			evaluate(mergedItems, mergedSegs, mergedListings, qualified[0].source)
		}
	}

	if bestCount < 2 {
		return Candidate{}, false
	}
	return bestCand, true
}

func isNonPostingTail(segLower string) bool {
	if careerLandingNames[segLower] {
		return true
	}
	for _, ext := range pgAssetExts {
		if strings.HasSuffix(segLower, ext) {
			return true
		}
	}
	return false
}

func commonDirPrefix(dirs [][]string) []string {
	if len(dirs) == 0 {
		return nil
	}
	common := slices.Clone(dirs[0])
	for _, d := range dirs[1:] {
		n := 0
		for n < len(common) && n < len(d) && common[n] == d[n] {
			n++
		}
		common = common[:n]
	}
	return common
}

func pickPGListingURL(
	domain string,
	prefix string,
	sourceCounts map[string]int,
	allURLs []discoveredURL,
) string {
	// Prefer the career landing page where these job links were actually found.
	bestPage := ""
	bestN := 0
	for pageURL, n := range sourceCounts {
		if pu, err := url.Parse(pageURL); err == nil && strings.EqualFold(pu.Hostname(), domain) {
			if pu.EscapedPath() != "/" && pu.EscapedPath() != "" && (n > bestN || (n == bestN && pageURL < bestPage)) {
				bestPage = pageURL
				bestN = n
			}
		}
	}
	if bestPage != "" {
		return bestPage
	}

	// Next prefer a discovered same-domain URL matching the directory root without trailing slash.
	rootNoSlash := "https://" + domain + strings.TrimRight(prefix, "/")
	for _, item := range allURLs {
		if strings.TrimRight(item.raw, "/") == rootNoSlash {
			return item.raw
		}
	}

	// Fall back to any discovered career landing page on domain, else prefix URL.
	for _, item := range allURLs {
		if item.parsed != nil && strings.EqualFold(item.parsed.Hostname(), domain) {
			if isCareerLandingURL(item.parsed, "") {
				return item.raw
			}
		}
	}
	return "https://" + domain + prefix
}

// hostSlug turns domain into a valid lower-case board token (<= 63 bytes,
// matching config.avatureToken and config.talentBrewToken).
func hostSlug(domain string) string {
	var sb strings.Builder
	dash := true
	for _, r := range strings.ToLower(domain) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
			dash = false
		case !dash:
			sb.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > slugMaxLen {
		s = strings.Trim(s[:slugMaxLen], "-")
	}
	if strings.HasSuffix(strings.ToLower(s), config.ScrapeSuffix) {
		s = strings.TrimSuffix(strings.ToLower(s), config.ScrapeSuffix) + "-site"
		s = strings.Trim(s, "-")
		if len(s) > slugMaxLen {
			s = strings.Trim(s[:slugMaxLen], "-")
		}
	}
	if s == "" {
		return "site"
	}
	return s
}

func splitPathSegments(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func requestPath(u *url.URL) string {
	if u == nil {
		return "/"
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		return p + "?" + u.RawQuery
	}
	return p
}

func cleanLine(s string, maxRunes int) string {
	s = htmltext.Plain(s, maxRunes*4)
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return strings.TrimSpace(s)
}
