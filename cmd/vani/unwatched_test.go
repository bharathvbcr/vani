package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

type engagedRow struct {
	key, company string
	mark         marks.Mark
}

// seedEngaged writes the postings (one Live record each, no description text)
// and sets their marks.
func seedEngaged(t *testing.T, state string, rows []engagedRow) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for _, r := range rows {
		p := posting.Posting{Key: posting.Key(r.key), ATS: posting.Key(r.key).ATS(), Company: r.company, Title: "Engineer", Description: "jd"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: at, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		if r.mark != marks.None {
			if _, err := marks.SetMark(marks.Dir(state), p.Key, r.mark, at); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func watch(t *testing.T, cfgPath string, boards ...sources.Board) {
	t.Helper()
	for _, b := range boards {
		mustCode(t, 0, "watchlist", "add", "--ats", string(b.ATS), "--token", b.Token, "--company", b.Company, "--allow-host")
	}
}

type unwatchedJSON struct {
	UnwatchedCompanies *struct {
		OK        bool   `json:"ok"`
		Reason    string `json:"reason"`
		Count     int    `json:"count"`
		Companies []struct {
			Company  string `json:"company"`
			Applied  int    `json:"applied"`
			Saved    int    `json:"saved"`
			Ingested int    `json:"ingested"`
		} `json:"companies"`
		Listed int `json:"listed"`
	} `json:"unwatched_companies"`
}

// Companies the owner engaged with (saved or applied to a posting, or had the
// extension ingest one) that no watched board covers are an attention line of
// `vani status` and a facet of its JSON: read-only, no network. A company a
// board of the watchlist covers (by the posting's key or by the company's name,
// ignoring case, punctuation and a legal suffix) is watched; a hidden posting
// is no engagement; a posting nobody marked or ingested is none either.
func TestStatusNamesEngagedCompaniesNoBoardWatches(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	watch(t, cfgPath,
		sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"},
		sources.Board{ATS: posting.Lever, Token: "hooli", Company: "Hooli, Inc."},
	)
	seedEngaged(t, state, []engagedRow{
		{"gh:acme:1", "Acme", marks.Applied},                        // on a watched board
		{"x:0000000000000000000000a1", "Acme", marks.Saved},         // watched by name, though fallback-keyed
		{"gh:other:9", "hooli", marks.Saved},                        // watched by name, loosely
		{"gh:globex:1", "Globex", marks.Applied},                    // not watched, applied
		{"gh:globex:2", "Globex", marks.Saved},                      // the same company
		{"gh:initech:1", "Initech Inc", marks.Saved},                // not watched, saved
		{"pg:news-scrape:aaaaaaaaaaaaaaaaaaaaaaaa", "Umbrella", ""}, // ingested through the extension
		{"pg:news-scrape:bbbbbbbbbbbbbbbbbbbbbbbb", "Umbrella", ""},
		{"gh:wayne:1", "Wayne", marks.Hidden}, // hidden: not interest
		{"gh:stark:1", "Stark", ""},           // nothing the owner did
		{"gh:nameless:1", "", marks.Saved},    // no company to name
	})

	o, _ := mustCode(t, 0, "status", "--json")
	var out unwatchedJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	u := out.UnwatchedCompanies
	if u == nil || !u.OK || u.Count != 3 || len(u.Companies) != 3 {
		t.Fatalf("facet = %+v\n%s", u, o)
	}
	got := u.Companies
	if got[0].Company != "Globex" || got[0].Applied != 1 || got[0].Saved != 1 ||
		got[1].Company != "Initech Inc" || got[1].Saved != 1 ||
		got[2].Company != "Umbrella" || got[2].Ingested != 2 {
		t.Errorf("companies = %+v (applied first, then saved, then ingested)", got)
	}
	txt, _ := mustCode(t, 0, "status")
	for _, want := range []string{"companies you engaged with but don't watch: 3", "Globex", "Initech Inc", "Umbrella"} {
		if !strings.Contains(txt, want) {
			t.Errorf("status lacks %q:\n%s", want, txt)
		}
	}
	for _, not := range []string{"Wayne", "Stark", "Acme"} {
		if strings.Contains(txt, not) {
			t.Errorf("status names %q:\n%s", not, txt)
		}
	}
}

// Watching the company clears the line; a ledger that engaged with nothing
// unwatched says nothing and the JSON facet is absent or empty.
func TestStatusHasNoEngagedLineWhenEveryCompanyIsWatched(t *testing.T) {
	cfgPath, state := setup(t)
	mustCode(t, 0, "init")
	watch(t, cfgPath, sources.Board{ATS: posting.Greenhouse, Token: "globex", Company: "Globex"})
	seedEngaged(t, state, []engagedRow{{"gh:globex:1", "Globex", marks.Applied}})
	txt, _ := mustCode(t, 0, "status")
	if strings.Contains(txt, "engaged with but don't watch") {
		t.Errorf("a watched company is reported:\n%s", txt)
	}
	o, _ := mustCode(t, 0, "status", "--json")
	var out unwatchedJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	if u := out.UnwatchedCompanies; u != nil && (u.Count != 0 || len(u.Companies) != 0) {
		t.Errorf("facet = %+v", u)
	}
}

// The list is bounded, the count is not, and the same company written two ways
// is one.
func TestStatusEngagedCompaniesListIsBoundedAndTheCountIsExact(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	var rows []engagedRow
	for i := 0; i < 45; i++ {
		rows = append(rows, engagedRow{fmt.Sprintf("gh:c%02d:1", i), fmt.Sprintf("Company %02d", i), marks.Saved})
	}
	rows = append(rows, engagedRow{"gh:c00:2", "COMPANY 00 LLC", marks.Saved}) // the same company, another spelling
	seedEngaged(t, state, rows)
	o, _ := mustCode(t, 0, "status", "--json")
	var out unwatchedJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	u := out.UnwatchedCompanies
	if u == nil || u.Count != 45 || len(u.Companies) != maxUnwatchedListed || u.Listed != maxUnwatchedListed {
		t.Fatalf("facet %+v", u)
	}
	if u.Companies[0].Company != "Company 00" || u.Companies[0].Saved != 2 {
		t.Errorf("the most engaged company leads: %+v", u.Companies[0])
	}
	txt, _ := mustCode(t, 0, "status")
	if !strings.Contains(txt, "companies you engaged with but don't watch: 45") || !strings.Contains(txt, "25 more") {
		t.Errorf("text:\n%s", txt)
	}
}

// Marks that cannot be read are said, not taken for none: the facet is not ok.
func TestStatusEngagedCompaniesSaysWhenMarksAreUnreadable(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedEngaged(t, state, []engagedRow{{"gh:globex:1", "Globex", ""}})
	if err := os.MkdirAll(marks.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(marks.Dir(state), "marks.jsonl"), "{not json\n")
	o, _ := mustCode(t, 0, "status", "--json")
	var out unwatchedJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	if u := out.UnwatchedCompanies; u == nil || u.OK || u.Reason == "" {
		t.Errorf("facet = %+v", u)
	}
}
