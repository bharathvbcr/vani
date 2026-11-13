package main

import (
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// sensitivityReply is the stage-0 measurement repeated under each way of
// reading the owner's labels (eval.LabelModes): every reader against the owner
// and every Score part's AUC over all judged postings. The owner's "maybe" has
// proved mixed, so a verdict that holds under one reading only is not one to
// act on.
type sensitivityReply struct {
	Modes []sensitivityMode `json:"modes"`
	// PartAUCError says why no mode has a part AUC (the items could not be built).
	PartAUCError string `json:"part_auc_error,omitempty"`
}

type sensitivityMode struct {
	Mode    eval.LabelMode       `json:"mode"`
	Readers eval.ReaderAgreement `json:"readers"`
	PartAUC *rankfit.PartAUC     `json:"part_auc,omitempty"`
}

// buildSensitivity computes sensitivityReply. items are the judged items
// (judgedItems) or nil with itemsErr saying why; eng is TitleIsEngineering by key.
func buildSensitivity(done map[posting.Key]eval.Entry, recs []ledger.Record, store *eval.TeacherStore, status string,
	names eval.Names, items []rankfit.Item, itemsErr error, base jobinsight.Weights, eng func(posting.Key) bool) sensitivityReply {
	out := sensitivityReply{Modes: make([]sensitivityMode, 0, len(eval.LabelModes))}
	if itemsErr != nil {
		out.PartAUCError = itemsErr.Error()
	}
	for _, m := range eval.LabelModes {
		view := eval.ViewLabels(done, m, eng)
		sm := sensitivityMode{Mode: m, Readers: eval.BuildReaderAgreement(view, recs, store, status, names)}
		if itemsErr == nil {
			p := rankfit.BuildPartAUC(itemsUnder(items, m, eng), base, string(m), nil, rankfit.DefaultPartAUCResamples, 1)
			sm.PartAUC = &p
		}
		out.Modes = append(out.Modes, sm)
	}
	return out
}

// gradeVerdict maps a rankfit grade to the would_apply verdict it came from.
var gradeVerdict = map[int]string{rankfit.GradeYes: "yes", rankfit.GradeMaybe: "maybe", rankfit.GradeNo: "no"}

// verdictGrade is gradeVerdict's inverse.
var verdictGrade = map[string]int{"yes": rankfit.GradeYes, "maybe": rankfit.GradeMaybe, "no": rankfit.GradeNo}

// itemsUnder is the judged items as mode m reads their labels: each grade
// remapped, the items m leaves out removed. Unjudged items are dropped too
// (BuildPartAUC reads judged items only). The input is not modified.
func itemsUnder(items []rankfit.Item, m eval.LabelMode, eng func(posting.Key) bool) []rankfit.Item {
	out := make([]rankfit.Item, 0, len(items))
	for _, it := range items {
		if !it.Judged {
			continue
		}
		v, ok := gradeVerdict[it.Grade]
		if !ok {
			continue
		}
		nv, keep := m.Remap(v, eng != nil && eng(posting.Key(it.Key)))
		if !keep {
			continue
		}
		it.Grade = verdictGrade[nv]
		out = append(out, it)
	}
	return out
}

// Markdown renders two compact tables, one row per reader and per part, one
// column per mode.
func (s sensitivityReply) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Sensitivity: do these verdicts hold under other readings of the labels?\n\n")
	b.WriteString("Modes: yes_or_maybe (wanted is yes or maybe, as above); yes_only (a maybe counts as unwanted); maybes_excluded (yes against no); engineering_only (postings whose title reads as software engineering, by a word list, wanted yes or maybe). A verdict that changes across the modes rests on how the maybes are read.\n\n")
	if len(s.Modes) == 0 {
		b.WriteString("no data\n")
		return b.String()
	}
	b.WriteString("| mode | wanted / unwanted |\n|---|---|\n")
	for _, m := range s.Modes {
		fmt.Fprintf(&b, "| %s | %d / %d |\n", m.Mode, m.Readers.Yes+m.Readers.Maybe, m.Readers.No)
	}

	// Readers: one row per reader seen in any mode.
	type rk struct{ kind, reader string }
	var order []rk
	seen := map[rk]bool{}
	for _, m := range s.Modes {
		for _, r := range m.Readers.Readers {
			k := rk{r.Kind, r.Reader}
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
			}
		}
	}
	b.WriteString("\nReaders, role_fit sensitivity / specificity (wanted read as a fit / unwanted not read as one):\n\n| reader |")
	for _, m := range s.Modes {
		fmt.Fprintf(&b, " %s |", m.Mode)
	}
	b.WriteString("\n|---|")
	for range s.Modes {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, k := range order {
		fmt.Fprintf(&b, "| %s %s |", k.kind, k.reader)
		for _, m := range s.Modes {
			cellText := "not read"
			for _, r := range m.Readers.Readers {
				if r.Kind == k.kind && r.Reader == k.reader {
					cellText = fmt.Sprintf("%s / %s", shortCount(r.Sensitivity), shortCount(r.Specificity))
				}
			}
			fmt.Fprintf(&b, " %s |", cellText)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nScore parts, AUC where the part was known (95% interval), all judged postings:\n\n")
	if s.PartAUCError != "" {
		fmt.Fprintf(&b, "not computed: %s\n", s.PartAUCError)
		return b.String()
	}
	b.WriteString("| part |")
	for _, m := range s.Modes {
		fmt.Fprintf(&b, " %s |", m.Mode)
	}
	b.WriteString("\n|---|")
	for range s.Modes {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	parts := append(rankfit.PartNames[:], rankfit.PartMatchGate)
	for _, part := range parts {
		fmt.Fprintf(&b, "| %s |", part)
		for _, m := range s.Modes {
			fmt.Fprintf(&b, " %s |", partCell(m.PartAUC, part))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// shortCount is a Count as "k/n (lo-hi%)", or "n=0".
func shortCount(c eval.Count) string {
	lo, hi, ok := c.Interval()
	if !ok {
		return "n=0"
	}
	return fmt.Sprintf("%d/%d (%.0f-%.0f%%)", c.Wanted, c.N, 100*lo, 100*hi)
}

// partCell is one part's known-only AUC in one mode's report.
func partCell(p *rankfit.PartAUC, part string) string {
	if p == nil {
		return "n/a"
	}
	if p.Status != rankfit.PartAUCOK {
		return p.Status
	}
	for _, r := range p.Rows {
		if r.Part != part {
			continue
		}
		if !r.KnownOK {
			return "too few known"
		}
		mark := ""
		switch {
		case r.KnownLo > 0.5:
			mark = " ↑"
		case r.KnownHi < 0.5:
			mark = " ↓"
		}
		return fmt.Sprintf("%.2f (%.2f-%.2f)%s", r.KnownAUC, r.KnownLo, r.KnownHi, mark)
	}
	return "n/a"
}
