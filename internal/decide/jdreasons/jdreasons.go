// Package jdreasons defines the career.jd_reasons question: the digest's
// "why this role" note, picked rather than written. A catalog of short reasons
// in three categories (why it fits, the biggest gap, what to check first) is
// drafted once from the user's target summary and kept in the state dir; for
// each posting a decision model picks one reason per category (or none) and
// its probability is the relevance score shown. No model writes free text per
// posting, and no personal data enters the question.
package jdreasons

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/decide"
)

const (
	Task     = "career.jd_reasons"
	FileName = "reasons.json"
	Version  = 1

	SlotFit   = "fit"
	SlotGap   = "gap"
	SlotCheck = "check"

	// NoneID is the option every slot ends with: no listed reason applies.
	NoneID = "none"
	// MaxItems leaves room for the none option within decide.MaxOptions.
	MaxItems            = decide.MaxOptions - 1
	MaxIDBytes          = 48
	MaxLabelBytes       = 120
	MaxDescriptionBytes = 400
	MaxDraftedByBytes   = 200
	maxFileBytes        = 256 << 10

	// NothingApplies is the note for a posting the model read where no
	// listed reason applied in any category.
	NothingApplies = "No listed reason clearly applies."
)

// ErrStale means the catalog was drafted for a different target summary.
var ErrStale = errors.New("jdreasons: catalog was drafted for a different target summary")

// Item is one pickable reason. Label is what the digest shows; Description is
// what the decision model judges the posting against.
type Item struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Catalog is reasons.json.
type Catalog struct {
	Version int `json:"version"`
	// TargetSHA256 is TargetHash of the summary the catalog was drafted for.
	TargetSHA256 string `json:"target_sha256"`
	// DraftedBy says where the reasons came from (`vani reasons draft` and its
	// model, or whoever wrote an imported file). Optional; shown, never judged.
	DraftedBy string `json:"drafted_by,omitempty"`
	Fit       []Item `json:"fit"`
	Gap       []Item `json:"gap"`
	Check     []Item `json:"check"`
}

// Draft is a catalog before Vani stamps it: the reasons, and optionally where
// they came from. Stamp keys it to a target summary.
type Draft struct {
	DraftedBy string `json:"drafted_by,omitempty"`
	Fit       []Item `json:"fit"`
	Gap       []Item `json:"gap"`
	Check     []Item `json:"check"`
}

// ParseDraft decodes a draft strictly: one object, no unknown fields, and none
// of the fields Stamp sets.
func ParseDraft(raw []byte) (Draft, error) {
	var d Draft
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Draft{}, fmt.Errorf("jdreasons: draft: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Draft{}, errors.New("jdreasons: draft: more than one JSON object")
	}
	return d, nil
}

// Stamp keys d to targetSummary and validates the result.
func (d Draft) Stamp(targetSummary string) (Catalog, error) {
	c := Catalog{Version: Version, TargetSHA256: TargetHash(targetSummary), DraftedBy: d.DraftedBy, Fit: d.Fit, Gap: d.Gap, Check: d.Check}
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

type category struct {
	slot, heading, instructions, none string
	items                             func(Catalog) []Item
}

var categories = []category{
	{SlotFit, "Fits",
		"Which listed reason best explains why this posting fits the target roles described at the top? Pick none if no listed reason is clearly supported by the posting.",
		"No listed reason for a fit is clearly supported by the posting.",
		func(c Catalog) []Item { return c.Fit }},
	{SlotGap, "Gap",
		"Which listed gap or risk is the biggest one in this posting for the target roles described at the top? Pick none if no listed gap is clearly supported by the posting.",
		"No listed gap or risk is clearly supported by the posting.",
		func(c Catalog) []Item { return c.Gap }},
	{SlotCheck, "Check",
		"Which listed item should the job seeker check first before applying to this posting? Pick none if no listed item is relevant to the posting.",
		"No listed item is relevant to this posting.",
		func(c Catalog) []Item { return c.Check }},
}

// Prompt is the combined question for backends that take one prompt.
const Prompt = "For each category, pick the one listed reason this job posting most clearly supports, or none."

var idPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// Headings are the labels a note's reasons start with ("Fits: …"), in note
// order, so a reader can split a note without its own copy of them.
func Headings() []string {
	out := make([]string, len(categories))
	for i, c := range categories {
		out[i] = c.heading
	}
	return out
}

// TargetHash identifies a target summary by its normalized text.
func TargetHash(summary string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(summary), " ")))
	return hex.EncodeToString(sum[:])
}

// Validate checks c is a catalog every backend can be asked from. Its text is
// model output: it is bounded, plain and may not carry the untrusted-content
// markers.
func (c Catalog) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("jdreasons: catalog version %d, want %d", c.Version, Version)
	}
	if len(c.TargetSHA256) != 64 {
		return errors.New("jdreasons: catalog has no target hash")
	}
	if c.DraftedBy != "" {
		if err := checkText(c.DraftedBy, MaxDraftedByBytes); err != nil {
			return fmt.Errorf("jdreasons: drafted_by: %w", err)
		}
	}
	for _, cat := range categories {
		its := cat.items(c)
		if len(its) == 0 || len(its) > MaxItems {
			return fmt.Errorf("jdreasons: %s has %d items, want 1..%d", cat.slot, len(its), MaxItems)
		}
		seen := map[string]bool{}
		for i, it := range its {
			switch {
			case !idPattern.MatchString(it.ID) || len(it.ID) > MaxIDBytes:
				return fmt.Errorf("jdreasons: %s item %d id is not 1..%d bytes of [a-z0-9_]", cat.slot, i, MaxIDBytes)
			case it.ID == NoneID:
				return fmt.Errorf("jdreasons: %s item %d uses the reserved id %q", cat.slot, i, NoneID)
			case seen[it.ID]:
				return fmt.Errorf("jdreasons: %s repeats id %q", cat.slot, it.ID)
			}
			seen[it.ID] = true
			if err := checkText(it.Label, MaxLabelBytes); err != nil {
				return fmt.Errorf("jdreasons: %s/%s label: %w", cat.slot, it.ID, err)
			}
			if err := checkText(it.Description, MaxDescriptionBytes); err != nil {
				return fmt.Errorf("jdreasons: %s/%s description: %w", cat.slot, it.ID, err)
			}
		}
	}
	return nil
}

func checkText(s string, max int) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("empty")
	case len(s) > max:
		return fmt.Errorf("%d bytes, max %d", len(s), max)
	case strings.Contains(s, "UNTRUSTED CONTENT"):
		return errors.New("carries an untrusted-content marker")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("carries a control character")
		}
	}
	return nil
}

// Parse decodes and validates a catalog strictly.
func Parse(raw []byte) (Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("jdreasons: catalog: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("jdreasons: catalog: trailing data after the object")
	}
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

// Load reads the catalog at path. It returns nil, nil when there is none, and
// ErrStale when it was drafted for another target summary.
func Load(path, targetSummary string) (*Catalog, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("jdreasons: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jdreasons: %w", err)
	}
	if len(raw) > maxFileBytes {
		return nil, fmt.Errorf("jdreasons: %s is larger than %d bytes", path, maxFileBytes)
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.TargetSHA256 != TargetHash(targetSummary) {
		return nil, ErrStale
	}
	return &c, nil
}

// Write validates c and writes it to path (0600, atomically). It refuses to
// replace an existing catalog unless force is set.
func Write(path string, c Catalog, force bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("jdreasons: %s exists (pass --force to replace it)", path)
		}
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'))
}

// Instructions is the per-slot question, as jev.Config.Instructions takes it.
func Instructions() map[string]string {
	out := make(map[string]string, len(categories))
	for _, cat := range categories {
		out[cat.slot] = cat.instructions
	}
	return out
}

// Descriptions is the slot -> option -> description table, as
// jev.Config.Descriptions takes it.
func (c Catalog) Descriptions() map[string]map[string]string {
	out := make(map[string]map[string]string, len(categories))
	for _, cat := range categories {
		m := map[string]string{NoneID: cat.none}
		for _, it := range cat.items(c) {
			m[it.ID] = it.Description
		}
		out[cat.slot] = m
	}
	return out
}

func (c Catalog) slots() []decide.Slot {
	out := make([]decide.Slot, len(categories))
	for i, cat := range categories {
		its := cat.items(c)
		opts := make([]string, 0, len(its)+1)
		for _, it := range its {
			opts = append(opts, it.ID)
		}
		out[i] = decide.Slot{Name: cat.slot, Options: append(opts, NoneID)}
	}
	return out
}

// maxMissingBytes bounds the missing-requirements line. The names come from
// the posting text by way of DevPrism, so the ledger holds them unbounded.
const maxMissingBytes = 1 << 10

// question renders the posting as the triage question does (target summary,
// title, location, description; never the company or employer badges), with
// the gap analysis ahead of it. The whole context stays within
// jdtriage.MaxContextBytes: the posting body is cut to what the facts leave.
//
// The question goes to Jev only (the notes picker), whose state is data to
// judge against the per-slot instructions, with no untrusted-content frame
// for the facts to sit inside. Of the two lines, only the coverage figure is
// the pipeline's own, and it is written only when it is a percentage; the
// missing names are posting text and are labelled so.
func (c Catalog) question(r ledger.Record, summary string) decide.Question {
	var facts strings.Builder
	if v, err := strconv.ParseFloat(r.Annotations[ledger.AnnCoverage], 64); err == nil && v >= 0 && v <= 100 {
		fmt.Fprintf(&facts, "Pipeline fact: the job seeker's knowledge base covers %s%% of this posting's must-have requirements.\n",
			strconv.FormatFloat(v, 'f', -1, 64))
	}
	if v := strings.Join(strings.Fields(r.Annotations[ledger.AnnMissing]), " "); v != "" {
		fmt.Fprintf(&facts, "Requirement names taken from the posting text that the knowledge base does not cover: %s\n", htmltext.Truncate(v, maxMissingBytes))
	}
	if facts.Len() > 0 {
		facts.WriteString("\n")
	}
	body, truncated := jdtriage.Render(r.Posting.Title, r.Posting.Location, r.Posting.Description, summary)
	if room := jdtriage.MaxContextBytes - facts.Len(); len(body) > room {
		body, truncated = []byte(htmltext.Truncate(string(body), room)), true
	}
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  append([]byte(facts.String()), body...),
		Slots:    c.slots(),
		Facts:    map[string]string{jdtriage.FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(r.Key),
	}
}

// Picker implements pipeline.Explainer by asking D to pick from the catalog.
type Picker struct {
	D             decide.Decider
	Catalog       Catalog
	TargetSummary string
}

// Explain returns the note for one record: each category's pick with its
// probability, NothingApplies when the model read the posting and picked none
// everywhere, or an error when no category was read at all (the posting stays
// scored and is explained on a later run).
func (p Picker) Explain(ctx context.Context, r ledger.Record) (string, error) {
	if p.D == nil {
		return "", errors.New("jdreasons: no Decider")
	}
	summary, err := jdtriage.NormalizeSummary(p.TargetSummary)
	if err != nil {
		return "", err
	}
	ans, err := p.D.Decide(ctx, p.Catalog.question(r, summary))
	if err != nil {
		return "", fmt.Errorf("jdreasons: %w", err)
	}
	var parts []string
	read := false
	for _, cat := range categories {
		rd, ok := ans.Slots[cat.slot]
		if !ok {
			continue
		}
		if rd.Reading == decide.Answered || rd.Reading == decide.Abstained {
			read = true
		}
		if rd.Reading != decide.Answered || rd.Value == NoneID {
			continue
		}
		label := ""
		for _, it := range cat.items(p.Catalog) {
			if it.ID == rd.Value {
				label = it.Label
				break
			}
		}
		if label == "" {
			continue
		}
		part := cat.heading + ": " + label
		if rd.Jev != nil {
			part += fmt.Sprintf(" (%.0f%%)", rd.Jev.Probability*100)
		}
		parts = append(parts, part+".")
	}
	if !read {
		return "", errors.New("jdreasons: no decision backend read the posting")
	}
	if len(parts) == 0 {
		return NothingApplies, nil
	}
	return strings.Join(parts, " "), nil
}
