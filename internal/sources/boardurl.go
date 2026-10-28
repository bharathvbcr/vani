package sources

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// smartRecruitersAppPaths are first path segments on SmartRecruiters' hosts that
// are not a company identifier (both seen in 2026-10-27 API and page links).
var smartRecruitersAppPaths = map[string]bool{"oneclick-ui": true, "external-referrals": true}

// tenantHostReserved are first labels of the per-tenant ATS domains that are
// the vendor's own site, app or API, not a company's board.
var tenantHostReserved = map[string]bool{"www": true, "api": true, "app": true}

// localeSegment is a leading language segment of a page path: "en", "en-US",
// "fr_CA". Workday and Oracle HCM put one before the site.
var localeSegment = regexp.MustCompile(`^[a-z]{2}([-_][A-Za-z]{2,4})?$`)

// ripplingLocale is the "xx-XX" locale Rippling's public site puts before the
// board (ats.rippling.com/en-GB/{board}/jobs/{id}).
var ripplingLocale = regexp.MustCompile(`^[a-z]{2}-[A-Za-z]{2}$`)

func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// BoardFromURL is the one rule for which board a posting or listing page of a
// supported ATS belongs to, read from its host and path alone (nothing is
// fetched). It serves `jobs ingest` (a saved search page) and `watchlist
// discover` (a posting URL from a discovery source).
//
// known is false when u's host is no ATS host with a board of its own (a
// company's careers page, an aggregator, an ATS Vani has no adapter for): not
// an error, the caller decides what such a page is. known with a non-nil err is
// an ATS host whose URL names no board (greenhouse.io with an empty path, a
// SmartRecruiters app page, a Workable "/j/{id}" shortlink); the error is the
// reason. Otherwise b is the board, without a Company. The query is read for
// the search term of the two ATSs whose pages carry one (Workday q, Oracle
// HCM keyword); no other query parameter is kept.
//
// u must be an https URL with a host; a caller that takes the URL from
// untrusted text checks that first.
func BoardFromURL(u *url.URL) (b Board, known bool, err error) {
	host := strings.ToLower(u.Hostname())
	segs := pathSegments(u.Path)
	first := ""
	if len(segs) > 0 {
		first = segs[0]
	}
	none := func(what string) (Board, bool, error) {
		return Board{}, true, fmt.Errorf("the page URL names no %s", what)
	}
	switch {
	case host == "boards.greenhouse.io" || host == "job-boards.greenhouse.io":
		if first == "" {
			return none("Greenhouse board")
		}
		return Board{ATS: posting.Greenhouse, Token: first}, true, nil
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co":
		if first == "" {
			return none("Lever board")
		}
		b := Board{ATS: posting.Lever, Token: first}
		if host == "jobs.eu.lever.co" {
			b.Region = "eu"
		}
		return b, true, nil
	case host == "jobs.gem.com":
		if first == "" {
			return none("Gem board")
		}
		return Board{ATS: posting.Gem, Token: first}, true, nil
	case host == "jobs.smartrecruiters.com" || host == "careers.smartrecruiters.com":
		// Both serve a company's page at /{companyIdentifier} (checked
		// 2026-10-27: jobs. redirects to careers.). The first segment of these two
		// is not a company: /oneclick-ui/... and /external-referrals/... are the
		// site's own apps.
		if first == "" || smartRecruitersAppPaths[strings.ToLower(first)] {
			return none("SmartRecruiters company")
		}
		return Board{ATS: posting.SmartRecruiters, Token: first}, true, nil
	case host == "jobs.ashbyhq.com":
		if first == "" {
			return none("Ashby board")
		}
		b := Board{ATS: posting.Ashby, Token: first}
		if strings.Contains(first, " ") {
			// A name with a space ("Hippocratic AI") cannot be a token, which
			// every posting key is built from: the token is its slug and the
			// name stays what the API is asked for. A name the rule refuses
			// (BoardNameProblem) keeps its token and is refused as before.
			if BoardNameProblem(first) == "" {
				b.Token, b.BoardName = strings.ToLower(strings.ReplaceAll(first, " ", "-")), first
			}
		}
		return b, true, nil
	case host == "apply.workable.com":
		// /{account}/... ; the shortlink /j/{shortcode} names the job only.
		if first == "" || first == "j" {
			return none("Workable account")
		}
		return Board{ATS: posting.Workable, Token: first}, true, nil
	case host == "ats.rippling.com":
		// /{board}/jobs/{id}, with an optional locale before the board; the bare
		// /jobs/{id} carries no board.
		if len(segs) > 0 && ripplingLocale.MatchString(segs[0]) {
			segs = segs[1:]
		}
		if len(segs) == 0 || segs[0] == "jobs" {
			return none("Rippling board")
		}
		return Board{ATS: posting.Rippling, Token: segs[0]}, true, nil
	case host == "lifeattiktok.com" || host == "www.lifeattiktok.com":
		// The site's job pages are /search/{id}; its other pages (blog,
		// locations) are not postings.
		if first != "search" {
			return none("TikTok job page")
		}
		return Board{ATS: posting.TikTok, Token: "tiktok"}, true, nil
	}
	if tenant, ok := WorkdayTenantHost(host); ok {
		if len(segs) > 0 && localeSegment.MatchString(segs[0]) {
			segs = segs[1:]
		}
		if len(segs) == 0 {
			return none("Workday site")
		}
		return Board{ATS: posting.Workday, Token: tenant + "/" + segs[0], Host: host,
			Search: strings.TrimSpace(u.Query().Get("q"))}, true, nil
	}
	if WorkdaySiteHost(host) {
		// The shared host's pages are /recruiting/{tenant}/{site}[/...]: the
		// host names no tenant, so the path does.
		if len(segs) > 0 && localeSegment.MatchString(segs[0]) {
			segs = segs[1:]
		}
		if len(segs) < 3 || segs[0] != "recruiting" {
			return none("Workday site")
		}
		return Board{ATS: posting.Workday, Token: segs[1] + "/" + segs[2], Host: host,
			Search: strings.TrimSpace(u.Query().Get("q"))}, true, nil
	}
	for ats, suffix := range TenantHostSuffix {
		// {token}.recruitee.com and the like: the host's first label is the
		// board, whatever the path.
		label, ok := strings.CutSuffix(host, suffix)
		if !ok {
			continue
		}
		if tenantHostReserved[label] {
			// The vendor's own site or API (api.recruitee.com, www.bamboohr.com).
			return Board{}, false, nil
		}
		if TenantLabelProblem(label) != "" {
			return Board{}, true, fmt.Errorf("the host %q names no %s board", host, ats)
		}
		return Board{ATS: ats, Token: label}, true, nil
	}
	if tenant, ok := OracleHCMHostTenant(host); ok {
		// The candidate-experience pages are /hcmUI/CandidateExperience/{lang}/sites/{site}
		// [/jobs | /job/{id}], and the search box's text is the "keyword" query
		// (checked in the live UI, 2026-10-27).
		if len(segs) < 5 || !strings.EqualFold(segs[0], "hcmUI") || !strings.EqualFold(segs[1], "CandidateExperience") ||
			!localeSegment.MatchString(segs[2]) || !strings.EqualFold(segs[3], "sites") {
			return none("Oracle HCM site")
		}
		return Board{ATS: posting.OracleHCM, Token: tenant + "/" + segs[4], Host: host,
			Search: strings.TrimSpace(u.Query().Get("keyword"))}, true, nil
	}
	return Board{}, false, nil
}
