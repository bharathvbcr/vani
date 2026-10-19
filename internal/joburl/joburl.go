// Package joburl reduces a page or posting URL to the form `vani jobs find`
// compares: scheme, host and path. The query and fragment are dropped (they
// carry tracking parameters and tokens, not the identity of a page), the host
// is lower-cased, a default port is dropped and repeated or trailing slashes
// collapse. Three things keep a page's identity that the path alone does not:
//
//   - a few query parameters that name the job (gh_jid and friends, see
//     IDParams) survive, with a value that is a plain identifier;
//   - a Greenhouse embedded form, boards.greenhouse.io/embed/job_app?for=B&token=J,
//     is the posting boards.greenhouse.io/B/jobs/J, and job-boards.greenhouse.io
//     is the same site as boards.greenhouse.io;
//   - ApplyBase names the posting an application sub-page belongs to.
//
// The extension applies the same reduction before a URL leaves the browser
// (extension/src/applied.ts); extension/test/vectors/joburl.json holds the
// cases both sides must agree on.
//
// The input is untrusted page text. Normalize only parses it: it never
// fetches, resolves or follows anything.
package joburl

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBytes bounds a URL accepted for matching. A longer one is refused, not cut.
const MaxBytes = 2048

// ErrInvalid wraps every refusal.
var ErrInvalid = errors.New("not a usable http(s) URL")

// IDParams are the query parameters that name a job on boards that serve every
// posting from one path (Greenhouse's gh_jid on a company's own careers page).
// They are kept, in this order, when their value is a plain identifier; every
// other parameter is dropped. extension/src/applied.ts has the same list.
var IDParams = []string{"gh_jid", "jid", "job_id", "jobid", "req_id"}

var (
	idValue    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	ghBoard    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	ghToken    = regexp.MustCompile(`^[0-9]{1,20}$`)
	applyWords = map[string]bool{"apply": true, "application": true}
)

// Normalize returns scheme://host[:port]/path[?id=value] for an http or https URL.
func Normalize(raw string) (string, error) {
	switch {
	case len(raw) > MaxBytes:
		return "", fmt.Errorf("%w: over %d bytes", ErrInvalid, MaxBytes)
	case !utf8.ValidString(raw):
		return "", fmt.Errorf("%w: not UTF-8", ErrInvalid)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalid)
	}
	if strings.ContainsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' || r == '\\'
	}) {
		return "", fmt.Errorf("%w: whitespace, control or markup characters", ErrInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: unparsable", ErrInvalid)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: scheme is not http or https", ErrInvalid)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf("%w: no host", ErrInvalid)
	}
	if host == "job-boards.greenhouse.io" {
		host = "boards.greenhouse.io"
	}
	if strings.Contains(host, ":") { // an IPv6 literal
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	p := path.Clean("/" + u.EscapedPath())
	q := u.Query()
	if host == "boards.greenhouse.io" && p == "/embed/job_app" && ghBoard.MatchString(q.Get("for")) && ghToken.MatchString(q.Get("token")) {
		return scheme + "://" + host + "/" + q.Get("for") + "/jobs/" + q.Get("token"), nil
	}
	var kept []string
	for _, name := range IDParams {
		if v := q.Get(name); idValue.MatchString(v) {
			kept = append(kept, name+"="+v)
		}
	}
	out := scheme + "://" + host + p
	if len(kept) > 0 {
		out += "?" + strings.Join(kept, "&")
	}
	return out, nil
}

// ApplyBase returns the posting a normalised application sub-page belongs to:
// the URL cut at its first path segment "apply" or "application" (Lever's
// /apply, Ashby's /application, Workday's /apply/applyManually), keeping the
// job-naming query. ok is false when there is no such segment or nothing
// would remain before it.
func ApplyBase(norm string) (string, bool) {
	rest, query, _ := strings.Cut(norm, "?")
	scheme, afterScheme, ok := strings.Cut(rest, "://")
	if !ok {
		return "", false
	}
	host, p, _ := strings.Cut(afterScheme, "/")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if i > 0 && applyWords[strings.ToLower(s)] {
			out := scheme + "://" + host + "/" + strings.Join(segs[:i], "/")
			if query != "" {
				out += "?" + query
			}
			return out, true
		}
	}
	return "", false
}
