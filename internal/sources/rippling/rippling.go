// Package rippling lists postings from a public Rippling board
// (GET https://api.rippling.com/platform/api/ats/v1/board/{slug}/jobs).
// The list has no description, so each job costs a detail request, and any
// detail that cannot be read fails the board: a partial list would age the
// missing jobs out.
//
// Detail volume is bounded as Workday's is (srcutil.PlanDetails): a source
// from New fails a board that needs more than MaxDetails detail requests; one
// from NewWith reuses the postings an earlier run stored (re-reading each on
// its turn, once every srcutil.RefreshDays days), drops new jobs DropListed
// rejects from their list row without a request, and defers new jobs past
// MaxDetails to the next run. Stored postings are matched by key
// (rp:{token}:{uuid}), which the list row carries, so a deferred row is never
// a stored one. The list has no updated stamp, so an edited description is
// seen on the job's next turn.
package rippling

import (
	"context"
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

// ErrBoardNotFound is returned when Rippling has no such board.
var ErrBoardNotFound = srcutil.ErrBoardNotFound

const baseURL = "https://api.rippling.com/platform/api/ats/v1/board/"

// maxDetailBody caps one job detail. The list is capped by srcutil.MaxBody.
var maxDetailBody int64 = 2 << 20

// MaxDetails is the most detail requests one board spends in one run. At
// egress's one-second per-host floor that is five minutes; before the bound,
// Rippling's own board spent about six minutes on details every run
// (2026-10-27).
const MaxDetails = 300

// Stats counts what one List did. Every distinct listed job is counted once,
// in Reused, Fetched, Refreshed, Prefiltered, Deferred or Unlisted.
type Stats struct {
	Listed int `json:"listed"`
	// Reused jobs were returned as stored, with no request.
	Reused int `json:"reused"`
	// Fetched jobs were new and got a detail request; Refreshed jobs were
	// stored and were re-read on their turn.
	Fetched   int `json:"fetched"`
	Refreshed int `json:"refreshed,omitempty"`
	// Prefiltered jobs were new and dropped by DropListed without a request.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Deferred jobs were new but past MaxDetails: fetched on a later run. Not
	// being stored, their absence ages nothing.
	Deferred int `json:"deferred,omitempty"`
	// Unlisted jobs' details said they are hidden from search: not returned.
	Unlisted int `json:"unlisted,omitempty"`
}

// Options turn on the bounded detail step (NewWith).
type Options struct {
	// Stored returns the postings an earlier run stored for b: every record
	// under b.KeyPrefix(), whatever its status. Required.
	Stored func(b sources.Board) []posting.Posting
	// DropListed reports whether the engine drops a posting before triage from
	// what a list row says: its title, the board's company and its location
	// labels (no description). A new job it drops is neither fetched nor
	// returned. Nil drops nothing.
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

// New returns the Rippling source, sending requests through d. Every listed
// job costs a detail request, and a board needing more than MaxDetails fails.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

// NewWith returns the source with the bounded detail step: stored postings are
// reused (a share of them re-read each run), new jobs DropListed rejects are
// never fetched, and at most MaxDetails detail requests go out per board per
// run.
func NewWith(d egress.Doer, o Options) (sources.Source, error) {
	if o.Stored == nil {
		return nil, errors.New("rippling: the bounded detail step needs the stored postings (Options.Stored)")
	}
	return &source{d: d, now: time.Now, opt: &o}, nil
}

func (*source) ATS() posting.ATS { return posting.Rippling }

type listJob struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Department struct {
		Label string `json:"label"`
	} `json:"department"`
	WorkLocation struct {
		Label string `json:"label"`
	} `json:"workLocation"`
}

type detail struct {
	UUID       string   `json:"uuid"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Unlisted   bool     `json:"unlistedFromSearch"`
	CreatedOn  string   `json:"createdOn"`
	Company    string   `json:"companyName"`
	Locations  []string `json:"workLocations"`
	Department struct {
		Name string `json:"name"`
	} `json:"department"`
	Description struct {
		Company string `json:"company"`
		Role    string `json:"role"`
	} `json:"description"`
}

func jobsURL(token string) string {
	return baseURL + url.PathEscape(token) + "/jobs"
}

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := srcutil.CheckBoard(b, posting.Rippling); err != nil {
		return nil, err
	}
	var jobs []listJob
	if err := srcutil.GetJSON(ctx, s.d, jobsURL(b.Token), &jobs); err != nil {
		return nil, fmt.Errorf("rippling %q: %w", b.Token, err)
	}
	if jobs == nil {
		return nil, fmt.Errorf("rippling %q: payload is not a jobs array", b.Token)
	}
	if err := srcutil.CheckCount(len(jobs)); err != nil {
		return nil, fmt.Errorf("rippling %q: %w", b.Token, err)
	}
	// Rippling lists a job once per work location, each row with the same
	// uuid (its own board, 2026-10-27: 134 of 654 rows). Rows sharing a uuid
	// are one job only when they agree on all but the location; the labels
	// are kept as the location's fallback.
	var uniq []listJob
	at := map[string]int{}
	places := map[string][]string{}
	for i, j := range jobs {
		id := strings.TrimSpace(j.UUID)
		if _, err := posting.RipplingKey(b.Token, id); err != nil {
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		k := strings.ToLower(id)
		if n, ok := at[k]; ok {
			if !sameJob(uniq[n], j) {
				return nil, fmt.Errorf("rippling %q: job %d: uuid %q repeats with a different title, link or department", b.Token, i, id)
			}
		} else {
			at[k] = len(uniq)
			uniq = append(uniq, j)
		}
		places[k] = append(places[k], j.WorkLocation.Label)
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
	keys := make([]posting.Key, len(uniq))
	plan := make([]srcutil.DetailRow, len(uniq))
	for i, j := range uniq {
		id := strings.TrimSpace(j.UUID)
		key, _ := posting.RipplingKey(b.Token, id) // checked above
		keys[i] = key
		_, ok := stored[key]
		plan[i] = srcutil.DetailRow{ID: strings.ToLower(id), Stored: ok}
		if !ok && s.opt != nil && s.opt.DropListed != nil {
			plan[i].Drop = s.opt.DropListed(listedPosting(b, j, places[strings.ToLower(id)]))
		}
	}
	pl := srcutil.PlanDetails(plan, srcutil.Day(seen), MaxDetails)
	if s.opt == nil && pl.Deferred > 0 {
		// Without stored postings a deferred job cannot be told from a stored
		// one, and leaving out a stored one would age it as absent.
		return nil, fmt.Errorf("rippling %q: %w: the board lists %d jobs and each costs a detail request, over the %d limit",
			b.Token, srcutil.ErrTooManyPostings, pl.New+pl.Deferred, MaxDetails)
	}
	st := Stats{Listed: len(uniq), Deferred: pl.Deferred, Prefiltered: pl.Prefiltered}
	if s.opt != nil && s.opt.Report != nil {
		defer func() { s.opt.Report(b, st) }()
	}
	slot := make([]*posting.Posting, len(uniq))
	for _, i := range pl.Reuse {
		p := stored[keys[i]]
		p.SeenAt = seen
		slot[i] = &p
		st.Reused++
	}
	for n, i := range pl.Fetch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		j := uniq[i]
		var d detail
		endpoint := jobsURL(b.Token) + "/" + url.PathEscape(strings.TrimSpace(j.UUID))
		if err := srcutil.GetJSONLimit(ctx, s.d, endpoint, maxDetailBody, &d); err != nil {
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				err = fmt.Errorf("job %s is not on the board", strings.TrimSpace(j.UUID))
			}
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		p, keep, err := d.posting(b, j, places[strings.ToLower(strings.TrimSpace(j.UUID))], seen)
		if err != nil {
			return nil, fmt.Errorf("rippling %q: job %d: %w", b.Token, i, err)
		}
		switch {
		case !keep:
			st.Unlisted++
			continue
		case n >= pl.New:
			st.Refreshed++
		default:
			st.Fetched++
		}
		slot[i] = &p
	}
	out := make([]posting.Posting, 0, len(uniq))
	for _, p := range slot {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// listedPosting is what a list row says of its job before any detail: the
// fields DropListed may judge.
func listedPosting(b sources.Board, j listJob, places []string) posting.Posting {
	return posting.Posting{
		ATS: posting.Rippling, Company: b.Company,
		Title: srcutil.Title(strings.TrimSpace(j.Name)), Location: joinParts("; ", places),
	}
}

// sameJob reports whether two list rows with one uuid describe the same job:
// the same uuid as written, title, link and department.
func sameJob(a, b listJob) bool {
	return strings.TrimSpace(a.UUID) == strings.TrimSpace(b.UUID) &&
		strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name) &&
		strings.TrimSpace(a.URL) == strings.TrimSpace(b.URL) &&
		strings.TrimSpace(a.Department.Label) == strings.TrimSpace(b.Department.Label)
}

// posting builds the job's posting from its detail, falling back to the list
// row and listPlaces, the location labels of every row the job was listed in.
func (d detail) posting(b sources.Board, list listJob, listPlaces []string, seen time.Time) (posting.Posting, bool, error) {
	if d.Unlisted {
		return posting.Posting{}, false, nil
	}
	id := strings.TrimSpace(list.UUID)
	if got := strings.TrimSpace(d.UUID); got != "" && !strings.EqualFold(got, id) {
		return posting.Posting{}, false, fmt.Errorf("detail uuid %q does not match %q", got, id)
	}
	key, err := posting.RipplingKey(b.Token, id)
	if err != nil {
		return posting.Posting{}, false, err
	}
	if !strings.HasPrefix(string(key), b.KeyPrefix()) {
		return posting.Posting{}, false, fmt.Errorf("key %q is outside %q", key, b.KeyPrefix())
	}
	title := strings.TrimSpace(d.Name)
	if title == "" {
		title = strings.TrimSpace(list.Name)
	}
	if title == "" {
		return posting.Posting{}, false, errors.New("empty title")
	}
	rawURL := strings.TrimSpace(d.URL)
	if rawURL == "" {
		rawURL = strings.TrimSpace(list.URL)
	}
	if rawURL == "" {
		return posting.Posting{}, false, errors.New("empty url")
	}
	if err := srcutil.CheckURL("url", rawURL); err != nil {
		return posting.Posting{}, false, err
	}
	posted, err := srcutil.ParseTime("createdOn", strings.TrimSpace(d.CreatedOn))
	if err != nil {
		return posting.Posting{}, false, err
	}
	loc := joinParts("; ", d.Locations)
	if loc == "" {
		loc = joinParts("; ", listPlaces)
	}
	dept := strings.TrimSpace(d.Department.Name)
	if dept == "" {
		dept = strings.TrimSpace(list.Department.Label)
	}
	desc := htmltext.Text(d.Description.Role, srcutil.MaxDescription)
	if desc == "" {
		desc = htmltext.Text(d.Description.Company, srcutil.MaxDescription)
	}
	company := strings.TrimSpace(d.Company)
	if company == "" {
		company = b.Company
	}
	return posting.Posting{
		Key:         key,
		ATS:         posting.Rippling,
		Company:     company,
		Title:       srcutil.Title(title),
		Location:    loc,
		Department:  dept,
		URL:         rawURL,
		PostedAt:    posted,
		Description: desc,
		SeenAt:      seen,
	}, true, nil
}

func joinParts(sep string, parts []string) string {
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
	return strings.Join(out, sep)
}
