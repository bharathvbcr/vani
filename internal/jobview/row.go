// Package jobview owns the jobs-row contract every Vani surface reads: the
// Mac app's Jobs tab, the browser extension (through the native host) and
// `vani jobs --json`. The Go side computes relevance, priority, the fit
// vocabulary and every display string for fit, status and age; the other
// surfaces render what arrives and compute none of it.
//
// A Row carries Version. A reader accepts exactly the versions it knows
// (Check) and fails loudly on any other, so a row from a newer vani is
// refused rather than half-rendered. Adding an optional field keeps the
// version; renaming, removing or re-meaning a field bumps it.
package jobview

import (
	"fmt"
	"math"
)

// Version is the schema version of Row. Surfaces pin it in their golden
// replays (testdata/rows.golden.json).
const Version = 1

// Row is one posting as every surface shows it. It is display-only: no
// engine rule reads it.
type Row struct {
	V        int    `json:"v"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	Company  string `json:"company"`
	Location string `json:"location,omitempty"`
	Status   string `json:"status"`
	// StatusLabel is the status in words ("Closed" for dead).
	StatusLabel string `json:"status_label"`
	// Relevance is 0-100 and age-free; Priority is Relevance discounted by
	// age (jobinsight.Score). RelevanceBasis says what Relevance rests on.
	Relevance      float64 `json:"relevance"`
	RelevanceBasis string  `json:"relevance_basis,omitempty"`
	Priority       float64 `json:"priority"`
	// Fit is triage's role_fit reading (one of FitOrder) and FitLabel its
	// display string; both are empty when triage gave no reading.
	Fit      string   `json:"fit,omitempty"`
	FitLabel string   `json:"fit_label,omitempty"`
	Reasons  []Reason `json:"reasons,omitempty"`
	// Similar counts the other postings this one is linked to as a
	// duplicate, a possible duplicate or the same role elsewhere (0-3).
	Similar int `json:"similar"`
	// AgeDays is how old the posting is, FreshSrc what it is measured from
	// ("posted": the board's date; "seen": when Vani first saw it, an upper
	// bound) and AgeLabel the display string ("3d", "~5w").
	AgeDays  *int   `json:"age_days,omitempty"`
	FreshSrc string `json:"fresh_src,omitempty"`
	AgeLabel string `json:"age_label,omitempty"`
}

// Reason is one picked reason from a posting's note.
type Reason struct {
	Kind string `json:"kind"` // fits, gap, check or other
	Text string `json:"text"`
	Pct  *int   `json:"pct,omitempty"`
}

// FitOrder is role_fit's options, best first (the career.jd_triage fixture).
var FitOrder = [...]string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"}

var fitLabels = map[string]string{
	"strong_fit":         "Strong fit",
	"partial_fit":        "Partial fit",
	"adjacent":           "Adjacent",
	"mismatch_seniority": "Seniority mismatch",
	"mismatch_domain":    "Different field",
}

var statusLabels = map[string]string{
	"discovered": "Discovered",
	"live":       "Live",
	"triaged":    "Triaged",
	"unsure":     "Unsure",
	"scored":     "Scored",
	"explained":  "Explained",
	"digested":   "Digested",
	"dead":       "Closed",
	"duplicate":  "Duplicate",
	"dropped":    "Dropped",
}

// FitLabel is the display string for a role_fit option, or "" for an unknown
// one (a reading outside the fixture is not shown as if it were known).
func FitLabel(fit string) string { return fitLabels[fit] }

// StatusLabel is the display string for a posting status; an unknown status
// shows as itself.
func StatusLabel(status string) string {
	if l, ok := statusLabels[status]; ok {
		return l
	}
	return status
}

// FitLabels returns a copy of the fit vocabulary, for fixtures.
func FitLabels() map[string]string { return copyMap(fitLabels) }

// StatusLabels returns a copy of the status vocabulary, for fixtures.
func StatusLabels() map[string]string { return copyMap(statusLabels) }

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// AgeLabel is "today", "3d", "5w" or "4mo" for a known age, with "~" in front
// when it is measured from first sight rather than a board date; "" for an
// unknown age.
func AgeLabel(days *int, freshSrc string) string {
	if days == nil || *days < 0 {
		return ""
	}
	prefix := ""
	if freshSrc == "seen" {
		prefix = "~"
	}
	d := *days
	switch {
	case d == 0:
		return prefix + "today"
	case d < 14:
		return fmt.Sprintf("%s%dd", prefix, d)
	case d < 60:
		return fmt.Sprintf("%s%dw", prefix, d/7)
	default:
		return fmt.Sprintf("%s%dmo", prefix, d/30)
	}
}

// Inputs are the facts a Row is built from; cmd/vani fills them from a ledger
// record and the insight engine.
type Inputs struct {
	Key, Title, Company, Location, Status string
	Relevance, Priority                   float64
	RelevanceBasis                        string
	RoleFit                               string
	Reasons                               []Reason
	DupLinks                              []string // dup_of, dup_candidate_of, dup_sibling_of; empty ones are ignored
	AgeDays                               *int
	FreshSrc                              string
}

// Build makes the Row for in. Non-finite scores become 0 rather than reach
// JSON, which cannot carry them.
func Build(in Inputs) Row {
	r := Row{
		V: Version, Key: in.Key, Title: in.Title, Company: in.Company, Location: in.Location,
		Status: in.Status, StatusLabel: StatusLabel(in.Status),
		Relevance: finite(in.Relevance), RelevanceBasis: in.RelevanceBasis, Priority: finite(in.Priority),
		Reasons: in.Reasons, AgeDays: in.AgeDays, FreshSrc: in.FreshSrc,
		AgeLabel: AgeLabel(in.AgeDays, in.FreshSrc),
	}
	if l := FitLabel(in.RoleFit); l != "" {
		r.Fit, r.FitLabel = in.RoleFit, l
	}
	for _, k := range in.DupLinks {
		if k != "" {
			r.Similar++
		}
	}
	return r
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// Check is the reader's gate: nil only for a version this build knows.
func Check(v int) error {
	if v != Version {
		return fmt.Errorf("jobs row schema version %d is not supported (this reader knows %d)", v, Version)
	}
	return nil
}
