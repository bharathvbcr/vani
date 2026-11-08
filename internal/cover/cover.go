package cover

import (
	"context"
	"errors"

	"github.com/bharathvbcr/Vani/internal/draft"
)

// Letter is a grounded cover-letter body and, when the font could draw it, a
// one-page PDF. PDF is nil when drawing failed; Body is still set. Letterhead
// text is never a field of Letter.
type Letter struct {
	Body          string
	PDF           []byte
	Model         string
	EvidenceItems int
	CompanyCheck  string
	Turns         int
	Repaired      bool
}

// Write asks d for the cover-letter body and draws it. letterhead is stamped
// into the PDF only. A draft failure returns an empty Letter: nothing usable
// was produced. A render failure returns the body and a CodeRender error.
func Write(ctx context.Context, d *draft.Drafter, job draft.Job, letterhead []string) (Letter, error) {
	if d == nil {
		return Letter{}, &Error{Code: "config", Msg: "cover: no drafter"}
	}
	res, err := d.Draft(ctx, draft.Request{Question: draft.CoverQuestion, Job: job, MaxWords: draft.CoverWords})
	if err != nil {
		return Letter{}, err
	}
	out := Letter{
		Body:          res.Draft,
		Model:         res.Model,
		EvidenceItems: res.EvidenceItems,
		CompanyCheck:  res.CompanyCheck,
		Turns:         res.Turns,
		Repaired:      res.Repaired,
	}
	pdf, err := Render(res.Draft, letterhead)
	if err != nil {
		return out, err
	}
	out.PDF = pdf
	return out, nil
}

// Kept reports whether err is a render failure that still holds the body.
func Kept(letter Letter, err error) bool {
	return err != nil && CodeOf(err) == CodeRender && letter.Body != "" && len(letter.PDF) == 0
}

// DraftFailed reports a failure that produced nothing to keep.
func DraftFailed(letter Letter, err error) bool {
	if err == nil {
		return false
	}
	var de *draft.Error
	if errors.As(err, &de) {
		return true
	}
	return letter.Body == ""
}
