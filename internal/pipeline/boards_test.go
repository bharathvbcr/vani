package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

var pageBoard = sources.Board{ATS: posting.Page, Token: "acme-careers", Company: "Acme",
	URL: "https://acme.example/careers", LinkPattern: `/careers/\d+`}

func pagePost(t *testing.T, url, title string) posting.Posting {
	t.Helper()
	k, err := posting.PageKey(pageBoard.Token, url)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Page, Company: "Acme", Title: title, Location: "Remote", URL: url, Description: "d"}
}

func TestPagePostingDuplicatesATSPosting(t *testing.T) {
	l := openLedger(t)
	gh := ghPost(t, "1", "jd") // Title "Engineer 1"
	if _, err := Reconcile(ctx, l, board, []posting.Posting{gh}, t0); err != nil {
		t.Fatal(err)
	}
	pg := pagePost(t, "https://acme.example/careers/1", "Engineer 1")
	s, err := Reconcile(ctx, l, pageBoard, []posting.Posting{pg}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Duplicate != 1 {
		t.Fatalf("summary %+v, want one duplicate", s)
	}
	got := rec(t, l, pg.Key)
	if got.Status != posting.Duplicate || got.Annotations[DuplicateOfKey] != string(gh.Key) {
		t.Fatalf("page twin: %s %v", got.Status, got.Annotations)
	}
	if rec(t, l, gh.Key).Status != posting.Live {
		t.Fatal("the ATS posting must stay live")
	}
}

func TestATSPostingArrivingLaterMarksPageTwin(t *testing.T) {
	l := openLedger(t)
	pg := pagePost(t, "https://acme.example/careers/1", "Engineer 1")
	if _, err := Reconcile(ctx, l, pageBoard, []posting.Posting{pg}, t0); err != nil {
		t.Fatal(err)
	}
	gh := ghPost(t, "1", "jd")
	s, err := Reconcile(ctx, l, board, []posting.Posting{gh}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Duplicate != 1 || rec(t, l, pg.Key).Status != posting.Duplicate {
		t.Fatalf("summary %+v, page status %s", s, rec(t, l, pg.Key).Status)
	}
}

func TestPostingKeyedUnderAnotherBoardIsInvalid(t *testing.T) {
	l := openLedger(t)
	other, _ := posting.GreenhouseKey("globex", "9")
	p := posting.Posting{Key: other, ATS: posting.Greenhouse, Company: "Globex", Title: "x", Description: "d"}
	s, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Invalid != 1 || len(l.All()) != 0 {
		t.Fatalf("summary %+v, %d records", s, len(l.All()))
	}
}

func TestPageBoardAgesItsOwnPostings(t *testing.T) {
	l := openLedger(t)
	pg := pagePost(t, "https://acme.example/careers/7", "Designer")
	if _, err := Reconcile(ctx, l, pageBoard, []posting.Posting{pg}, t0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MissesToDead; i++ {
		if _, err := Reconcile(ctx, l, pageBoard, nil, t0); err != nil {
			t.Fatal(err)
		}
	}
	if st := rec(t, l, pg.Key).Status; st != posting.Dead {
		t.Fatalf("status %s, want dead after %d absent listings", st, MissesToDead)
	}
}

func TestWorkdayKeyPrefixMatchesKeys(t *testing.T) {
	b := sources.Board{ATS: posting.Workday, Token: "Acme/External", Host: "acme.wd5.myworkdayjobs.com"}
	k, err := posting.WorkdayKey("acme", "external", "R123")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.KeyPrefix(); got != "wd:acme:external:" || string(k)[:len(got)] != got {
		t.Fatalf("prefix %q does not cover key %q", got, k)
	}
	l := openLedger(t)
	p := posting.Posting{Key: k, ATS: posting.Workday, Company: "Acme", Title: "x", Description: "d"}
	if s, err := Reconcile(ctx, l, b, []posting.Posting{p}, t0); err != nil || s.New != 1 {
		t.Fatalf("summary %+v err %v", s, err)
	}
}
