package resume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// modelArgs are the arguments shared by the scoring tools. PageBudget is
// pinned; PersonaID is omitted when empty so DevPrism applies its default.
type modelArgs struct {
	JD         string `json:"jd_text"`
	PersonaID  string `json:"persona_id,omitempty"`
	PageBudget int    `json:"page_budget"`
}

// Gap calls resume_gap_analysis. Mapping (output shape: mcp/tools_resume.rs
// :637-647, items in career_match/gap.rs:51-69):
//
//	coveragePercentage -> Gap.CoveragePct
//	mustHave.items[]   -> Gap.MustHaves {skill -> Requirement, status -> Status}
//
// Status is "covered", "weak" or "missing" (gap.rs:21-27).
func (c *Client) Gap(ctx context.Context, jd, personaID string) (Gap, error) {
	raw, err := c.CallTool(ctx, "resume_gap_analysis", modelArgs{JD: jd, PersonaID: personaID, PageBudget: PageBudget})
	if err != nil {
		return Gap{}, err
	}
	var out struct {
		CoveragePercentage *float64 `json:"coveragePercentage"`
		MustHave           *struct {
			Items []struct {
				Skill  string `json:"skill"`
				Status string `json:"status"`
			} `json:"items"`
		} `json:"mustHave"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.CoveragePercentage == nil || out.MustHave == nil {
		return Gap{}, fmt.Errorf("%w: resume_gap_analysis", ErrMalformed)
	}
	g := Gap{CoveragePct: *out.CoveragePercentage, MustHaves: make([]Coverage, 0, len(out.MustHave.Items))}
	for _, it := range out.MustHave.Items {
		switch it.Status {
		case "covered", "weak", "missing":
		default:
			return Gap{}, fmt.Errorf("%w: resume_gap_analysis status %q", ErrMalformed, bound(it.Status))
		}
		g.MustHaves = append(g.MustHaves, Coverage{Requirement: it.Skill, Status: it.Status})
	}
	return g, nil
}

// Score calls resume_score_and_select. Only BlockIDs is mapped
// (selectedBlocks[].blockId, mcp/tools_resume.rs:716-729). The output has no
// aggregate score and no aggregate components (only per-block "score" and
// "scoreComponents"), so Total and Components stay zero rather than being
// invented; rank on Gap.CoveragePct and the uncovered must-haves instead.
func (c *Client) Score(ctx context.Context, jd, personaID string) (Score, error) {
	raw, err := c.CallTool(ctx, "resume_score_and_select", modelArgs{JD: jd, PersonaID: personaID, PageBudget: PageBudget})
	if err != nil {
		return Score{}, err
	}
	var out struct {
		SelectedBlocks *[]struct {
			BlockID string `json:"blockId"`
		} `json:"selectedBlocks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.SelectedBlocks == nil {
		return Score{}, fmt.Errorf("%w: resume_score_and_select", ErrMalformed)
	}
	s := Score{}
	for _, b := range *out.SelectedBlocks {
		s.BlockIDs = append(s.BlockIDs, b.BlockID)
	}
	return s, nil
}

// AnalyzeJD calls resume_analyze_jd and returns its {profile, metadata}
// payload untouched.
func (c *Client) AnalyzeJD(ctx context.Context, jd string) (json.RawMessage, error) {
	return c.CallTool(ctx, "resume_analyze_jd", struct {
		JD string `json:"jd_text"`
	}{JD: jd})
}

// Header is the personal data rendered at the top of a resume. It exists only
// on the trusted side of the PII seam: no model-facing type carries it.
type Header struct {
	Name         string
	ContactLines []string
}

// HeaderSource supplies the header from the vault.
type HeaderSource interface {
	Header(ctx context.Context) (Header, error)
}

// SynthesizeArgs are the model-safe arguments of resume_synthesize. It has no
// name or contact fields by design; see Synthesize.
type SynthesizeArgs struct {
	JD         string `json:"jd_text"`
	PersonaID  string `json:"persona_id,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
}

// synthesizeRequest is the wire form: the model-safe args plus the pinned page
// budget and the header filled in here from the HeaderSource.
type synthesizeRequest struct {
	SynthesizeArgs
	PageBudget   int      `json:"page_budget"`
	HeaderName   string   `json:"header_name"`
	ContactLines []string `json:"contact_lines,omitempty"`
}

// maxContactLines is resume_synthesize's documented limit (tools_resume.rs:178).
const maxContactLines = 4

// Synthesize calls resume_synthesize. header_name and contact_lines are read
// from src inside this function, so callers (and any model) never supply them
// and they never appear in SynthesizeArgs. Errors never include header values.
func (c *Client) Synthesize(ctx context.Context, src HeaderSource, args SynthesizeArgs) (json.RawMessage, error) {
	if src == nil {
		return nil, errors.New("resume: Synthesize requires a HeaderSource")
	}
	h, err := src.Header(ctx)
	if err != nil {
		return nil, fmt.Errorf("resume: header unavailable: %w", err)
	}
	if h.Name == "" {
		return nil, errors.New("resume: header has no name")
	}
	if len(h.ContactLines) > maxContactLines {
		return nil, fmt.Errorf("resume: header has %d contact lines, limit is %d", len(h.ContactLines), maxContactLines)
	}
	return c.CallTool(ctx, "resume_synthesize", synthesizeRequest{
		SynthesizeArgs: args,
		PageBudget:     PageBudget,
		HeaderName:     h.Name,
		ContactLines:   h.ContactLines,
	})
}
