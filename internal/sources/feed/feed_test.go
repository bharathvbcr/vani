package feed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const docURL = "https://jobs.example/careers/feed.xml"

var board = sources.Board{ATS: posting.Feed, Token: "Shopify", Company: "Board Co", URL: docURL}

func newSource(body string, status int) (*source, *srctest.Doer) {
	d := srctest.New(map[string]srctest.Reply{docURL: {Status: status, Body: []byte(body)}})
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func doc(jobs ...string) string {
	return "<source>" + strings.Join(jobs, "") + "</source>"
}

func job(id, extra string) string {
	return fmt.Sprintf(`<job>
		<partnerJobId>  %s  </partnerJobId>
		<company> Shopify </company>
		<title> Account Executive </title>
		<description><![CDATA[<p>Build &amp; ship</p><a href="https://evil.test/x">link</a><script>x()</script>]]></description>
		<applyUrl> https://www.shopify.com/careers/%s?utm_source=feed </applyUrl>
		<location> NAMER </location>
		<workplaceTypes> Remote </workplaceTypes>
		<listDate>10/15/2026</listDate>
		%s
	</job>`, id, id, extra)
}

func TestFeed(t *testing.T) {
	s, d := newSource(doc(job("1affb074-f055-4f5a-a97e-80d97d77da6e", "")), 200)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != docURL {
		t.Fatalf("fetched %v; the apply link must not be requested", urls)
	}
	if len(got) != 1 {
		t.Fatalf("%d", len(got))
	}
	p := got[0]
	if !p.PostedAt.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PostedAt %v", p.PostedAt)
	}
	p.PostedAt = time.Time{}
	want := posting.Posting{
		Key: "fd:shopify:1affb074-f055-4f5a-a97e-80d97d77da6e", ATS: posting.Feed,
		Company: "Shopify", Title: "Account Executive", Location: "NAMER", Remote: true,
		URL:         "https://www.shopify.com/careers/1affb074-f055-4f5a-a97e-80d97d77da6e?utm_source=feed",
		ApplyURL:    "https://www.shopify.com/careers/1affb074-f055-4f5a-a97e-80d97d77da6e?utm_source=feed",
		Description: "Build & ship\nlink", SeenAt: fixedNow,
	}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestHybridIsNotRemoteAndEmptyCompanyUsesTheBoard(t *testing.T) {
	body := doc(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl><workplaceTypes>Hybrid</workplaceTypes><company>   </company></job>`)
	s, _ := newSource(body, 200)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Remote || got[0].Company != "Board Co" || !got[0].PostedAt.IsZero() {
		t.Fatalf("%+v", got[0])
	}
}

func TestEmptyFeedIsValid(t *testing.T) {
	for _, body := range []string{"<source></source>", "<source/>", `<?xml version="1.0"?><source></source>`} {
		s, _ := newSource(body, 200)
		got, err := s.List(context.Background(), board)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("%s: %#v %v", body, got, err)
		}
	}
}

func TestNotFoundAndServerError(t *testing.T) {
	s, _ := newSource("missing", 404)
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrBoardNotFound) {
		t.Fatal(err)
	}
	s, _ = newSource("<source/>", 500)
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestMalformedDocumentsAreErrors(t *testing.T) {
	bad := map[string]string{
		"html":        `<html><body>no</body></html>`,
		"truncated":   `<source><job>`,
		"empty":       ``,
		"not xml":     `{"jobs":[]}`,
		"empty id":    doc(`<job><title>T</title><applyUrl>https://jobs.example/1</applyUrl></job>`),
		"colon id":    doc(`<job><partnerJobId>a:b</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl></job>`),
		"empty title": doc(`<job><partnerJobId>1</partnerJobId><title>  </title><applyUrl>https://jobs.example/1</applyUrl></job>`),
		"empty url":   doc(`<job><partnerJobId>1</partnerJobId><title>T</title></job>`),
		"javascript":  doc(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>javascript:alert(1)</applyUrl></job>`),
		"userinfo":    doc(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://user@evil.test/1</applyUrl></job>`),
		"bad date":    doc(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl><listDate>yesterday</listDate></job>`),
		"duplicate":   doc(job("Same", ""), job("same", "")),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(body, 200)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d", len(got))
			}
		})
	}
}

func TestExternalEntityIsNotFetched(t *testing.T) {
	body := `<!DOCTYPE source [<!ENTITY xxe SYSTEM "https://evil.test/secret">]><source><job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl><description>&xxe;</description></job></source>`
	s, d := newSource(body, 200)
	got, err := s.List(context.Background(), board)
	if urls := d.URLs(); len(urls) != 1 || urls[0] != docURL {
		t.Fatalf("urls %v", urls)
	}
	if err == nil {
		if strings.Contains(got[0].Description, "secret") || strings.Contains(got[0].Description, "evil.test") {
			t.Fatalf("entity expanded into %q", got[0].Description)
		}
	}
}

func TestBoardURLIsTheOnlyRequestAndMustBeHTTPS(t *testing.T) {
	s, _ := newSource("<source/>", 200)
	for _, raw := range []string{"http://jobs.example/feed.xml", "https://user@jobs.example/feed.xml", "javascript:alert(1)", ""} {
		_, err := s.List(context.Background(), sources.Board{ATS: posting.Feed, Token: "acme", URL: raw})
		if err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}

func TestPostingCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<source>")
	for i := 0; i < srcutil.MaxPostings+1; i++ {
		fmt.Fprintf(&b, `<job><partnerJobId>%d</partnerJobId><title>T</title><applyUrl>https://jobs.example/%d</applyUrl></job>`, i, i)
	}
	b.WriteString("</source>")
	s, d := newSource(b.String(), 200)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("%v urls %d", err, len(d.URLs()))
	}
}

func TestBodyCap(t *testing.T) {
	old := maxFeedBody
	maxFeedBody = 8
	t.Cleanup(func() { maxFeedBody = old })
	s, _ := newSource("<source></source>", 200)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestDescriptionCap(t *testing.T) {
	huge := strings.Repeat("a", srcutil.MaxDescription+100)
	body := doc(fmt.Sprintf(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl><description>%s</description></job>`, huge))
	s, _ := newSource(body, 200)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Description) > srcutil.MaxDescription {
		t.Fatalf("%d", len(got[0].Description))
	}
}

func TestCanceledDoesNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := newSource("<source/>", 200)
	_, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v %v", err, d.URLs())
	}
}

func TestNilDoerAndWrongBoard(t *testing.T) {
	if _, err := New(nil).List(context.Background(), board); err == nil {
		t.Fatal("nil doer")
	}
	s, _ := newSource("<source/>", 200)
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Page, Token: "x", URL: docURL}); err == nil {
		t.Fatal("wrong ats")
	}
	if New(nil).ATS() != posting.Feed {
		t.Fatal(New(nil).ATS())
	}
}

func TestCommaSeparatedRemote(t *testing.T) {
	body := doc(`<job><partnerJobId>1</partnerJobId><title>T</title><applyUrl>https://jobs.example/1</applyUrl><workplaceTypes>Onsite, Remote</workplaceTypes></job>`)
	s, _ := newSource(body, 200)
	got, err := s.List(context.Background(), board)
	if err != nil || !got[0].Remote {
		t.Fatalf("%v %+v", err, got)
	}
}
