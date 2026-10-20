// Package jdtriage defines the career.jd_triage question: four closed readings
// of a job posting. The answers only annotate; dropping a posting is an engine
// rule keyed on an annotation, and no personal data enters the question.
//
// fixture.json is the one definition of the task, question, context layout and
// options. Lappi only answers questions rendered exactly as it was trained, so
// Lappi-decision's generator keeps a byte-identical copy (checked by sha256 on
// both sides). Changing the file is a contract change: it needs a retrain.
package jdtriage

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	Task = "career.jd_triage"

	// MaxContextBytes caps the rendered context (target, title, location,
	// description).
	MaxContextBytes = 24 << 10
	// MaxTargetSummaryBytes caps the target-role summary after whitespace is
	// collapsed.
	MaxTargetSummaryBytes = 1024

	FactContextTruncated = "context_truncated"

	// maxOptions is Lappi's choice-slot cap (caller-contract §4).
	maxOptions = 16
)

// FixtureJSON is the shared fixture, byte for byte.
//
//go:embed fixture.json
var FixtureJSON []byte

// Fixture is fixture.json decoded.
type Fixture struct {
	Version  int    `json:"fixture_version"`
	Task     string `json:"task"`
	Question string `json:"question"`
	Context  struct {
		Layout          string `json:"layout"`
		Target          string `json:"target"`
		TargetWhenEmpty string `json:"target_when_empty"`
		// SummaryRule and TruncateRule state the normalization and cut in
		// words for other implementations; NormalizeSummary and Render are
		// this one.
		SummaryRule     string `json:"summary_whitespace"`
		MaxSummaryBytes int    `json:"max_summary_bytes"`
		MaxBytes        int    `json:"max_bytes"`
		TruncateRule    string `json:"truncate"`
	} `json:"context"`
	// Families are the trained Lappi families, one per slot. Lappi's
	// training data carries one choice slot per example, so Lappi is asked
	// each slot as its own family with its own exact question; Liquid D and the
	// LLM are asked all slots at once with Question.
	Families []Family `json:"families"`
}

// Family is one single-slot Lappi family.
type Family struct {
	Task     string   `json:"task"`
	Question string   `json:"question"`
	Slot     SlotSpec `json:"slot"`
}

// Option is one legal answer with the description Liquid D judges it by.
type Option struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

// SlotSpec is one question slot.
type SlotSpec struct {
	Name    string   `json:"name"`
	Options []Option `json:"options"`
}

var (
	// Prompt is the fixed question text.
	Prompt string
	// Slots is the table of slots and options, in fixture order.
	Slots []SlotSpec
	// Families are the Lappi families, one per slot, in fixture order.
	Families []Family

	layout, targetTmpl string
)

func init() {
	f, err := parseFixture(FixtureJSON)
	if err != nil {
		panic(err) // a broken embedded file is a build defect; the tests catch it first
	}
	Prompt, Families = f.Question, f.Families
	Slots = make([]SlotSpec, len(f.Families))
	for i, fam := range f.Families {
		Slots[i] = fam.Slot
	}
	layout, targetTmpl = f.Context.Layout, f.Context.Target
}

// parseFixture decodes the fixture strictly and checks it agrees with the
// constants the renderer uses.
func parseFixture(raw []byte) (Fixture, error) {
	var f Fixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Fixture{}, fmt.Errorf("jdtriage: fixture: %w", err)
	}
	switch {
	case f.Version != 2:
		return Fixture{}, fmt.Errorf("jdtriage: fixture version %d, want 2", f.Version)
	case f.Task != Task:
		return Fixture{}, fmt.Errorf("jdtriage: fixture task %q, want %q", f.Task, Task)
	case f.Question == "":
		return Fixture{}, errors.New("jdtriage: fixture has no question")
	case f.Context.MaxBytes != MaxContextBytes || f.Context.MaxSummaryBytes != MaxTargetSummaryBytes:
		return Fixture{}, errors.New("jdtriage: fixture byte caps disagree with the renderer")
	case f.Context.TargetWhenEmpty != "":
		return Fixture{}, errors.New("jdtriage: fixture renders an empty target as text")
	case strings.Count(f.Context.Layout, "{target}") != 1 || strings.Count(f.Context.Target, "{summary}") != 1:
		return Fixture{}, errors.New("jdtriage: fixture layout placeholders are wrong")
	case len(f.Families) == 0:
		return Fixture{}, errors.New("jdtriage: fixture has no families")
	}
	names, tasks := map[string]bool{}, map[string]bool{}
	for _, fam := range f.Families {
		s := fam.Slot
		if s.Name == "" || names[s.Name] {
			return Fixture{}, fmt.Errorf("jdtriage: fixture slot %q is empty or repeated", s.Name)
		}
		names[s.Name] = true
		if !strings.HasPrefix(fam.Task, "career.") || fam.Task == Task || tasks[fam.Task] {
			return Fixture{}, fmt.Errorf("jdtriage: fixture family task %q is not a distinct career.* family", fam.Task)
		}
		tasks[fam.Task] = true
		if strings.TrimSpace(fam.Question) == "" || len(fam.Question) > 4096 {
			return Fixture{}, fmt.Errorf("jdtriage: fixture family %s has an empty or oversized question", fam.Task)
		}
		if len(s.Options) < 2 || len(s.Options) > maxOptions {
			return Fixture{}, fmt.Errorf("jdtriage: fixture slot %s has %d options", s.Name, len(s.Options))
		}
		seen := map[string]bool{}
		for _, o := range s.Options {
			if o.Value == "" || seen[o.Value] || strings.TrimSpace(o.Description) == "" {
				return Fixture{}, fmt.Errorf("jdtriage: fixture option %s/%q is empty, repeated or undescribed", s.Name, o.Value)
			}
			seen[o.Value] = true
		}
	}
	return f, nil
}

// SlotValues maps each slot to its legal values, in fixture order: what a
// drop rule may name.
func SlotValues() map[string][]string {
	m := make(map[string][]string, len(Slots))
	for _, s := range Slots {
		for _, o := range s.Options {
			m[s.Name] = append(m[s.Name], o.Value)
		}
	}
	return m
}

// Descriptions returns the table as the slot -> option -> description map the
// jev.Config (the decision-model client) takes.
func Descriptions() map[string]map[string]string {
	out := make(map[string]map[string]string, len(Slots))
	for _, s := range Slots {
		m := make(map[string]string, len(s.Options))
		for _, o := range s.Options {
			m[o.Value] = o.Description
		}
		out[s.Name] = m
	}
	return out
}

func slots() []decide.Slot {
	out := make([]decide.Slot, len(Slots))
	for i, s := range Slots {
		opts := make([]string, len(s.Options))
		for j, o := range s.Options {
			opts[j] = o.Value
		}
		out[i] = decide.Slot{Name: s.Name, Options: opts}
	}
	return out
}

// Question builds the triage question for p with no target-role summary.
func Question(p posting.Posting) decide.Question {
	return build(p, "")
}

// build renders the question. The question text never varies: the target
// summary goes into the context, so Lappi sees the exact trained question.
func build(p posting.Posting, summary string) decide.Question {
	text, truncated := Render(p.Title, p.Location, p.Description, summary)
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  text,
		Slots:    slots(),
		Facts:    map[string]string{FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(p.Key),
	}
}

// Render lays out the context as fixture.json describes, cut to
// MaxContextBytes at a rune boundary. summary must already be normalized
// (NormalizeSummary).
func Render(title, location, description, summary string) ([]byte, bool) {
	target := ""
	if summary != "" {
		target = strings.Replace(targetTmpl, "{summary}", summary, 1)
	}
	full := strings.NewReplacer("{target}", target, "{title}", title, "{location}", location, "{description}", description).Replace(layout)
	if len(full) <= MaxContextBytes {
		return []byte(full), false
	}
	cut := MaxContextBytes
	for cut > 0 && !utf8.RuneStart(full[cut]) {
		cut--
	}
	return []byte(full[:cut]), true
}

// NormalizeSummary collapses whitespace runs to one space and trims, and
// refuses a summary over MaxTargetSummaryBytes.
func NormalizeSummary(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > MaxTargetSummaryBytes {
		return "", fmt.Errorf("jdtriage: target summary is %d bytes, max %d", len(s), MaxTargetSummaryBytes)
	}
	return s, nil
}

// Triager asks a Decider the triage question.
type Triager struct {
	D decide.Decider
	// TargetSummary is a non-identifying, user-written description of the
	// target roles (for example "ML systems / inference / agents engineering
	// roles"). It is rendered at the top of the context; never put personal
	// data in it.
	TargetSummary string
	// ExtraSlots, when non-empty, are appended to the question's slots so
	// additional decisions (e.g. category) are answered in one single flow.
	ExtraSlots []decide.Slot
}

// Triage asks D about p.
func (t Triager) Triage(ctx context.Context, p posting.Posting) (decide.Answer, error) {
	if t.D == nil {
		return decide.Answer{}, errors.New("jdtriage: no Decider")
	}
	s, err := NormalizeSummary(t.TargetSummary)
	if err != nil {
		return decide.Answer{}, err
	}
	q := build(p, s)
	if len(t.ExtraSlots) > 0 {
		q.Slots = append(append([]decide.Slot(nil), q.Slots...), t.ExtraSlots...)
	}
	return t.D.Decide(ctx, q)
}
