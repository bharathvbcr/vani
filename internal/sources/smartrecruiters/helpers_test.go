package smartrecruiters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

const (
	stamp    = "2026-10-01T10:00:00.000Z"
	company  = "ServiceNow"
	idBase   = 744000000000
	listPath = "/v1/companies/" + company + "/postings"
)

// fixedNow is "now" for every source under test.
var fixedNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func read(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func okReply(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func notFound() srctest.Reply { return srctest.Reply{Status: 404, Body: []byte(`{"httpCode":404}`)} }

// idOf is the id of the i-th posting of a fake board.
func idOf(i int) string { return strconv.Itoa(idBase + i) }

// api is a fake SmartRecruiters company. ids(country) is what the listing holds
// for a query; the listing serves it in pages the way the real one does, and
// every detail is answered unless bad holds a reply for its id.
type api struct {
	ids func(country string) []string
	// list, when set, answers a listing request itself; ok=false falls through
	// to the honest listing.
	list func(country string, offset int) (srctest.Reply, bool)
	// bad replaces the detail of an id.
	bad map[string]srctest.Reply
	// detail, when set, edits an id's detail object before it is served.
	detail func(id string, d map[string]any)

	mu      sync.Mutex
	queries []url.Values // every listing request's query, in order
	fetched []string     // every detail id asked for, in order
}

// board is an api whose every query lists the first n postings.
func board(n int) *api {
	return &api{ids: func(string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = idOf(i)
		}
		return out
	}}
}

func rowJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"name":"Role %s","releasedDate":%q,"company":{"identifier":%q,"name":%q},`+
		`"location":{"city":"Austin","region":"Texas","country":"us","remote":false,"hybrid":false,"fullLocation":"Austin, Texas, United States"},`+
		`"department":{"id":"1","label":"Engineering"},"typeOfEmployment":{"id":"permanent","label":"Full-time"}}`,
		id, id, stamp, company, company)
}

func page(total int, ids []string) string {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = rowJSON(id)
	}
	return fmt.Sprintf(`{"offset":0,"limit":100,"totalFound":%d,"content":[%s]}`, total, strings.Join(rows, ","))
}

func detailObject(id string) map[string]any {
	return map[string]any{
		"id": id, "name": "Role " + id, "releasedDate": stamp,
		"postingUrl": "https://jobs.smartrecruiters.com/" + company + "/" + id + "-role-" + id,
		"applyUrl":   "https://jobs.smartrecruiters.com/" + company + "/" + id + "-role-" + id + "?oga=true",
		"company":    map[string]any{"identifier": company, "name": company},
		"location": map[string]any{"city": "Austin", "region": "Texas", "country": "us", "remote": false, "hybrid": false,
			"fullLocation": "Austin, Texas, United States"},
		"department":       map[string]any{"label": "Engineering"},
		"typeOfEmployment": map[string]any{"label": "Full-time"},
		"jobAd": map[string]any{"sections": map[string]any{
			"jobDescription": map[string]any{"title": "Job Description", "text": "<p>About role " + id + "</p>"},
		}},
	}
}

func (a *api) Do(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != "api.smartrecruiters.com" {
		return srctest.Reply{}, false
	}
	if u.Path == listPath {
		q := u.Query()
		a.mu.Lock()
		a.queries = append(a.queries, q)
		a.mu.Unlock()
		offset, _ := strconv.Atoi(q.Get("offset"))
		cc := q.Get("country")
		if a.list != nil {
			if rep, ok := a.list(cc, offset); ok {
				return rep, true
			}
		}
		ids := a.ids(cc)
		lo := min(offset, len(ids))
		hi := min(offset+100, len(ids))
		return okReply(page(len(ids), ids[lo:hi])), true
	}
	if id, ok := strings.CutPrefix(u.Path, listPath+"/"); ok {
		a.mu.Lock()
		a.fetched = append(a.fetched, id)
		a.mu.Unlock()
		if rep, hit := a.bad[id]; hit {
			return rep, true
		}
		d := detailObject(id)
		if a.detail != nil {
			a.detail(id, d)
		}
		raw, _ := json.Marshal(d)
		return srctest.Reply{Status: 200, Body: raw}, true
	}
	return srctest.Reply{}, false
}

func (a *api) doer() *srctest.Doer { return srctest.NewFunc(a.Do) }

func (a *api) fetchedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.fetched...)
}

func (a *api) listQueries() []url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]url.Values(nil), a.queries...)
}

// src is a source over d with no waiting and a fixed clock. opt nil is New's
// (every row fetched); otherwise NewWith's.
func src(t *testing.T, d *srctest.Doer, opt *Options) *source {
	t.Helper()
	s := &source{d: d, now: func() time.Time { return fixedNow }, wait: noWait}
	if opt != nil {
		o := *opt
		if o.Stored == nil {
			o.Stored = func(sources.Board) []posting.Posting { return nil }
		}
		if o.Wait != nil {
			s.wait = o.Wait
		}
		s.opt = &o
	}
	return s
}

func srBoard() sources.Board {
	return sources.Board{ATS: posting.SmartRecruiters, Token: company, Company: "ServiceNow"}
}
