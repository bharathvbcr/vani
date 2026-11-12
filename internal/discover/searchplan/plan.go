// Package searchplan turns the owner's profile into the searches board
// discovery asks a third-party site: the role words of title_signals.include,
// then the skills the knowledge base lists most often in its entries' stacks.
//
// What leaves the machine is the point of this package, so it is narrow by
// construction. Only role words the owner wrote for this purpose and skill
// names from entries' stack fields are read; titles, organisations, dates,
// bullets and the target summary never are. A skill is searched only when at
// least MinEntries entries list it, because a stack also names the owner's own
// projects and those appear in one or two entries; a skill under that floor is
// counted, never named, in the plan.
package searchplan

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Origin says where a search came from.
type Origin string

const (
	// Role is a word or phrase from title_signals.include.
	Role Origin = "role"
	// Skill is a skill named in the stacks of at least MinEntries entries.
	Skill Origin = "kb_skill"
)

// Bounds. Searches are requests to a third-party site, so every one is
// capped and none is unbounded.
const (
	DefaultSearches   = 16
	MaxSearches       = 24
	DefaultSkills     = 6
	DefaultMinEntries = 3
	// MinRunes is the shortest search: a two-letter word ("ML", "Go") matches
	// too much text on its own to say anything about a board.
	MinRunes = 3
	// MaxBytes is the longest search: a board's own search may be at most
	// sources.MaxSearchLen, and a Workday or Oracle HCM board takes the
	// search that found it.
	MaxBytes = sources.MaxSearchLen
	// maxStacks bounds the entries read.
	maxStacks = 4096
)

// Why a term was not searched. Stable phrases the CLI prints and tests match.
const (
	WhyShort     = "too short to search on its own"
	WhyLong      = "longer than a board search may be"
	WhyUnclean   = "holds control or invisible characters"
	WhyDuplicate = "the same as an earlier search"
	WhyOverCap   = "over the search cap"
)

// Input is the profile a plan is made from.
type Input struct {
	// Roles is title_signals.include, in the owner's order.
	Roles []string
	// Stacks is each knowledge-base entry's stack text, one per entry. The
	// caller passes only entries it may use (the eligible bank).
	Stacks []string
	// MaxSearches is the most searches, 1..MaxSearches; zero is DefaultSearches.
	MaxSearches int
	// MaxSkills is the most skill searches, 0..MaxSearches; negative is none,
	// zero is DefaultSkills.
	MaxSkills int
	// MinEntries is how many entries must list a skill; zero is
	// DefaultMinEntries, and it is never below 2.
	MinEntries int
}

// Search is one planned search.
type Search struct {
	Text string `json:"text"`
	From Origin `json:"from"`
	// Entries is how many entries list the skill; zero for a role.
	Entries int `json:"entries,omitempty"`
}

// Skipped is a named term left out, with why.
type Skipped struct {
	Text string `json:"text"`
	From Origin `json:"from"`
	Why  string `json:"why"`
}

// Plan is the searches to run, in order, and what was left out.
type Plan struct {
	Searches []Search
	Skipped  []Skipped
	// SkillsBelowFloor is how many distinct skills fewer than MinEntries
	// entries list. They are never named.
	SkillsBelowFloor int
	// StacksUnread is how many stacks could not be parsed (kbsync.ParseStack)
	// or were past maxStacks; their skills are not counted.
	StacksUnread int
	MinEntries   int
}

// Build makes the plan. Roles come first, in the owner's order, then skills
// by how many entries list them; when both do not fit, skills keep up to
// MaxSkills of the slots and roles the rest, and each term left out is listed.
func Build(in Input) Plan {
	maxSearches := in.MaxSearches
	if maxSearches <= 0 {
		maxSearches = DefaultSearches
	}
	maxSearches = min(maxSearches, MaxSearches)
	maxSkills := in.MaxSkills
	switch {
	case maxSkills < 0:
		maxSkills = 0
	case maxSkills == 0:
		maxSkills = DefaultSkills
	}
	maxSkills = min(maxSkills, maxSearches)
	minEntries := in.MinEntries
	if minEntries <= 0 {
		minEntries = DefaultMinEntries
	}
	minEntries = max(minEntries, 2)

	p := Plan{MinEntries: minEntries}
	var taken []string // folded texts of everything planned or considered
	usable := func(text string, from Origin) (string, bool) {
		t := strings.Join(strings.Fields(text), " ")
		why := ""
		switch {
		case !utf8.ValidString(t) || strings.ContainsFunc(t, func(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }):
			why, t = WhyUnclean, strings.Map(func(r rune) rune {
				if unicode.IsPrint(r) {
					return r
				}
				return -1
			}, t)
		case utf8.RuneCountInString(t) < MinRunes:
			why = WhyShort
		case len(t) > MaxBytes:
			why = WhyLong
		case slices.Contains(taken, strings.ToLower(t)):
			why = WhyDuplicate
		}
		if t == "" {
			return "", false
		}
		if why != "" {
			if len(t) > MaxBytes {
				t = truncate(t, MaxBytes)
			}
			p.Skipped = append(p.Skipped, Skipped{Text: t, From: from, Why: why})
			return "", false
		}
		taken = append(taken, strings.ToLower(t))
		return t, true
	}

	var roles []Search
	for _, r := range in.Roles {
		if t, ok := usable(r, Role); ok {
			roles = append(roles, Search{Text: t, From: Role})
		}
	}

	type skill struct {
		text    string
		entries int
		first   int
	}
	counts := map[string]*skill{}
	for i, stack := range in.Stacks {
		if i >= maxStacks {
			p.StacksUnread += len(in.Stacks) - maxStacks
			break
		}
		names, err := kbsync.ParseStack(stack)
		if err != nil {
			p.StacksUnread++
			continue
		}
		for _, n := range names {
			k := strings.ToLower(strings.Join(strings.Fields(n), " "))
			if s := counts[k]; s != nil {
				s.entries++ // ParseStack drops duplicates within one stack
			} else {
				counts[k] = &skill{text: n, entries: 1, first: len(counts)}
			}
		}
	}
	ranked := make([]*skill, 0, len(counts))
	for _, s := range counts {
		if s.entries < minEntries {
			p.SkillsBelowFloor++
			continue
		}
		ranked = append(ranked, s)
	}
	slices.SortFunc(ranked, func(x, y *skill) int {
		return cmp.Or(cmp.Compare(y.entries, x.entries), cmp.Compare(x.first, y.first))
	})
	var skills []Search
	for _, s := range ranked {
		if t, ok := usable(s.text, Skill); ok {
			skills = append(skills, Search{Text: t, From: Skill, Entries: s.entries})
		}
	}

	nSkills := min(maxSkills, len(skills))
	nRoles := min(len(roles), maxSearches-nSkills)
	p.Searches = append(p.Searches, roles[:nRoles]...)
	p.Searches = append(p.Searches, skills[:nSkills]...)
	for _, s := range roles[nRoles:] {
		p.Skipped = append(p.Skipped, Skipped{Text: s.Text, From: Role, Why: WhyOverCap})
	}
	for _, s := range skills[nSkills:] {
		p.Skipped = append(p.Skipped, Skipped{Text: s.Text, From: Skill, Why: WhyOverCap})
	}
	return p
}

func truncate(s string, n int) string {
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
