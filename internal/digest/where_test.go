package digest

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestWhere(t *testing.T) {
	for _, c := range []struct {
		loc    string
		remote bool
		want   string
	}{
		{"El Paso, TX", false, "El Paso, TX"},
		{"Las Cruces, New Mexico", false, "Las Cruces, NM"},
		{"Remote - US", false, "Remote \\(US\\)"},
		{"Austin, TX", true, "Austin, TX · Remote"},
		{"", true, "Remote"},
		{"", false, "location not listed"},
		// Nothing recognised: the board's text, escaped.
		{"2 Locations", false, "2 Locations"},
		// Markdown in the board's text never survives as a link or emphasis.
		{"*Anywhere* [x](y)", false, "\\*Anywhere\\*, x, y"},
		{"**Bold**, TX", false, "\\*\\*Bold\\*\\*, TX"},
		{"Line\nbreak", false, "Line; break"},
	} {
		if got := Where(posting.Posting{Location: c.loc, Remote: c.remote}); got != c.want {
			t.Errorf("Where(%q, %v) = %q, want %q", c.loc, c.remote, got, c.want)
		}
	}
}

func TestDigestWhereLine(t *testing.T) {
	r := rec("gh:a:1", posting.Explained, "Engineer", nil)
	r.Posting.Location = "Remote (Canada)"
	md, _ := Render([]ledger.Record{r}, nil, day, Counts{})
	if !strings.Contains(md, "- Where: Remote \\(Canada\\)\n") {
		t.Errorf("where line:\n%s", md)
	}
}
