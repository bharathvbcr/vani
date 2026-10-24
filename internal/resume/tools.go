package resume

import (
	"context"
	"encoding/json"
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
//	coveragePercentage -> Gap.CoveragePct (null, DevPrism's answer for a JD with no
//	                      must-have skills, is read only then: Gap.NoMustHaves, CoveragePct 0)
//	mustHave.items[]   -> Gap.MustHaves {skill -> Requirement, status -> Status}
//
// Status is "covered", "weak" or "missing" (gap.rs:21-27).
func (c *Client) Gap(ctx context.Context, jd, personaID string) (Gap, error) {
	raw, err := c.CallTool(ctx, "resume_gap_analysis", modelArgs{JD: jd, PersonaID: personaID, PageBudget: PageBudget})
	if err != nil {
		return Gap{}, err
	}
	var out struct {
		// Raw, so an absent key (nil) is told from an explicit null.
		CoveragePercentage json.RawMessage `json:"coveragePercentage"`
		ExtractionEmpty    bool            `json:"extractionEmpty"`
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
	// DevPrism reports a null coverage for a JD with no must-have skills (it
	// used to report 0). Null is valid only then; beside must-haves it is not
	// a measurement and not "no must-haves".
	var pct *float64
	if err := json.Unmarshal(out.CoveragePercentage, &pct); err != nil {
		return Gap{}, fmt.Errorf("%w: resume_gap_analysis coveragePercentage", ErrMalformed)
	}
	if pct == nil && !out.ExtractionEmpty && len(out.MustHave.Items) > 0 {
		return Gap{}, fmt.Errorf("%w: resume_gap_analysis coveragePercentage is null with must-haves", ErrMalformed)
	}
	g := Gap{MustHaves: make([]Coverage, 0, len(out.MustHave.Items))}
	if pct != nil {
		g.CoveragePct = *pct
	}
	for _, it := range out.MustHave.Items {
		switch it.Status {
		case "covered", "weak", "missing":
		default:
			return Gap{}, fmt.Errorf("%w: resume_gap_analysis status %q", ErrMalformed, bound(it.Status))
		}
		g.MustHaves = append(g.MustHaves, Coverage{Requirement: it.Skill, Status: it.Status})
	}
	g.NoMustHaves = out.ExtractionEmpty || len(g.MustHaves) == 0
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
//
// Nothing in this package uses it any more: `vani resume` builds the resume
// with rkb, whose header comes from the bank (internal/rkbresume), and
// DevPrism's resume_synthesize, which took this header, is no longer called.
// Header and HeaderSource stay because internal/vault implements HeaderSource
// (docs/FOLLOWUPS.md, "internal/vault is built but not wired").
type Header struct {
	Name         string
	ContactLines []string
}

// HeaderSource supplies the header from the vault.
type HeaderSource interface {
	Header(ctx context.Context) (Header, error)
}
