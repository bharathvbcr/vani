package jobview

import (
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
)

// LabelsVersion is the schema version of Labels, pinned by the surfaces'
// golden replays (testdata/labels.golden.json). A reader accepts exactly the
// versions it knows. Adding a vocabulary keeps it; renaming or re-meaning one
// bumps it.
const LabelsVersion = 1

// StatusOrder is the posting lifecycle in pipeline order, for menus and charts.
var StatusOrder = [...]string{"discovered", "live", "triaged", "unsure", "scored", "explained", "digested", "dead", "duplicate", "dropped"}

// triageLabels is the display string of every non-role_fit jdtriage option
// (role_fit is fitLabels), keyed by slot then value. TestVocabularyCoversItsSources
// fails when the fixture gains an option with no words here.
var triageLabels = map[string]map[string]string{
	"seniority": {
		"intern_or_new_grad": "Intern / new grad", "mid": "Mid-level", "senior": "Senior",
		"staff_plus": "Staff+", "manager": "Manager",
	},
	"work_mode": {"remote": "Remote", "hybrid": "Hybrid", "onsite": "On-site", "not_stated": "Not stated"},
	"sponsorship": {
		"sponsors_visa": "Sponsors visas", "no_sponsorship": "No sponsorship",
		"citizenship_or_clearance_required": "Citizenship or clearance", "not_stated": "Not stated",
	},
}

// TriageLabel is the display string for a seniority, work_mode or sponsorship
// option, or "" for an unknown slot or option.
func TriageLabel(slot, value string) string { return triageLabels[slot][value] }

// LabelEntry is one option of a vocabulary: the wire value and its words.
type LabelEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Labels is the single table of label vocabularies every surface reads
// (`vani labels --json`), each list in display order. Menus, chips and charts
// show these strings; a surface keeps no vocabulary of its own except an
// explicitly marked fallback for when vani cannot be run.
//
// There is no drop-reason vocabulary: a dropped posting's reason is free text
// composed per decision (pipeline.DropPolicy), not a coded set.
type Labels struct {
	V        int                     `json:"v"`
	Fit      []LabelEntry            `json:"fit"`
	Status   []LabelEntry            `json:"status"`
	Category []LabelEntry            `json:"category"`
	Triage   map[string][]LabelEntry `json:"triage"` // seniority, work_mode, sponsorship
}

// Vocabulary builds the Labels table from the owners: fit and status labels
// above, categories from jobcategory and triage options (and their order)
// from the jdtriage fixture. The result is a fresh copy.
func Vocabulary() Labels {
	v := Labels{V: LabelsVersion, Triage: map[string][]LabelEntry{}}
	for _, f := range FitOrder {
		v.Fit = append(v.Fit, LabelEntry{f, fitLabels[f]})
	}
	for _, s := range StatusOrder {
		v.Status = append(v.Status, LabelEntry{s, statusLabels[s]})
	}
	for _, o := range jobcategory.Options {
		v.Category = append(v.Category, LabelEntry{o.Value, o.Label})
	}
	for _, s := range jdtriage.Slots {
		words, ok := triageLabels[s.Name]
		if !ok {
			continue
		}
		for _, o := range s.Options {
			v.Triage[s.Name] = append(v.Triage[s.Name], LabelEntry{o.Value, words[o.Value]})
		}
	}
	return v
}
