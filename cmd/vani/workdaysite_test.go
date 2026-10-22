package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani watchlist add` takes Snap's board on the shared Workday host, whose
// name carries no tenant.
func TestWatchlistAddWorkdaySiteHost(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "wd", "--token", "snapchat/snap", "--company", "Snap",
		"--host", "wd1.myworkdaysite.com", "--search", "machine learning", "--allow-host")
	mustCode(t, 0, "config", "check")
	o, _ := mustCode(t, 0, "watchlist", "list")
	if !strings.Contains(o, "snapchat/snap") {
		t.Fatalf("list %q", o)
	}
	for _, bad := range [][]string{
		{"--host", "wd1.myworkdaysite.com:8080"},
		{"--host", "wd1.myworkdaysite.com/x"},
		{"--host", "evil.myworkdaysite.com"},
		{"--host", "wd1.myworkdaysite.com", "--token", "snap%2e/snap"},
	} {
		args := append([]string{"watchlist", "add", "--ats", "wd", "--token", "snapchat/other", "--company", "Snap"}, bad...)
		if _, e := mustCode(t, 2, args...); !strings.Contains(e, "needs host") && !strings.Contains(e, "tenant/site") {
			t.Errorf("%v: stderr %q", bad, e)
		}
	}
}

// A saved search of a shared-host listing page becomes the board, its tenant
// read from the path.
func TestDeriveBoardWorkdaySiteHost(t *testing.T) {
	for name, tc := range map[string]struct {
		url, token, search string
	}{
		"plain":      {"https://wd1.myworkdaysite.com/recruiting/snapchat/snap", "snapchat/snap", ""},
		"locale":     {"https://wd1.myworkdaysite.com/en-US/recruiting/snapchat/snap?q=machine+learning", "snapchat/snap", "machine learning"},
		"job page":   {"https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/ML_R1-1", "snapchat/snap", ""},
		"upper host": {"https://WD2.MyWorkdaySite.com/recruiting/acme/Ext", "acme/Ext", ""},
	} {
		t.Run(name, func(t *testing.T) {
			b, res := deriveBoard(tc.url, nil, "Snap", nil)
			if res.Status != "" {
				t.Fatalf("result %+v", res)
			}
			if b.ATS != posting.Workday || b.Token != tc.token || b.Search != tc.search || !strings.HasSuffix(b.Host, ".myworkdaysite.com") || b.Host != strings.ToLower(b.Host) {
				t.Errorf("board %+v", b)
			}
		})
	}
	for _, u := range []string{
		"https://wd1.myworkdaysite.com/",
		"https://wd1.myworkdaysite.com/recruiting",
		"https://wd1.myworkdaysite.com/recruiting/snapchat",
		"https://wd1.myworkdaysite.com/en-US/recruiting/snapchat",
		"https://wd1.myworkdaysite.com/snap/job/x/R1", // the myworkdayjobs shape names no tenant here
		"https://wd1.myworkdaysite.com/careers/snapchat/snap",
	} {
		if b, res := deriveBoard(u, nil, "Snap", nil); res.Status != searchInvalid {
			t.Errorf("%s: board %+v result %+v, want invalid", u, b, res)
		}
	}
}

// Every route to a shared-host posting finds the one the ledger holds.
func TestJobsFindFoldsSharedWorkdayHostRoutes(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "wd:snapchat:snap:r1", url: "https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R1-1", status: posting.Digested},
		findSeed{key: "wd:snapchat:snap:r2", url: "https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R2-1", status: posting.Digested},
	)
	const host = "https://wd1.myworkdaysite.com"
	for _, u := range []string{
		host + "/en-US/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R1-1",
		host + "/recruiting/snapchat/snap/job/Palo-Alto-California/Machine-Learning-Engineer_R1-1",
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R1-1/apply/applyManually",
	} {
		if out := findJSON(t, u); out.Job == nil || string(out.Job.Key) != "wd:snapchat:snap:r1" {
			t.Errorf("%s: %+v", u, out)
		}
	}
	for _, u := range []string{
		host + "/recruiting/othertenant/snap/job/Paris-France/Machine-Learning-Engineer_R1-1",
		host + "/recruiting/snapchat/other/job/Paris-France/Machine-Learning-Engineer_R1-1",
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R9-1",
	} {
		if out := findJSON(t, u); out.Job != nil {
			t.Errorf("%s matched %+v", u, out.Job)
		}
	}
}
