package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A saved search of a SmartRecruiters company page becomes that company's
// board, on either host the company page is served from.
func TestDeriveBoardSmartRecruiters(t *testing.T) {
	for name, tc := range map[string]struct {
		url, token string
	}{
		"jobs company page":     {"https://jobs.smartrecruiters.com/ServiceNow", "ServiceNow"},
		"careers company page":  {"https://careers.smartrecruiters.com/Canva", "Canva"},
		"posting page":          {"https://jobs.smartrecruiters.com/Canva/6000000001526540-user-voice-program-manager-spain-", "Canva"},
		"upper host":            {"https://JOBS.SmartRecruiters.com/ServiceNow/", "ServiceNow"},
		"search query":          {"https://careers.smartrecruiters.com/ServiceNow?search=engineer", "ServiceNow"},
		"hyphen and underscore": {"https://jobs.smartrecruiters.com/Bosch-Group_2", "Bosch-Group_2"},
	} {
		t.Run(name, func(t *testing.T) {
			b, res := deriveBoard(tc.url, nil, "Co", nil)
			if res.Status != "" {
				t.Fatalf("result %+v", res)
			}
			if b.ATS != posting.SmartRecruiters || b.Token != tc.token || b.Company != "Co" || b.Host != "" || b.Search != "" {
				t.Errorf("board %+v", b)
			}
			if err := config.ValidBoard(b); err != nil {
				t.Errorf("the derived board is refused: %v", err)
			}
		})
	}
}

func TestDeriveBoardSmartRecruitersRefusesWhatIsNotACompany(t *testing.T) {
	for _, u := range []string{
		"https://jobs.smartrecruiters.com/",
		"https://careers.smartrecruiters.com",
		// The site's own apps, seen in referral and apply links.
		"https://jobs.smartrecruiters.com/oneclick-ui/company/Canva",
		"https://jobs.smartrecruiters.com/ONECLICK-UI/company/Canva",
		"https://jobs.smartrecruiters.com/external-referrals/company/Canva/publication/d638d246",
		"https://careers.smartrecruiters.com/external-referrals/x",
	} {
		if b, res := deriveBoard(u, nil, "Co", nil); res.Status != searchInvalid || b.ATS != "" {
			t.Errorf("%s: board %+v result %+v, want invalid", u, b, res)
		}
	}
}

// Only the two verified hosts are SmartRecruiters: a lookalike or a suffix trick
// is an ordinary careers page, never an sr board.
func TestDeriveBoardSmartRecruitersHostsAreExact(t *testing.T) {
	for _, u := range []string{
		"https://smartrecruiters.com/Canva",
		"https://www.smartrecruiters.com/Canva",
		"https://api.smartrecruiters.com/v1/companies/Canva/postings",
		"https://jobs.smartrecruiters.com.evil.example/Canva",
		"https://evil.example/jobs.smartrecruiters.com/Canva",
		"https://xjobs.smartrecruiters.com/Canva",
		"http://jobs.smartrecruiters.com/Canva", // not https
	} {
		if b, _ := deriveBoard(u, nil, "Co", nil); b.ATS == posting.SmartRecruiters {
			t.Errorf("%s was read as a SmartRecruiters board: %+v", u, b)
		}
	}
}

// A first segment that cannot be a company identifier still derives (as for
// Greenhouse), and is then refused where every board is: ValidBoard.
func TestDeriveBoardSmartRecruitersHostileSegmentIsRefusedByValidBoard(t *testing.T) {
	for _, seg := range []string{"now.com", "a%2eb", "..", "a%20b", "-lead", strings.Repeat("a", 101)} {
		b, res := deriveBoard("https://jobs.smartrecruiters.com/"+seg, nil, "Co", nil)
		if res.Status != "" || b.ATS != posting.SmartRecruiters {
			continue // refused earlier is fine too
		}
		if err := config.ValidBoard(b); err == nil {
			t.Errorf("segment %q: token %q passed ValidBoard", seg, b.Token)
		}
	}
}

// `vani watchlist add` takes an sr board with a country filter, and refuses a
// country the API would answer with zero postings.
func TestWatchlistAddSmartRecruiters(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "sr", "--token", "servicenow", "--company", "ServiceNow",
		"--filter", "country=us", "--allow-host")
	mustCode(t, 0, "config", "check")
	if o, _ := mustCode(t, 0, "watchlist", "list"); !strings.Contains(o, "sr\tservicenow") {
		t.Fatalf("list %q", o)
	}
	for _, bad := range [][]string{
		{"--filter", "country=usa"},
		{"--filter", "country=US,CA"},
		{"--filter", "region=x"},
		{"--search", "engineer"},
		{"--host", "api.smartrecruiters.com"},
		{"--token", "now.com"},
	} {
		args := append([]string{"watchlist", "add", "--ats", "sr", "--token", "canva", "--company", "Canva"}, bad...)
		mustCode(t, 2, args...)
	}
}
