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
	// Available is false when there is no current teacher reading to use (no
	// log, an empty one, or a config that names no teacher model); the rows
	// then say nothing.
	Available bool             `json:"available"`
	Rows      []TeacherDropRow `json:"rows"`
}

// BuildTeacherDropProxy counts, per DropClass, the dropped records with a
// current teacher reading and the positive ones. teacher nil is not available.
func BuildTeacherDropProxy(recs []ledger.Record, rules []DropRule, teacher TeacherReadings) TeacherDropProxy {
	out := TeacherDropProxy{Available: teacher != nil, Rows: []TeacherDropRow{}}
	if teacher == nil {
		return out
	}
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

// Markdown renders the proxy.
func (p TeacherDropProxy) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Teacher proxy: dropped postings another model reads as a fit\n\nModel against model, not the owner: a dropped posting the teacher (`vani teacher`, the llm tier asked the same question) read as strong_fit or partial_fit is a candidate miss worth opening. It is not recall, and the teacher reads only the decision model's postings under the current question.\n\n")
	if !p.Available {
		b.WriteString("not available: no current teacher readings (run `vani teacher`, or the question changed since they were made)\n")
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
