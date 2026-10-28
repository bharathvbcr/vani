package workday

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Snap's board lives on the shared host wd1.myworkdaysite.com, whose name
// carries no tenant: the tenant comes from the token. The fixtures are real
// captures (testdata/README.md).
const (
	siteHost    = "wd1.myworkdaysite.com"
	snapJobsURL = "https://" + siteHost + "/wday/cxs/snapchat/snap/jobs"
	snapBase    = "https://" + siteHost + "/wday/cxs/snapchat/snap"
)

var snapBoard = sources.Board{ATS: posting.Workday, Token: "snapchat/snap", Company: "Snap", Host: siteHost}

func snapHandler(t *testing.T) func(srctest.Request) (srctest.Reply, bool) {
	list, d1, d2 := read(t, "snap_list.json"), read(t, "snap_detail1.json"), read(t, "snap_detail2.json")
	return func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case r.Method == "POST" && r.URL == snapJobsURL:
			return ok(list), true
		case r.Method == "GET" && r.URL == snapBase+"/job/Paris-France/Machine-Learning-Engineer_R0047006-1":
			return ok(d1), true
		case r.Method == "GET" && r.URL == snapBase+"/job/Palo-Alto-California/Machine-Learning-Engineer--Level-5_Q426SWEML5-1":
			return ok(d2), true
		}
		return srctest.Reply{}, false
	}
}

func TestSiteHostFixture(t *testing.T) {
	s, d := newSource(snapHandler(t))
	got, err := s.List(context.Background(), snapBoard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	p := got[0]
	if p.Key != "wd:snapchat:snap:r0047006" || p.Company != "Snap" || p.Location != "Paris, France" ||
		p.URL != "https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1" {
		t.Errorf("posting 0: %+v", p)
	}
	if got[1].Key != "wd:snapchat:snap:q426sweml5" {
		t.Errorf("posting 1 key: %q", got[1].Key)
	}
	for _, g := range got {
		if !strings.HasPrefix(string(g.Key), snapBoard.KeyPrefix()) {
			t.Errorf("key %q not under prefix %q", g.Key, snapBoard.KeyPrefix())
		}
	}
	if n := len(d.Requests()); n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}

// With no externalUrl in the detail, the recorded URL is the public page on the
// shared host, /recruiting/{tenant}/{site}/job/..., not /{site}/job/....
func TestSiteHostPublicURLFallback(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			return ok(`{"total":1,"jobPostings":[{"title":"t","externalPath":"/job/Paris-France/ML_R1-1"}]}`), r.URL == snapJobsURL
		}
		return ok(`{"jobPostingInfo":{"jobReqId":"R1","title":"t"}}`), r.URL == snapBase+"/job/Paris-France/ML_R1-1"
	})
	got, err := s.List(context.Background(), snapBoard)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/ML_R1-1"; got[0].URL != want {
		t.Errorf("URL %q, want %q", got[0].URL, want)
	}
}

// A tenant-labelled myworkdayjobs board keeps the /{site}/job/... fallback.
func TestJobsHostPublicURLFallbackUnchanged(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			return ok(`{"total":1,"jobPostings":[{"title":"t","externalPath":"/job/x/ML_R1-1"}]}`), r.URL == jobsURL
		}
		return ok(`{"jobPostingInfo":{"jobReqId":"R1","title":"t"}}`), true
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://" + host + "/NVIDIAExternalCareerSite/job/x/ML_R1-1"; got[0].URL != want {
		t.Errorf("URL %q, want %q", got[0].URL, want)
	}
}

func TestSiteHostBoardValidation(t *testing.T) {
	never := func(srctest.Request) (srctest.Reply, bool) {
		t.Error("request made for an invalid board")
		return srctest.Reply{}, false
	}
	cases := map[string]sources.Board{
		"host with path":      {ATS: posting.Workday, Token: "snapchat/snap", Host: "wd1.myworkdaysite.com/x"},
		"host with port":      {ATS: posting.Workday, Token: "snapchat/snap", Host: "wd1.myworkdaysite.com:8080"},
		"host in userinfo":    {ATS: posting.Workday, Token: "snapchat/snap", Host: "wd1.myworkdaysite.com@evil.example"},
		"suffix host":         {ATS: posting.Workday, Token: "snapchat/snap", Host: "wd1.myworkdaysite.com.evil.example"},
		"prefixed host":       {ATS: posting.Workday, Token: "snapchat/snap", Host: "evil.wd1.myworkdaysite.com"},
		"no wd number":        {ATS: posting.Workday, Token: "snapchat/snap", Host: "wd.myworkdaysite.com"},
		"bare domain":         {ATS: posting.Workday, Token: "snapchat/snap", Host: "myworkdaysite.com"},
		"tenant with percent": {ATS: posting.Workday, Token: "snap%2e/snap", Host: siteHost},
		"site with percent":   {ATS: posting.Workday, Token: "snapchat/sn%2fap", Host: siteHost},
		"extra segment":       {ATS: posting.Workday, Token: "snapchat/snap/x", Host: siteHost},
		"empty tenant":        {ATS: posting.Workday, Token: "/snap", Host: siteHost},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(never)
			if _, err := s.List(context.Background(), b); err == nil {
				t.Fatal("invalid board accepted")
			}
		})
	}
}

func TestSiteHostMatchIsCaseInsensitive(t *testing.T) {
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		return ok(`{"total":0,"jobPostings":[]}`), r.URL == "https://"+siteHost+"/wday/cxs/Snapchat/snap/jobs"
	})
	b := snapBoard
	b.Host = "WD1.MyWorkdaySite.com"
	b.Token = "Snapchat/snap"
	if _, err := s.List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

// A stored posting on the shared host is reused (no detail request), not left
// "unmatched": an unmatched stored posting disables deferral for the board.
func TestSiteHostStoredPostingIsReused(t *testing.T) {
	first, _ := newSource(snapHandler(t))
	stored, err := first.List(context.Background(), snapBoard)
	if err != nil {
		t.Fatal(err)
	}
	// A day on which neither stored row's re-read turn comes up.
	day := 0
	for srcutil.RefreshTurn("/job/Paris-France/Machine-Learning-Engineer_R0047006-1", day) ||
		srcutil.RefreshTurn("/job/Palo-Alto-California/Machine-Learning-Engineer--Level-5_Q426SWEML5-1", day) {
		day++
	}
	now := time.Unix(int64(day)*86400+3600, 0).UTC()
	var st Stats
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" {
			return ok(read(t, "snap_list.json")), r.URL == snapJobsURL
		}
		t.Errorf("detail request for a stored posting: %s", r.URL)
		return srctest.Reply{}, false
	})
	s := &source{d: d, now: func() time.Time { return now }, opt: &Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, x Stats) { st = x },
	}}
	out, err := s.List(context.Background(), snapBoard)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || st.Reused != 2 {
		t.Fatalf("got %d postings, %d reused; want 2 and 2", len(out), st.Reused)
	}
}
