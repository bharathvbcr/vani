package digest

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestEmployerBadgeShownAndBoostExplained(t *testing.T) {
	boosted := rec("gh:a:1", posting.Explained, "Boosted", map[string]string{
		ledger.AnnRank: "70", ledger.AnnCoverage: "60",
		ledger.AnnEmployer: "likely H-1B cap-exempt (degree-granting public or non-profit institution, IPEDS) (exact name match)",
	})
	plain := rec("gh:a:2", posting.Explained, "Plain", map[string]string{
		ledger.AnnRank: "65", ledger.AnnCoverage: "65",
		ledger.AnnEmployer: "no employer-list match",
	})
	unscored := rec("gh:a:3", posting.Explained, "Unscored", map[string]string{
		ledger.AnnEmployer: "likely non-profit (IRS exempt organizations list) (exact name match)",
	})
	md, keys := Render([]ledger.Record{plain, boosted, unscored}, day, Counts{})
	if len(keys) != 3 || keys[0] != "gh:a:1" || keys[1] != "gh:a:2" {
		t.Fatalf("order %v: the boosted rank must sort first", keys)
	}
	sections := strings.Split(md, "### ")
	if !strings.Contains(sections[1], "Must-have coverage: 60%") ||
		!strings.Contains(sections[1], "- Employer: likely H\\-1B cap\\-exempt") ||
		!strings.Contains(sections[1], "ranked up: the active track prefers this kind of employer") {
		t.Fatalf("boosted role:\n%s", sections[1])
	}
	if strings.Contains(sections[2], "ranked up") || !strings.Contains(sections[2], "- Employer: no employer\\-list match") {
		t.Fatalf("plain role:\n%s", sections[2])
	}
	if strings.Contains(sections[3], "ranked up") || !strings.Contains(sections[3], "Must-have coverage: not scored") {
		t.Fatalf("unscored role:\n%s", sections[3])
	}
}

func TestEmployerBadgeIsEscaped(t *testing.T) {
	r := rec("gh:a:1", posting.Explained, "X", map[string]string{
		ledger.AnnEmployer: "likely non-profit [click](javascript:alert(1))\n# heading",
	})
	md, _ := Render([]ledger.Record{r}, day, Counts{})
	if strings.Contains(md, "[click](") || strings.Contains(md, "\n# heading") {
		t.Fatalf("badge not escaped:\n%s", md)
	}
}
