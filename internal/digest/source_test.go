package digest

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A job board's rows say where they came from, and the notes its terms ask for
// (Dice's AI disclosure, Adzuna's attribution) are shown once, only when such
// rows are on the page.
func TestJobBoardRowsNameTheirSourceAndCarryTheirNotes(t *testing.T) {
	recs := []ledger.Record{
		rec("dc:dice:46f0d513-ff59-4049-b4ab-9b45d8002fbe", posting.Explained, "Dice role", map[string]string{"rank": "80", "explanation": "x"}),
		rec("dc:dice:56f0d513-ff59-4049-b4ab-9b45d8002fbe", posting.Explained, "Dice role two", map[string]string{"rank": "70", "explanation": "y"}),
		rec("ad:us:5012345678", posting.Explained, "Adzuna role", map[string]string{"rank": "60", "explanation": "z"}),
		rec("gh:acme:1", posting.Explained, "Employer role", map[string]string{"rank": "50", "explanation": "w"}),
	}
	md, _ := Render(recs, nil, day, Counts{})
	if strings.Count(md, "- Source: Dice\n") != 2 || strings.Count(md, "- Source: Adzuna\n") != 1 {
		t.Fatalf("source lines wrong:\n%s", md)
	}
	if strings.Count(md, "- Source:") != 3 {
		t.Fatalf("an employer's own row got a source line:\n%s", md)
	}
	dice, adzuna := posting.SourceOf("dc:dice:x").Note, posting.SourceOf("ad:us:1").Note
	if strings.Count(md, dice) != 1 || strings.Count(md, adzuna) != 1 {
		t.Fatalf("notes not shown exactly once:\n%s", md)
	}
	if strings.Index(md, dice) < strings.Index(md, "---") {
		t.Fatalf("the notes belong in the footer:\n%s", md)
	}

	only, _ := Render(recs[3:], nil, day, Counts{})
	if strings.Contains(only, dice) || strings.Contains(only, adzuna) || strings.Contains(only, "- Source:") {
		t.Fatalf("notes or sources without job-board rows:\n%s", only)
	}
}
