package goneparity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/bamboohr"
	"github.com/bharathvbcr/Vani/internal/sources/personio"
	"github.com/bharathvbcr/Vani/internal/sources/pinpoint"
	"github.com/bharathvbcr/Vani/internal/sources/recruitee"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// tenant is one per-tenant ATS (Recruitee, Pinpoint, BambooHR, Personio), whose
// boards each live on {token}.{domain}: the contract they share, put side by
// side. listURL is the one request that lists the board; list builds its reply
// for the given ids, and detail (BambooHR only) the reply for one id's detail.
type tenant struct {
	name    string
	ats     posting.ATS
	listURL string
	source  func(egress.Doer) sources.Source
	list    func(ids []string) string
	detail  func(id string) (path, body string)
	// redirectIsGone: the list request answering 3xx is a board that is not
	// served (BambooHR), where for the others it is a failure.
	redirectIsGone bool
}

const token = "acme"

func tenants() []tenant {
	noSleep := func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return []tenant{
		{
			name: "recruitee", ats: posting.Recruitee, listURL: "https://acme.recruitee.com/api/offers/",
			source: recruitee.New,
			list: func(ids []string) string {
				var rows []string
				for _, id := range ids {
					rows = append(rows, fmt.Sprintf(`{"id":%s,"title":"Role %s","careers_url":"https://acme.recruitee.com/o/%s","description":"<p>Body %s</p>","location":"Berlin"}`, id, id, id, id))
				}
				return `{"offers":[` + strings.Join(rows, ",") + `]}`
			},
		},
		{
			name: "pinpoint", ats: posting.Pinpoint, listURL: "https://acme.pinpointhq.com/postings.json",
			source: pinpoint.New,
			list: func(ids []string) string {
				var rows []string
				for _, id := range ids {
					rows = append(rows, fmt.Sprintf(`{"id":"%s","title":"Role %s","url":"https://acme.pinpointhq.com/en/postings/%s","description":"<p>Body %s</p>","location":{"city":"Berlin"}}`, id, id, id, id))
				}
				return `{"data":[` + strings.Join(rows, ",") + `]}`
			},
		},
		{
			name: "personio", ats: posting.Personio, listURL: "https://acme.jobs.personio.com/xml?language=en",
			source: personio.New,
			list: func(ids []string) string {
				var rows []string
				for _, id := range ids {
					rows = append(rows, fmt.Sprintf(`<position><id>%s</id><office>Berlin</office><name>Role %s</name><jobDescriptions><jobDescription><name>Task</name><value><![CDATA[<p>Body %s</p>]]></value></jobDescription></jobDescriptions></position>`, id, id, id))
				}
				return `<?xml version="1.0" encoding="UTF-8"?><workzag-jobs>` + strings.Join(rows, "") + `</workzag-jobs>`
			},
		},
		{
			name: "bamboohr", ats: posting.BambooHR, listURL: "https://acme.bamboohr.com/careers/list",
			source: func(d egress.Doer) sources.Source { return bamboohr.NewWith(d, bamboohr.Options{Wait: noSleep}) },
			list: func(ids []string) string {
				var rows []string
				for _, id := range ids {
					rows = append(rows, fmt.Sprintf(`{"id":"%s","jobOpeningName":"Role %s","locationType":"0"}`, id, id))
				}
				return fmt.Sprintf(`{"meta":{"totalCount":%d},"result":[%s]}`, len(ids), strings.Join(rows, ","))
			},
			detail: func(id string) (string, string) {
				return "/careers/" + id + "/detail", fmt.Sprintf(`{"result":{"jobOpening":{"jobOpeningShareUrl":"https://acme.bamboohr.com/careers/%s","jobOpeningName":"Role %s","jobOpeningStatus":"Open","description":"<p>Body %s</p>"}}}`, id, id, id)
			},
			redirectIsGone: true,
		},
	}
}

func (tn tenant) board() sources.Board {
	return sources.Board{ATS: tn.ats, Token: token, Company: "Acme"}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint(1000 + i)
	}
	return out
}

// serve answers the list with listReply and, for BambooHR, each detail of ids.
func (tn tenant) serve(listReply srctest.Reply, ids []string) *srctest.Doer {
	details := map[string]string{}
	if tn.detail != nil {
		for _, id := range ids {
			p, body := tn.detail(id)
			details[p] = body
		}
	}
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == tn.listURL {
			return listReply, true
		}
		u, err := url.Parse(r.URL)
		if err != nil {
			return srctest.Reply{}, false
		}
		if body, found := details[u.EscapedPath()]; found && u.Host == "acme.bamboohr.com" {
			return ok(body), true
		}
		return srctest.Reply{}, false
	})
}

// blocked never answers: it returns when the request's context is done.
type blocked struct{}

func (blocked) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A board of n postings is read whole, every posting keyed under the board's
// prefix with an absolute https URL and plain-text description, and every
// request goes to a host config.BoardHosts says the board needs allowed.
func TestTenantBoardsAreReadWhole(t *testing.T) {
	for _, tn := range tenants() {
		for _, n := range []int{0, 1, 2, 25} {
			t.Run(fmt.Sprintf("%s/n=%d", tn.name, n), func(t *testing.T) {
				ids := ids(n)
				d := tn.serve(ok(tn.list(ids)), ids)
				b := tn.board()
				got, err := tn.source(d).List(context.Background(), b)
				if err != nil || got == nil || len(got) != n {
					t.Fatalf("got %#v, %v; want %d postings", got, err, n)
				}
				for i, p := range got {
					if p.ATS != tn.ats || !strings.HasPrefix(string(p.Key), b.KeyPrefix()) || p.Key != posting.Key(b.KeyPrefix()+ids[i]) {
						t.Errorf("posting %d: ats %q key %q, want %s%s", i, p.ATS, p.Key, b.KeyPrefix(), ids[i])
					}
					if p.Company != "Acme" || p.Title != "Role "+ids[i] || !strings.HasPrefix(p.URL, "https://acme.") {
						t.Errorf("posting %d: %+v", i, p)
					}
					if !strings.Contains(p.Description, "Body "+ids[i]) || strings.ContainsAny(p.Description, "<>") {
						t.Errorf("posting %d description %q", i, p.Description)
					}
					if p.SeenAt.IsZero() {
						t.Errorf("posting %d has no SeenAt", i)
					}
				}
				allowed := config.BoardHosts(b)
				if len(allowed) != 1 {
					t.Fatalf("BoardHosts = %v", allowed)
				}
				for _, u := range d.URLs() {
					pu, _ := url.Parse(u)
					if pu.Hostname() != allowed[0] {
						t.Errorf("requested %s, but the board allows %v", u, allowed)
					}
				}
				if d.URLs()[0] != tn.listURL {
					t.Errorf("first request %s, want the list %s", d.URLs()[0], tn.listURL)
				}
			})
		}
	}
}

// The list's failure modes, the same on every adapter: only a 404 is a board
// that is gone, and nothing else is read as one.
func TestTenantListFailures(t *testing.T) {
	for _, tn := range tenants() {
		for name, tc := range map[string]struct {
			reply srctest.Reply
			gone  bool
		}{
			"404":       {srctest.Reply{Status: 404, Body: []byte("{}")}, true},
			"410":       {srctest.Reply{Status: 410, Body: []byte("{}")}, false},
			"429":       {srctest.Reply{Status: 429, Header: http.Header{"Retry-After": {"0"}}}, false},
			"500":       {srctest.Reply{Status: 500, Body: []byte("{}")}, false},
			"502":       {srctest.Reply{Status: 502, Body: []byte("{}")}, false},
			"503":       {srctest.Reply{Status: 503, Body: []byte("{}"), Header: http.Header{"Retry-After": {"0"}}}, false},
			"redirect":  {srctest.Reply{Status: 302, Header: http.Header{"Location": {"https://www.example.com/"}}}, tn.redirectIsGone},
			"empty":     {ok(""), false},
			"malformed": {ok("{not json <xml"), false},
			"htmlpage":  {ok("<html><body>404 Page Not Found</body></html>"), false},
			"null":      {ok("null"), false},
		} {
			t.Run(tn.name+"/"+name, func(t *testing.T) {
				d := tn.serve(tc.reply, nil)
				got, err := tn.source(d).List(context.Background(), tn.board())
				if err == nil || got != nil {
					t.Fatalf("got %v, %v; want an error and no postings", got, err)
				}
				if errors.Is(err, srcutil.ErrBoardNotFound) != tc.gone {
					t.Errorf("not-found = %v, want %v: %v", errors.Is(err, srcutil.ErrBoardNotFound), tc.gone, err)
				}
			})
		}
	}
}

func TestTenantOversizedListIsAnError(t *testing.T) {
	body := make([]byte, srcutil.MaxBody+1)
	for i := range body {
		body[i] = ' '
	}
	for _, tn := range tenants() {
		t.Run(tn.name, func(t *testing.T) {
			d := tn.serve(srctest.Reply{Status: 200, Body: body}, nil)
			got, err := tn.source(d).List(context.Background(), tn.board())
			if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
				t.Fatalf("got %v, %v, want ErrBodyTooLarge", got, err)
			}
		})
	}
}

// A board over the posting cap fails whole, before any posting is read, and a
// repeated id fails it too: neither is cut down to something that fits.
func TestTenantCapAndDuplicateIDs(t *testing.T) {
	for _, tn := range tenants() {
		t.Run(tn.name+"/over the cap", func(t *testing.T) {
			ids := ids(srcutil.MaxPostings + 1)
			d := tn.serve(ok(tn.list(ids)), nil)
			got, err := tn.source(d).List(context.Background(), tn.board())
			if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
				t.Fatalf("got %d postings, %v, want ErrTooManyPostings", len(got), err)
			}
			if len(d.URLs()) != 1 {
				t.Errorf("%d requests for a board refused whole: %d", len(d.URLs()), len(d.URLs()))
			}
		})
		t.Run(tn.name+"/duplicate id", func(t *testing.T) {
			ids := []string{"1001", "1002", "1001"}
			d := tn.serve(ok(tn.list(ids)), ids)
			got, err := tn.source(d).List(context.Background(), tn.board())
			if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("got %v, %v, want a duplicate-id error", got, err)
			}
		})
	}
}

func TestTenantTimeoutIsAnError(t *testing.T) {
	for _, tn := range tenants() {
		t.Run(tn.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			got, err := tn.source(blocked{}).List(ctx, tn.board())
			if !errors.Is(err, context.DeadlineExceeded) || got != nil {
				t.Fatalf("got %v, %v, want a deadline error", got, err)
			}
			if _, err := tn.source(blocked{}).List(ctx, tn.board()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("an expired context still listed: %v", err)
			}
		})
	}
}

// The token is written into a host, so only one lower-case host label gets a
// request, on every adapter, and a board of another ATS is refused.
func TestTenantTokensAreHostLabels(t *testing.T) {
	for _, tn := range tenants() {
		t.Run(tn.name, func(t *testing.T) {
			d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false })
			s := tn.source(d)
			for _, tok := range []string{"", " ", "..", "a.b", "evil.example", "a/b", "a:b", "a@b", "A", "-a", "a-", "a_b", "a b", "a%2eb", strings.Repeat("a", 64), "é"} {
				if _, err := s.List(context.Background(), sources.Board{ATS: tn.ats, Token: tok}); err == nil {
					t.Errorf("token %q accepted", tok)
				}
			}
			other := posting.Workable
			if tn.ats == other {
				other = posting.Gem
			}
			if _, err := s.List(context.Background(), sources.Board{ATS: other, Token: token}); err == nil {
				t.Error("a board of another ATS was accepted")
			}
			if _, err := s.List(context.Background(), sources.Board{ATS: tn.ats, Token: token, BoardName: "Acme"}); err == nil {
				t.Error("board_name was accepted outside Ashby")
			}
			if n := len(d.URLs()); n != 0 {
				t.Errorf("refused boards still made %d requests: %v", n, d.URLs())
			}
		})
	}
}

// Every token config accepts for these boards is one every adapter accepts and
// the other way round, so a watchlist entry is never valid and unreadable.
func TestTenantConfigAndAdaptersAgreeOnTokens(t *testing.T) {
	for _, tn := range tenants() {
		for _, tok := range []string{"acme", "a", "a-b", "9lives", "acme-2", strings.Repeat("a", 63), "", "a.b", "A", "-a", "a-", "a_b", strings.Repeat("a", 64), "a b", "a/b", "a:b"} {
			b := sources.Board{ATS: tn.ats, Token: tok, Company: "X"}
			cfgOK := config.ValidBoard(b) == nil
			d := tn.serve(srctest.Reply{Status: 404, Body: []byte("{}")}, nil)
			_, err := tn.source(d).List(context.Background(), b)
			adapterOK := len(d.URLs()) > 0 // it got as far as asking
			if cfgOK != adapterOK {
				t.Errorf("%s token %q: config accepts %v, adapter asks %v (%v)", tn.name, tok, cfgOK, adapterOK, err)
			}
		}
	}
}
