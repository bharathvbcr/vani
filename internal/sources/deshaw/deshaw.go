// Package deshaw lists D. E. Shaw's postings from its own careers page,
// GET https://www.deshaw.com/careers. The page is a Next.js render that embeds
// every posting in one <script id="__NEXT_DATA__" type="application/json">
// block, at props.pageProps.regularJobs and props.pageProps.internships (94
// and 11 on 2026-10-27), each with its full description. A listing therefore
// costs one request and no detail requests. apply.deshaw.com is only the
// application form. robots.txt disallows /careers/open-roles, not /careers
// (checked 2026-10-27).
//
// The page is HTML, so a layout change shows up as a missing or renamed
// script block, not as an error status. Every such change is an error here:
// an empty listing would age out every live posting. internalJobs, which the
// same JSON carries, is not read.
package deshaw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	site    = "https://www.deshaw.com"
	listURL = site + "/careers"
	// MaxPostings bounds one listing; 105 were seen on 2026-10-27.
	MaxPostings = 1000
	// maxPage bounds the HTML page (about 1.0 MB on 2026-10-27), and maxJSON the
	// embedded block (about 645 KB). Both sit far under srcutil.MaxBody, which
	// is the cap on the read itself; these keep a hostile or broken page from
	// being parsed at that size.
	maxPage = 8 << 20
	maxJSON = 4 << 20
)

// ErrShape is a page whose layout is not the one this source knows.
var ErrShape = errors.New("deshaw: unexpected careers page")

var (
	openTag  = []byte(`<script id="__NEXT_DATA__" type="application/json">`)
	closeTag = []byte(`</script>`)
	// slugRE is the characters seen in a jobUrl (105 of 105 on 2026-10-27).
	slugRE = regexp.MustCompile(`^[A-Za-z0-9._()~-]{1,300}$`)
)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the D. E. Shaw source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.DEShaw }

type nextData struct {
	Props struct {
		PageProps struct {
			RegularJobs       *[]entry `json:"regularJobs"`
			Internships       *[]entry `json:"internships"`
			JobsFetchingError bool     `json:"jobsFetchingError"`
		} `json:"pageProps"`
	} `json:"props"`
}

type entry struct {
	ID   int `json:"id"`
	Data struct {
		ID                  int    `json:"id"`
		DisplayName         string `json:"displayName"`
		JobURL              string `json:"jobUrl"`
		ActiveOnJobsListing *bool  `json:"activeOnJobsListing"`
		Department          struct {
			Name string `json:"name"`
		} `json:"department"`
		JobDescription struct {
			WebsiteDescription        string   `json:"websiteDescription"`
			Responsibilities          string   `json:"responsibilities"`
			ResponsibilitiesHTML      string   `json:"responsibilitiesHtml"`
			PeopleWeAreLookingFor     []string `json:"peopleWeAreLookingFor"`
			PeopleWeAreLookingForHTML string   `json:"peopleWeAreLookingForHtml"`
		} `json:"jobDescription"`
		JobMetadata struct {
			WorkStatus    string `json:"workStatus"`
			IsExploratory bool   `json:"isExploratory"`
			JobLocations  []struct {
				Name string `json:"name"`
			} `json:"jobLocations"`
		} `json:"jobMetadata"`
	} `json:"data"`
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.DEShaw); err != nil {
		return nil, err
	}
	if len(b.SearchTerms()) > 0 {
		return nil, fmt.Errorf("deshaw %q: the listing is one page; searches do not apply", b.Token)
	}
	if err := sources.CheckFilters(posting.DEShaw, b.Filters); err != nil {
		return nil, fmt.Errorf("deshaw %q: %w", b.Token, err)
	}
	page, err := srcutil.GetBody(ctx, s.d, listURL)
	if err != nil {
		return nil, fmt.Errorf("deshaw %q: %w", b.Token, err)
	}
	entries, err := parsePage(page)
	if err != nil {
		return nil, fmt.Errorf("deshaw %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(entries))
	byKey := map[posting.Key]posting.Posting{}
	for _, e := range entries {
		if e.Data.ActiveOnJobsListing != nil && !*e.Data.ActiveOnJobsListing {
			continue
		}
		p, err := e.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("deshaw %q: job %d: %w", b.Token, e.Data.ID, err)
		}
		if prev, dup := byKey[p.Key]; dup {
			if prev != p {
				return nil, fmt.Errorf("deshaw %q: %w: job %d appears twice with different content", b.Token, ErrShape, e.Data.ID)
			}
			continue
		}
		byKey[p.Key] = p
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("deshaw %q: %w: no active postings", b.Token, ErrShape)
	}
	if len(out) > MaxPostings {
		return nil, fmt.Errorf("deshaw %q: %w: %d postings, over %d", b.Token, srcutil.ErrTooManyPostings, len(out), MaxPostings)
	}
	return out, nil
}

// parsePage reads the postings out of the careers page. A page without the
// data block, with it twice, unterminated, oversized, not JSON, flagged by its
// own jobsFetchingError, with either list missing or with no regular jobs is
// an error: none of these is a board with no postings.
func parsePage(page []byte) ([]entry, error) {
	if len(page) > maxPage {
		return nil, fmt.Errorf("%w: page is %d bytes, over %d", srcutil.ErrBodyTooLarge, len(page), maxPage)
	}
	i := bytes.Index(page, openTag)
	if i < 0 {
		return nil, fmt.Errorf("%w: no __NEXT_DATA__ script block", ErrShape)
	}
	rest := page[i+len(openTag):]
	if bytes.Contains(rest, openTag) {
		return nil, fmt.Errorf("%w: more than one __NEXT_DATA__ script block", ErrShape)
	}
	j := bytes.Index(rest, closeTag)
	if j < 0 {
		return nil, fmt.Errorf("%w: __NEXT_DATA__ script block is not closed", ErrShape)
	}
	if j > maxJSON {
		return nil, fmt.Errorf("%w: __NEXT_DATA__ is %d bytes, over %d", srcutil.ErrBodyTooLarge, j, maxJSON)
	}
	block := bytes.TrimSpace(rest[:j])
	if len(block) == 0 {
		return nil, fmt.Errorf("%w: __NEXT_DATA__ script block is empty", ErrShape)
	}
	var nd nextData
	if err := json.Unmarshal(block, &nd); err != nil {
		return nil, fmt.Errorf("%w: __NEXT_DATA__ is not JSON: %v", ErrShape, err)
	}
	pp := nd.Props.PageProps
	if pp.JobsFetchingError {
		return nil, fmt.Errorf("%w: the page reports jobsFetchingError", ErrShape)
	}
	if pp.RegularJobs == nil || pp.Internships == nil {
		return nil, fmt.Errorf("%w: pageProps lacks regularJobs or internships", ErrShape)
	}
	if len(*pp.RegularJobs) == 0 {
		return nil, fmt.Errorf("%w: regularJobs is empty", ErrShape)
	}
	if n := len(*pp.RegularJobs) + len(*pp.Internships); n > MaxPostings {
		return nil, fmt.Errorf("%w: %d postings, over %d", srcutil.ErrTooManyPostings, n, MaxPostings)
	}
	return append(append([]entry{}, *pp.RegularJobs...), *pp.Internships...), nil
}

func (e entry) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	d := e.Data
	if d.ID <= 0 {
		return posting.Posting{}, fmt.Errorf("%w: id %d", ErrShape, d.ID)
	}
	if e.ID != d.ID {
		return posting.Posting{}, fmt.Errorf("%w: id %d and data.id %d differ", ErrShape, e.ID, d.ID)
	}
	id := strconv.Itoa(d.ID)
	if !slugRE.MatchString(d.JobURL) || !strings.HasSuffix(d.JobURL, "-"+id) {
		return posting.Posting{}, fmt.Errorf("%w: jobUrl %q does not name job %s", ErrShape, d.JobURL, id)
	}
	key, err := posting.DEShawKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, err
	}
	title := srcutil.Title(d.DisplayName)
	if title == "" {
		return posting.Posting{}, fmt.Errorf("%w: empty title", ErrShape)
	}
	company := b.Company
	if company == "" {
		company = "D. E. Shaw"
	}
	// No date: the JSON carries none, and a job page's JSON-LD datePosted is
	// the fetch time (2026-10-27).
	return posting.Posting{
		Key:         key,
		ATS:         posting.DEShaw,
		Company:     company,
		Title:       title,
		Location:    location(e),
		Department:  strings.TrimSpace(d.Department.Name),
		URL:         site + "/careers/" + d.JobURL,
		Description: description(e),
		SeenAt:      seen,
	}, nil
}

// location joins the job's office names ("New York", "Denver", "London",
// "Singapore", "Off-site") as the board wrote them.
func location(e entry) string {
	var locs []string
	for _, l := range e.Data.JobMetadata.JobLocations {
		n := strings.Join(strings.Fields(l.Name), " ")
		if n != "" && !slices.Contains(locs, n) {
			locs = append(locs, n)
		}
	}
	return strings.Join(locs, "; ")
}

// employment is workStatus in the wording shared across boards: "Regular
// Full-Time" is full-time, "Intern" internship, "Contractor" contract.
func employment(workStatus string) string {
	return srcutil.EmploymentType(strings.TrimPrefix(strings.TrimSpace(workStatus), "Regular "))
}

// description is the posting's sections as text, led by a srcutil.RoleLine of
// the board's own role facts. The three "All Positions in ..." entries are
// general applications (isExploratory), not roles, and say so.
func description(e entry) string {
	jd, md := e.Data.JobDescription, e.Data.JobMetadata
	flags := []string{employment(md.WorkStatus)}
	if md.IsExploratory {
		flags = append(flags, "general application, no specific role")
	}
	lead, room := srcutil.Lead(srcutil.RoleLine(flags...))
	resp := jd.ResponsibilitiesHTML
	if strings.TrimSpace(resp) == "" && strings.TrimSpace(jd.Responsibilities) != "" {
		resp = "<p>" + html.EscapeString(jd.Responsibilities) + "</p>"
	}
	want := jd.PeopleWeAreLookingForHTML
	if strings.TrimSpace(want) == "" && len(jd.PeopleWeAreLookingFor) > 0 {
		var sb strings.Builder
		sb.WriteString("<ul>")
		for _, w := range jd.PeopleWeAreLookingFor {
			sb.WriteString("<li>" + html.EscapeString(w) + "</li>")
		}
		sb.WriteString("</ul>")
		want = sb.String()
	}
	var parts []string
	for _, sec := range []struct{ head, body string }{
		{"Description", jd.WebsiteDescription},
		{"Responsibilities", resp},
		{"People we are looking for", want},
	} {
		if strings.TrimSpace(sec.body) != "" {
			parts = append(parts, "<h3>"+sec.head+"</h3>"+sec.body)
		}
	}
	return lead + htmltext.Text(strings.Join(parts, ""), room)
}
