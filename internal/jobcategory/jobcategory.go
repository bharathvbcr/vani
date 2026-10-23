// Package jobcategory defines the career.job_category question: which career
// track in drafts/deciders a job posting best matches. The reading only
// annotates (Rule 7); dropping or ranking a posting remains an engine rule.
//
// Categories correspond directly to the owner's knowledge-base career tracks:
//   - ai-engineer: Applied AI / LLM engineer
//   - agent-systems: Agent platforms and evaluation
//   - ml-inference-systems: ML systems / inference and GPU performance
//   - ml-research: ML research engineer / applied scientist
//   - biomedical-ai: Biomedical AI / computational biology / health data
//   - biomedical-research: Biomedical R&D (wet lab)
//   - human-factors-medical-devices: Human factors / medical device usability
//   - other: Other / unclassified
package jobcategory

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	// Task names the decision task for job categorization.
	Task = "career.job_category"
	// Slot is the question's slot name.
	Slot = "category"
	// Prompt is the fixed question prompt.
	Prompt = "Which career track does this job posting best match? Judge only from what the posting actually says."
)

// The category option identifiers.
const (
	AIEngineer                 = "ai-engineer"
	AgentSystems               = "agent-systems"
	MLInferenceSystems         = "ml-inference-systems"
	MLResearch                 = "ml-research"
	BiomedicalAI               = "biomedical-ai"
	BiomedicalResearch         = "biomedical-research"
	HumanFactorsMedicalDevices = "human-factors-medical-devices"
	Other                      = "other"
)

// Option is one pickable career category with its description and display label.
type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Options is the table of career categories in fixed canonical order.
var Options = []Option{
	{
		Value:       AIEngineer,
		Label:       "AI Engineer",
		Description: "Applied AI and LLM engineering: building production LLM and agent features end to end, retrieval-augmented generation (RAG), tool calling, context construction, provider fallbacks, and inference serving.",
	},
	{
		Value:       AgentSystems,
		Label:       "Agent Systems",
		Description: "AI agent infrastructure and platforms: agent harnesses, tool protocols (MCP), coding-agent platforms, code indexing and retrieval, sandboxed execution, approval gates, and agent evaluation.",
	},
	{
		Value:       MLInferenceSystems,
		Label:       "ML Inference Systems",
		Description: "ML systems and performance engineering: LLM inference runtimes and serving, GPU kernel development in Metal or CUDA, quantization, throughput and memory optimization, and Go/Rust serving boundaries.",
	},
	{
		Value:       MLResearch,
		Label:       "ML Research",
		Description: "ML research engineering and applied science: controlled experiments, calibrated decision models, evaluation methodology and benchmarks, interpretability, and training recipe comparisons.",
	},
	{
		Value:       BiomedicalAI,
		Label:       "Biomedical AI",
		Description: "Biomedical AI and computational biology: multi-modal machine learning on health signals (wearables, physiology, imaging), generative models for biomolecules or sequences, and clinical data pipelines.",
	},
	{
		Value:       BiomedicalResearch,
		Label:       "Biomedical Research",
		Description: "Biomedical R&D and wet-lab engineering: tissue engineering, biomaterials, magnetic hydrogels, stem-cell constructs, microfluidics, and molecular assays.",
	},
	{
		Value:       HumanFactorsMedicalDevices,
		Label:       "Human Factors",
		Description: "Human factors and medical device usability: human-subjects studies, user testing, biomechanical testing, risk analysis under ISO 14971, and usability evaluations.",
	},
	{
		Value:       Other,
		Label:       "Other",
		Description: "The role does not clearly fit any of the listed career tracks, or is outside technical engineering.",
	},
}

// OptionValues returns the legal category values in canonical order.
func OptionValues() []string {
	vals := make([]string, len(Options))
	for i, o := range Options {
		vals[i] = o.Value
	}
	return vals
}

// Validate reports whether val is a valid category option.
func Validate(val string) error {
	val = strings.TrimSpace(val)
	if val == "" {
		return errors.New("jobcategory: category must not be empty")
	}
	for _, o := range Options {
		if o.Value == val {
			return nil
		}
	}
	return fmt.Errorf("jobcategory: unknown category %q (valid: %s)", val, strings.Join(OptionValues(), ", "))
}

// Label returns the human-readable display label for a category value,
// or the value itself if unknown.
func Label(val string) string {
	for _, o := range Options {
		if o.Value == val {
			return o.Label
		}
	}
	return val
}

// DecideSlot returns the decide.Slot definition for job category.
func DecideSlot() decide.Slot {
	return decide.Slot{
		Name:    Slot,
		Options: OptionValues(),
	}
}

// Descriptions returns the slot -> option -> description map for decision-model
// backends (jev.Config.Descriptions).
func Descriptions() map[string]string {
	m := make(map[string]string, len(Options))
	for _, o := range Options {
		m[o.Value] = o.Description
	}
	return m
}

// Question builds a standalone decide.Question for categorizing posting p.
func Question(p posting.Posting, summary string) decide.Question {
	text, truncated := jdtriage.Render(p.Title, p.Location, p.Description, summary)
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  text,
		Slots:    []decide.Slot{DecideSlot()},
		Facts:    map[string]string{jdtriage.FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(p.Key),
	}
}

// UnifiedQuestion builds the single-flow question combining triage and category
// slots into ONE decide.Question so all decisions execute in one model pass.
func UnifiedQuestion(p posting.Posting, summary string) decide.Question {
	q := jdtriage.Question(p)
	if summary != "" {
		s, err := jdtriage.NormalizeSummary(summary)
		if err == nil {
			text, truncated := jdtriage.Render(p.Title, p.Location, p.Description, s)
			q.Context = text
			q.Facts[jdtriage.FactContextTruncated] = fmt.Sprint(truncated)
		}
	}
	q.Slots = append(q.Slots, DecideSlot())
	return q
}

// UnifiedDescriptions returns the merged slot -> option -> description map
// for decision-model backends covering both triage slots and category in one schema.
func UnifiedDescriptions() map[string]map[string]string {
	out := jdtriage.Descriptions()
	out[Slot] = Descriptions()
	return out
}
