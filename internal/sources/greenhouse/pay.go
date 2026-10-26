package greenhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Greenhouse returns structured pay (pay_input_ranges) only from the
// single-job endpoint with pay_transparency=true, never from the board list,
// so pay costs one request per posting. These bound that cost.
const (
	// maxPayFetchesPerBoard is the most pay requests one board makes in a run.
	maxPayFetchesPerBoard = 200
	// maxPayFailuresPerBoard stops a board's pay requests once this many have
	// failed: an endpoint that is down would otherwise be asked 200 times.
	maxPayFailuresPerBoard = 10
	// maxPayRanges is the most pay lines one posting carries.
	maxPayRanges = 16
)

// Variables only so tests can exercise them without large bodies or waits.
var (
	// maxPayBody caps one single-job response.
	maxPayBody int64 = 2 << 20
	// payRequestTimeout bounds one pay request, retries included.
	payRequestTimeout = 15 * time.Second
	// payRunBudget is the most time one run's pay fetches may take. It is one
	// budget for the source, not one per board, and it runs only while a
	// fetch is in progress. Tests shrink it.
	payRunBudget = 3 * time.Minute
	// payFanOut is how many pay requests are in flight at once. A variable
	// only so a test can force one-at-a-time and see which posting is asked first.
	payFanOut = 4
)

// Pay turns on structured pay for Greenhouse postings.
type Pay struct {
	// Known returns the posting stored for key by an earlier run. Unchanged
	// board text keeps a completed pay check: pay lines in the stored
	// description, or a remembered empty answer or 404. A skip or a failed
	// request is not a completed check, and neither is a stored body that
	// simply has no pay lines. Required: without Known every posting would
	// be fetched every run.
	Known func(posting.Key) (posting.Posting, bool)
	// Report, when set, receives each board's pay counts after List reads it.
	Report func(sources.Board, PayStats)
	// Resolved is the file that remembers completed pay checks across runs.
	// Empty keeps that memory for this process only.
	Resolved string
	// Warn receives a problem reading or writing Resolved. Nil discards it.
	// A problem there does not fail the board: the postings are still listed,
	// and an unreadable file means those bodies are asked again.
	Warn func(string, ...any)
}

// PayStats counts what one board's pay step did. Every posting the board
// listed is counted exactly once, in Reused or one of the fetch outcomes.
type PayStats struct {
	// Reused postings had unchanged board text and a completed pay check, so
	// nothing was requested.
	Reused int `json:"reused"`
	// Fetched postings were requested and answered; WithPay of them carry at
	// least one pay line (the rest publish none).
	Fetched int `json:"fetched"`
	WithPay int `json:"with_pay"`
	// BadRanges counts pay ranges left out as unusable (malformed entry, no
	// currency, no positive amount, bottom above top).
	BadRanges int `json:"bad_ranges,omitempty"`
	// Failed requests (status, decode, body cap, timeout) and NotFound ones
	// (the posting went away between the list and the request) leave the
	// posting without a pay line.
	Failed   int `json:"failed,omitempty"`
	NotFound int `json:"not_found,omitempty"`
	// Skipped postings needed a request but did not get one: the board's cap
	// was reached, the run's pay budget ran out, or the board's failure limit
	// was hit. They carry no pay line.
	Skipped int `json:"skipped,omitempty"`
	// SkipReason is why a skip happened for the cap or the budget. A
	// failure-limit skip is explained by FirstError instead. Empty when
	// nothing was skipped for those two reasons.
	SkipReason string `json:"skip_reason,omitempty"`
	// FirstError is the first failure's message, for the run log.
	FirstError string `json:"first_error,omitempty"`
}

// Why a pay fetch was skipped without a request failing.
const (
	payBudgetSpent = "pay budget spent"
	payBoardCap    = "pay board cap"
)

// NewWithPay returns the Greenhouse source with structured pay: new and
// changed postings get one bounded single-job request each, and a posting
// that publishes pay gets its ranges as the description's first lines.
func NewWithPay(d egress.Doer, p Pay) (sources.Source, error) {
	if p.Known == nil {
		return nil, errors.New("greenhouse: pay needs the stored postings (Pay.Known)")
	}
	s := &source{d: d, now: time.Now, pay: &payer{cfg: p, checks: loadPayChecks(p.Resolved, p.Warn)}}
	return s, nil
}

// payer holds a source's pay configuration and its run budget.
type payer struct {
	cfg    Pay
	checks *payChecks

	mu sync.Mutex
	// started is set by the first pay fetch. left is the pay time still
	// available. It shrinks only while a fetch is in progress, so listing
	// other boards does not spend it. started with left <= 0 means spent.
	started bool
	left    time.Duration
	// now overrides the clock. Tests set it. It must not call back into payer.
	now func() time.Time
}

// at is the payer's clock.
func (p *payer) at() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// startSpan opens a pay fetch against the run's remaining budget. open is
// false when that budget is already spent. The caller then skips, and does
// not call stopSpan.
func (p *payer) startSpan() (deadline time.Time, open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.at()
	if !p.started {
		p.started = true
		p.left = payRunBudget
	}
	if p.left <= 0 {
		return now, false
	}
	return now.Add(p.left), true
}

// stopSpan charges the time since startSpan to the budget. deadline is what
// startSpan returned.
func (p *payer) stopSpan(deadline time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	left := deadline.Sub(p.at())
	if left < 0 {
		left = 0
	}
	if left > p.left {
		left = p.left
	}
	p.left = left
}

// applyPay gives each posting its pay lines. out[i] came from jobs[i] and holds
// the description without pay. It fails only when ctx is done; any pay
// failure is counted and leaves that posting without a pay line.
//
// A posting is fetched when it is new, its board text changed, or its pay has
// not been checked. Pay lines already stored are a check. So is a remembered
// empty answer or 404. A skip or a failed request is not, and the failures are
// asked after the ones never asked, so the next run moves past them.
func (s *source) applyPay(ctx context.Context, b sources.Board, jobs []job, out []posting.Posting) error {
	p := s.pay
	var st PayStats
	var fresh, retry []int
	for i := range out {
		base := out[i].Description
		if prev, known := p.cfg.Known(out[i].Key); known {
			if desc, same := reuse(prev.Description, base); same {
				if len(storedPayLines(prev.Description)) > 0 || p.checks.done(out[i].Key, base) {
					out[i].Description = desc
					st.Reused++
					// The lines are already in the stored posting, so the check
					// can be written down. A later run that no longer has the
					// lines still knows this body was checked.
					if len(storedPayLines(prev.Description)) > 0 {
						p.checks.remember(out[i].Key, base, true, true)
					}
					continue
				}
			}
		}
		if p.checks.failed(out[i].Key, base) {
			retry = append(retry, i)
		} else {
			fresh = append(fresh, i)
		}
	}
	need := append(fresh, retry...)
	capped := 0
	if len(need) > maxPayFetchesPerBoard {
		capped = len(need) - maxPayFetchesPerBoard
		st.Skipped += capped
		need = need[:maxPayFetchesPerBoard]
	}
	if len(need) > 0 {
		lines, results, fetchSt := s.fetchPay(ctx, b, jobs, need)
		st.Fetched += fetchSt.Fetched
		st.BadRanges += fetchSt.BadRanges
		st.Failed += fetchSt.Failed
		st.NotFound += fetchSt.NotFound
		st.Skipped += fetchSt.Skipped
		st.FirstError = fetchSt.FirstError
		st.SkipReason = fetchSt.SkipReason
		for k, i := range need {
			base := out[i].Description
			switch resultAt(results, k) {
			case payChecked:
				if len(lines[k]) > 0 {
					out[i].Description = withPay(lines[k], base)
					st.WithPay++
					// Not written to the file yet: reconcile may still fail
					// before these lines are stored. The next run sees them
					// and writes the check then.
					p.checks.remember(out[i].Key, base, true, false)
				} else {
					p.checks.remember(out[i].Key, base, true, true)
				}
			case payFailedTry:
				p.checks.remember(out[i].Key, base, false, true)
			}
		}
	}
	if st.SkipReason == "" && capped > 0 {
		st.SkipReason = payBoardCap
	}
	p.checks.save()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.cfg.Report != nil {
		p.cfg.Report(b, st)
	}
	return nil
}

// payResult is what one pay request settled as. The zero value is a skip:
// nothing was asked, so nothing is remembered.
type payResult int

const (
	payMissed payResult = iota
	payChecked
	payFailedTry
)

func resultAt(r []payResult, k int) payResult {
	if k < 0 || k >= len(r) {
		return payMissed
	}
	return r[k]
}

// fetchPay requests pay for jobs[need[k]] with bounded fan-out, under the run
// budget, and returns each one's pay lines and how the request settled, by k.
func (s *source) fetchPay(ctx context.Context, b sources.Board, jobs []job, need []int) ([][]string, []payResult, PayStats) {
	lines := make([][]string, len(need))
	deadline, open := s.pay.startSpan()
	if !open {
		return lines, nil, PayStats{Skipped: len(need), SkipReason: payBudgetSpent}
	}
	defer s.pay.stopSpan(deadline)
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	results := make([]payResult, len(need))
	var (
		mu     sync.Mutex
		st     PayStats
		failed int
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		st.Failed++
		failed++
		if st.FirstError == "" {
			st.FirstError = err.Error()
		}
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(payFanOut, len(need)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range work {
				mu.Lock()
				stop := failed >= maxPayFailuresPerBoard
				mu.Unlock()
				if stop || runCtx.Err() != nil {
					mu.Lock()
					st.Skipped++
					if !stop {
						st.SkipReason = payBudgetSpent
					}
					mu.Unlock()
					continue
				}
				got, bad, err := s.fetchOne(runCtx, b, jobs[need[k]].ID.String())
				switch {
				case err == nil:
					lines[k] = got
					results[k] = payChecked
					mu.Lock()
					st.Fetched++
					st.BadRanges += bad
					mu.Unlock()
				case errors.Is(err, srcutil.ErrBoardNotFound):
					results[k] = payChecked
					mu.Lock()
					st.NotFound++
					mu.Unlock()
				case runCtx.Err() != nil && ctx.Err() == nil:
					// The run's pay budget ended under this request.
					mu.Lock()
					st.Skipped++
					st.SkipReason = payBudgetSpent
					mu.Unlock()
				case ctx.Err() != nil:
					// The run was cancelled. That is not a failed check.
					fail(err)
				default:
					results[k] = payFailedTry
					fail(err)
				}
			}
		}()
	}
	for k := range need {
		work <- k
	}
	close(work)
	wg.Wait()
	return lines, results, st
}

type payReply struct {
	PayInputRanges []json.RawMessage `json:"pay_input_ranges"`
}

type payRange struct {
	MinCents     *json.Number `json:"min_cents"`
	MaxCents     *json.Number `json:"max_cents"`
	CurrencyType string       `json:"currency_type"`
	Title        string       `json:"title"`
}

// fetchOne requests one posting's pay and renders each usable range as a
// PayLine. bad counts ranges left out. A posting that publishes no pay is
// no lines and no error.
func (s *source) fetchOne(ctx context.Context, b sources.Board, id string) (lines []string, bad int, err error) {
	ctx, cancel := context.WithTimeout(ctx, payRequestTimeout)
	defer cancel()
	endpoint := baseURL + url.PathEscape(b.Token) + "/jobs/" + url.PathEscape(id) + "?pay_transparency=true"
	var r payReply
	if err := srcutil.GetJSONLimit(ctx, s.d, endpoint, maxPayBody, &r); err != nil {
		return nil, 0, fmt.Errorf("job %s pay: %w", id, err)
	}
	seen := map[string]bool{}
	for _, raw := range r.PayInputRanges {
		var pr payRange
		if err := json.Unmarshal(raw, &pr); err != nil {
			bad++
			continue
		}
		line := srcutil.PayLine(pr.CurrencyType, units(pr.MinCents), units(pr.MaxCents), period(pr.Title))
		if line == "" {
			bad++
			continue
		}
		if seen[line] || len(lines) >= maxPayRanges {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return lines, bad, nil
}

// units converts a cents amount to currency units; nil when absent or not a
// number (PayLine then treats that end as missing).
func units(cents *json.Number) *float64 {
	if cents == nil {
		return nil
	}
	c, err := strconv.ParseFloat(cents.String(), 64)
	if err != nil {
		return nil
	}
	v := c / 100
	return &v
}

// period reads a pay period from a range's title, by whole word only: titles
// are free text ("Annual Salary:", "Zone 2 (Denver, CO; ...)"), and a place
// such as Daytona Beach must not read as "per day". No period word is "".
func period(title string) string {
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return r < 'a' || r > 'z' }) {
		switch w {
		case "hour", "hourly":
			return "hour"
		case "annual", "annually", "year", "yearly":
			return "year"
		case "month", "monthly":
			return "month"
		case "week", "weekly":
			return "week"
		case "daily":
			return "day"
		}
	}
	return ""
}

// withPay puts the pay lines ahead of the description, as the Lever and Ashby
// adapters do, so the description cap never cuts them.
func withPay(lines []string, desc string) string {
	return htmltext.Plain(strings.Join(lines, "\n")+"\n\n"+desc, srcutil.MaxDescription)
}

// reuse reports whether a posting's content is unchanged since it was stored,
// and if so returns the stored description: base (today's description without
// pay) either is the stored description, or is it once the stored pay lines
// are put back. Comparing composed descriptions by hash, rather than peeling
// the pay lines off, keeps the description cap's cut out of the question.
func reuse(stored, base string) (string, bool) {
	want := posting.Posting{Description: stored}.DescriptionHash()
	if (posting.Posting{Description: base}).DescriptionHash() == want {
		return base, true
	}
	if lines := storedPayLines(stored); len(lines) > 0 {
		if desc := withPay(lines, base); (posting.Posting{Description: desc}).DescriptionHash() == want {
			return desc, true
		}
	}
	return "", false
}

// storedPayLines returns the leading pay lines of a stored description.
func storedPayLines(desc string) []string {
	var lines []string
	for _, line := range strings.SplitN(desc, "\n", maxPayRanges+1) {
		if !strings.HasPrefix(line, srcutil.PayLinePrefix) {
			break
		}
		lines = append(lines, line)
	}
	return lines
}
