package ashby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/Vani/internal/applyform"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// The job page's GraphQL carries a posting's application form beside the
// posting itself, with no session (checked 2026-10-27 on the "ashby" board:
// jobPosting.applicationForm.sections[].fieldEntries[] is {isRequired, field},
// and each field has id, path, title, type, and selectableValues for a select).
// The posting API has no form, so a board listed through it costs one request
// per posting to read it; a board read through the job-board GraphQL gets it in
// the detail request it already sends.
//
// A form that cannot be read leaves the posting whole with no summary
// (posting.Posting.Form nil, "form not read"): it never fails the board and is
// never taken for a form with no knock-outs.

// formFields is the part of the posting query that asks for the form.
const formFields = `applicationForm { sections { title fieldEntries { ... on FormFieldEntry { isRequired field } } } }`

// Bounds on what one form read scans and on the requests a run spends.
const (
	maxFormSections = 50
	maxFormEntries  = 300
	// MaxFormsPerBoard is the most form requests one board spends in one run;
	// MaxFormsPerRun is the most the whole run spends. A posting past either is
	// read on a later run: its form is "not read" until then.
	MaxFormsPerBoard = 60
	MaxFormsPerRun   = 400
	// maxFormFailures stops a board's form requests once this many have failed:
	// an endpoint that is down would otherwise be asked 60 times.
	maxFormFailures = 5
)

// Variables only so tests can shrink them.
var (
	// maxFormBody caps one form response.
	maxFormBody int64 = 1 << 20
	// formRequestTimeout bounds one form request, retries included.
	formRequestTimeout = 15 * time.Second
)

// FormOptions turns on the application-form read for boards listed through the
// posting API (NewWithForms).
type FormOptions struct {
	// Known returns the posting an earlier run stored for key. A stored form is
	// reused while the posting's text is unchanged, so only new and changed
	// postings cost a request. Required: without it every posting would be read
	// every run.
	Known func(posting.Key) (posting.Posting, bool)
	// Skip reports a listed posting the engine drops before triage; its form is
	// never read. Nil skips nothing.
	Skip func(posting.Posting) bool
	// Report, when set, receives each board's form counts after List reads it.
	// The counts carry no question text.
	Report func(sources.Board, FormStats)
}

// FormStats counts what one board's form step did. Every posting the posting
// API listed is counted once.
type FormStats struct {
	// Reused postings carried the form an earlier run read, their text unchanged.
	Reused int `json:"reused"`
	// Fetched postings were asked and answered with a form.
	Fetched int `json:"fetched"`
	// Unread postings were answered with no usable form (none, or one whose
	// shape could not be read).
	Unread int `json:"unread,omitempty"`
	// Prefiltered postings were dropped by Skip and not asked.
	Prefiltered int `json:"prefiltered,omitempty"`
	// Failed requests (status, decode, body cap, timeout) and NotFound ones (the
	// posting closed since the listing) leave the posting with no summary.
	Failed   int `json:"failed,omitempty"`
	NotFound int `json:"not_found,omitempty"`
	// Skipped postings needed a request but did not get one: a cap was reached or
	// the board's failure limit was hit.
	Skipped    int    `json:"skipped,omitempty"`
	FirstError string `json:"first_error,omitempty"`
}

// formReader holds a source's form configuration and its run budget.
type formReader struct {
	opt  FormOptions
	left atomic.Int64
}

// NewWithForms returns the Ashby source with the application-form read for
// boards listed through the posting API: new and changed postings get one
// bounded request each, at the egress client's per-host pace.
func NewWithForms(d egress.Doer, o FormOptions) (sources.Source, error) {
	if o.Known == nil {
		return nil, errors.New("ashby: the form read needs the stored postings (FormOptions.Known)")
	}
	s := &source{d: d, now: time.Now, forms: &formReader{opt: o}}
	s.forms.left.Store(MaxFormsPerRun)
	return s, nil
}

type formPayload struct {
	Data struct {
		JobPosting *struct {
			ID              string          `json:"id"`
			ApplicationForm json.RawMessage `json:"applicationForm"`
		} `json:"jobPosting"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

// read fills in the form of each posting of out (ids[i] is the board's id of
// out[i]), asking the job page for those that need it.
func (r *formReader) read(ctx context.Context, s *source, b sources.Board, out []posting.Posting, ids []string) error {
	var st FormStats
	defer func() {
		if r.opt.Report != nil {
			r.opt.Report(b, st)
		}
	}()
	asked := 0
	for i := range out {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := &out[i]
		stored, known := r.opt.Known(p.Key)
		if known && stored.Form.Usable() && (stored.Description == "" || stored.DescriptionHash() == p.DescriptionHash()) {
			p.Form = stored.Form
			st.Reused++
			continue
		}
		if r.opt.Skip != nil && r.opt.Skip(*p) {
			st.Prefiltered++
			continue
		}
		if asked >= MaxFormsPerBoard || st.Failed >= maxFormFailures || r.left.Add(-1) < 0 {
			st.Skipped++
			continue
		}
		asked++
		form, err := s.fetchForm(ctx, b, ids[i])
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, srcutil.ErrBoardNotFound):
			st.NotFound++
		case err != nil:
			st.Failed++
			if st.FirstError == "" {
				st.FirstError = err.Error()
			}
		case form == nil:
			st.Unread++
		default:
			p.Form = form
			st.Fetched++
		}
	}
	return nil
}

// fetchForm asks the job page for one posting's form. A nil form with a nil
// error is a posting answered without a usable form.
func (s *source) fetchForm(ctx context.Context, b sources.Board, id string) (*posting.ApplyForm, error) {
	ctx, cancel := context.WithTimeout(ctx, formRequestTimeout)
	defer cancel()
	req := gqlRequest{
		OperationName: jobPostingOp,
		Query: `query ApiJobPosting($organizationHostedJobsPageName: String!, $jobPostingId: String!) {
  jobPosting(organizationHostedJobsPageName: $organizationHostedJobsPageName, jobPostingId: $jobPostingId) {
    id ` + formFields + `
  }
}`,
		Variables: map[string]any{
			"organizationHostedJobsPageName": b.APIName(),
			"jobPostingId":                   id,
		},
	}
	var resp formPayload
	if err := srcutil.PostJSONLimit(ctx, s.d, graphQLURL(jobPostingOp), req, nil, maxFormBody, &resp); err != nil {
		return nil, fmt.Errorf("form read: %w", err)
	}
	if resp.Data.JobPosting == nil {
		if msg := gqlMessage(resp.Errors); msg != "" {
			return nil, fmt.Errorf("form read: %s", msg)
		}
		return nil, srcutil.ErrBoardNotFound
	}
	if got := strings.TrimSpace(resp.Data.JobPosting.ID); got != "" && !strings.EqualFold(got, strings.TrimSpace(id)) {
		return nil, fmt.Errorf("form read: detail id %q does not match %q", got, id)
	}
	return parseForm(resp.Data.JobPosting.ApplicationForm), nil
}

// formEntry is one fieldEntries member. Its field is a JSON object whose type
// decides the question; a member that is not that shape is counted, not fatal.
type formEntry struct {
	IsRequired bool `json:"isRequired"`
	Field      *struct {
		Path   string `json:"path"`
		Title  string `json:"title"`
		Type   string `json:"type"`
		Values []struct {
			Label      string `json:"label"`
			Value      string `json:"value"`
			IsArchived bool   `json:"isArchived"`
		} `json:"selectableValues"`
	} `json:"field"`
}

// basicPaths are the applicant basics every form has: left out of the counts.
var basicPaths = map[string]bool{
	"_systemfield_name": true, "_systemfield_email": true, "_systemfield_resume": true,
}

// parseForm reads the applicationForm of a posting reply into the summary. It
// returns nil, "form not read", when the form is null or absent, is not the
// shape {sections: [{fieldEntries: [...]}]}, or has no readable entry: a
// changed shape must not look like a form with no knock-outs.
func parseForm(raw json.RawMessage) *posting.ApplyForm {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var form struct {
		Sections []struct {
			FieldEntries []json.RawMessage `json:"fieldEntries"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &form); err != nil || form.Sections == nil {
		return nil
	}
	var qs []applyform.Question
	incomplete := len(form.Sections) > maxFormSections
	for si, sec := range form.Sections {
		if si >= maxFormSections {
			break
		}
		for _, rawEntry := range sec.FieldEntries {
			if len(qs) >= maxFormEntries {
				incomplete = true
				break
			}
			var e formEntry
			if err := json.Unmarshal(rawEntry, &e); err != nil || e.Field == nil || e.Field.Type == "" {
				incomplete = true
				continue
			}
			qs = append(qs, e.question())
		}
	}
	return applyform.Summarize(qs, incomplete)
}

func (e formEntry) question() applyform.Question {
	f := e.Field
	q := applyform.Question{Label: f.Title, Required: e.IsRequired, Basic: basicPaths[f.Path]}
	switch f.Type {
	case "LongText":
		q.Type = applyform.LongText
	case "Boolean":
		q.Type = applyform.YesNo
	case "File":
		q.Type = applyform.File
	case "ValueSelect", "MultiValueSelect":
		q.Type = applyform.Choice
		for _, v := range f.Values {
			if v.IsArchived {
				continue
			}
			label := v.Label
			if strings.TrimSpace(label) == "" {
				label = v.Value
			}
			q.Options = append(q.Options, label)
		}
		q.OptionsTotal = len(q.Options)
	}
	return q
}
