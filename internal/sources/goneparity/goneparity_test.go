// Package goneparity holds one table that puts Workday, Eightfold, Apple,
// SmartRecruiters, Avature and Oracle HCM side by side on the same question:
// what does a detail that answers 404 (or
// fails otherwise) do to a board of n listed postings. tenantparity_test.go
// does the same for the per-tenant boards (Recruitee, Pinpoint, BambooHR and
// Personio), on the questions that apply to a board read in one request.
package goneparity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/apple"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/eightfold"
	"github.com/bharathvbcr/Vani/internal/sources/oraclehcm"
	"github.com/bharathvbcr/Vani/internal/sources/smartrecruiters"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
	"github.com/bharathvbcr/Vani/internal/sources/talentbrew"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// fake is one adapter's board of n postings. bad[i] replaces posting i's
// detail reply; searchReply, when set, replaces every listing reply.
type fake struct {
	name string
	// build returns the source, its board, and a pointer to the reports seen
	// (listed, gone) in call order.
	build func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports)
}

type reports struct {
	mu    sync.Mutex
	calls [][2]int // listed, gone
}

func (r *reports) add(listed, gone int) {
	r.mu.Lock()
	r.calls = append(r.calls, [2]int{listed, gone})
	r.mu.Unlock()
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func read(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---- workday ----

const wdHost = "nvidia.wd5.myworkdayjobs.com"

func workdayFake() fake {
	return fake{"workday", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		b := sources.Board{ATS: posting.Workday, Token: "nvidia/Site", Company: "NVIDIA", Host: wdHost}
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			if r.Method == "POST" {
				var req struct{ Limit, Offset int }
				if json.Unmarshal(r.Body, &req) != nil {
					return srctest.Reply{}, false
				}
				var rows []string
				for i := req.Offset; i < n && i < req.Offset+req.Limit; i++ {
					rows = append(rows, fmt.Sprintf(`{"title":"T%d","externalPath":"/job/X/T%d_JR%d","locationsText":"L","postedOn":"Posted 3 Days Ago"}`, i, i, i))
				}
				return ok(fmt.Sprintf(`{"total":%d,"jobPostings":[%s]}`, n, strings.Join(rows, ","))), true
			}
			i := 0
			id := r.URL[strings.LastIndex(r.URL, "_JR")+3:]
			fmt.Sscan(id, &i)
			if rep, hit := bad[i]; hit {
				return rep, true
			}
			return ok(fmt.Sprintf(`{"jobPostingInfo":{"jobReqId":"JR%s","title":"T%s","location":"Loc","jobDescription":"<p>d</p>"}}`, id, id)), true
		})
		s, err := workday.NewWith(d, workday.Options{
			Stored: func(sources.Board) []posting.Posting { return nil },
			Report: func(_ sources.Board, st workday.Stats) { rep.add(st.Listed, st.Gone) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, b, rep
	}}
}

// ---- eightfold ----

func eightfoldFake() fake {
	return fake{"eightfold", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		var detail map[string]any
		dec := json.NewDecoder(strings.NewReader(string(read(t, "../eightfold/testdata/detail.json"))))
		dec.UseNumber()
		var whole map[string]any
		if err := dec.Decode(&whole); err != nil {
			t.Fatal(err)
		}
		detail = whole["data"].(map[string]any)
		const host = "apply.careers.microsoft.com"
		idOf := func(i int) string { return fmt.Sprint(7000000000 + i) }
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			q := u.Query()
			var data any
			switch u.Path {
			case "/api/pcsx/search":
				start := 0
				fmt.Sscan(q.Get("start"), &start)
				var page []map[string]any
				for i := start; i < n && i < start+10; i++ {
					page = append(page, map[string]any{"id": json.Number(idOf(i)), "name": "R"})
				}
				data = map[string]any{"count": n, "positions": page}
			case "/api/pcsx/position_details":
				id := q.Get("position_id")
				var i int
				fmt.Sscan(id, &i)
				i -= 7000000000
				if rp, hit := bad[i]; hit {
					return rp, true
				}
				dd := map[string]any{}
				for k, v := range detail {
					dd[k] = v
				}
				dd["id"], dd["publicUrl"] = json.Number(id), "https://"+host+"/careers/job/"+id
				data = dd
			default:
				return srctest.Reply{}, false
			}
			raw, _ := json.Marshal(map[string]any{"status": 200, "error": map[string]any{}, "data": data})
			return srctest.Reply{Status: 200, Body: raw}, true
		})
		s := eightfold.NewWith(d, eightfold.Options{Report: func(_ sources.Board, st eightfold.Stats) { rep.add(st.Listed, st.Gone) }, Wait: noWait})
		return s, sources.Board{ATS: posting.Eightfold, Token: "microsoft.com", Company: "Microsoft", Host: host, Searches: []string{"t"}}, rep
	}}
}

// ---- apple ----

func appleFake() fake {
	return fake{"apple", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		var jd map[string]any
		if err := json.Unmarshal(read(t, "../apple/testdata/jobsdata.json"), &jd); err != nil {
			t.Fatal(err)
		}
		page := func(jd map[string]any) string {
			doc, _ := json.Marshal(map[string]any{"loaderData": map[string]any{"root": map[string]any{"locale": "en-us"}, "jobDetails": map[string]any{"jobsData": jd}}})
			lit, _ := json.Marshal(string(doc))
			return `<html><head><script>window.__staticRouterHydrationData = JSON.parse(` + string(lit) + `);</script></head><body>x</body></html>`
		}
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			if u.Path == "/en-us/search" {
				var b strings.Builder
				fmt.Fprintf(&b, `<html><body><p id="search-result-count">%d Result(s)</p><ul>`, n)
				p := 1
				fmt.Sscan(u.Query().Get("page"), &p)
				for i := (p - 1) * 20; i < n && i < p*20; i++ {
					fmt.Fprintf(&b, `<li><a href="/en-us/details/%d-0001/role-%d">T</a></li>`, 400000000+i, i)
				}
				return ok(b.String() + `</ul></body></html>`), true
			}
			// /en-us/details/{id}-0001/role-{i}
			parts := strings.Split(u.Path, "/")
			i := 0
			fmt.Sscan(strings.TrimPrefix(parts[len(parts)-1], "role-"), &i)
			if rp, hit := bad[i]; hit {
				return rp, true
			}
			id := fmt.Sprint(400000000 + i)
			c := map[string]any{}
			for k, v := range jd {
				c[k] = v
			}
			c["positionId"], c["jobNumber"], c["postingTitle"] = id, id+"-0001", "Role "+id
			return ok(page(c)), true
		})
		s := apple.NewWith(d, apple.Options{Report: func(_ sources.Board, st apple.Stats) { rep.add(st.Listed, st.Gone) }})
		return s, sources.Board{ATS: posting.Apple, Token: "apple", Company: "Apple", Searches: []string{"t"}}, rep
	}}
}

// ---- smartrecruiters ----

func smartrecruitersFake() fake {
	return fake{"smartrecruiters", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		const base, listPath = 744000000000, "/v1/companies/ServiceNow/postings"
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			switch {
			case u.Path == listPath:
				offset := 0
				fmt.Sscan(u.Query().Get("offset"), &offset)
				var rows []string
				for i := offset; i < n && i < offset+100; i++ {
					rows = append(rows, fmt.Sprintf(`{"id":"%d","name":"R%d","releasedDate":"2026-10-01T10:00:00.000Z"}`, base+i, i))
				}
				return ok(fmt.Sprintf(`{"totalFound":%d,"content":[%s]}`, n, strings.Join(rows, ","))), true
			case strings.HasPrefix(u.Path, listPath+"/"):
				id := strings.TrimPrefix(u.Path, listPath+"/")
				i := 0
				fmt.Sscan(id, &i)
				if rp, hit := bad[i-base]; hit {
					return rp, true
				}
				return ok(fmt.Sprintf(`{"id":"%s","name":"R%s","releasedDate":"2026-10-01T10:00:00.000Z",`+
					`"postingUrl":"https://jobs.smartrecruiters.com/ServiceNow/%s-r","company":{"identifier":"ServiceNow","name":"ServiceNow"},`+
					`"jobAd":{"sections":{"jobDescription":{"title":"Job Description","text":"<p>d</p>"}}}}`, id, id, id)), true
			}
			return srctest.Reply{}, false
		})
		s, err := smartrecruiters.NewWith(d, smartrecruiters.Options{
			Stored: func(sources.Board) []posting.Posting { return nil },
			Report: func(_ sources.Board, st smartrecruiters.Stats) { rep.add(st.Listed, st.Gone) },
			Wait:   noWait,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, sources.Board{ATS: posting.SmartRecruiters, Token: "ServiceNow", Company: "ServiceNow"}, rep
	}}
}

// ---- oracle hcm ----

func oracleFake() fake {
	return fake{"oraclehcm", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		const host = "jpmc.fa.oraclecloud.com"
		idOf := func(i int) string { return fmt.Sprint(210000000 + i) }
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			finder := u.Query().Get("finder")
			switch {
			case strings.HasSuffix(u.Path, "/recruitingCEJobRequisitions"):
				var offset int
				fmt.Sscan(finder[strings.Index(finder, "offset=")+len("offset="):], &offset)
				var rows []string
				for i := offset; i < n && i < offset+200; i++ {
					rows = append(rows, fmt.Sprintf(`{"Id":"%s","Title":"T%d","PrimaryLocation":"L"}`, idOf(i), i))
				}
				return ok(fmt.Sprintf(`{"items":[{"TotalJobsCount":%d,"requisitionList":[%s]}]}`, n, strings.Join(rows, ","))), true
			case strings.HasSuffix(u.Path, "/recruitingCEJobRequisitionDetails"):
				id := strings.Split(finder, `"`)[1]
				var i int
				fmt.Sscan(id, &i)
				i -= 210000000
				if rp, hit := bad[i]; hit {
					return rp, true
				}
				return ok(fmt.Sprintf(`{"items":[{"Id":"%s","Title":"T%d","ExternalDescriptionStr":"<p>d</p>"}]}`, id, i)), true
			}
			return srctest.Reply{}, false
		})
		s, err := oraclehcm.NewWith(d, oraclehcm.Options{
			Stored: func(sources.Board) []posting.Posting { return nil },
			Report: func(_ sources.Board, st oraclehcm.Stats) { rep.add(st.Listed, st.Gone) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, sources.Board{ATS: posting.OracleHCM, Token: "jpmc/CX_1001", Company: "JPMorgan Chase", Host: host}, rep
	}}
}

func all() []fake {
	return []fake{workdayFake(), eightfoldFake(), appleFake(), smartrecruitersFake(), avatureFake(), oracleFake(), talentbrewFake()}
}

// ---- talentbrew ----

func talentbrewFake() fake {
	return fake{"talentbrew", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		const host, base = "jobs.intuit.com", 102000000000
		loc := func(i int) string {
			return fmt.Sprintf("https://%s/job/mountain-view/role-%d/27595/%d", host, i, base+i)
		}
		var site strings.Builder
		site.WriteString(`<?xml version="1.0" encoding="utf-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://` + host + `</loc></url>`)
		for i := 0; i < n; i++ {
			fmt.Fprintf(&site, "<url><loc>%s</loc></url>", loc(i))
		}
		site.WriteString("</urlset>")
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			switch u.Path {
			case "/robots.txt":
				return ok("User-agent: *\nDisallow: /search-jobs/\n"), true
			case "/sitemap.xml":
				return ok(site.String()), true
			}
			i := 0
			fmt.Sscan(u.Path[strings.LastIndex(u.Path, "/")+1:], &i)
			i -= base
			if i < 0 || i >= n {
				return srctest.Reply{}, false
			}
			if rp, hit := bad[i]; hit {
				return rp, true
			}
			return ok(fmt.Sprintf(`<html><head><script type="application/ld+json">{"@type":"JobPosting","title":"R%d","datePosted":"2026-10-27",`+
				`"description":"<p>d</p>","url":%q}</script></head></html>`, i, loc(i))), true
		})
		s, err := talentbrew.NewWith(d, talentbrew.Options{
			Stored: func(sources.Board) []posting.Posting { return nil },
			Report: func(_ sources.Board, st talentbrew.Stats) { rep.add(st.Listed, st.Gone) },
			Wait:   noWait,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, sources.Board{ATS: posting.TalentBrew, Token: "intuit", Company: "Intuit", Host: host}, rep
	}}
}

// ---- avature ----

func avatureFake() fake {
	return fake{"avature", func(t *testing.T, n int, bad map[int]srctest.Reply) (sources.Source, sources.Board, *reports) {
		rep := &reports{}
		const host = "careers.twosigma.com"
		detail := string(read(t, "../avature/testdata/detail.html"))
		empty := read(t, "../avature/testdata/list-empty.html")
		idOf := func(i int) string { return fmt.Sprint(14000 + i) }
		d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			switch {
			case u.Path == "/robots.txt":
				return ok("User-agent: *\nAllow: /\n"), true
			case u.Path == "/careers/OpenRoles/":
				off := 0
				fmt.Sscan(u.Query().Get("jobOffset"), &off)
				if off >= n {
					return srctest.Reply{Status: 200, Body: empty}, true
				}
				var b strings.Builder
				b.WriteString(`<div class="results__panel">`)
				for i := off; i < n && i < off+10; i++ {
					link := "https://" + host + "/careers/JobDetail/Role-" + idOf(i) + "/" + idOf(i)
					fmt.Fprintf(&b, `<article class="article article--result"><h3><a class="link" href="%s">Role %d</a></h3><span class="paragraph_inner-span">United States - NY New York</span></article>`, link, i)
				}
				return ok(b.String() + `</div>`), true
			}
			parts := strings.Split(u.Path, "/")
			i := 0
			fmt.Sscan(parts[len(parts)-1], &i)
			i -= 14000
			if rp, hit := bad[i]; hit {
				return rp, true
			}
			return ok(strings.Replace(detail, "/13045\"", "/"+idOf(i)+"\"", 1)), true
		})
		s := avature.NewWith(d, avature.Options{Report: func(_ sources.Board, st avature.Stats) { rep.add(st.Listed, st.Gone) }, Wait: noWait})
		return s, sources.Board{ATS: posting.Avature, Token: "twosigma", Company: "Two Sigma", Host: host, URL: "https://" + host + "/careers/OpenRoles"}, rep
	}}
}

func notFound() srctest.Reply { return srctest.Reply{Status: http.StatusNotFound, Body: []byte("{}")} }

func goneSet(k int) map[int]srctest.Reply {
	m := map[int]srctest.Reply{}
	for i := 0; i < k; i++ {
		m[i] = notFound()
	}
	return m
}

// wantFail is the contract every adapter here shares: more than max(1, n/10)
// gone, or every posting gone, fails the board.
func wantFail(n, k int) bool { return k > max(1, n/10) || k == n }

// The bound table, run on every adapter: n at every integer-division
// edge, gone at the bound, one over, and all.
func TestGoneBoundParity(t *testing.T) {
	for _, f := range all() {
		for _, n := range []int{1, 2, 9, 10, 11, 19, 20, 21, 100} {
			bound := max(1, n/10)
			seenK := map[int]bool{}
			for _, k := range []int{1, bound, bound + 1, n} {
				if k > n || k < 1 || seenK[k] {
					continue
				}
				seenK[k] = true
				t.Run(fmt.Sprintf("%s/n=%d/gone=%d", f.name, n, k), func(t *testing.T) {
					s, b, rep := f.build(t, n, goneSet(k))
					ps, err := s.List(context.Background(), b)
					if wantFail(n, k) {
						if err == nil {
							t.Fatalf("got %d postings, want failure (n=%d gone=%d)", len(ps), n, k)
						}
						if errors.Is(err, srcutil.ErrBoardNotFound) {
							t.Fatalf("detail 404s read as board-not-found: %v", err)
						}
					} else if err != nil || len(ps) != n-k {
						t.Fatalf("got %d postings, err %v; want %d, nil", len(ps), err, n-k)
					}
					rep.mu.Lock()
					defer rep.mu.Unlock()
					if f.name == "workday" && wantFail(n, k) {
						// Divergence: Workday's Report is success-only (see
						// TestReportOnDetailFailureDivergence).
						return
					}
					if len(rep.calls) != 1 {
						t.Fatalf("Report called %d times, want once: %v", len(rep.calls), rep.calls)
					}
					c := rep.calls[0]
					if c[0] != n || c[1] > c[0] {
						t.Errorf("report listed=%d gone=%d for n=%d", c[0], c[1], n)
					}
					if !wantFail(n, k) && c[1] != k {
						t.Errorf("report gone=%d, want %d", c[1], k)
					}
				})
			}
		}
	}
}

// Non-404 failures are never gone, on any adapter.
func TestNotGoneParity(t *testing.T) {
	for _, f := range all() {
		for name, rep := range map[string]srctest.Reply{
			"410":       {Status: 410, Body: []byte("{}")},
			"429":       {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
			"500":       {Status: 500, Body: []byte("{}")},
			"malformed": ok("{not json <html>"),
			"oversized": ok(strings.Repeat("x", srcutil.MaxBody+1)),
			"htmlpage":  ok("<html><body>404 Page Not Found</body></html>"),
		} {
			t.Run(f.name+"/"+name, func(t *testing.T) {
				s, b, r := f.build(t, 20, map[int]srctest.Reply{5: rep})
				ps, err := s.List(context.Background(), b)
				if err == nil {
					t.Fatalf("got %d postings, want the board to fail", len(ps))
				}
				if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
					t.Errorf("read as gone/not-found: %v", err)
				}
				r.mu.Lock()
				defer r.mu.Unlock()
				for _, c := range r.calls {
					if c[1] != 0 {
						t.Errorf("counted gone %v for a non-404", c)
					}
				}
			})
		}
	}
}

// Report on a failed board: how each adapter behaves when the board fails
// from a non-404 detail error. Workday reports only on success; the new
// adapters report on failure too. Logged, not asserted, since the contract
// of Workday's Report is the reference.
func TestReportOnDetailFailureDivergence(t *testing.T) {
	for _, f := range all() {
		s, b, r := f.build(t, 20, map[int]srctest.Reply{5: {Status: 500, Body: []byte("{}")}})
		if _, err := s.List(context.Background(), b); err == nil {
			t.Fatalf("%s: want error", f.name)
		}
		t.Logf("%s: Report calls after a 500 detail failure: %v", f.name, r.calls)
	}
}

// noWait stands in for the board's request spacing, so these tests do not wait in real time.
func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
