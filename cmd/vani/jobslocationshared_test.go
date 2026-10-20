package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Exact company+title duplicates fold into one row of the ranked "new" view
// (diversifyJobs), whatever their location; the "all" view and the location
// filters still see every posting. The macOS contract test once seeded three
// same-title postings and read the fold as a location-filter bug, so its
// fixture now gives each posting its own title; this pins the fold itself.
func TestJobsSameTitleFoldsInNewButLocationsStayFilterable(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for i, s := range []locSeed{{key: "gh:acme:1", where: "Las Cruces, New Mexico; El Paso, TX"},
		{key: "gh:acme:2", where: "Remote - US"}, {key: "gh:acme:3", where: "London, United Kingdom"}} {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: "Acme", Title: "Engineer",
			URL: "https://boards.example.com/1", Location: s.where}
		e := ledger.Event{V: 1, At: jobsT0.Add(-time.Duration(i+1) * time.Hour), Key: p.Key, Status: posting.Explained, Posting: &p}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	n := listJSON(t)
	if len(n.Jobs) != 1 || n.Jobs[0].Similar != 2 {
		t.Errorf("new view: %q (similar %d), want one row folding 2", keys(n), func() int {
			if len(n.Jobs) > 0 {
				return n.Jobs[0].Similar
			}
			return -1
		}())
	}
	if got := keys(listJSON(t, "--view", "all")); !sameKeys(got, "gh:acme:1 gh:acme:2 gh:acme:3") {
		t.Errorf("all view: %q", got)
	}
	if got := keys(listJSON(t, "--view", "all", "--state", "none")); !sameKeys(got, "gh:acme:2 gh:acme:3") {
		t.Errorf("--state none: %q", got)
	}
}
