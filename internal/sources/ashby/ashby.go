// Package ashby lists postings from an Ashby public job board
// (https://developers.ashbyhq.com/docs/public-job-posting-api).
//
// A board that has turned that API off still publishes the same roles on its
// job page. That page reads them from the public job-board GraphQL endpoint
// on jobs.ashbyhq.com (no session). List tries the posting API first and,
// only when that answers 404, reads the job board the page uses: one list,
// then one detail per posting, because the list carries no description.
//
// The same job-page GraphQL carries each posting's application form; form.go
// reads it into the posting's Form summary (see there for the bounds).
package ashby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// maxGraphQLBody caps one job-board response. A package variable so a test
// can shrink it. The posting-API list keeps GetJSON's board cap.
var maxGraphQLBody int64 = 2 << 20

// ErrBoardNotFound is returned when Ashby has no such job board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const baseURL = "https://api.ashbyhq.com/posting-api/job-board/"

type source struct {
	d   egress.Doer
	now func() time.Time
	// forms is set by NewWithForms: the posting-API list then reads each new
	// posting's application form. Nil reads none there.
	forms *formReader
}

// New returns the Ashby source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (s *source) ATS() posting.ATS { return posting.Ashby }

type response struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Location         string `json:"location"`
	Department       string `json:"department"`
	JobURL           string `json:"jobUrl"`
	ApplyURL         string `json:"applyUrl"`
	PublishedAt      string `json:"publishedAt"`
	IsRemote         bool   `json:"isRemote"`
	IsListed         *bool  `json:"isListed"`
	DescriptionPlain string `json:"descriptionPlain"`
	DescriptionHTML  string `json:"descriptionHtml"`
	// Compensation is present with includeCompensation=true. It is used only
	// when the company shows it on its postings (an absent flag counts as
	// shown: the board returned it publicly).
	Compensation *compensation `json:"compensation"`
	ShowPay      *bool         `json:"shouldDisplayCompensationOnJobPostings"`
}

type compensation struct {
	CompensationTierSummary             string `json:"compensationTierSummary"`
	ScrapeableCompensationSalarySummary string `json:"scrapeableCompensationSalarySummary"`
	SummaryComponents                   []struct {
		CompensationType string   `json:"compensationType"`
		Interval         string   `json:"interval"`
		CurrencyCode     *string  `json:"currencyCode"`
		MinValue         *float64 `json:"minValue"`
		MaxValue         *float64 `json:"maxValue"`
	} `json:"summaryComponents"`
}

// payLine is the job's salary as one line: the structured Salary component
// when there is one, else the board's salary summary, else its tier summary
// (which may also name equity and bonus; jobinsight.ParsePay reads the
// salary out of it). Empty without compensation.
func (c *compensation) payLine() string {
	if c == nil {
		return ""
	}
	for _, s := range c.SummaryComponents {
		if s.CompensationType == "Salary" && s.CurrencyCode != nil {
			if line := srcutil.PayLine(*s.CurrencyCode, s.MinValue, s.MaxValue, s.Interval); line != "" {
				return line
			}
		}
	}
	for _, s := range []string{c.ScrapeableCompensationSalarySummary, c.CompensationTierSummary} {
		if line := srcutil.CompensationLine(s); line != "" {
			return line
		}
	}
	return ""
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Ashby); err != nil {
		return nil, err
	}
	var resp response
	endpoint := baseURL + url.PathEscape(b.APIName()) + "?includeCompensation=true"
	if err := srcutil.GetJSON(ctx, s.d, endpoint, &resp); err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return s.listJobBoard(ctx, b)
		}
		return nil, fmt.Errorf("ashby %q: %w", b.Token, err)
	}
	if resp.Jobs == nil {
		return nil, fmt.Errorf("ashby %q: payload has no jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Jobs)); err != nil {
		return nil, fmt.Errorf("ashby %q: %w", b.Token, err)
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(resp.Jobs))
	ids := make([]string, 0, len(resp.Jobs))
	for i, j := range resp.Jobs {
		// An unlisted job is reachable by link but not published on the board.
		if j.IsListed != nil && !*j.IsListed {
			continue
		}
		p, err := j.posting(b, seen)
		if err != nil {
			return nil, fmt.Errorf("ashby %q: job %d: %w", b.Token, i, err)
		}
		out = append(out, p)
		ids = append(ids, strings.TrimSpace(j.ID))
	}
	if s.forms != nil {
		if err := s.forms.read(ctx, s, b, out, ids); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.AshbyKey(b.Token, j.ID)
	if err != nil {
		return posting.Posting{}, err
	}
	if strings.TrimSpace(j.Title) == "" {
		return posting.Posting{}, errors.New("empty title")
	}
	if j.JobURL == "" {
		return posting.Posting{}, errors.New("empty jobUrl")
	}
	if err := srcutil.CheckURL("jobUrl", j.JobURL); err != nil {
		return posting.Posting{}, err
	}
	if err := srcutil.CheckURL("applyUrl", j.ApplyURL); err != nil {
		return posting.Posting{}, err
	}
	posted, err := srcutil.ParseTime("publishedAt", j.PublishedAt)
	if err != nil {
		return posting.Posting{}, err
	}
	desc := htmltext.Plain(j.DescriptionPlain, srcutil.MaxDescription)
	if desc == "" {
		desc = htmltext.Text(j.DescriptionHTML, srcutil.MaxDescription)
	}
	// The pay line goes first, so the description cap never cuts it.
	if line := j.Compensation.payLine(); line != "" && (j.ShowPay == nil || *j.ShowPay) {
		desc = htmltext.Plain(line+"\n\n"+desc, srcutil.MaxDescription)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Ashby,
		Company:     b.Company,
		Title:       srcutil.Title(j.Title),
		Location:    strings.TrimSpace(j.Location),
		Remote:      j.IsRemote,
		Department:  strings.TrimSpace(j.Department),
		URL:         j.JobURL,
		ApplyURL:    j.ApplyURL,
		PostedAt:    posted,
		Description: desc,
		SeenAt:      seen,
	}, nil
}

// The job-board queries are the ones the public jobs page sends
// (ApiJobBoardWithTeams, ApiJobPosting), cut down to the fields a posting
// uses. The page's list has no description, so each posting is read again.
const (
	jobBoardOp    = "ApiJobBoardWithTeams"
	jobPostingOp  = "ApiJobPosting"
	jobBoardQuery = `query ApiJobBoardWithTeams($organizationHostedJobsPageName: String!) {
  jobBoard: jobBoardWithTeams(organizationHostedJobsPageName: $organizationHostedJobsPageName) {
    teams { id name externalName }
    jobPostings { id title locationName workplaceType teamId }
  }
}`
	jobPostingFields = `id title departmentName departmentExternalName locationName workplaceType
    descriptionHtml isListed compensationTierSummary scrapeableCompensationSalarySummary`
)

// jobPostingQuery is the per-posting query: the posting's fields and, when
// withForm, its application form (form.go).
func jobPostingQuery(withForm bool) string {
	fields := jobPostingFields
	if withForm {
		fields += "\n    " + formFields
	}
	return `query ApiJobPosting($organizationHostedJobsPageName: String!, $jobPostingId: String!) {
  jobPosting(organizationHostedJobsPageName: $organizationHostedJobsPageName, jobPostingId: $jobPostingId) {
    ` + fields + `
  }
}`
}

func graphQLURL(op string) string {
	return "https://jobs.ashbyhq.com/api/non-user-graphql?op=" + op
}

type gqlRequest struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
}

type gqlError struct {
	Message string `json:"message"`
}

type boardPayload struct {
	Data struct {
		JobBoard *struct {
			Teams []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				ExternalName string `json:"externalName"`
			} `json:"teams"`
			JobPostings []boardJob `json:"jobPostings"`
		} `json:"jobBoard"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

type boardJob struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	LocationName  string `json:"locationName"`
	WorkplaceType string `json:"workplaceType"`
	TeamID        string `json:"teamId"`
}

type postingPayload struct {
	Data struct {
		JobPosting *struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			DepartmentName  string `json:"departmentName"`
			DepartmentExt   string `json:"departmentExternalName"`
			LocationName    string `json:"locationName"`
			WorkplaceType   string `json:"workplaceType"`
			DescriptionHTML string `json:"descriptionHtml"`
			IsListed        *bool  `json:"isListed"`
			TierSummary     string `json:"compensationTierSummary"`
			SalarySummary   string `json:"scrapeableCompensationSalarySummary"`
			// ApplicationForm stays raw so a form in a shape this reader does
			// not know cannot fail the posting's own decode (parseForm).
			ApplicationForm json.RawMessage `json:"applicationForm"`
		} `json:"jobPosting"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

func (s *source) listJobBoard(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var resp boardPayload
	req := gqlRequest{
		OperationName: jobBoardOp,
		Query:         jobBoardQuery,
		Variables:     map[string]any{"organizationHostedJobsPageName": b.APIName()},
	}
	if err := srcutil.PostJSON(ctx, s.d, graphQLURL(jobBoardOp), req, nil, &resp); err != nil {
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			return nil, fmt.Errorf("ashby %q: %w", b.Token, srcutil.ErrBoardNotFound)
		}
		return nil, fmt.Errorf("ashby %q: job board: %w", b.Token, err)
	}
	if msg := gqlMessage(resp.Errors); msg != "" && resp.Data.JobBoard == nil {
		return nil, fmt.Errorf("ashby %q: job board: %s", b.Token, msg)
	}
	if resp.Data.JobBoard == nil {
		return nil, fmt.Errorf("ashby %q: %w", b.Token, srcutil.ErrBoardNotFound)
	}
	jobs := resp.Data.JobBoard.JobPostings
	if jobs == nil {
		return nil, fmt.Errorf("ashby %q: job board has no jobPostings array", b.Token)
	}
	if err := srcutil.CheckCount(len(jobs)); err != nil {
		return nil, fmt.Errorf("ashby %q: %w", b.Token, err)
	}
	teams := map[string]string{}
	for _, t := range resp.Data.JobBoard.Teams {
		name := strings.TrimSpace(t.ExternalName)
		if name == "" {
			name = strings.TrimSpace(t.Name)
		}
		if t.ID != "" && name != "" {
			teams[t.ID] = name
		}
	}
	seenID := map[string]bool{}
	for i, j := range jobs {
		id := strings.TrimSpace(j.ID)
		if _, err := posting.AshbyKey(b.Token, id); err != nil {
			return nil, fmt.Errorf("ashby %q: job %d: %w", b.Token, i, err)
		}
		if seenID[strings.ToLower(id)] {
			return nil, fmt.Errorf("ashby %q: job %d: duplicate id %q", b.Token, i, id)
		}
		seenID[strings.ToLower(id)] = true
	}
	seen := s.now()
	out := make([]posting.Posting, 0, len(jobs))
	for i, j := range jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(j.ID)
		detail, err := s.readDetail(ctx, b, id)
		if err != nil {
			return nil, fmt.Errorf("ashby %q: job %d: %w", b.Token, i, err)
		}
		p, keep, err := detail.posting(b, j, teams, seen)
		if err != nil {
			return nil, fmt.Errorf("ashby %q: job %d: %w", b.Token, i, err)
		}
		if keep {
			out = append(out, p)
		}
	}
	return out, nil
}

// readDetail reads one posting's detail through the job-board GraphQL, with its
// application form. A reply that errors with no posting at all may be the
// form's doing, so it is asked once more without the form: the form is not
// worth the posting. An answer that still has no posting is the real failure.
func (s *source) readDetail(ctx context.Context, b sources.Board, id string) (postingPayload, error) {
	post := func(withForm bool) (d postingPayload, err error) {
		req := gqlRequest{
			OperationName: jobPostingOp,
			Query:         jobPostingQuery(withForm),
			Variables: map[string]any{
				"organizationHostedJobsPageName": b.APIName(),
				"jobPostingId":                   id,
			},
		}
		if err := srcutil.PostJSONLimit(ctx, s.d, graphQLURL(jobPostingOp), req, nil, maxGraphQLBody, &d); err != nil {
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				err = fmt.Errorf("job %s is not on the board", id)
			}
			return postingPayload{}, err
		}
		return d, nil
	}
	detail, err := post(true)
	if err != nil {
		return detail, err
	}
	if gqlMessage(detail.Errors) != "" && detail.Data.JobPosting == nil {
		if detail, err = post(false); err != nil {
			return detail, err
		}
	}
	if msg := gqlMessage(detail.Errors); msg != "" && detail.Data.JobPosting == nil {
		return detail, errors.New(msg)
	}
	if detail.Data.JobPosting == nil {
		return detail, fmt.Errorf("job %s is not on the board", id)
	}
	return detail, nil
}

func gqlMessage(errs []gqlError) string {
	if len(errs) == 0 {
		return ""
	}
	msg := strings.TrimSpace(errs[0].Message)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func (p postingPayload) posting(b sources.Board, list boardJob, teams map[string]string, seen time.Time) (posting.Posting, bool, error) {
	j := p.Data.JobPosting
	if j.IsListed != nil && !*j.IsListed {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(list.ID)
	if got := strings.TrimSpace(j.ID); got != "" && !strings.EqualFold(got, id) {
		return posting.Posting{}, false, fmt.Errorf("detail id %q does not match %q", got, id)
	}
	key, err := posting.AshbyKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	title := strings.TrimSpace(j.Title)
	if title == "" {
		title = strings.TrimSpace(list.Title)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	page, apply, err := jobPage(b.APIName(), id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	loc := strings.TrimSpace(j.LocationName)
	if loc == "" {
		loc = strings.TrimSpace(list.LocationName)
	}
	dept := strings.TrimSpace(j.DepartmentExt)
	if dept == "" {
		dept = strings.TrimSpace(j.DepartmentName)
	}
	if dept == "" {
		dept = teams[list.TeamID]
	}
	workplace := strings.TrimSpace(j.WorkplaceType)
	if workplace == "" {
		workplace = strings.TrimSpace(list.WorkplaceType)
	}
	desc := htmltext.Text(j.DescriptionHTML, srcutil.MaxDescription)
	if line := paySummary(j.SalarySummary, j.TierSummary); line != "" {
		desc = htmltext.Plain(line+"\n\n"+desc, srcutil.MaxDescription)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Ashby,
		Company:     b.Company,
		Title:       srcutil.Title(title),
		Location:    loc,
		Remote:      strings.EqualFold(workplace, "Remote"),
		Department:  dept,
		URL:         page,
		ApplyURL:    apply,
		Description: desc,
		SeenAt:      seen,
		Form:        parseForm(j.ApplicationForm),
	}, true, nil
}

func paySummary(salary, tier string) string {
	if line := srcutil.CompensationLine(salary); line != "" {
		return line
	}
	return srcutil.CompensationLine(tier)
}

func jobPage(name, id string) (page, apply string, err error) {
	page = "https://jobs.ashbyhq.com/" + url.PathEscape(name) + "/" + url.PathEscape(id)
	apply = page + "/application"
	if err := srcutil.CheckURL("jobUrl", page); err != nil {
		return "", "", err
	}
	if err := srcutil.CheckURL("applyUrl", apply); err != nil {
		return "", "", err
	}
	return page, apply, nil
}
