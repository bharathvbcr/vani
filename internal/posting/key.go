package posting

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Key is a posting's canonical identity: "ats:part:part...". Two sources that
// report the same ATS posting produce the same Key, which is what makes
// cross-source deduplication possible.
type Key string

// ErrKeyPart is returned when a key component is empty or contains the
// separator, either of which would make two different postings collide.
var ErrKeyPart = errors.New("posting: invalid key component")

func build(ats ATS, parts ...string) (Key, error) {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, string(ats))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.Contains(p, ":") {
			return "", fmt.Errorf("%w: %q", ErrKeyPart, p)
		}
		clean = append(clean, strings.ToLower(p))
	}
	return Key(strings.Join(clean, ":")), nil
}

// GreenhouseKey is gh:{board_token}:{job_id}.
func GreenhouseKey(board, jobID string) (Key, error) { return build(Greenhouse, board, jobID) }

// LeverKey is lv:{company}:{posting_id}. The EU and US hosts share one key
// space: a company lives on exactly one of them.
func LeverKey(company, postingID string) (Key, error) { return build(Lever, company, postingID) }

// AshbyKey is ab:{job_board_name}:{job_id}.
func AshbyKey(board, jobID string) (Key, error) { return build(Ashby, board, jobID) }

// WorkdayKey is wd:{tenant}:{site}:{job_req_id}.
func WorkdayKey(tenant, site, reqID string) (Key, error) {
	return build(Workday, tenant, site, reqID)
}

// OracleHCMKey is oc:{tenant}:{site}:{requisition_id}; the id is the public
// requisition number ("210791916"), not the internal RequisitionId.
func OracleHCMKey(tenant, site, reqID string) (Key, error) {
	return build(OracleHCM, tenant, site, reqID)
}

// AmazonKey is az:{board}:{id_icims}.
func AmazonKey(board, id string) (Key, error) { return build(Amazon, board, id) }

// GoogleKey is gg:{board}:{job_id}.
func GoogleKey(board, id string) (Key, error) { return build(Google, board, id) }

// AppleKey is ap:{board}:{position_id}. The position id, not the job number
// ("200677962-3760"), is the role: one role posted in several locales keeps
// one key.
func AppleKey(board, positionID string) (Key, error) { return build(Apple, board, positionID) }

// EightfoldKey is ef:{domain}:{position_id}; the domain ("microsoft.com") is
// the board token.
func EightfoldKey(domain, id string) (Key, error) { return build(Eightfold, domain, id) }

// JibeKey is jb:{client_code}:{req_id}; the client code ("amd") is the board
// token.
func JibeKey(client, reqID string) (Key, error) { return build(Jibe, client, reqID) }

// GemKey is gm:{board}:{post_id}. The post id is the last segment of the
// post's jobs.gem.com link. Gem's ids are case-sensitive base64 and the key
// lowercases every part, so the gem adapter refuses a listing in which two
// ids share a key.
func GemKey(board, postID string) (Key, error) { return build(Gem, board, postID) }

// SmartRecruitersKey is sr:{company_id}:{posting_id}; the company identifier
// ("servicenow") is the board token and the posting id is the ATS's own.
func SmartRecruitersKey(company, id string) (Key, error) { return build(SmartRecruiters, company, id) }

// IBMKey is ib:{board}:{jobId}; the board is "ibm" or "confluent". The job id
// is the number in the careers.ibm.com job link (JobDetail?jobId=135407).
func IBMKey(board, jobID string) (Key, error) { return build(IBM, board, jobID) }

// AvatureKey is av:{token}:{job_id}; the token is the board's own slug
// ("twosigma"), the job id the number at the end of its JobDetail URL.
func AvatureKey(token, jobID string) (Key, error) { return build(Avature, token, jobID) }

// DEShawKey is ds:{board}:{id}, the id of deshaw.com's own job record.
func DEShawKey(board, id string) (Key, error) { return build(DEShaw, board, id) }

// AtlassianKey is at:{board}:{id}, the id of Atlassian's own job record (the
// iCIMS job id in its portal URL).
func AtlassianKey(board, id string) (Key, error) { return build(Atlassian, board, id) }

// RipplingKey is rp:{board_slug}:{job_uuid}.
func RipplingKey(board, jobID string) (Key, error) { return build(Rippling, board, jobID) }

// WorkableKey is wk:{account_slug}:{shortcode}.
func WorkableKey(account, shortcode string) (Key, error) {
	return build(Workable, account, shortcode)
}

// RecruiteeKey is rc:{subdomain}:{offer_id}.
func RecruiteeKey(board, offerID string) (Key, error) { return build(Recruitee, board, offerID) }

// PinpointKey is pp:{subdomain}:{posting_id}.
func PinpointKey(board, postingID string) (Key, error) { return build(Pinpoint, board, postingID) }

// BambooHRKey is bh:{subdomain}:{job_opening_id}.
func BambooHRKey(board, jobID string) (Key, error) { return build(BambooHR, board, jobID) }

// PersonioKey is ps:{subdomain}:{position_id}.
func PersonioKey(board, positionID string) (Key, error) { return build(Personio, board, positionID) }

// TalentBrewKey is tb:{slug}:{job_id}; the id is the last number in the job's
// URL (/job/{city}/{title}/{company_id}/{job_id}), not the requisition number
// in its JSON-LD identifier.
func TalentBrewKey(board, jobID string) (Key, error) { return build(TalentBrew, board, jobID) }

// FeedKey is fd:{board}:{partner_job_id}. The id is the feed's own, so a
// cosmetic change to the apply URL does not split one role into two.
func FeedKey(board, jobID string) (Key, error) { return build(Feed, board, jobID) }

// PageKey is pg:{board}:{sha256(canonical posting URL)[:24]}. The URL is
// canonicalised (lowercase scheme and host, no fragment, no utm_* parameters,
// no trailing slash) so cosmetic link differences do not split one posting
// into two.
func PageKey(board, rawURL string) (Key, error) {
	canon, err := CanonicalURL(rawURL)
	if err != nil {
		return "", err
	}
	b, err := build(Page, board)
	if err != nil {
		return "", err
	}
	return Key(string(b) + ":" + hashOf(canon)[:24]), nil
}

// CanonicalURL is the form PageKey hashes. Only absolute http(s) URLs with a
// host are accepted.
func CanonicalURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%w: posting URL %q", ErrKeyPart, rawURL)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	u.User = nil
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

// FallbackKey is x:{sha256(norm(company)|normTitle(title)|norm(location))[:24]},
// for postings with no ATS identity. Location may be empty; company and title
// may not.
func FallbackKey(company, title, location string) (Key, error) {
	c, t := norm(company), normTitle(title)
	if c == "" || t == "" {
		return "", fmt.Errorf("%w: fallback key needs company and title", ErrKeyPart)
	}
	return Key(string(Other) + ":" + hashOf(c + "|" + t + "|" + norm(location))[:24]), nil
}

// ATS returns the system named in the key, or "" if the key is malformed.
func (k Key) ATS() ATS {
	head, _, ok := strings.Cut(string(k), ":")
	if !ok {
		return ""
	}
	switch a := ATS(head); a {
	case Greenhouse, Lever, Ashby, Workday, Amazon, Google, Apple, Eightfold, Jibe,
		Gem, SmartRecruiters, IBM, Avature, DEShaw, Atlassian, OracleHCM, Rippling, Workable,
		Recruitee, Pinpoint, BambooHR, Personio, TalentBrew, Feed, Page, Other:
		return a
	}
	return ""
}
