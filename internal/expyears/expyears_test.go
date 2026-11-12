package expyears

import (
	"strings"
	"testing"
)

func TestRequired(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       int
		ok         bool
	}{
		{"empty", "", 0, false},
		{"no figure", "Strong Go and Rust; ships production LLM features.", 0, false},
		{"plain plus", "5+ years of experience as a software engineer", 5, true},
		// Real postings often name no "experience" beside the figure (Anthropic, Staff+ Caching).
		{"no experience word", "10+ years building and scaling distributed infrastructure, with 3+ years leading large-scale projects", 10, true},
		{"largest figure wins", "3+ years with Kubernetes. 8+ years of software engineering.", 8, true},
		{"range takes its lower bound", "2 to 10+ years of experience", 2, true},
		{"en dash range", "8–15+ years in security", 8, true},
		{"hyphen range", "3-5 years building backend services", 3, true},
		{"plus word", "7 plus years of industry experience", 7, true},
		{"yrs", "6 yrs experience with CUDA", 6, true},
		{"singular", "1 year of Go", 1, true},
		{"case", "5+ YEARS OF EXPERIENCE", 5, true},
		{"ago is history, not a requirement", "Founded 10 years ago, we now serve millions.", 0, false},
		{"old is age", "Applicants must be 18 years old.", 0, false},
		{"within is a deadline", "Ship the platform within 2 years.", 0, false},
		{"past is history", "Over the past 5 years we tripled. Over the last 3 years too.", 0, false},
		{"hyphenated degree", "A 4-year degree in computer science.", 0, false},
		{"three digits are no figure", "Our 100 years of combined experience.", 0, false},
		{"implausible figure ignored", "50 years of history; 5+ years of Python.", 5, true},
		{"zero ignored", "0 years of experience needed", 0, false},
		{"glued to a word is no figure", "v2years of x", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Required(tc.text)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Required(%q) = %d, %v; want %d, %v", tc.text, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRequiredBoundsItsInput(t *testing.T) {
	// A figure past the scan bound is not read: the bound holds on hostile input.
	long := strings.Repeat("x ", MaxScanBytes) + "12+ years of experience"
	if y, ok := Required(long); ok {
		t.Fatalf("read %d years past the %d-byte scan bound", y, MaxScanBytes)
	}
	// One inside it is.
	if y, ok := Required("12+ years of experience " + strings.Repeat("x ", MaxScanBytes)); !ok || y != 12 {
		t.Fatalf("Required = %d, %v; want 12, true", y, ok)
	}
	// Many figures stay linear and pick the largest.
	many := strings.Repeat("2+ years of Go. ", 2000) + "9+ years of systems work."
	if len(many) > MaxScanBytes {
		t.Fatalf("fixture is %d bytes, past the %d-byte bound", len(many), MaxScanBytes)
	}
	if y, ok := Required(many); !ok || y != 9 {
		t.Fatalf("Required = %d, %v; want 9, true", y, ok)
	}
}

func TestBelowStaff(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		ok               bool
	}{
		{"no figure keeps the reading", "Staff engineer for our platform.", "", false},
		{"staff-level years keep it", "8+ years of experience", "", false},
		{"exactly the floor keeps it", "7+ years building production systems", "", false},
		{"5 years reads senior", "5+ years of experience as a software engineer", "senior", true},
		{"6 years reads senior", "6+ years", "senior", true},
		{"4 years reads mid", "Member of Technical Staff: 4+ years of experience", "mid", true},
		{"1 year reads mid, never a new-grad claim", "1 year of Go", "mid", true},
		{"largest figure decides", "3+ years leading teams; 10+ years building infrastructure", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := BelowStaff(tc.text)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("BelowStaff(%q) = %q, %v; want %q, %v", tc.text, got, ok, tc.want, tc.ok)
			}
		})
	}
}
