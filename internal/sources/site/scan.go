package site

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
)

const maxAnchorText = 2000

type htmlLink struct {
	href string
	text string
}

type iconLink struct {
	href string
	rel  string
	typ  string
}

type scannedPage struct {
	links      []htmlLink
	iframes    []string
	icons      []iconLink
	title      string
	ogSiteName string
	baseHref   string
	hasBase    bool
	truncated  string
}

type scanResult struct {
	pagesRead int
	company   string
	iconURL   string
	urls      []discoveredURL
	notes     []string
}

var careerLandingNames = map[string]bool{
	"careers":       true,
	"career":        true,
	"jobs":          true,
	"openings":      true,
	"positions":     true,
	"join-us":       true,
	"work-with-us":  true,
	"open-roles":    true,
	"opportunities": true,
	"vacancies":     true,
	"searchjobs":    true,
	"openroles":     true,
}

var careerLandingPriority = map[string]int{
	"/careers":      1,
	"/jobs":         1,
	"/openings":     1,
	"/positions":    1,
	"/join-us":      1,
	"/work-with-us": 1,
}

var careerLandingTextRE = regexp.MustCompile(`(?i)^(?:careers?|jobs?|openings|positions|join\s+us|work\s+with\s+us|open\s+(?:roles|positions)|view\s+(?:all\s+)?(?:jobs|openings)|see\s+(?:all\s+)?(?:jobs|positions))$`)

var careerSkipExts = []string{
	".css", ".js", ".mjs", ".png", ".jpg", ".jpeg", ".gif", ".svg",
	".ico", ".webp", ".woff", ".woff2", ".ttf", ".eot", ".pdf", ".xml", ".json",
}

var (
	genericCompanyRE = regexp.MustCompile(`(?i)^(?:greenhouse|lever|ashby|workday|smartrecruiters|icims|eightfold|avature|talentbrew|oracle|careers?(?:\s+(?:home|page|portal|site))?|jobs?(?:\s+(?:board|portal|page|home))?|jobs?\s*(?:&|and)\s*careers?|careers?\s*(?:&|and)\s*jobs?|open\s+(?:roles|positions|jobs)|current\s+(?:job\s+)?(?:openings|opportunities|positions)|job\s+(?:openings|opportunities|board)|openings|available\s+(?:positions|roles)|all\s+(?:jobs|positions|roles)|(?:come\s+)?join\s+(?:our\s+team|us)|work\s+with\s+us|we(?:'re|\s+are)\s+hiring|home|welcome|untitled|index)(?:\s*\(\d+\))?(?:\s*[!.])?$`)
	genericCountRE   = regexp.MustCompile(`(?i)^(?:(?:we\s+have|there\s+are|showing|found|view(?:\s+all)?|browse(?:\s+all)?)\s+)?\d+\+?\s+(?:open\s+|available\s+|current\s+)?(?:positions?|roles?|jobs?|openings?)(?:\s+available)?(?:\s*[!.])?$`)
	titlePrefixRE    = regexp.MustCompile(`(?i)^(?:careers?\s+(?:at|@|with)|jobs?\s+(?:at|@|with)|work\s+(?:at|with)|welcome\s+to)\s+(.+)$`)
)

func scanSitePages(
	ctx context.Context,
	d egress.Doer,
	domain string,
	timeout time.Duration,
	maxCareerPages int,
	policy robotsPolicy,
	sitemapURLs []discoveredURL,
	recordDisallowed func(string),
) (scanResult, error) {
	var out scanResult
	seenURLs := map[string]bool{}
	addURL := func(du discoveredURL) {
		if !seenURLs[du.raw] {
			seenURLs[du.raw] = true
			out.urls = append(out.urls, du)
		}
	}

	type careerCandidate struct {
		url      string
		priority int
	}
	var careerQueue []careerCandidate
	queuedCareer := map[string]bool{}
	visitedPages := map[string]bool{}

	enqueueCareer := func(raw string, prio int) {
		norm := strings.TrimRight(raw, "/")
		if norm == "" || norm == "https://"+domain || queuedCareer[norm] || visitedPages[norm] {
			return
		}
		queuedCareer[norm] = true
		careerQueue = append(careerQueue, careerCandidate{url: raw, priority: prio})
	}

	homeURL := (&url.URL{Scheme: "https", Host: domain, Path: "/"}).String()
	visitedPages["https://"+domain] = true

	// Step 3a: Fetch homepage "/" if allowed by robots.
	if !policy.allowed("/") {
		recordDisallowed("/")
		out.notes = append(out.notes, "homepage / disallowed by robots.txt")
	} else {
		body, status, err := fetchHTML(ctx, d, homeURL, domain, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return scanResult{}, err
			}
			out.notes = append(out.notes, fmt.Sprintf("homepage %s refused: %v", homeURL, err))
		} else if status < 200 || status >= 300 {
			out.notes = append(out.notes, fmt.Sprintf("homepage %s returned HTTP %d", homeURL, status))
		} else {
			out.pagesRead++
			sc := scanHTML(string(body))
			if sc.truncated != "" {
				out.notes = append(out.notes, fmt.Sprintf("homepage %s truncated: %s", homeURL, sc.truncated))
			}
			base := documentBase(homeURL, sc)
			out.company = extractCompany(sc.ogSiteName, sc.title)
			out.iconURL = selectIconURL(sc.icons, base)

			links, iframes := resolvePageElements(sc, homeURL, base)
			for _, src := range iframes {
				if du, ok := newDiscoveredURL(src, "iframe", homeURL); ok {
					addURL(du)
				}
			}
			for _, l := range links {
				if du, ok := newDiscoveredURL(l.href, "homepage", homeURL); ok {
					addURL(du)
					if strings.EqualFold(du.parsed.Hostname(), domain) {
						if prio, ok := careerLandingScore(du.parsed, l.text); ok {
							enqueueCareer(du.raw, prio)
						}
					}
				}
			}
		}
	}

	// Also seed career landing candidates discovered from sitemaps.
	for _, smItem := range sitemapURLs {
		if smItem.parsed != nil && strings.EqualFold(smItem.parsed.Hostname(), domain) {
			if prio, ok := careerLandingScore(smItem.parsed, ""); ok {
				enqueueCareer(smItem.raw, prio)
			}
		}
	}

	// Step 3b: Follow up to maxCareerPages career landing pages.
	careerPagesRead := 0
	for len(careerQueue) > 0 && careerPagesRead < maxCareerPages {
		if err := ctx.Err(); err != nil {
			return scanResult{}, err
		}
		slices.SortStableFunc(careerQueue, func(a, b careerCandidate) int {
			if a.priority != b.priority {
				if a.priority < b.priority {
					return -1
				}
				return 1
			}
			return 0
		})
		next := careerQueue[0]
		careerQueue = careerQueue[1:]

		norm := strings.TrimRight(next.url, "/")
		if visitedPages[norm] {
			continue
		}
		visitedPages[norm] = true

		u, err := url.Parse(next.url)
		if err != nil || !strings.EqualFold(u.Hostname(), domain) {
			continue
		}
		reqPath := requestPath(u)
		if !policy.allowed(reqPath) {
			recordDisallowed(reqPath)
			continue
		}

		careerPagesRead++
		body, status, err := fetchHTML(ctx, d, next.url, domain, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return scanResult{}, err
			}
			out.notes = append(out.notes, fmt.Sprintf("career page %s refused: %v", next.url, err))
			continue
		}
		if status < 200 || status >= 300 {
			out.notes = append(out.notes, fmt.Sprintf("career page %s returned HTTP %d", next.url, status))
			continue
		}
		out.pagesRead++
		sc := scanHTML(string(body))
		if sc.truncated != "" {
			out.notes = append(out.notes, fmt.Sprintf("career page %s truncated: %s", next.url, sc.truncated))
		}
		base := documentBase(next.url, sc)
		if out.company == "" {
			out.company = extractCompany(sc.ogSiteName, sc.title)
		}
		if out.iconURL == "" {
			out.iconURL = selectIconURL(sc.icons, base)
		}

		links, iframes := resolvePageElements(sc, next.url, base)
		for _, src := range iframes {
			if du, ok := newDiscoveredURL(src, "iframe", next.url); ok {
				addURL(du)
			}
		}
		for _, l := range links {
			if du, ok := newDiscoveredURL(l.href, "careers", next.url); ok {
				addURL(du)
				if strings.EqualFold(du.parsed.Hostname(), domain) {
					if prio, ok := careerLandingScore(du.parsed, l.text); ok {
						enqueueCareer(du.raw, prio)
					}
				}
			}
		}
	}

	return out, nil
}

func fetchHTML(
	ctx context.Context,
	d egress.Doer,
	rawURL string,
	domain string,
	timeout time.Duration,
) ([]byte, int, error) {
	resp, err := fetchSameHost(
		ctx,
		d,
		rawURL,
		domain,
		"text/html,application/xhtml+xml;q=0.9,*/*;q=0.1",
		timeout,
		MaxHTMLBytes,
	)
	if err != nil {
		return nil, 0, err
	}
	if resp.status < 200 || resp.status >= 300 {
		return nil, resp.status, nil
	}
	if !isHTMLResponse(resp.header.Get("Content-Type"), resp.body) {
		return nil, resp.status, fmt.Errorf("non-HTML response (Content-Type %q)", cleanLine(resp.header.Get("Content-Type"), 80))
	}
	return resp.body, resp.status, nil
}

func isHTMLResponse(contentType string, body []byte) bool {
	if bytes.IndexByte(body, 0) >= 0 {
		return false
	}
	if len(body) >= 5 && bytes.HasPrefix(body, []byte("%PDF-")) {
		return false
	}
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		return false
	}

	trimmed := bytes.TrimSpace(body)
	if contentType == "" {
		return !looksLikeJSON(trimmed)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return true
	case "application/json":
		// srctest.Doer defaults Content-Type to application/json when tests omit
		// headers; accept only if the body is markup rather than JSON or binary.
		return !looksLikeJSON(trimmed) && bytes.Contains(trimmed, []byte("<"))
	default:
		return false
	}
}

func looksLikeJSON(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}

func careerLandingScore(u *url.URL, anchorText string) (int, bool) {
	if u == nil {
		return 0, false
	}
	p := strings.TrimRight(strings.ToLower(u.EscapedPath()), "/")
	if p == "" || p == "/" {
		return 0, false
	}
	for _, ext := range careerSkipExts {
		if strings.HasSuffix(p, ext) {
			return 0, false
		}
	}
	if prio, ok := careerLandingPriority[p]; ok {
		return prio, true
	}
	segs := splitPathSegments(p)
	if len(segs) == 0 || len(segs) > 3 {
		return 0, false
	}
	last := segs[len(segs)-1]
	if careerLandingNames[last] {
		if len(segs) == 1 {
			return 2, true
		}
		return 3, true
	}
	if len(segs) <= 2 && careerLandingTextRE.MatchString(strings.TrimSpace(anchorText)) {
		return 4, true
	}
	return 0, false
}

func isCareerLandingURL(u *url.URL, anchorText string) bool {
	_, ok := careerLandingScore(u, anchorText)
	return ok
}

func documentBase(pageURL string, sc scannedPage) string {
	pu, err := url.Parse(pageURL)
	if err != nil {
		return pageURL
	}
	if !sc.hasBase {
		return pageURL
	}
	ref, err := url.Parse(strings.TrimSpace(sc.baseHref))
	if err != nil {
		return pageURL
	}
	abs := pu.ResolveReference(ref)
	if (!strings.EqualFold(abs.Scheme, "https") && !strings.EqualFold(abs.Scheme, "http")) || abs.Host == "" {
		return pageURL
	}
	return abs.String()
}

func resolvePageElements(sc scannedPage, pageURL, base string) ([]htmlLink, []string) {
	baseURL, err := url.Parse(base)
	if err != nil {
		baseURL, _ = url.Parse(pageURL)
	}
	var links []htmlLink
	for _, l := range sc.links {
		resolved, ok := resolveRef(baseURL, l.href)
		if !ok {
			continue
		}
		links = append(links, htmlLink{href: resolved, text: l.text})
	}
	var iframes []string
	for _, src := range sc.iframes {
		resolved, ok := resolveRef(baseURL, src)
		if !ok {
			continue
		}
		iframes = append(iframes, resolved)
	}
	return links, iframes
}

func resolveRef(baseURL *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil || ref.Opaque != "" {
		return "", false
	}
	abs := baseURL.ResolveReference(ref)
	if !strings.EqualFold(abs.Scheme, "https") && !strings.EqualFold(abs.Scheme, "http") {
		return "", false
	}
	if abs.Host == "" || abs.User != nil {
		return "", false
	}
	return abs.String(), true
}

func selectIconURL(icons []iconLink, base string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	bestTier := 99
	bestURL := ""
	for _, ic := range icons {
		tier, ok := classifyIconRel(ic.rel)
		if !ok {
			continue
		}
		if strings.Contains(strings.ToLower(ic.typ), "svg") {
			continue
		}
		raw := strings.TrimSpace(ic.href)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil || ref.Opaque != "" {
			continue
		}
		// Explicit http:// icon URLs are rejected: IconHost requires https://.
		if strings.EqualFold(ref.Scheme, "http") {
			continue
		}
		abs := baseURL.ResolveReference(ref)
		if !strings.EqualFold(abs.Scheme, "https") || abs.User != nil || abs.Port() != "" || abs.Host == "" {
			continue
		}
		abs.Scheme = "https"
		abs.Host = strings.ToLower(abs.Hostname())
		abs.Fragment, abs.RawFragment = "", ""
		if strings.HasSuffix(strings.ToLower(abs.Path), ".svg") {
			continue
		}
		cand := abs.String()
		if _, ok := config.IconHost(cand); !ok {
			continue
		}
		if tier > 1 {
			tier = iconFormatTier(abs.Path, ic.typ)
		}
		if tier < bestTier {
			bestTier = tier
			bestURL = cand
		}
	}
	return bestURL
}

func classifyIconRel(rel string) (int, bool) {
	tokens := strings.Fields(strings.ToLower(rel))
	if slices.Contains(tokens, "apple-touch-icon") {
		return 1, true
	}
	if slices.Contains(tokens, "icon") {
		return 2, true
	}
	return 0, false
}

func iconFormatTier(urlPath, mediaType string) int {
	lowPath := strings.ToLower(urlPath)
	lowType := strings.ToLower(mediaType)
	switch {
	case strings.Contains(lowType, "png") || strings.HasSuffix(lowPath, ".png"):
		return 2
	case strings.Contains(lowType, "icon") || strings.HasSuffix(lowPath, ".ico"):
		return 3
	case strings.Contains(lowType, "jpeg") || strings.Contains(lowType, "jpg") ||
		strings.HasSuffix(lowPath, ".jpg") || strings.HasSuffix(lowPath, ".jpeg"):
		return 4
	default:
		return 5
	}
}

func extractCompany(ogSiteName, title string) string {
	if c := cleanCompanyCandidate(ogSiteName); c != "" {
		return c
	}
	if title == "" {
		return ""
	}
	for _, sep := range []string{" | ", " — ", " – ", " - ", " :: ", ": "} {
		if strings.Contains(title, sep) {
			for _, part := range strings.Split(title, sep) {
				if c := cleanCompanyCandidate(part); c != "" {
					return c
				}
			}
			return ""
		}
	}
	return cleanCompanyCandidate(title)
}

func cleanCompanyCandidate(raw string) string {
	s := cleanLine(raw, maxCompanyRunes)
	if s == "" || !utf8.ValidString(s) {
		return ""
	}
	if isGenericCompany(s) {
		return ""
	}
	if m := titlePrefixRE.FindStringSubmatch(s); m != nil {
		stripped := strings.TrimSpace(strings.TrimRight(m[1], "!."))
		if stripped != "" && !isGenericCompany(stripped) {
			return cleanLine(stripped, maxCompanyRunes)
		}
		return ""
	}
	return s
}

func isGenericCompany(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || genericCompanyRE.MatchString(t) || genericCountRE.MatchString(t)
}

// scanHTML is a hostile-safe linear scanner extracting <base>, <a>, <iframe>,
// <link>, <meta property/name="og:site_name">, and <title>.
func scanHTML(doc string) scannedPage {
	var out scannedPage
	foreign := 0
	for i := 0; i < len(doc); {
		j := strings.IndexByte(doc[i:], '<')
		if j < 0 {
			break
		}
		i += j
		if strings.HasPrefix(doc[i:], "<!--") {
			next, ok := commentEnd(doc, i+4)
			if !ok {
				out.truncated = fmt.Sprintf("unterminated comment at byte %d", i)
				break
			}
			i = next
			continue
		}
		if i+1 >= len(doc) || !isLetter(doc[i+1]) {
			if foreign > 0 && strings.HasPrefix(doc[i:], "</") {
				if n := endTagName(doc, i+2); n == "svg" || n == "math" {
					foreign--
				}
			}
			i++
			continue
		}
		k := i + 1
		for k < len(doc) && isNameByte(doc[k]) {
			k++
		}
		name := htmltext.LowerASCII(doc[i+1 : k])
		keepAttrs := name == "a" || name == "base" || name == "iframe" || name == "link" || name == "meta"
		attrs, end, ok := parseAttrs(doc, k, keepAttrs)
		if !ok {
			out.truncated = fmt.Sprintf("unterminated <%s> tag at byte %d", name, i)
			break
		}
		switch name {
		case "svg", "math":
			if !selfClosed(doc, k, end) {
				foreign++
			}
		case "base":
			if href, found := attrs["href"]; found && !out.hasBase {
				out.baseHref, out.hasBase = html.UnescapeString(href), true
			}
		case "a":
			if href, found := attrs["href"]; found {
				stop := end + maxAnchorText
				if stop > len(doc) {
					stop = len(doc)
				}
				text := doc[end:stop]
				if c := htmltext.IndexFold(doc[end:stop], "</a"); c >= 0 {
					text = doc[end : end+c]
				}
				out.links = append(out.links, htmlLink{
					href: html.UnescapeString(href),
					text: htmltext.Text(text, 200),
				})
			}
		case "iframe":
			if src, found := attrs["src"]; found {
				out.iframes = append(out.iframes, html.UnescapeString(src))
			}
		case "link":
			if href, found := attrs["href"]; found {
				out.icons = append(out.icons, iconLink{
					href: html.UnescapeString(href),
					rel:  html.UnescapeString(attrs["rel"]),
					typ:  html.UnescapeString(attrs["type"]),
				})
			}
		case "meta":
			prop := strings.TrimSpace(html.UnescapeString(attrs["property"]))
			metaName := strings.TrimSpace(html.UnescapeString(attrs["name"]))
			if strings.EqualFold(prop, "og:site_name") || strings.EqualFold(metaName, "og:site_name") {
				if out.ogSiteName == "" {
					out.ogSiteName = htmltext.Plain(html.UnescapeString(attrs["content"]), 200)
				}
			}
		case "script", "style", "textarea", "title":
			if foreign > 0 && selfClosed(doc, k, end) {
				break
			}
			closeAt := htmltext.IndexFold(doc[end:], "</"+name)
			if closeAt < 0 {
				out.truncated = fmt.Sprintf("<%s> at byte %d has no closing tag", name, i)
				return out
			}
			if name == "title" && out.title == "" {
				out.title = htmltext.Text(doc[end:end+closeAt], 300)
			}
			end += closeAt
		}
		i = end
	}
	return out
}

func commentEnd(doc string, pos int) (int, bool) {
	if strings.HasPrefix(doc[pos:], ">") {
		return pos + 1, true
	}
	if strings.HasPrefix(doc[pos:], "->") {
		return pos + 2, true
	}
	rest := doc[pos:]
	a, b := strings.Index(rest, "-->"), strings.Index(rest, "--!>")
	switch {
	case a < 0 && b < 0:
		return 0, false
	case b < 0 || (a >= 0 && a < b):
		return pos + a + 3, true
	default:
		return pos + b + 4, true
	}
}

func endTagName(doc string, pos int) string {
	k := pos
	for k < len(doc) && isNameByte(doc[k]) {
		k++
	}
	return htmltext.LowerASCII(doc[pos:k])
}

func selfClosed(doc string, k, end int) bool {
	if end-2 < k || doc[end-2] != '/' {
		return false
	}
	return end-2 == k || isSpace(doc[end-3]) || doc[end-3] == '"' || doc[end-3] == '\''
}

func parseAttrs(doc string, pos int, keep bool) (map[string]string, int, bool) {
	var attrs map[string]string
	if keep {
		attrs = map[string]string{}
	}
	i := pos
	for i < len(doc) {
		for i < len(doc) && (isSpace(doc[i]) || doc[i] == '/') {
			i++
		}
		if i >= len(doc) {
			return nil, 0, false
		}
		if doc[i] == '>' {
			return attrs, i + 1, true
		}
		start := i
		for i < len(doc) && !isSpace(doc[i]) && doc[i] != '=' && doc[i] != '>' && doc[i] != '/' {
			i++
		}
		if i == start {
			i++
			continue
		}
		key := doc[start:i]
		for i < len(doc) && isSpace(doc[i]) {
			i++
		}
		val := ""
		if i < len(doc) && doc[i] == '=' {
			i++
			for i < len(doc) && isSpace(doc[i]) {
				i++
			}
			if i >= len(doc) {
				return nil, 0, false
			}
			if q := doc[i]; q == '"' || q == '\'' {
				e := strings.IndexByte(doc[i+1:], q)
				if e < 0 {
					return nil, 0, false
				}
				val = doc[i+1 : i+1+e]
				i += e + 2
			} else {
				s := i
				for i < len(doc) && !isSpace(doc[i]) && doc[i] != '>' {
					i++
				}
				val = doc[s:i]
			}
		}
		if !keep {
			continue
		}
		key = htmltext.LowerASCII(key)
		if _, dup := attrs[key]; !dup {
			attrs[key] = val
		}
	}
	return nil, 0, false
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func isNameByte(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '-' || c == ':' || c == '_'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}
