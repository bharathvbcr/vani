package workday

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	pathA = "/job/US-CA-Santa-Clara/NVIDIA-2027-Ignite-Internships--Hardware-Engineering_JR2027047"
	pathB = "/job/India-Bengaluru/CAD-Engineer_JR2014480-1"
)

// aDayNoneIsDue is a time whose day is no stored row's turn to be re-read, so
// the only thing that can change a reused row is its listing.
func aDayNoneIsDue(t *testing.T, ids ...string) time.Time {
	t.Helper()
	for d := 0; d < srcutil.RefreshDays; d++ {
		at := fixedNow.AddDate(0, 0, d)
		due := false
		for _, id := range ids {
			due = due || srcutil.RefreshTurn(id, srcutil.Day(at))
		}
		if !due {
			return at
		}
	}
	t.Fatal("every day is some row's turn")
	return time.Time{}
}

// listRows serves a listing of the two fixture rows with the given place text,
// and counts the detail requests.
func listAndCount(textA, textB string, details *int) func(srctest.Request) (srctest.Reply, bool) {
	row := func(path, text, title string) string {
		return fmt.Sprintf(`{"title":%q,"externalPath":%q,"locationsText":%q,"postedOn":"Posted Today","bulletFields":["JR1"]}`, title, path, text)
	}
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			return ok(`{"total":2,"jobPostings":[` + row(pathA, textA, "A") + `,` + row(pathB, textB, "B") + `]}`), true
		}
		*details++
		return srctest.Reply{Status: 500}, true
	}
}

func storedFrom(locA, locB string) []posting.Posting {
	mk := func(path, loc string) posting.Posting {
		k, _ := posting.WorkdayKey("nvidia", "NVIDIAExternalCareerSite", path[strings.LastIndex(path, "_")+1:])
		return posting.Posting{Key: k, ATS: posting.Workday, Company: "NVIDIA", Title: "T", Location: loc,
			URL: "https://" + host + "/NVIDIAExternalCareerSite" + path, Description: "stored body"}
	}
	return []posting.Posting{mk(pathA, locA), mk(pathB, locB)}
}

// A stored row is reused without a detail request, so a place that moved on the
// board stayed hidden until the row's re-read turn, and an exclusion drop for
// the old place was never judged again. The listed place is carried onto the
// reused copy.
func TestReusedRowCarriesTheListedPlace(t *testing.T) {
	long := strings.Repeat("Far Away ", 200)
	for name, tc := range map[string]struct {
		textA, textB string
		storedA      string
		storedB      string
		wantA, wantB string
		relocated    int
	}{
		"moved":                  {"US, CA, Santa Clara", "India, Pune", "US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Pune", 1},
		"both moved":             {"US, TX, Austin", "India, Pune", "US, CA, Santa Clara", "India, Bengaluru", "US, TX, Austin", "India, Pune", 2},
		"unchanged":              {"US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Bengaluru", 0},
		"case and spacing only":  {"us,  ca, santa clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Bengaluru", 0},
		"count names no place":   {"3 Locations", "12 locations", "US, CA, Santa Clara; US, TX, Austin", "India, Bengaluru", "US, CA, Santa Clara; US, TX, Austin", "India, Bengaluru", 0},
		"primary of several":     {"US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara; US, TX, Austin", "India, Bengaluru; India, Pune", "US, CA, Santa Clara; US, TX, Austin", "India, Bengaluru; India, Pune", 0},
		"row names none":         {"", "  ", "US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Bengaluru", 0},
		"oversized text":         {long, "India, Pune", "US, CA, Santa Clara", "India, Bengaluru", "US, CA, Santa Clara", "India, Pune", 1},
		"stored had no location": {"US, CA, Santa Clara", "India, Pune", "", "India, Bengaluru", "US, CA, Santa Clara", "India, Pune", 2},
	} {
		t.Run(name, func(t *testing.T) {
			details := 0
			s, _ := newSource(listAndCount(tc.textA, tc.textB, &details))
			s.now = func() time.Time { return aDayNoneIsDue(t, pathA, pathB) }
			var st Stats
			stored := storedFrom(tc.storedA, tc.storedB)
			s.opt = &Options{
				Stored: func(sources.Board) []posting.Posting { return stored },
				Report: func(_ sources.Board, got Stats) { st = got },
			}
			got, err := s.List(context.Background(), board)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Location != tc.wantA || got[1].Location != tc.wantB {
				t.Fatalf("locations %q, %q; want %q, %q", got[0].Location, got[1].Location, tc.wantA, tc.wantB)
			}
			if details != 0 || st.Reused != 2 || st.Relocated != tc.relocated {
				t.Fatalf("%d detail requests, stats %+v; want none, 2 reused, %d relocated", details, st, tc.relocated)
			}
			if got[0].Description != "stored body" {
				t.Fatalf("the reused copy lost its stored body: %q", got[0].Description)
			}
		})
	}
}
