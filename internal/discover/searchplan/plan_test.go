package searchplan

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func texts(ss []Search) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Text
	}
	return out
}

func skippedWhy(p Plan, text string) string {
	for _, s := range p.Skipped {
		if s.Text == text {
			return s.Why
		}
	}
	return ""
}

// Roles come first in the owner's order, then skills by how many entries list
// them, ties in first-seen order.
func TestBuildOrder(t *testing.T) {
	p := Build(Input{
		Roles:  []string{"Inference", "LLM Engineer"},
		Stacks: []string{"Rust, CUDA", "CUDA; Rust", "Rust, CUDA, PyTorch", "Rust", "PyTorch, Go", "PyTorch"},
	})
	if got, want := texts(p.Searches), []string{"Inference", "LLM Engineer", "Rust", "CUDA", "PyTorch"}; !slices.Equal(got, want) {
		t.Fatalf("searches %v, want %v", got, want)
	}
	if p.Searches[2].From != Skill || p.Searches[2].Entries != 4 || p.Searches[0].From != Role || p.Searches[0].Entries != 0 {
		t.Errorf("origins %+v", p.Searches)
	}
	if p.MinEntries != DefaultMinEntries {
		t.Errorf("min entries %d", p.MinEntries)
	}
}

// A skill fewer than MinEntries entries list is counted and never named: a
// stack also names the owner's own projects.
func TestBuildNeverNamesASkillUnderTheFloor(t *testing.T) {
	p := Build(Input{
		Roles:  []string{"Inference"},
		Stacks: []string{"Rust, ProjectAurora", "Rust, [ProjectBorealis](https://x.test/b)", "Rust, ProjectAurora"},
	})
	if got := texts(p.Searches); !slices.Equal(got, []string{"Inference", "Rust"}) {
		t.Fatalf("searches %v", got)
	}
	if p.SkillsBelowFloor != 2 {
		t.Errorf("below floor %d, want 2", p.SkillsBelowFloor)
	}
	dump := fmt.Sprintf("%+v", p)
	for _, secret := range []string{"ProjectAurora", "ProjectBorealis", "x.test"} {
		if strings.Contains(dump, secret) {
			t.Errorf("plan names %q: %s", secret, dump)
		}
	}
	// The floor is never below two, whatever is asked.
	p = Build(Input{Stacks: []string{"Solo"}, MinEntries: 1})
	if len(p.Searches) != 0 || p.SkillsBelowFloor != 1 || p.MinEntries != 2 {
		t.Errorf("min entries 1: %+v", p)
	}
}

func TestBuildSkipsWithReasons(t *testing.T) {
	long := strings.Repeat("x", MaxBytes+1)
	p := Build(Input{
		Roles: []string{"ML", "  ", "Inference", "inference", "  Applied   AI ", long, "GPU‮", "Go"},
		// "Rust" two ways, and a skill that repeats a role.
		Stacks: []string{"Rust, Inference", "rust, inference", "RUST, Inference"},
	})
	if got, want := texts(p.Searches), []string{"Inference", "Applied AI", "Rust"}; !slices.Equal(got, want) {
		t.Fatalf("searches %v, want %v", got, want)
	}
	for text, why := range map[string]string{
		"ML": WhyShort, "Go": WhyShort, "inference": WhyDuplicate, "GPU": WhyUnclean,
		strings.Repeat("x", MaxBytes): WhyLong,
	} {
		if got := skippedWhy(p, text); got != why {
			t.Errorf("%q skipped as %q, want %q (skipped %+v)", text, got, why, p.Skipped)
		}
	}
	for _, s := range p.Skipped {
		if strings.ContainsRune(s.Text, '‮') || len(s.Text) > MaxBytes {
			t.Errorf("skipped text not cleaned: %q", s.Text)
		}
	}
	// The skill that repeats a role is a duplicate, not a second search.
	if got := skippedWhy(p, "Inference"); got != WhyDuplicate {
		t.Errorf("skill Inference: %q", got)
	}
}

// When roles and skills do not both fit, skills keep up to MaxSkills slots and
// roles the rest; every term left out is listed with WhyOverCap.
func TestBuildCap(t *testing.T) {
	var roles, stacks []string
	for i := range 30 {
		roles = append(roles, fmt.Sprintf("Role %02d", i))
	}
	for range 5 {
		stacks = append(stacks, "Alpha, Bravo, Charlie, Delta, Echo, Foxtrot, Golf, Hotel")
	}
	p := Build(Input{Roles: roles, Stacks: stacks})
	if len(p.Searches) != DefaultSearches {
		t.Fatalf("%d searches, want %d", len(p.Searches), DefaultSearches)
	}
	nSkill := 0
	for _, s := range p.Searches {
		if s.From == Skill {
			nSkill++
		}
	}
	if nSkill != DefaultSkills {
		t.Errorf("%d skill searches, want %d", nSkill, DefaultSkills)
	}
	over := 0
	for _, s := range p.Skipped {
		if s.Why == WhyOverCap {
			over++
		}
	}
	if want := 30 + 8 - DefaultSearches; over != want {
		t.Errorf("%d over the cap, want %d", over, want)
	}
	// The hard cap holds whatever is asked, and MaxSkills < 0 is none.
	p = Build(Input{Roles: roles, Stacks: stacks, MaxSearches: 1000, MaxSkills: -1})
	if len(p.Searches) != MaxSearches || p.Searches[len(p.Searches)-1].From != Role {
		t.Errorf("%d searches, last %+v", len(p.Searches), p.Searches[len(p.Searches)-1])
	}
	// Fewer roles than slots: the plan is roles plus MaxSkills skills, no more.
	p = Build(Input{Roles: []string{"Inference"}, Stacks: stacks, MaxSkills: 3})
	if got := texts(p.Searches); !slices.Equal(got, []string{"Inference", "Alpha", "Bravo", "Charlie"}) {
		t.Errorf("searches %v", got)
	}
}

func TestBuildCountsUnreadableStacks(t *testing.T) {
	stacks := []string{strings.Repeat("a", 3000), "Rust\x01", "Rust", "Rust", "Rust"}
	p := Build(Input{Stacks: stacks})
	if p.StacksUnread != 2 || !slices.Equal(texts(p.Searches), []string{"Rust"}) {
		t.Errorf("%+v", p)
	}
	big := make([]string, maxStacks+7)
	for i := range big {
		big[i] = "Rust"
	}
	if p := Build(Input{Stacks: big}); p.StacksUnread != 7 || p.Searches[0].Entries != maxStacks {
		t.Errorf("past maxStacks: unread %d, entries %d", p.StacksUnread, p.Searches[0].Entries)
	}
}

func TestBuildOfNothing(t *testing.T) {
	p := Build(Input{})
	if len(p.Searches) != 0 || len(p.Skipped) != 0 || p.SkillsBelowFloor != 0 {
		t.Errorf("%+v", p)
	}
}
