package breezy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func board() sources.Board {
	return sources.Board{ATS: posting.Breezy, Token: "thirdchannel-inc", Company: "ThirdChannel"}
}

func reply(body string) *srctest.Doer {
	return srctest.New(map[string]srctest.Reply{
		"https://thirdchannel-inc.breezy.hr/json": {Status: 200, Body: []byte(body)},
	})
}

func TestListReadsTheLiveShape(t *testing.T) {
	body := `[{
		"id":"e9d983bbc5b2",
		"friendly_id":"e9d983bbc5b2-100-eyewear",
		"name":"  Eyewear\nmerchandiser  ",
		"url":"https://thirdchannel-inc.breezy.hr/p/e9d983bbc5b2-100-eyewear",
		"published_date":"2026-10-19T15:06:19.572Z",
		"type":{"id":"contract","name":"Contract"},
		"location":{"name":"Lomita, CA","is_remote":false,"primary":true},
		"locations":[
			{"name":"Lomita, CA","is_remote":false,"primary":true},
			{"name":"Remote","is_remote":true}
		],
		"department":"100%",
		"salary":"$23+ / hour",
		"company":{"name":"ThirdChannel","friendly_id":"thirdchannel-inc"}
	}]`
	got, err := New(reply(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "bz:thirdchannel-inc:e9d983bbc5b2" || p.ATS != posting.Breezy {
		t.Errorf("key %s ats %s", p.Key, p.ATS)
	}
	if p.Company != "ThirdChannel" || p.Title != "Eyewear merchandiser" || p.Location != "Lomita, CA" || p.Remote || p.Department != "100%" {
		t.Errorf("posting %+v", p)
	}
	if p.URL != "https://thirdchannel-inc.breezy.hr/p/e9d983bbc5b2-100-eyewear" {
		t.Errorf("url %s", p.URL)
	}
	wantPosted := time.Date(2026, 10, 19, 15, 6, 19, 572e6, time.UTC)
	if !p.PostedAt.Equal(wantPosted) || p.SeenAt.IsZero() {
		t.Errorf("posted %s seen %s", p.PostedAt, p.SeenAt)
	}
	if !strings.Contains(p.Description, "Compensation: $23+ / hour") || !strings.Contains(p.Description, "Role: contract") || strings.ContainsAny(p.Description, "<>") {
		t.Errorf("description %q", p.Description)
	}
}

func TestListUsesSalaryAsTheOnlyBody(t *testing.T) {
	body := `[{"id":"1000","name":"Role 1000","url":"https://thirdchannel-inc.breezy.hr/p/1000-role","salary":"Body 1000","location":{"name":"Berlin","is_remote":false}}]`
	got, err := New(reply(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if !strings.Contains(got[0].Description, "Body 1000") || strings.ContainsAny(got[0].Description, "<>") {
		t.Errorf("description %q", got[0].Description)
	}
}

func TestRemoteFollowsThePrimaryLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"primary remote":                  {`[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","location":{"name":"Remote","is_remote":true},"locations":[{"name":"Austin","is_remote":false}]}]`, true},
		"primary on site, another remote": {`[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","location":{"name":"Lomita, CA","is_remote":false},"locations":[{"name":"Remote","is_remote":true}]}]`, false},
		"no primary, mixed":               {`[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","locations":[{"name":"Austin","is_remote":false},{"name":"Remote","is_remote":true}]}]`, false},
		"no primary, all remote":          {`[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","locations":[{"name":"Remote","is_remote":true},{"name":"Remote, US","is_remote":true}]}]`, true},
		"disagreeing primaries":           {`[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","locations":[{"name":"Remote","primary":true,"is_remote":true},{"name":"Austin","primary":true,"is_remote":false}]}]`, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := New(reply(tc.body)).List(context.Background(), board())
			if err != nil || len(got) != 1 || got[0].Remote != tc.want {
				t.Fatalf("got %+v, %v; want remote %v", got, err, tc.want)
			}
		})
	}
}

func TestCompanyNameFallsBack(t *testing.T) {
	body := `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","company":{"name":"ThirdChannel","friendly_id":"ThirdChannel-Inc"}}]`
	b := sources.Board{ATS: posting.Breezy, Token: "thirdchannel-inc"}
	got, err := New(reply(body)).List(context.Background(), b)
	if err != nil || len(got) != 1 || got[0].Company != "ThirdChannel" {
		t.Fatalf("got %+v, %v", got, err)
	}
	bare := `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1"}]`
	got, err = New(reply(bare)).List(context.Background(), b)
	if err != nil || got[0].Company != "thirdchannel-inc" {
		t.Fatalf("fallback got %+v, %v", got, err)
	}
}

func TestListFailures(t *testing.T) {
	for name, body := range map[string]string{
		"object":          `{}`,
		"html":            `<html>not json</html>`,
		"empty title":     `[{"id":"1","name":"  ","url":"https://thirdchannel-inc.breezy.hr/p/1"}]`,
		"html title only": `[{"id":"1","name":"<b></b>","url":"https://thirdchannel-inc.breezy.hr/p/1"}]`,
		"bad date":        `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","published_date":"yesterday"}]`,
		"off host":        `[{"id":"1","name":"Role","url":"https://evil.test/p/1"}]`,
		"http":            `[{"id":"1","name":"Role","url":"http://thirdchannel-inc.breezy.hr/p/1"}]`,
		"userinfo":        `[{"id":"1","name":"Role","url":"https://user:pass@thirdchannel-inc.breezy.hr/p/1"}]`,
		"port":            `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr:443/p/1"}]`,
		"wrong company":   `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","company":{"friendly_id":"other-co","name":"Other"}}]`,
		"colon in id":     `[{"id":"a:b","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1"}]`,
		"second mismatch": `[{"id":"1","name":"Role","url":"https://thirdchannel-inc.breezy.hr/p/1","company":{"friendly_id":"thirdchannel-inc"}},{"id":"2","name":"Other","url":"https://thirdchannel-inc.breezy.hr/p/2","company":{"friendly_id":"nope"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := New(reply(body)).List(context.Background(), board())
			if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestEmptyDateAndStrippedTitle(t *testing.T) {
	body := `[{"id":"1","name":"<b>Role</b>  extra","url":"https://thirdchannel-inc.breezy.hr/p/1","published_date":"","salary":"` + strings.Repeat("x", 2000) + `"}]`
	got, err := New(reply(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].Title != "Role extra" || !got[0].PostedAt.IsZero() {
		t.Errorf("title %q posted %s", got[0].Title, got[0].PostedAt)
	}
	if strings.Contains(got[0].Description, strings.Repeat("x", 2000)) || len(got[0].Description) > srcutil.MaxDescription {
		t.Errorf("description length %d kept the salary", len(got[0].Description))
	}
}

func TestCancelledContextMakesNoRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := reply(`[]`)
	got, err := New(d).List(ctx, board())
	if !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 0 {
		t.Fatalf("got %+v, %v, urls %v", got, err, d.URLs())
	}
}

func TestDependencyDownIsAnError(t *testing.T) {
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{}, false
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(d.URLs()) != 1 {
		t.Errorf("urls %v", d.URLs())
	}
}
