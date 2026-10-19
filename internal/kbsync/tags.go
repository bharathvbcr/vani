package kbsync

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DevPrism's resume scorer has no embeddings for synced blocks, so it ranks
// them by skills, persona, recency and seniority (career_match/scoring.rs).
// rkb has no skills or personas, so the sync derives them, as the owner
// decided on 2026-10-19: skills from the entry's free-text stack, and one
// persona for every block.

// syncPersonas is the persona list every synced block carries. "ai" is the
// persona resume_synthesize scores against when none is given
// (mcp/tools_resume.rs).
var syncPersonas = []string{"ai"}

// skillLevel is the SkillTag level a stack skill gets: the level DevPrism
// writes on its own blocks. The scorer reads only the name.
const skillLevel = 3

// Stack bounds. A stack over them blocks its block rather than being cut.
const (
	MaxStackBytes     = 2048
	MaxSkills         = 32
	MaxSkillNameBytes = 64
)

// yearTokenRe matches the dates rkb sometimes appends to a stack
// ("Rust, Python, AWS; 2025–26"): a year, or a year range.
var yearTokenRe = regexp.MustCompile(`^\d{4}(\s*[–—-]\s*\d{2,4})?$`)

// ParseStack turns rkb's free-text stack ("Go, Rust; [MANVI](url); 2026")
// into skill names: split on ',' and ';', markdown removed, year tokens
// dropped, duplicates (ignoring case and spacing) dropped with the first
// spelling kept, in stack order. An empty stack has no skills.
func ParseStack(stack string) ([]string, error) {
	if len(stack) > MaxStackBytes {
		return nil, fmt.Errorf("stack is %d bytes, over %d", len(stack), MaxStackBytes)
	}
	if !utf8.ValidString(stack) {
		return nil, fmt.Errorf("stack is not valid UTF-8")
	}
	var out []string
	seen := map[string]bool{}
	// Markdown first: a link's url may hold ',' or ';'.
	for _, part := range strings.FieldsFunc(PlainText(stack), func(r rune) bool { return r == ',' || r == ';' }) {
		name := strings.TrimSpace(part)
		if name == "" || yearTokenRe.MatchString(name) {
			continue
		}
		if strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("a stack skill has a control character")
		}
		if len(name) > MaxSkillNameBytes {
			return nil, fmt.Errorf("stack skill %q is over %d bytes", truncate(name, 40), MaxSkillNameBytes)
		}
		key := skillKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
		if len(out) > MaxSkills {
			return nil, fmt.Errorf("stack has more than %d skills", MaxSkills)
		}
	}
	return out, nil
}

// skillKey is the identity of a skill name for de-duplication.
func skillKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}
