package dice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// TestMain answers srcutil's retry waits at once (srcutil.RunWithoutWaits).
func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

const (
	posted   = "2026-11-20T10:00:00Z"
	modified = "2026-11-23T10:00:00Z"
)

// fixedNow is "now" for every source under test.
var fixedNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// guidOf is the guid of the i-th job of a fake search.
func guidOf(i int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", 0x1000+i, i) }

func guids(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, guidOf(i))
	}
	return out
}

// answer is one tool reply: structuredContent, a tool error, a captured reply
// (its JSON-RPC id rewritten), or a raw HTTP reply.
type answer struct {
	sc      any
	toolErr string
	capture string
	raw     *srctest.Reply
}

type toolCall struct {
	Name string
	Args map[string]any
}

// fake is Dice's MCP endpoint: initialize, the initialized notification, and
// tools/call for search_jobs (paging jobs[keyword] the way Dice does) and
// get_job_details.
type fake struct {
	t    *testing.T
	jobs map[string][]string
	// total and pages, when set, replace a search's metadata.total and
	// metadata.totalPages.
	total func(keyword string, n int) int
	pages func(keyword string, n int) int
	// edit changes a row before it is served.
	edit func(guid string, row map[string]any)
	// search and detail, when set, answer instead of the honest server;
	// ok=false falls through to it.
	search func(args map[string]any) (answer, bool)
	detail func(guid string) (answer, bool)
	// init, when set, answers initialize.
	init *srctest.Reply

	mu    sync.Mutex
	calls []toolCall
	posts int
}

func newFake(t *testing.T, jobs map[string][]string) *fake {
	return &fake{t: t, jobs: jobs}
}

func rowObject(guid string) map[string]any {
	return map[string]any{
		"id": strings.ReplaceAll(guid, "-", ""), "guid": guid, "title": "Machine Learning Engineer " + guid[:8],
		"summary": "A short plain-text summary.", "postedDate": posted, "modifiedDate": modified,
		"jobLocation":    map[string]any{"displayName": "Austin, Texas, USA"},
		"detailsPageUrl": "https://www.dice.com/job-detail/" + guid + "?utm_source=vani-test&utm_medium=mcp",
		"salary":         "$120,000 - $150,000", "companyName": "Example Corp", "employmentType": "Full-time",
		"workplaceTypes": []any{"On-Site"}, "isRemote": false, "willingToSponsor": false,
		"employerType": "Direct Hire", "easyApply": false, "score": 1.5,
	}
}

func detailObject(guid string) map[string]any {
	return map[string]any{
		"description": "<p>About role " + guid + "</p><ul><li>Build models</li></ul>",
		"skills":      []any{map[string]any{"name": "Python"}, map[string]any{"name": "aws"}},
	}
}

func (f *fake) searchAnswer(args map[string]any) answer {
	kw, _ := args["keyword"].(string)
	page := int(args["page_number"].(float64))
	per := int(args["jobs_per_page"].(float64))
	all := f.jobs[kw]
	total := len(all)
	if f.total != nil {
		total = f.total(kw, len(all))
	}
	pages := (total + per - 1) / per
	if f.pages != nil {
		pages = f.pages(kw, len(all))
	}
	lo := min((page-1)*per, len(all))
	hi := min(page*per, len(all))
	data := []any{}
	for _, g := range all[lo:hi] {
		row := rowObject(g)
		if f.edit != nil {
			f.edit(g, row)
		}
		data = append(data, row)
	}
	return answer{sc: map[string]any{
		"data":     data,
		"metadata": map[string]any{"page": page, "pageSize": per, "total": total, "totalPages": pages, "totalJobCount": 124940},
	}}
}

func sse(msg any) srctest.Reply {
	raw, _ := json.Marshal(msg)
	return srctest.Reply{Status: 200, Body: []byte("event: message\ndata: " + string(raw) + "\n\n"),
		Header: http.Header{"Content-Type": {"text/event-stream"}}}
}

// replay is a captured reply with its id set to id.
func (f *fake) replay(name string, id json.RawMessage) srctest.Reply {
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var msg map[string]any
			if err := json.Unmarshal([]byte(data), &msg); err != nil {
				f.t.Fatal(err)
			}
			msg["id"] = id
			return sse(msg)
		}
	}
	f.t.Fatalf("testdata/%s has no data line", name)
	return srctest.Reply{}
}

func (f *fake) reply(a answer, id json.RawMessage) srctest.Reply {
	switch {
	case a.raw != nil:
		return *a.raw
	case a.capture != "":
		return f.replay(a.capture, id)
	case a.toolErr != "":
		return sse(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": a.toolErr}}, "isError": true}})
	}
	return sse(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "(same as structuredContent)"}}, "structuredContent": a.sc, "isError": false}})
}

func (f *fake) Do(r srctest.Request) (srctest.Reply, bool) {
	if r.Method != http.MethodPost || r.URL != Endpoint {
		return srctest.Reply{}, false
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(r.Body, &msg); err != nil {
		return srctest.Reply{}, false
	}
	f.mu.Lock()
	f.posts++
	f.mu.Unlock()
	switch msg.Method {
	case "initialize":
		if f.init != nil {
			return *f.init, true
		}
		return f.replay("initialize.sse", msg.ID), true
	case "notifications/initialized":
		return srctest.Reply{Status: http.StatusAccepted}, true
	case "tools/call":
	default:
		return srctest.Reply{}, false
	}
	f.mu.Lock()
	f.calls = append(f.calls, toolCall{Name: msg.Params.Name, Args: msg.Params.Arguments})
	f.mu.Unlock()
	switch msg.Params.Name {
	case "search_jobs":
		if f.search != nil {
			if a, ok := f.search(msg.Params.Arguments); ok {
				return f.reply(a, msg.ID), true
			}
		}
		return f.reply(f.searchAnswer(msg.Params.Arguments), msg.ID), true
	case "get_job_details":
		guid, _ := msg.Params.Arguments["job_id"].(string)
		if f.detail != nil {
			if a, ok := f.detail(guid); ok {
				return f.reply(a, msg.ID), true
			}
		}
		return f.reply(answer{sc: detailObject(guid)}, msg.ID), true
	}
	return srctest.Reply{}, false
}

func (f *fake) doer() *srctest.Doer { return srctest.NewFunc(f.Do) }

// toolCalls is every tools/call seen, in order.
func (f *fake) toolCalls() []toolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolCall(nil), f.calls...)
}

func (f *fake) count(name string) int {
	n := 0
	for _, c := range f.toolCalls() {
		if c.Name == name {
			n++
		}
	}
	return n
}

// detailIDs is the job_id of every get_job_details call, in order.
func (f *fake) detailIDs() []string {
	var out []string
	for _, c := range f.toolCalls() {
		if c.Name == "get_job_details" {
			out = append(out, c.Args["job_id"].(string))
		}
	}
	return out
}

// pagesAsked is the page_number of every search_jobs call for keyword.
func (f *fake) pagesAsked(keyword string) []int {
	var out []int
	for _, c := range f.toolCalls() {
		if c.Name == "search_jobs" && c.Args["keyword"] == keyword {
			out = append(out, int(c.Args["page_number"].(float64)))
		}
	}
	return out
}

// src is a source over d with no waiting and a fixed clock. opt nil is New's
// (every row fetched); otherwise NewWith's.
func src(d *srctest.Doer, opt *Options) *source {
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

func diceBoard(terms ...string) sources.Board {
	b := sources.Board{ATS: posting.Dice, Token: Token, Company: "Dice"}
	if len(terms) > 0 {
		b.Search = terms[0]
		b.Searches = terms[1:]
	}
	return b
}

func key(t *testing.T, guid string) posting.Key {
	t.Helper()
	k, err := posting.DiceKey(Token, guid)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// storedPosting is what an earlier run stored for guid, at the listing's
// modifiedDate.
func storedPosting(t *testing.T, guid string) posting.Posting {
	t.Helper()
	mod, _ := time.Parse(time.RFC3339, modified)
	return posting.Posting{Key: key(t, guid), ATS: posting.Dice, Company: "Example Corp", Title: "Stored " + guid[:8],
		URL: jobPage + guid, ApplyURL: jobPage + guid, UpdatedAt: mod, Description: "stored text " + guid,
		SeenAt: fixedNow.Add(-24 * time.Hour)}
}

// notOnTurn is n guids, from start on, whose refresh turn is not today, so a
// stored one is reused rather than re-read.
// onTurn is n guids, from start on, whose refresh turn is today.
func onTurn(start, n int) []string {
	var out []string
	day := srcutil.Day(fixedNow)
	for i := start; len(out) < n; i++ {
		if g := guidOf(i); srcutil.RefreshTurn(g, day) {
			out = append(out, g)
		}
	}
	return out
}

func notOnTurn(start, n int) []string {
	var out []string
	day := srcutil.Day(fixedNow)
	for i := start; len(out) < n; i++ {
		if g := guidOf(i); !srcutil.RefreshTurn(g, day) {
			out = append(out, g)
		}
	}
	return out
}

// timeoutErr is a transport timeout (srcutil.Transient).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
