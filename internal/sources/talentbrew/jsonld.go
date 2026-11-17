package talentbrew

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/jsonld"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// The page scan, the schema.org shapes and the date and pay readers are
// package jsonld's, shared with the Meta source. These names keep this
// package's own spelling of them.
type (
	stringList = jsonld.StringList
	ldPlace    = jsonld.Place
)

const (
	maxLDBlocks = jsonld.MaxBlocks
	maxTagLen   = jsonld.MaxTagLen
)

func ldBlocks(doc string) []string          { return jsonld.Blocks(doc) }
func controlToSpace(s string) string        { return jsonld.ControlToSpace(s) }
func isJobPosting(t stringList) bool        { return jsonld.IsJobPosting(t) }
func parseDate(s string) (time.Time, error) { return jsonld.ParseDate(s) }
func payLine(raw json.RawMessage) string    { return jsonld.PayLine(raw) }

// ldJob is the part of a schema.org JobPosting that this source reads, every
// field seen on a live page (Intuit and Citi, 2026-10-28) except where payLine
// says otherwise.
type ldJob struct {
	Type           stringList `json:"@type"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	DatePosted     string     `json:"datePosted"`
	EmploymentType stringList `json:"employmentType"`
	URL            string     `json:"url"`
	JobLocation    []ldPlace  `json:"jobLocation"`
	// JobLocationType is "TELECOMMUTE" on a remote job (Intuit).
	JobLocationType string `json:"jobLocationType"`
	Hiring          struct {
		Name string `json:"name"`
	} `json:"hiringOrganization"`
	// BaseSalary stays raw: it is the empty template on every page seen, and a
	// value that is not shaped as payLine expects is no pay, not a failed page.
	BaseSalary json.RawMessage `json:"baseSalary"`
}

// findJobPosting returns the first JobPosting block of the page that is for
// job id of the tenant's company on host: its url is https on host and ends
// /{company}/{id}. A block that is not valid JSON, is not a JobPosting, or is
// for another job (the url a hostile page text could copy is still checked
// here) is passed over; ok is false when none is left. Raw control characters
// are read as spaces first (controlToSpace).
func findJobPosting(page, host, company, id string) (ldJob, bool) {
	for _, raw := range ldBlocks(page) {
		var j ldJob
		if err := jsonld.Decode(raw, &j); err != nil {
			continue
		}
		if !isJobPosting(j.Type) || !sameJob(j.URL, host, company, id) {
			continue
		}
		return j, true
	}
	return ldJob{}, false
}

// sameJob reports whether raw is the address of job id of company on host.
func sameJob(raw, host, company, id string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), host) || u.Port() != "" || u.User != nil {
		return false
	}
	m := jobPath.FindStringSubmatch(u.EscapedPath())
	return m != nil && m[3] == company && m[4] == id
}

// posting builds the job's posting from its page. The URL is the sitemap's
// (the page's own url names the same job: sameJob).
func (j ldJob) posting(b sources.Board, r row, key posting.Key, seen time.Time) (posting.Posting, error) {
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := line(j.Title)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	posted, err := parseDate(strings.TrimSpace(j.DatePosted))
	if err != nil {
		return posting.Posting{}, fmt.Errorf("%w: %v", ErrShape, err)
	}
	loc, remote := places(j.JobLocation)
	remote = remote || strings.EqualFold(strings.TrimSpace(j.JobLocationType), "TELECOMMUTE")
	flags := make([]string, 0, len(j.EmploymentType)+1)
	for _, e := range j.EmploymentType {
		flags = append(flags, srcutil.EmploymentType(e))
	}
	if remote {
		flags = append(flags, "remote")
	}
	lead, room := srcutil.Lead(payLine(j.BaseSalary), srcutil.RoleLine(flags...))
	return posting.Posting{
		Key:         key,
		ATS:         posting.TalentBrew,
		Company:     boardCompany(b, j.Hiring.Name),
		Title:       title,
		Location:    loc,
		Remote:      remote,
		URL:         r.loc,
		PostedAt:    posted,
		Description: lead + htmltext.Text(j.Description, room),
		SeenAt:      seen,
	}, nil
}

// places is the job's locations as one line, each "locality, region, country"
// without its empty parts, joined by "; ", at most maxPlaces. A place whose
// only part is the country "Remote" (TalentBrew's own marker, with no locality
// and no region) is no place: it sets remote instead. Intuit writes region and
// country in full ("California", "United States") and Citi as codes ("MH",
// "IN"); both are kept as written, except a region that is only digits (region).
func places(in []ldPlace) (loc string, remote bool) {
	var parts []string
	for _, p := range in {
		a := p.Address
		if line(a.Locality) == "" && line(a.Region) == "" && strings.EqualFold(line(a.Country), "remote") {
			remote = true
			continue
		}
		if len(parts) == maxPlaces {
			break
		}
		parts = append(parts, srcutil.JoinDistinct(", ", line(a.Locality), region(a.Region), line(a.Country)))
	}
	return srcutil.JoinDistinct("; ", parts...), remote
}

// region is a place's addressRegion as text, or "" for a bare number: Citi's
// outside the US and India are subdivision codes ("85" for Olsztyn, "11" for
// Seoul, "09" for Mexico City), which read as nothing in a location.
func region(s string) string {
	r := line(s)
	if r != "" && strings.Trim(r, "0123456789") == "" {
		return ""
	}
	return r
}
