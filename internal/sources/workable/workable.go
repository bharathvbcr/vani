// Package workable lists postings from a public Workable account
// (GET https://apply.workable.com/api/v1/widget/accounts/{slug}).
// The widget has no description. Each job is read from
// /api/v2/accounts/{slug}/jobs/{shortcode}, and a detail that cannot be read
// fails the board, with two exceptions that leave the row out: a 404 under a
// widget that answered (the job closed after it was listed; bounded by
// srcutil.MaxGone, since many at once are the endpoint failing) and, under
// NewWith, a transient miss (srcutil.DetailMisses). A detail that answers and
// is wrong (a mismatched shortcode, an unreadable field, a row that cannot be
// stored) still fails the board: a partial listing would age healthy postings.
//
// Detail volume is bounded as Workday's is (srcutil.PlanDetails): a source from
// New fails a board that needs more than MaxDetails detail requests; one from
// NewWith reuses the postings an earlier run stored (re-reading each on its
// turn, once every srcutil.RefreshDays days), drops new jobs DropListed rejects
// from their widget row without a request, and defers new jobs past MaxDetails
// to the next run. Stored postings are matched by key (wk:{token}:{shortcode}),
// which the widget row carries, so a deferred row is never a stored one. The
// widget has no updated stamp, so an edited description is seen on the job's
// next turn.
package workable

import (
	"bytes"
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

// ErrBoardNotFound is returned when Workable has no such account.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const widgetURL = "https://apply.workable.com/api/v1/widget/accounts/"

// maxDetailBody caps one job detail. The widget list is capped by srcutil.MaxBody.
var maxDetailBody int64 = srcutil.MaxDetailBody

// MaxDetails is the most detail requests one board spends in one run. At
// egress's one-second per-host floor that is five minutes.
const MaxDetails = 300

// errDetailGone is a job detail answering 404 under a widget that answered: the
// job closed after it was listed. Deliberately not ErrBoardNotFound.
var errDetailGone = errors.New("detail is gone (HTTP 404)")

// Stats counts what one List did. Every distinct listed job is counted once, in
// Reused, Fetched, Refreshed, Skipped, Prefiltered, Deferred, Gone or Unanswered.
type Stats struct {
	Listed int `json:"listed"`
	// Reused jobs were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched jobs were new and got a detail request; Refreshed jobs were stored
	// and were re-read on their turn.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Skipped jobs answered their detail and are not listed (internal or not
	// published).
	Skipped int `json:"skipped,omitempty"`
	// Prefiltered jobs were new and dropped by DropListed without a request.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred jobs were new but past MaxDetails: fetched on a later run. Not
	// being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Gone jobs answered their detail with 404: closed since the widget listed
	// them.
	Gone int `json:"gone,omitempty"`
	// Unanswered jobs' detail request failed transiently (srcutil.DetailMisses): a
	// stored one was returned as stored, a new one was left for a later run like
	// a deferred one.
	Unanswered int `json:"unanswered,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// Unusable reports a stored posting whose text must not be reused (see
	// srcutil.DetailRow.Unusable). It is an explicit mark, never read from the
	// text. Nil marks none.
	Unusable func(k posting.Key) bool
	// DropListed reports whether the engine drops a posting before triage from
	// what a widget row says: its title, the board's company and its location
	// (no description). A new job it drops is neither fetched nor returned. Nil
	// drops nothing.
	DropListed func(p posting.Posting) bool
	// Report, when set, receives each listed board's counts, once, whether the
	// board then succeeded or failed on a detail.
	Report func(sources.Board, Stats)
}

type source struct {
	d   egress.Doer
	now func() time.Time
	opt *Options
}

// New returns the Workable source, sending requests through d. Every listed job
// costs a detail request, and a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (a share of them re-read each run), new jobs DropListed rejects are
// never fetched, and at most MaxDetails detail requests go out per board per
// run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("workable: the bounded detail step needs the stored postings (Options.Stored)")
	}
	return &source{d: d, now: time.Now, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.Workable }

type response struct {
	Name string    `json:"name"`
	Jobs []listJob `json:"jobs"`
}

type listJob struct {
	Title          string `json:"title"`
	Shortcode      string `json:"shortcode"`
	URL            string `json:"url"`
	ApplicationURL string `json:"application_url"`
	Department     string `json:"department"`
	Telecommuting  bool   `json:"telecommuting"`
	City           string `json:"city"`
	State          string `json:"state"`
	Country        string `json:"country"`
}

type detail struct {
	Shortcode    string          `json:"shortcode"`
	Title        string          `json:"title"`
	Remote       bool            `json:"remote"`
	Workplace    string          `json:"workplace"`
	IsInternal   bool            `json:"isInternal"`
	State        string          `json:"state"`
	Published    string          `json:"published"`
	Description  string          `json:"description"`
	Requirements string          `json:"requirements"`
	Benefits     string          `json:"benefits"`
	Department   json.RawMessage `json:"department"`
	Location     place           `json:"location"`
	Locations    []struct {
		place
		Hidden bool `json:"hidden"`
	} `json:"locations"`
}

type place struct {
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
}

func accountURL(token string) string {
	return widgetURL + url.PathEscape(token)
}

func jobURL(token, code string) string {
	return "https://apply.workable.com/api/v2/accounts/" + url.PathEscape(token) + "/jobs/" + url.PathEscape(code)
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Workable); err != nil {
		return nil, err
	}
	var resp response
	if err := srcutil.GetJSON(ctx, s.d, accountURL(b.Token), &resp); err != nil {
		return nil, fmt.Errorf("workable %q: %w", b.Token, err)
	}
	if resp.Jobs == nil {
		return nil, fmt.Errorf("workable %q: payload has no jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(resp.Jobs)); err != nil {
		return nil, fmt.Errorf("workable %q: %w", b.Token, err)
	}
	seenID := map[string]bool{}
	for i, j := range resp.Jobs {
		id := strings.TrimSpace(j.Shortcode)
		if _, err := posting.WorkableKey(b.Token, id); err != nil {
			return nil, fmt.Errorf("workable %q: job %d: %w", b.Token, i, err)
		}
		if seenID[strings.ToLower(id)] {
			return nil, fmt.Errorf("workable %q: job %d: duplicate shortcode %q", b.Token, i, id)
		}
		seenID[strings.ToLower(id)] = true
	}
	company := b.Company
	if strings.TrimSpace(company) == "" {
		company = strings.TrimSpace(resp.Name)
	}
	seen := s.now()
	stored := map[posting.Key]posting.Posting{}
	if s.opt != nil {
		// Looked up by the key a row builds, so another board's posting can
		// never match one of this board's rows.
		for _, p := range s.opt.Stored(b) {
			stored[p.Key] = p
		}
	}
	keys := make([]posting.Key, len(resp.Jobs))
	plan := make([]srcutil.DetailRow, len(resp.Jobs))
	for i, j := range resp.Jobs {
		key, _ := posting.WorkableKey(b.Token, strings.TrimSpace(j.Shortcode)) // checked above
		keys[i] = key
		_, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: strings.ToLower(strings.TrimSpace(j.Shortcode)), Stored: ok, Unusable: ok && s.opt != nil && srcutil.Marked(s.opt.Unusable, key)}
		if !ok && s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(listedPosting(company, j))
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		// Without stored postings a deferred job cannot be told from a stored one,
		// and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("workable %q: %w: the board lists %d jobs and each costs a detail request, over the %d limit",
			b.Token, srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st := Stats{Listed: len(resp.Jobs), Deferred: pl.Deferred, Prefiltered: pl.Prefiltered}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	res, err := srcutil.DetailStep{
		Board: b, Rows: len(resp.Jobs), Plan: pl, CanDefer: s.opt != nil,
		Fetch: func(ctx context.Context, i int) (*posting.Posting, error) {
			j := resp.Jobs[i]
			var d detail
			if err := srcutil.GetJSONLimit(ctx, s.d, jobURL(b.Token, strings.TrimSpace(j.Shortcode)), maxDetailBody, &d); err != nil {
				if errors.Is(err, srcutil.ErrBoardNotFound) {
					return nil, errDetailGone
				}
				return nil, err
			}
			p, keep, err := d.posting(b, j, company, seen)
			if err != nil || !keep {
				return nil, err
			}
			return &p, nil
		},
		Reuse: func(i int) posting.Posting {
			p := stored[keys[i]]
			p.SeenAt = seen
			return p
		},
		Gone: errDetailGone,
		GoneErr: func(gone, fetch int) error {
			return fmt.Errorf("workable %q: %d of %d job details are gone (HTTP 404); the detail endpoint is not answering", b.Token, gone, fetch)
		},
		FetchErr: func(i int, err error) error {
			return fmt.Errorf("workable %q: job %d: %w", b.Token, i, err)
		},
		DoneErr: func(err error) error { return fmt.Errorf("workable %q: %w", b.Token, err) },
	}.Run(ctx)
	st.Reused, st.Fetched, st.Refreshed, st.Skipped, st.Gone, st.Unanswered = res.Reused, res.Fetched, res.Refreshed, res.Skipped, res.Gone, res.Unanswered
	if err != nil {
		return nil, err
	}
	return srcutil.Postings(res.Slots), nil
}

// listedPosting is what a widget row says of its job before any detail: the
// fields DropListed may judge.
func listedPosting(company string, j listJob) posting.Posting {
	return posting.Posting{
		ATS: posting.Workable, Company: company, Title: srcutil.Title(strings.TrimSpace(j.Title)),
		Location: joinComma(j.City, j.State, j.Country), Remote: j.Telecommuting,
	}
}

func (d detail) posting(b sources.Board, list listJob, company string, seen time.Time) (posting.Posting, bool, error) {
	state := strings.TrimSpace(d.State)
	if d.IsInternal || (state != "" && !strings.EqualFold(state, "published")) {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(list.Shortcode)
	if got := strings.TrimSpace(d.Shortcode); got != "" && !strings.EqualFold(got, id) {
		return posting.Posting{}, false, fmt.Errorf("detail shortcode %q does not match %q", got, id)
	}
	key, err := posting.WorkableKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = strings.TrimSpace(list.Title)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(list.URL)
	if rawURL == "" {
		return posting.Posting{}, false, errors.New("empty url")
	}
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	apply := strings.TrimSpace(list.ApplicationURL)
	if err := srcutil.CheckURL("application_url", apply); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := srcutil.ParseTime("published", strings.TrimSpace(d.Published))
	if err != nil {
		return posting.Posting{}, false, err
	}
	dept, err := departmentText(d.Department)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if dept == "" {
		dept = strings.TrimSpace(list.Department)
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Workable,
		Company:     company,
		Title:       srcutil.Title(title),
		Location:    d.where(list),
		Remote:      d.remote(list),
		Department:  dept,
		URL:         rawURL,
		ApplyURL:    apply,
		PostedAt:    posted,
		Description: htmlSections(d.Description, d.Requirements, d.Benefits),
		SeenAt:      seen,
	}, true, nil
}

func (d detail) remote(list listJob) bool {
	if d.Remote || remoteWord(d.Workplace) {
		return true
	}
	if strings.TrimSpace(d.Workplace) == "" && !d.Remote {
		return list.Telecommuting
	}
	return false
}

func (d detail) where(list listJob) string {
	for _, loc := range d.Locations {
		if loc.Hidden {
			continue
		}
		if p := loc.place.join(); p != "" {
			return p
		}
	}
	if p := d.Location.join(); p != "" {
		return p
	}
	return joinComma(list.City, list.State, list.Country)
}

func (p place) join() string { return joinComma(p.City, p.Region, p.Country) }

func departmentText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return strings.TrimSpace(one), nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return "", errors.New("department is not text")
	}
	return joinComma(many...), nil
}

func remoteWord(v string) bool {
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.EqualFold(strings.TrimSpace(p), "remote") {
			return true
		}
	}
	return false
}

func joinComma(parts ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}

func htmlSections(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		text := htmltext.Text(p, srcutil.MaxDescription)
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		if b.Len() >= srcutil.MaxDescription {
			break
		}
	}
	return htmltext.Plain(b.String(), srcutil.MaxDescription)
}
