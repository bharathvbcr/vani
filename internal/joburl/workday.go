package joburl

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources"
)

var workdayLocale = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}$`)

// WorkdayPosting folds a normalised Workday posting URL to one key per posting
// across its routes: [xx-XX/]site/(job|details)/…/slug[/apply/…] on a
// *.myworkdayjobs.com host becomes scheme://host/site/job/slug; on the shared
// wd{N}.myworkdaysite.com host (sources.WorkdaySiteHost) the site is
// recruiting/{tenant}/{site}. The locale, the location segment(s) and the
// application tail are dropped; the whole last segment before an "apply"
// segment is the posting's slug. ok is false for any
// other URL and for Workday paths that name no posting (/site, /site/login,
// /site/job/apply), which keep their literal form.
//
// It is a second comparison for `vani jobs find`, not part of Normalize:
// Normalize's output is the URL the reply reports and stays literal.
// postingKey in extension/src/applied.ts is the same fold for the per-job
// resume key.
func WorkdayPosting(norm string) (string, bool) {
	rest, _, _ := strings.Cut(norm, "?")
	scheme, afterScheme, ok := strings.Cut(rest, "://")
	if !ok {
		return "", false
	}
	host, p, _ := strings.Cut(afterScheme, "/")
	name, err := url.Parse(scheme + "://" + host)
	if err != nil {
		return "", false
	}
	shared := sources.WorkdaySiteHost(name.Hostname())
	if !shared && !strings.HasSuffix(name.Hostname(), ".myworkdayjobs.com") {
		return "", false
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) > 0 && workdayLocale.MatchString(segs[0]) {
		segs = segs[1:]
	}
	// The shared host's pages carry the tenant too: recruiting/{tenant}/{site}.
	site := 1
	if shared {
		if len(segs) < 3 || segs[0] != "recruiting" {
			return "", false
		}
		site = 3
	}
	if len(segs) < site+2 || (segs[site] != "job" && segs[site] != "details") {
		return "", false
	}
	posting := segs[site+1:]
	for i, s := range posting {
		if strings.EqualFold(s, "apply") {
			posting = posting[:i]
			break
		}
	}
	if len(posting) == 0 {
		return "", false
	}
	return scheme + "://" + host + "/" + strings.Join(segs[:site], "/") + "/job/" + posting[len(posting)-1], true
}
