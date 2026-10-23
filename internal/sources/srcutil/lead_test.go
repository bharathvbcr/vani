package srcutil

import (
	"strings"
	"testing"
)

func TestRoleLine(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"none", nil, ""},
		{"blank", []string{"", " \t"}, ""},
		{"one", []string{"full-time"}, "Role: full-time"},
		// Microsoft's flags, live 2026-10-20.
		{"eightfold", []string{"Individual Contributor", "full-time", "0 days / week in-office – remote"}, "Role: Individual Contributor, full-time, 0 days / week in-office – remote"},
		{"repeat any case", []string{"full-time", "Full-Time", " full-time "}, "Role: full-time"},
		{"whitespace collapsed", []string{"security\n clearance"}, "Role: security clearance"},
		{"over-long flag dropped", []string{strings.Repeat("x", maxRoleFlagLen+1), "temporary"}, "Role: temporary"},
		{"bounded count", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, "Role: a, b, c, d, e, f, g, h"},
	} {
		if got := RoleLine(c.flags...); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEmploymentType(t *testing.T) {
	for in, want := range map[string]string{
		"FULL_TIME": "full-time", "Full time": "full-time", "Full-Time": "full-time", "full-time": "full-time",
		"PART_TIME": "part-time", "Part time": "part-time",
		"TEMPORARY": "temporary", "CONTRACTOR": "contract", "Contract": "contract", "INTERN": "internship",
		"": "", "  ": "", "Seasonal  Role": "Seasonal Role", "PER_DIEM": "PER_DIEM",
	} {
		if got := EmploymentType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestLead(t *testing.T) {
	lead, room := Lead("", "Pay range: USD 1 – 2 per year", " ", "Role: full-time")
	if lead != "Pay range: USD 1 – 2 per year\nRole: full-time\n\n" || room != MaxDescription-len(lead) {
		t.Errorf("lead %q room %d", lead, room)
	}
	if lead, room := Lead(); lead != "" || room != MaxDescription {
		t.Errorf("empty lead %q room %d", lead, room)
	}
	if lead, _ := Lead("Role: a\nb"); lead != "" {
		t.Errorf("a line with a break was kept: %q", lead)
	}
	// Oversized lines are dropped whole from the end, never cut.
	big := RolePrefix + strings.Repeat("x", maxLeadLinesLen)
	lead, room = Lead("Pay range: USD 1 – 2", big)
	if lead != "Pay range: USD 1 – 2\n\n" || room < MaxDescription-maxLeadLinesLen-2 {
		t.Errorf("oversized: %q room %d", lead, room)
	}
	// Whatever the lead, SourceText of lead + board text is the board text.
	for _, lines := range [][]string{
		{"Pay range: USD 1 – 2 per year", "Role: full-time"},
		{"Role: full-time"},
		{"Compensation: $1 - $2", "Role: temporary"},
	} {
		lead, _ := Lead(lines...)
		if got := SourceText(lead + "Build.\nShip."); got != "Build.\nShip." {
			t.Errorf("%v: SourceText %q", lines, got)
		}
	}
}

func TestSourceTextRoleLines(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"role then pay", "Role: full-time\nPay range: USD 1 – 2\n\nBuild.", "Build."},
		{"role after board text stays", "Build.\n\nRole: full-time", "Build.\n\nRole: full-time"},
		{"role inside a line stays", "The Role: lead\n\nBuild.", "The Role: lead\n\nBuild."},
		{"role without its space stays", "Role:x\n\nBuild.", "Role:x\n\nBuild."},
	} {
		if got := SourceText(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
