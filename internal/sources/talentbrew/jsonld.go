package talentbrew

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// ldBlocks returns the bodies of the <script type="application/ld+json">
// blocks of a page, at most maxLDBlocks of them, in document order. It is a
// small tokenizer, not a parser (page.scanHTML is the same idea; that one is
// private to the page board and gathers every anchor of the page too): it
// finds each <script> opening tag, reads at most maxTagLen bytes of it, takes
// the text up to the next </script>, and resumes after it, so a "<script" inside
// another script's text is never read as a tag. An opening tag or script that
// never ends stops the scan rather than being guessed at. The page is untrusted
// and nothing in it is executed.
func ldBlocks(doc string) []string {
	var out []string
	for i := 0; i < len(doc) && len(out) < maxLDBlocks; {
		j := htmltext.IndexFold(doc[i:], "<script")
		if j < 0 {
			break
		}
		k := i + j + len("<script")
		if k >= len(doc) {
			break
		}
		if c := doc[k]; c != '>' && c != '/' && c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\f' {
			i = k // "<scripts", not a script tag
			continue
		}
		gt := strings.IndexByte(doc[k:min(len(doc), k+maxTagLen)], '>')
		if gt < 0 {
			break
		}
		tag := htmltext.LowerASCII(doc[k : k+gt])
		bodyAt := k + gt + 1
		end := htmltext.IndexFold(doc[bodyAt:], "</script")
		if end < 0 {
			break
		}
		if strings.Contains(tag, "application/ld+json") {
			out = append(out, doc[bodyAt:bodyAt+end])
		}
		i = bodyAt + end
	}
	return out
}

// stringList is a JSON string or an array of strings, as schema.org allows
// for @type and employmentType.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("not a string or an array of strings")
	}
	*l = stringList(many)
	return nil
}

type ldPlace struct {
	Address struct {
		Locality string `json:"addressLocality"`
		Region   string `json:"addressRegion"`
		Country  string `json:"addressCountry"`
	} `json:"address"`
}

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
		if err := json.Unmarshal([]byte(controlToSpace(strings.TrimSpace(raw))), &j); err != nil {
			continue
		}
		if !isJobPosting(j.Type) || !sameJob(j.URL, host, company, id) {
			continue
		}
		return j, true
	}
	return ldJob{}, false
}

// controlToSpace replaces the raw control characters (U+0000 to U+001F) of a
// JSON-LD block with spaces. Citi's pages write a raw tab inside the
// description string ("<p>●\tDesign and configure API proxies…", job
// 102455422752, 2026-10-28), which is not JSON, so the block was passed over and
// the board failed every run. Outside strings these characters are whitespace
// or invalid, and inside them the text is HTML, where a space reads the same.
func controlToSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
}

func isJobPosting(t stringList) bool {
	for _, s := range t {
		if s == "JobPosting" {
			return true
		}
	}
	return false
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

// parseDate reads datePosted: a date ("2026-10-27", what every page seen
// wrote, taken as midnight UTC) or an RFC 3339 time; empty is the zero time.
func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("datePosted %q is neither a date nor an RFC 3339 time", s)
	}
	return t.UTC(), nil
}

// amount is a JSON number or a numeric string.
type amount struct {
	v  float64
	ok bool
}

func (a *amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	a.v, a.ok = v, true
	return nil
}

// payLine is the pay line for a schema.org baseSalary (MonetaryAmount: a
// currency and a QuantitativeValue with minValue, maxValue or value and a
// unitText), or "" when there is none or it is not well formed. Every page
// sampled carried the empty template (currency "", no amount, unitText ""), so
// a range was never seen in it live: the amount fields are schema.org's, read
// as unverified here. srcutil.PayLine refuses a currency that is not three
// letters, a non-positive amount and a bottom above the top.
func payLine(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s struct {
		Currency string `json:"currency"`
		Value    struct {
			Min      amount `json:"minValue"`
			Max      amount `json:"maxValue"`
			Value    amount `json:"value"`
			UnitText string `json:"unitText"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	v := s.Value
	// srcutil.PayLine reads a bad bottom as a missing one and keeps the top;
	// here an amount that is given and unusable makes the whole range unwell
	// formed.
	for _, a := range []amount{v.Min, v.Max, v.Value} {
		if a.ok && (a.v <= 0 || math.IsInf(a.v, 0) || math.IsNaN(a.v)) {
			return ""
		}
	}
	var lo, hi *float64
	pick := func(a amount) *float64 {
		if a.ok {
			return &a.v
		}
		return nil
	}
	if v.Min.ok || v.Max.ok {
		lo, hi = pick(v.Min), pick(v.Max)
	} else {
		lo = pick(v.Value)
	}
	return srcutil.PayLine(s.Currency, lo, hi, v.UnitText)
}
