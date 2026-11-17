package remoteok

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

var board = sources.Board{ATS: posting.RemoteOK, Token: Token, Company: "Remote OK"}

const notice = `{"last_updated":1794916000,"legal":"API Terms of Service: link back"}`

func listBody(ctx context.Context, body string) ([]posting.Posting, *srctest.Doer, error) {
	d := srctest.New(map[string]srctest.Reply{apiURL: {Status: 200, Body: []byte(body)}})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(ctx, board)
	return got, d, err
}

func row(id string) string {
	return fmt.Sprintf(`{"id":"%s","epoch":1794735000,"company":"Acme","position":"Role %s","description":"<p>Body</p>","url":"https://remoteOK.com/remote-jobs/role-%s"}`, id, id, id)
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/api.json")
	if err != nil {
		t.Fatal(err)
	}
	got, d, err := listBody(context.Background(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != apiURL {
		t.Fatalf("requests %v, want only %s", urls, apiURL)
	}
	want := []posting.Posting{
		{
			Key: "ro:remoteok:1137796", ATS: posting.RemoteOK, Company: "Acme Labs", Title: "Senior Backend Engineer",
			Location: "United States", Remote: true,
			URL:      "https://remoteOK.com/remote-jobs/remote-senior-backend-engineer-acme-labs-1137796",
			ApplyURL: "https://remoteOK.com/remote-jobs/remote-senior-backend-engineer-acme-labs-1137796",
			PostedAt: time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC), SeenAt: fixedNow,
		},
		{
			// A numeric id, no epoch (the date is read), no apply_url (the job's
			// Remote OK page is the link back), no location.
			Key: "ro:remoteok:1137801", ATS: posting.RemoteOK, Company: "Globex", Title: "Data Analyst", Remote: true,
			URL:      "https://remoteOK.com/remote-jobs/remote-data-analyst-globex-1137801",
			ApplyURL: "https://remoteOK.com/remote-jobs/remote-data-analyst-globex-1137801",
			PostedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), SeenAt: fixedNow,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d (the notice is not a job): %+v", len(got), len(want), got)
	}
	leads := []string{"Role: remote\n\nBuild APIs in Go.", "Role: remote\n\nDashboards."}
	for i, w := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if g != w {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w)
		}
		if !strings.HasPrefix(desc, leads[i]) {
			t.Errorf("posting %d description %q, want prefix %q", i, desc, leads[i])
		}
	}
}

// The notice is neither a posting nor a bad row: a listing of only the notice
// is an empty board, and a sink hears nothing about it.
func TestNoticeIsNotARow(t *testing.T) {
	ctx := srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, first string) {
		t.Errorf("sink called: %d skipped, %q", n, first)
	})
	for _, body := range []string{"[" + notice + "]", "[]"} {
		got, _, err := listBody(ctx, body)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: got %d postings, %v; want an empty listing", body, len(got), err)
		}
	}
	got, _, err := listBody(ctx, "["+notice+","+row("1")+"]")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
}

// A notice anywhere but first is a malformed row, not a second notice to skip.
func TestLateNoticeIsABadRow(t *testing.T) {
	var skipped int
	ctx := srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, _ string) { skipped += n })
	rows := []string{notice}
	for i := 0; i < 12; i++ {
		rows = append(rows, row(strconv.Itoa(i)))
	}
	rows = append(rows, notice)
	got, _, err := listBody(ctx, "["+strings.Join(rows, ",")+"]")
	if err != nil || len(got) != 12 || skipped != 1 {
		t.Fatalf("got %d postings, %v, %d skipped; want 12 and the late notice skipped", len(got), err, skipped)
	}
}

func TestShapeAndTransportFailures(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"an object, not an array": {Status: 200, Body: []byte(`{"jobs":[]}`)},
		"null":                    {Status: 200, Body: []byte(`null`)},
		"html":                    {Status: 200, Body: []byte(`<html>blocked</html>`)},
		"truncated":               {Status: 200, Body: []byte(`[` + notice + `,{"id":"1"`)},
		"rate limited":            {Status: 403},
		"not found":               {Status: 404},
		"timeout":                 {Err: context.DeadlineExceeded},
		"duplicate ids":           {Status: 200, Body: []byte("[" + row("7") + "," + row("7") + "]")},
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(map[string]srctest.Reply{apiURL: rep})
			s := &source{d: d, now: func() time.Time { return fixedNow }}
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
		})
	}
}

func TestOversizedReplyFails(t *testing.T) {
	big := "[" + notice + `,{"id":"1","company":"A","position":"P","url":"https://remoteok.com/x","description":"` + strings.Repeat("x", int(maxBody)) + `"}]`
	_, _, err := listBody(context.Background(), big)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestBoardChecksSendNothing(t *testing.T) {
	for name, mod := range map[string]func(*sources.Board){
		"other ats":   func(b *sources.Board) { b.ATS = posting.Himalayas },
		"other token": func(b *sources.Board) { b.Token = "remote-ok" },
		"search":      func(b *sources.Board) { b.Search = "golang" },
		"filter":      func(b *sources.Board) { b.Filters = map[string][]string{"tag": {"go"}} },
	} {
		t.Run(name, func(t *testing.T) {
			b := board
			mod(&b)
			d := srctest.New(nil)
			s := &source{d: d, now: func() time.Time { return fixedNow }}
			if _, err := s.List(context.Background(), b); err == nil {
				t.Fatal("no error")
			}
			if n := len(d.URLs()); n != 0 {
				t.Fatalf("%d requests sent for a refused board", n)
			}
		})
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, d, err := listBody(ctx, "[]")
	if !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("err = %v after %d requests, want context.Canceled and none", err, len(d.URLs()))
	}
}

func TestUndoLatin1(t *testing.T) {
	for in, want := range map[string]string{
		"":                         "",
		"plain ascii":              "plain ascii",
		"DoiTâ\u0080\u0099s team":  "DoiT’s team",
		"ZÃ¼rich":                  "Zürich",
		"café":                     "café",                     // genuine Latin-1 range: 0xE9 alone is not UTF-8
		"naïve DoiTâ\u0080\u0099s": "naïve DoiTâ\u0080\u0099s", // mixed in one run: left as served
		"日本 â\u0080\u0099 🚀":       "日本 ’ 🚀",                   // a rune past U+00FF ends a run
		"Ã":                        "Ã",                        // a lone lead byte is not UTF-8
		"ÿþ":                       "ÿþ",
	} {
		if got := undoLatin1(in); got != want {
			t.Errorf("undoLatin1(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDoubleEncodedRowIsRepaired(t *testing.T) {
	body := `[` + notice + `,{"id":"5","company":"CafÃ© Co","position":"Engineer â\u0080\u0094 Platform","location":"ZÃ¼rich","description":"<p>DoiTâ\u0080\u0099s team</p>","url":"https://remoteok.com/x-5"}]`
	got, _, err := listBody(context.Background(), body)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
	p := got[0]
	if p.Company != "Café Co" || p.Title != "Engineer — Platform" || p.Location != "Zürich" || !strings.HasSuffix(p.Description, "DoiT’s team") {
		t.Fatalf("not repaired: %+v", p)
	}
}

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			got, _, err := listBody(ctx, payload)
			return got, err
		},
		Payload: func(n int, bad map[int]string) string {
			rows := []string{notice}
			for i := 0; i < n; i++ {
				r := row(strconv.Itoa(1000 + i))
				if raw, ok := bad[i]; ok {
					r = raw
				}
				rows = append(rows, r)
			}
			return "[" + strings.Join(rows, ",") + "]"
		},
		BadRows: map[string]string{
			"not an object":   `"just a string"`,
			"id not a number": `{"id":"abc","company":"C","position":"P","url":"https://remoteok.com/x"}`,
			"missing id":      `{"company":"C","position":"P","url":"https://remoteok.com/x"}`,
			"empty position":  `{"id":"9` + rowstest.Index + `","company":"C","position":" ","url":"https://remoteok.com/x"}`,
			"empty company":   `{"id":"9` + rowstest.Index + `","company":"","position":"P","url":"https://remoteok.com/x"}`,
			"javascript url":  `{"id":"9` + rowstest.Index + `","company":"C","position":"P","url":"javascript:alert(1)"}`,
			"missing url":     `{"id":"9` + rowstest.Index + `","company":"C","position":"P"}`,
			"bad date":        `{"id":"9` + rowstest.Index + `","company":"C","position":"P","url":"https://remoteok.com/x","date":"yesterday"}`,
		},
	})
}
