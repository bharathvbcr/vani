// Package track loads and validates track deciders: one JSON file per kind of
// job, holding the target summary and drop rules `vani track use` copies into
// the config. The file format is described in docs/TRACKS.md.
//
// A target summary goes into every triage and explanation prompt, so Check
// refuses identifying words in it: a built-in list of contact and
// work-authorization terms, plus the owner's own list (identifying-words.txt
// in the tracks directory), which is personal and therefore never in source.
package track

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/employer"
)

const (
	// Version is the only decider_version this build reads.
	Version = 1
	// MaxFileBytes bounds one track file.
	MaxFileBytes = 64 << 10
	// MaxTracks bounds how many files List reads.
	MaxTracks = 256
	// WordsFile is the owner's identifying-word list in the tracks directory.
	WordsFile = "identifying-words.txt"
	// maxWordsBytes and maxWords bound that list.
	maxWordsBytes = 64 << 10
	maxWords      = 2000
	maxWordBytes  = 100
	// maxBankBytes bounds the knowledge-base bank read for evidence checks.
	maxBankBytes = 64 << 20

	maxListLen   = 64
	maxTextLen   = 2000
	useBadgeRank = "badge_and_rank"
)

// Decider is one track file.
type Decider struct {
	DeciderVersion      int                 `json:"decider_version"`
	ID                  string              `json:"id"`
	Label               string              `json:"label"`
	Description         string              `json:"description,omitempty"`
	TargetSummary       string              `json:"target_summary"`
	DropRules           []config.DropRule   `json:"drop_rules"`
	Seniority           Seniority           `json:"seniority"`
	EmployerPreferences EmployerPreferences `json:"employer_preferences"`
	WatchlistSearch     []string            `json:"watchlist_search,omitempty"`
	TitleSignals        TitleSignals        `json:"title_signals,omitzero"`
	Evidence            []Evidence          `json:"evidence,omitempty"`
	Gaps                []string            `json:"gaps,omitempty"`
	DevPrismPersona     *string             `json:"devprism_persona"`
}

// Seniority partitions the seniority levels.
type Seniority struct {
	Want    []string `json:"want"`
	Stretch []string `json:"stretch"`
	Drop    []string `json:"drop"`
}

// EmployerPreferences marks the kinds of employer a track favours. Use must
// be badge_and_rank and Drops must be present and false: a preference ranks,
// it never drops.
type EmployerPreferences struct {
	Prefer []string `json:"prefer"`
	Use    string   `json:"use"`
	Drops  *bool    `json:"drops"`
}

// TitleSignals are title words for and against the track (not wired).
type TitleSignals struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// Evidence names the knowledge-base entries behind one strength.
type Evidence struct {
	Area      string   `json:"area"`
	KBEntries []string `json:"kb_entries"`
	Strength  string   `json:"strength"`
}

// Entry is one file found by List: its id and either the decoded decider or
// the reason it could not be read.
type Entry struct {
	ID      string
	Decider Decider
	Err     error
}

// Dir resolves the tracks directory: cfg.TracksDir, or <stateDir>/tracks.
func Dir(cfg config.Config, stateDir string) string {
	if cfg.TracksDir != "" {
		return cfg.TracksDir
	}
	return filepath.Join(stateDir, "tracks")
}

// Load reads <dir>/<id>.json. The id is checked before it touches a path,
// and the file is opened inside dir with os.OpenInRoot, so neither "../x"
// nor a symlink leading out of dir is followed. Unknown fields, trailing
// data, an oversized file and an id that disagrees with the file name are
// all refused.
func Load(dir, id string) (Decider, error) {
	if !config.ValidTrackID(id) {
		return Decider{}, fmt.Errorf("track: %q is not a track id", id)
	}
	f, err := os.OpenInRoot(dir, id+".json")
	if err != nil {
		return Decider{}, fmt.Errorf("track %s: %w", id, err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return Decider{}, fmt.Errorf("track %s: %w", id, err)
	} else if !st.Mode().IsRegular() {
		return Decider{}, fmt.Errorf("track %s: not a regular file", id)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Decider{}, fmt.Errorf("track %s: %w", id, err)
	}
	if len(data) > MaxFileBytes {
		return Decider{}, fmt.Errorf("track %s: file exceeds %d bytes", id, MaxFileBytes)
	}
	var d Decider
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Decider{}, fmt.Errorf("track %s: decode: %w", id, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Decider{}, fmt.Errorf("track %s: trailing data after JSON document", id)
	}
	if d.ID != id {
		return Decider{}, fmt.Errorf("track %s: id %q does not match the file name", id, d.ID)
	}
	return d, nil
}

// List reads every <id>.json in dir, sorted by id. A file that fails to load
// is returned with its error rather than hidden. A missing dir is an error.
func List(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("track: %w", err)
	}
	var out []Entry
	for _, de := range des {
		id, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok || de.IsDir() {
			continue
		}
		if len(out) >= MaxTracks {
			return nil, fmt.Errorf("track: more than %d track files in %s", MaxTracks, dir)
		}
		e := Entry{ID: id}
		e.Decider, e.Err = Load(dir, id)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// builtinWords are identifying terms refused in every target summary,
// whoever the owner is: contact handles, work-authorization terms, and the
// employer attributes, which belong in employer_preferences and imply work
// authorization. Personal names, employers and schools go in WordsFile.
var builtinWords = []string{
	"@", "linkedin", "github", "visa", "h-1b", "h1b", "opt", "cpt", "ead", "citizen", "citizenship",
	"green card", "permanent resident", "work authorization", "sponsorship", "sponsor",
	"cap-exempt", "cap exempt", "nonprofit", "non-profit",
}

// Words is the identifying-word list Check applies.
type Words struct {
	terms []string
	// Personal is true when the owner's WordsFile was found and read; false
	// means only the built-in list was checked.
	Personal bool
}

// LoadWords reads <dir>/identifying-words.txt (one term per line, '#'
// comments) on top of the built-in list. A missing file is not an error:
// Words.Personal reports it so the caller can say the check was partial.
func LoadWords(dir string) (Words, error) {
	w := Words{terms: slices.Clone(builtinWords)}
	f, err := os.OpenInRoot(dir, WordsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return Words{}, fmt.Errorf("track: %s: %w", WordsFile, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxWordsBytes+1))
	if err != nil {
		return Words{}, fmt.Errorf("track: %s: %w", WordsFile, err)
	}
	if len(data) > maxWordsBytes {
		return Words{}, fmt.Errorf("track: %s exceeds %d bytes", WordsFile, maxWordsBytes)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	for sc.Scan() {
		t := strings.ToLower(strings.Join(strings.Fields(sc.Text()), " "))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if len(t) > maxWordBytes {
			return Words{}, fmt.Errorf("track: %s line %q exceeds %d bytes", WordsFile, t[:20], maxWordBytes)
		}
		if n++; n > maxWords {
			return Words{}, fmt.Errorf("track: %s has more than %d terms", WordsFile, maxWords)
		}
		w.terms = append(w.terms, t)
	}
	if err := sc.Err(); err != nil {
		return Words{}, fmt.Errorf("track: %s: %w", WordsFile, err)
	}
	w.Personal = true
	return w, nil
}

// BuiltinWords returns the built-in list alone (Personal false).
func BuiltinWords() Words { return Words{terms: slices.Clone(builtinWords)} }

// found returns the terms that occur in s as whole words: bounded on both
// sides by a non-letter or the end of the text.
func (w Words) found(s string) []string {
	low := strings.ToLower(s)
	var out []string
	for _, t := range w.terms {
		if t == "@" {
			if strings.Contains(low, "@") {
				out = append(out, t)
			}
			continue
		}
		re := regexp.MustCompile(`(^|[^a-z])` + regexp.QuoteMeta(t) + `([^a-z]|$)`)
		if re.MatchString(low) {
			out = append(out, t)
		}
	}
	return out
}

// Bank is the set of knowledge-base entry ids evidence may cite.
type Bank map[string]struct{}

// LoadBank reads rkb's bank.json ({"entries": {id: ...}}) for evidence checks.
func LoadBank(path string) (Bank, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("track: bank: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBankBytes+1))
	if err != nil {
		return nil, fmt.Errorf("track: bank: %w", err)
	}
	if len(data) > maxBankBytes {
		return nil, fmt.Errorf("track: bank exceeds %d bytes", maxBankBytes)
	}
	var doc struct {
		Entries map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("track: bank: %w", err)
	}
	if doc.Entries == nil {
		return nil, errors.New("track: bank has no entries object")
	}
	b := make(Bank, len(doc.Entries))
	for id := range doc.Entries {
		b[id] = struct{}{}
	}
	return b, nil
}

// slotOptions maps each triage slot to its legal values, from the fixture.
func slotOptions() map[string][]string {
	m := make(map[string][]string, len(jdtriage.Slots))
	for _, s := range jdtriage.Slots {
		for _, o := range s.Options {
			m[s.Name] = append(m[s.Name], o.Value)
		}
	}
	return m
}

// trackIndependentSlots never appear in a track: they encode personal data
// or preferences that do not change by track, and stay in the main config.
var trackIndependentSlots = []string{"sponsorship"}

// Check validates a decider beyond its shape and returns every problem. bank
// nil means evidence ids are not checked; the caller says so.
func Check(d Decider, w Words, bank Bank) []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }

	if d.DeciderVersion != Version {
		add("decider_version is %d, want %d", d.DeciderVersion, Version)
	}
	if !config.ValidTrackID(d.ID) {
		add("id %q is not a track id", d.ID)
	}
	if strings.TrimSpace(d.Label) == "" || len(d.Label) > 200 {
		add("label must be 1..200 bytes")
	}
	if len(d.Description) > maxTextLen {
		add("description exceeds %d bytes", maxTextLen)
	}

	summary, err := jdtriage.NormalizeSummary(d.TargetSummary)
	switch {
	case err != nil:
		add("target_summary: %v", err)
	case summary == "":
		add("target_summary is empty")
	default:
		for _, t := range w.found(summary) {
			add("target_summary contains the identifying term %q", t)
		}
	}

	opts := slotOptions()
	seenSlot := map[string]bool{}
	var seniorityRule *config.DropRule
	for i, r := range d.DropRules {
		legal, ok := opts[r.Slot]
		switch {
		case !ok:
			add("drop_rules[%d]: slot %q is not a triage slot", i, r.Slot)
			continue
		case slices.Contains(trackIndependentSlots, r.Slot):
			add("drop_rules[%d]: %s rules belong in the main config, not a track", i, r.Slot)
		case seenSlot[r.Slot]:
			add("drop_rules[%d]: second rule for slot %q", i, r.Slot)
		}
		seenSlot[r.Slot] = true
		if len(r.Values) == 0 {
			add("drop_rules[%d]: values is empty", i)
		}
		for _, v := range r.Values {
			if !slices.Contains(legal, v) {
				add("drop_rules[%d]: %q is not a %s value", i, v, r.Slot)
			}
		}
		if !(r.MinOpenRouterProb > 0 && r.MinOpenRouterProb <= 1) {
			add("drop_rules[%d]: min_openrouter_prob %v must be within (0, 1]", i, r.MinOpenRouterProb)
		}
		if !(r.MinLappiScore >= 0) {
			add("drop_rules[%d]: min_lappi_score %v must be >= 0", i, r.MinLappiScore)
		}
		if r.Slot == "seniority" {
			seniorityRule = &d.DropRules[i]
		}
	}

	levels := opts["seniority"]
	s := d.Seniority
	all := slices.Concat(s.Want, s.Stretch, s.Drop)
	for _, v := range all {
		if !slices.Contains(levels, v) {
			add("seniority: %q is not a seniority value", v)
		}
	}
	if uniq := slices.Compact(slices.Sorted(slices.Values(all))); len(uniq) != len(all) || len(all) != len(levels) {
		add("seniority want/stretch/drop must partition the levels %s", strings.Join(levels, ", "))
	}
	var ruleDrop []string
	if seniorityRule != nil {
		ruleDrop = seniorityRule.Values
	}
	if !slices.Equal(slices.Sorted(slices.Values(ruleDrop)), slices.Sorted(slices.Values(s.Drop))) {
		add("seniority.drop %v differs from the seniority drop rule %v", s.Drop, ruleDrop)
	}

	ep := d.EmployerPreferences
	if ep.Use != useBadgeRank || ep.Drops == nil || *ep.Drops {
		add("employer_preferences must say use %q and drops false: a preference ranks, it never drops", useBadgeRank)
	}
	seenPref := map[string]bool{}
	for _, p := range ep.Prefer {
		if !slices.Contains(employer.Attributes, p) {
			add("employer_preferences: unknown preference %q (want one of %s)", p, strings.Join(employer.Attributes, ", "))
		} else if seenPref[p] {
			add("employer_preferences: duplicate preference %q", p)
		}
		seenPref[p] = true
	}

	for name, l := range map[string][]string{"watchlist_search": d.WatchlistSearch, "gaps": d.Gaps,
		"title_signals.include": d.TitleSignals.Include, "title_signals.exclude": d.TitleSignals.Exclude} {
		if len(l) > maxListLen {
			add("%s has more than %d entries", name, maxListLen)
		}
		for _, v := range l {
			if len(v) > maxTextLen {
				add("%s has an entry over %d bytes", name, maxTextLen)
			}
		}
	}
	if len(d.Evidence) > maxListLen {
		add("evidence has more than %d entries", maxListLen)
	}
	if bank != nil {
		for _, ev := range d.Evidence {
			for _, id := range ev.KBEntries {
				if _, ok := bank[id]; !ok {
					add("evidence %q cites %q, which is not in the bank", ev.Area, id)
				}
			}
		}
	}
	sort.Strings(probs)
	return probs
}

// Change summarises what Apply did, for the CLI to report.
type Change struct {
	Removed []config.DropRule // config rules replaced by the track's, or left by the previous track
	Kept    []config.DropRule // every other config rule
	Added   []config.DropRule
}

// Apply returns cfg with the track's target summary, drop rules and employer
// preferences. A config rule is replaced when the track has a rule for its
// slot, or when it is identical to a rule of prev, the track applied before
// (nil when none or unreadable): that removes what the previous track put
// there without touching a rule the owner has since edited. Every other rule
// (sponsorship, work mode, ...) is kept, in order. The result is validated;
// cfg itself is not modified.
func Apply(cfg config.Config, d Decider, prev *Decider) (config.Config, Change, error) {
	var ch Change
	owned := map[string]bool{}
	for _, r := range d.DropRules {
		if slices.Contains(trackIndependentSlots, r.Slot) {
			return cfg, ch, fmt.Errorf("track %s: has a %s rule; those belong in the main config", d.ID, r.Slot)
		}
		owned[r.Slot] = true
	}
	fromPrev := func(r config.DropRule) bool {
		if prev == nil || slices.Contains(trackIndependentSlots, r.Slot) {
			return false
		}
		return slices.ContainsFunc(prev.DropRules, func(p config.DropRule) bool {
			return p.Slot == r.Slot && slices.Equal(p.Values, r.Values) &&
				p.MinOpenRouterProb == r.MinOpenRouterProb && p.MinLappiScore == r.MinLappiScore
		})
	}
	out := cfg
	out.DropRules = make([]config.DropRule, 0, len(cfg.DropRules)+len(d.DropRules))
	for _, r := range cfg.DropRules {
		r.Values = slices.Clone(r.Values)
		if owned[r.Slot] || fromPrev(r) {
			ch.Removed = append(ch.Removed, r)
			continue
		}
		ch.Kept = append(ch.Kept, r)
		out.DropRules = append(out.DropRules, r)
	}
	for _, r := range d.DropRules {
		r.Values = slices.Clone(r.Values)
		ch.Added = append(ch.Added, r)
		out.DropRules = append(out.DropRules, r)
	}
	out.TargetSummary = d.TargetSummary
	out.Track = d.ID
	out.EmployerPreferences = nil
	if len(d.EmployerPreferences.Prefer) > 0 {
		out.EmployerPreferences = slices.Clone(d.EmployerPreferences.Prefer)
	}
	if err := out.Validate(); err != nil {
		return cfg, Change{}, err
	}
	return out, ch, nil
}
