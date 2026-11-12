package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TeacherDropRow is, for one drop class, how many dropped postings the second
// teacher (the llm tier re-reading the decision model's postings, `vani
// teacher`) read under the current question, and how many of those it read as
// worth applying to (role_fit strong_fit or partial_fit).
type TeacherDropRow struct {
	Class    string `json:"class"`
	Dropped  int    `json:"dropped"`
	Read     int    `json:"read"`
	Positive int    `json:"positive"`
}

// TeacherDropProxy is a model-against-model check of the drops: a dropped
// posting another model reads as a fit is a candidate miss. It needs no owner
// labels and is not recall: the teacher can be wrong the same way, and it
// reads only what it was asked to (the decision model's postings, not title
// or exclusion drops).
type TeacherDropProxy struct {
	// Available is true only when Status is TeacherProxyAvailable; otherwise
	// the rows are empty and Status says why.
	Available bool `json:"available"`
	// Status names why the proxy is or is not available (TeacherProxy*).
	Status string `json:"status"`
	// Detail is the error behind an unreadable log or a bad config, if any.
	Detail string           `json:"detail,omitempty"`
	Rows   []TeacherDropRow `json:"rows"`
}

// What the teacher proxy could use. Each cause of "no proxy" is named apart,
// because a log written under an older question is not the same finding as no
// log at all, and neither is a table of drops the teacher read and passed.
const (
	TeacherProxyAvailable  = "available"  // current readings exist
	TeacherProxyMissing    = "missing"    // no teacher log was ever written
	TeacherProxyEmpty      = "empty"      // the log holds no valid row
	TeacherProxyUnreadable = "unreadable" // the log could not be opened or read
	TeacherProxyNoModel    = "no_model"   // the config names no teacher model
	TeacherProxyBadConfig  = "bad_config" // the config or target summary could not be read
	TeacherProxyNoCurrent  = "no_current" // rows exist, none under the current model and question
)

// TeacherSource is what the proxy reads: current readings when Status is
// TeacherProxyAvailable, else nil and the reason.
type TeacherSource struct {
	Readings TeacherReadings
	Status   string
	Detail   string
}

// BuildTeacherDropProxy counts, per DropClass, the dropped records with a
// current teacher reading and the positive ones. A source whose Status is not
// TeacherProxyAvailable, or that holds no readings, gives no rows: an
// unavailable teacher is never a table of zeros.
func BuildTeacherDropProxy(recs []ledger.Record, rules []DropRule, src TeacherSource) TeacherDropProxy {
	out := TeacherDropProxy{Status: src.Status, Detail: src.Detail, Rows: []TeacherDropRow{}}
	if out.Status == "" {
		out.Status = TeacherProxyMissing
	}
	teacher := src.Readings
	if out.Status != TeacherProxyAvailable || teacher == nil {
		if out.Status == TeacherProxyAvailable {
			out.Status = TeacherProxyMissing // "available" with nothing to read is not available
		}
		return out
	}
	out.Available = true
	rows := map[string]*TeacherDropRow{}
	seen := map[posting.Key]bool{}
	for _, r := range recs {
		if r.Status != posting.Dropped || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		class := droppedSource(DropClass(r, rules))
		row := rows[class]
		if row == nil {
			row = &TeacherDropRow{Class: class}
			rows[class] = row
		}
		row.Dropped++
		slots, ok := teacher.Reading(string(r.Key))
		if !ok {
			continue
		}
		v, answered := slots["role_fit"]
		if !answered {
			continue
		}
		row.Read++
		if PositiveReadings[v] {
			row.Positive++
		}
	}
	for _, row := range rows {
		out.Rows = append(out.Rows, *row)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Class < out.Rows[j].Class })
	return out
}

// teacherProxyWhy says why the proxy is unavailable, in words.
func teacherProxyWhy(status, detail string) string {
	var s string
	switch status {
	case TeacherProxyEmpty:
		s = "the teacher log holds no valid reading (run `vani teacher`)"
	case TeacherProxyUnreadable:
		s = "the teacher log is unreadable"
	case TeacherProxyNoModel:
		s = "the config names no teacher model (llm.triage_model or llm.model)"
	case TeacherProxyBadConfig:
		s = "the config or target summary could not be read for the teacher's question"
	case TeacherProxyNoCurrent:
		s = "the teacher log holds readings, but the question or model changed since they were made (target summary, prompt or model), so none is current; run `vani teacher` again"
	default:
		s = "no teacher log (run `vani teacher`)"
	}
	if detail != "" {
		s += ": " + detail
	}
	return s
}

// Markdown renders the proxy.
func (p TeacherDropProxy) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Teacher proxy: dropped postings another model reads as a fit\n\nModel against model, not the owner: a dropped posting the teacher (`vani teacher`, the llm tier asked the same question) read as strong_fit or partial_fit is a candidate miss worth opening. It is not recall, and the teacher reads only the decision model's postings under the current question.\n\n")
	if !p.Available {
		b.WriteString("not available: " + teacherProxyWhy(p.Status, p.Detail) + "\n")
		return b.String()
	}
	if len(p.Rows) == 0 {
		b.WriteString("no dropped postings\n")
		return b.String()
	}
	b.WriteString("| drop class | dropped | read by the teacher | read as a fit |\n|---|---|---|---|\n")
	for _, r := range p.Rows {
		fit := "no readings"
		if r.Read > 0 {
			fit = Count{Wanted: r.Positive, N: r.Read}.String()
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s |\n", r.Class, r.Dropped, r.Read, fit)
	}
	return b.String()
}
