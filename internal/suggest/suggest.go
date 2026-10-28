// Package suggest answers "what can Vani put in these new form fields?" for one
// application page. Each free-text field is triaged by the decision model
// (fieldtriage: label and type only), and the engine rule here decides what
// follows: only an "essay" reading is drafted by the LLM, and every other
// reading, an uncertain one included, leaves the field for the applicant.
// Drafts are suggestions the extension fills for review, never as confident
// answers.
package suggest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// Bounds. All are ceilings.
const (
	MaxFields         = 60
	DefaultDrafts     = 3
	MaxDrafts         = 5
	TriageConcurrency = 4
	DefaultTriage     = 45 * time.Second
	// MinDescriptionRunes is the shortest posting a draft is written from.
	MinDescriptionRunes = 40
)

// Statuses of one field.
const (
	Drafted = "drafted" // Draft holds a suggestion to review
	Manual  = "manual"  // the applicant answers it; Reason says why
	Unsure  = "unsure"  // the decision model gave no reading; kept for the applicant
	Failed  = "failed"  // the draft failed; Code says why
	Skipped = "skipped" // an essay over this page's draft cap
)

// Request is one page's new fields and the posting they belong to.
type Request struct {
	Job       draft.Job           `json:"job"`
	Fields    []fieldtriage.Field `json:"fields"`
	MaxDrafts int                 `json:"max_drafts,omitempty"`
}

// Suggestion is the outcome for one field.
type Suggestion struct {
	ID            string `json:"id"`
	Category      string `json:"category,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Backend       string `json:"backend,omitempty"`
	Draft         string `json:"draft,omitempty"`
	Model         string `json:"model,omitempty"`
	EvidenceItems int    `json:"evidence_items,omitempty"`
	Code          string `json:"code,omitempty"`
	// Kind says what a draft.CodeUngrounded refusal found unbacked (draft.Kind*).
	Kind string `json:"kind,omitempty"`
}

// Drafter writes one draft (draft.Drafter in production).
type Drafter interface {
	Draft(ctx context.Context, r draft.Request) (draft.Result, error)
}

// Engine triages and drafts. D and Drafter are required.
type Engine struct {
	D       decide.Decider
	Drafter Drafter
	// Triage bounds the whole triage phase; default DefaultTriage.
	Triage time.Duration
}

// ErrBadRequest marks a request the engine refuses before asking anything.
var ErrBadRequest = errors.New("suggest: bad request")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadRequest}, a...)...)
}

// Validate checks req's bounds and normalizes it in place.
func (req *Request) Validate() error {
	if len(req.Fields) == 0 {
		return bad("no fields")
	}
	if len(req.Fields) > MaxFields {
		return bad("%d fields, max %d", len(req.Fields), MaxFields)
	}
	seen := make(map[string]bool, len(req.Fields))
	for i := range req.Fields {
		f := &req.Fields[i]
		if err := f.Validate(); err != nil {
			return bad("%v", err)
		}
		if seen[f.ID] {
			return bad("field id %q is repeated", f.ID)
		}
		seen[f.ID] = true
	}
	switch {
	case req.MaxDrafts == 0:
		req.MaxDrafts = DefaultDrafts
	case req.MaxDrafts < 0 || req.MaxDrafts > MaxDrafts:
		return bad("max_drafts %d outside [1, %d]", req.MaxDrafts, MaxDrafts)
	}
	if utf8.RuneCountInString(req.Job.Company) > draft.MaxCompanyRunes || utf8.RuneCountInString(req.Job.Title) > draft.MaxTitleRunes {
		return bad("the company or job title is too long")
	}
	return nil
}

// manualReason is the reason for a field the decision model read as a
// category that is never drafted.
var manualReason = map[string]string{
	fieldtriage.Fact:     "A detail about you: answer it once and it is saved for the next form.",
	fieldtriage.WorkAuth: "Work authorization: answer it yourself. Vani never drafts it.",
	fieldtriage.Consent:  "An agreement or policy: read it and answer it yourself.",
	fieldtriage.SelfID:   "Voluntary self-identification: answer it yourself if you choose to.",
	fieldtriage.Other:    "Not a question Vani drafts.",
}

// guardedReason is the reason for a label the guard patterns pinned: unlike a
// model reading, these labels were not sent to the decision model at all.
func guardedReason(category string) string {
	if category == fieldtriage.WorkAuth {
		return "Work authorization: answer it yourself. The question was not sent to a model."
	}
	return manualReason[category]
}

// Run triages every field, then drafts the essays in page order up to the
// page's cap. It returns one Suggestion per field, in request order. It never
// drafts a field the decision model did not read as an essay.
func (e Engine) Run(ctx context.Context, req Request) ([]Suggestion, error) {
	if e.D == nil || e.Drafter == nil {
		return nil, errors.New("suggest: a decider and a drafter are required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	out := e.triage(ctx, req.Fields)

	hasPosting := utf8.RuneCountInString(req.Job.Description) >= MinDescriptionRunes
	drafted, stop := 0, ""
	for i := range out {
		s := &out[i]
		if s.Status != "" {
			continue // decided in triage (guarded or unsure)
		}
		// The one place a draft is decided: only an essay reading of a label no
		// sensitive pattern flags (a pattern the guard missed still stops here).
		if s.Category != fieldtriage.Essay {
			s.Status, s.Reason = Manual, manualReason[s.Category]
			continue
		}
		if c := fieldtriage.Sensitive(req.Fields[i].Label); c != "" {
			s.Category, s.Status, s.Reason = c, Manual, manualReason[c]
			continue
		}
		switch {
		case !hasPosting:
			s.Status, s.Reason = Manual, "No job description was found on this page to draft from."
		case stop != "":
			s.Status, s.Code, s.Reason = Failed, stop, "Not drafted: an earlier draft on this page hit the same limit."
		case drafted >= req.MaxDrafts:
			s.Status, s.Reason = Skipped, fmt.Sprintf("Only %d drafts are written per page. Use Draft on this one.", req.MaxDrafts)
		default:
			drafted++
			res, err := e.Drafter.Draft(ctx, draft.Request{Question: req.Fields[i].Label, Job: req.Job})
			if err != nil {
				code := draft.CodeProvider
				var de *draft.Error
				if errors.As(err, &de) {
					code = de.Code
				}
				s.Status, s.Code, s.Kind, s.Reason = Failed, code, draft.KindOf(err), err.Error()
				// A spent budget or a cancelled request will refuse every later draft too.
				if code == draft.CodeBudget || ctx.Err() != nil {
					stop = code
				}
				continue
			}
			s.Status, s.Draft, s.Model, s.EvidenceItems = Drafted, res.Draft, res.Model, res.EvidenceItems
		}
	}
	return out, nil
}

// triage fills Category for every field, and Status for fields it settles
// itself: guarded labels (never asked) and unread ones (unsure).
func (e Engine) triage(ctx context.Context, fields []fieldtriage.Field) []Suggestion {
	timeout := e.Triage
	if timeout <= 0 {
		timeout = DefaultTriage
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := make([]Suggestion, len(fields))
	sem := make(chan struct{}, TriageConcurrency)
	var wg sync.WaitGroup
	for i, f := range fields {
		out[i].ID = f.ID
		if g := fieldtriage.Guard(f.Label); g != "" {
			out[i].Category, out[i].Status, out[i].Reason = g, Manual, guardedReason(g)
			continue
		}
		wg.Add(1)
		go func(i int, f fieldtriage.Field) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i].Status, out[i].Reason = Unsure, "The decision model was not asked in time; answer it yourself."
				return
			}
			out[i] = read(ctx, e.D, f)
		}(i, f)
	}
	wg.Wait()
	return out
}

// read asks one field and maps the reading. Anything but an answered value
// is "unsure": an unread field is kept for the applicant, never drafted.
func read(ctx context.Context, d decide.Decider, f fieldtriage.Field) Suggestion {
	s := Suggestion{ID: f.ID}
	ans, err := d.Decide(ctx, fieldtriage.Question(f))
	if err != nil {
		s.Status, s.Reason = Unsure, "The decision model is unavailable; answer it yourself."
		return s
	}
	s.Backend = ans.Backend
	r, ok := ans.Slots[fieldtriage.Slot]
	if !ok || r.Reading != decide.Answered {
		reading := decide.NotAsked
		if ok {
			reading = r.Reading
		}
		s.Status, s.Reason = Unsure, fmt.Sprintf("The decision model gave no reading (%s); answer it yourself.", reading)
		return s
	}
	if _, known := manualReason[r.Value]; !known && r.Value != fieldtriage.Essay {
		s.Status, s.Reason = Unsure, "The decision model answered outside the options; answer it yourself."
		return s
	}
	s.Category = r.Value
	return s
}
