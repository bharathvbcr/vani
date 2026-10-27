package page

import (
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// advPage is already in posting.CanonicalURL form. Selection compares refs to
// this string after upgradeHTTP and CanonicalURL.
const advPage = "https://careers.example.com/jobs/abc"

const (
	advDead = "2020-01-01"
	advLive = "2027-06-01"
)

type jobWant struct {
	ok      bool
	title   string
	desc    string
	expired bool
}

func assertJob(t *testing.T, page string, blocks []string, want jobWant) {
	t.Helper()
	jp, ok := findJobPosting(page, blocks, fixedNow)
	if ok != want.ok {
		t.Fatalf("ok=%v, want %v; job %+v", ok, want.ok, jp)
	}
	if !want.ok {
		if jp != (jobPosting{}) {
			t.Fatalf("unselected page returned %+v, want the zero posting", jp)
		}
		return
	}
	if jp.title != want.title || jp.description != want.desc {
		t.Fatalf("title %q desc %q, want title %q desc %q", jp.title, jp.description, want.title, want.desc)
	}
	if jp.expired(fixedNow) != want.expired {
		t.Fatalf("expired=%v, want %v (validThrough %s)", jp.expired(fixedNow), want.expired, jp.validThrough.Format("2006-01-02T15:04:05Z07:00"))
	}
}

func jpObj(fields string) string {
	return `{"@type":"JobPosting",` + fields + `}`
}

func nestObjects(depth int, leaf string) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString(`{"a":`)
	}
	b.WriteString(leaf)
	for i := 0; i < depth; i++ {
		b.WriteByte('}')
	}
	return b.String()
}

// canonRef is the comparison findJobPosting claims to use: upgradeHTTP then
// posting.CanonicalURL. It fails the test when raw is not an absolute URL.
func canonRef(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	upgradeHTTP(u)
	c, err := posting.CanonicalURL(u.String())
	if err != nil {
		t.Fatalf("canon %q: %v", raw, err)
	}
	return c
}

func TestJSONLDAdversarial(t *testing.T) {
	if maxJSONLDDepth != 6 || maxJSONLDParseDepth != 64 {
		t.Fatalf("walk cap %d parse cap %d, contract is 6 and 64", maxJSONLDDepth, maxJSONLDParseDepth)
	}
	other := jpObj(`"title":"Sibling","description":"sib-body","url":"https://careers.example.com/jobs/other-9","validThrough":"` + advLive + `"`)
	deadMatch := jpObj(`"title":"Dead Match","description":"dead-body","url":"` + advPage + `","validThrough":"` + advDead + `"`)

	t.Run("malformed", func(t *testing.T) {
		good := jpObj(`"title":"Kept","description":"kept-body","url":"` + advPage + `"`)
		deepLeaf := jpObj(`"title":"Too Deep","description":"deep-body","url":"` + advPage + `"`)
		// One past the decoder cap. A value at depth 65 is rejected, so 70
		// nested objects never yield Too Deep, and the other block still can.
		deep := nestObjects(maxJSONLDParseDepth+6, deepLeaf)
		cases := []struct {
			name   string
			blocks []string
			want   jobWant
		}{
			{"truncated object", []string{`{"@type":"JobPosting","title":"Trunc"`}, jobWant{}},
			{"trailing second value", []string{good + ` ` + good}, jobWant{}},
			{"trailing comma", []string{`{"@type":"JobPosting","title":"Comma","url":"` + advPage + `",}`}, jobWant{}},
			{"empty", []string{""}, jobWant{}},
			{"whitespace", []string{"  \n\t"}, jobWant{}},
			{"nul", []string{"\x00" + good}, jobWant{}},
			{"bom", []string{"\uFEFF" + good}, jobWant{}},
			{"numeric key", []string{`{"@type":"JobPosting",1:"x","title":"NumKey","url":"` + advPage + `"}`}, jobWant{}},
			{"bare numeric key", []string{`{1:2}`}, jobWant{}},
			{"deep then kept", []string{deep, good}, jobWant{ok: true, title: "Kept", desc: "kept-body"}},
			{"kept then deep", []string{good, deep}, jobWant{ok: true, title: "Kept", desc: "kept-body"}},
			{"truncated then kept", []string{`{"@type":"JobPosting","title":"Trunc"`, good}, jobWant{ok: true, title: "Kept", desc: "kept-body"}},
			{"bom then kept", []string{"\uFEFF" + good, good}, jobWant{ok: true, title: "Kept", desc: "kept-body"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assertJob(t, advPage, tc.blocks, tc.want)
			})
		}
	})

	t.Run("duplicate keys", func(t *testing.T) {
		// Last key wins. The first @type or url must not be the one that matches.
		assertJob(t, advPage, []string{
			`{"@type":"JobPosting","title":"Overwritten","description":"type-body","url":"` + advPage + `","@type":"Organization"}`,
		}, jobWant{})
		assertJob(t, advPage, []string{
			`{"@type":"Organization","title":"Restored","description":"restored-body","url":"` + advPage + `","@type":"JobPosting"}`,
		}, jobWant{ok: true, title: "Restored", desc: "restored-body"})
		assertJob(t, advPage, []string{
			`{"@type":"JobPosting","title":"First URL","description":"first-body","url":"` + advPage + `","url":"https://careers.example.com/jobs/other-9"}`,
		}, jobWant{})
		assertJob(t, advPage, []string{
			`{"@type":"JobPosting","title":"Last URL","description":"last-body","url":"https://careers.example.com/jobs/other-9","url":"` + advPage + `"}`,
		}, jobWant{ok: true, title: "Last URL", desc: "last-body"})
	})

	t.Run("url types", func(t *testing.T) {
		// A non-string is not a ref. Alone, the node is the single bare posting.
		// Beside a node that names another url, it must not be selected — that
		// is the failure if a number, bool, null, object, or numeric array matches.
		for _, tc := range []struct{ name, raw string }{
			{"number", "1"},
			{"bool", "true"},
			{"null", "null"},
			{"object", `{"@id":"` + advPage + `"}`},
			{"number array", "[1,2,3]"},
			{"nested string array", `[["` + advPage + `"]]`},
		} {
			t.Run(tc.name+" alone is bare", func(t *testing.T) {
				assertJob(t, advPage, []string{
					jpObj(`"title":"Alone","description":"alone-body","url":` + tc.raw),
				}, jobWant{ok: true, title: "Alone", desc: "alone-body"})
			})
			t.Run(tc.name+" does not match", func(t *testing.T) {
				assertJob(t, advPage, []string{
					`[` + other + `,` + jpObj(`"title":"Nope","description":"nope-body","url":`+tc.raw) + `]`,
				}, jobWant{})
			})
		}
		padded := "  " + advPage + "\n"
		assertJob(t, advPage, []string{
			`[` + other + `,` + jpObj(`"title":"Padded","description":"pad-body","url":`+strconv.Quote(padded)) + `]`,
		}, jobWant{ok: true, title: "Padded", desc: "pad-body"})
		assertJob(t, advPage, []string{
			`[` + other + `,` + jpObj(`"title":"Mix","description":"mix-body","url":[1,`+strconv.Quote(advPage)+`,false]`) + `]`,
		}, jobWant{ok: true, title: "Mix", desc: "mix-body"})
		assertJob(t, advPage, []string{
			`[` + other + `,` + jpObj(`"title":"Mix First","description":"mix-first-body","url":[`+strconv.Quote(advPage)+`,1]`) + `]`,
		}, jobWant{ok: true, title: "Mix First", desc: "mix-first-body"})
		// A non-string sameAs does not cancel a string url that names this page.
		assertJob(t, advPage, []string{
			jpObj(`"title":"URL Wins","description":"url-body","url":"` + advPage + `","sameAs":1`),
		}, jobWant{ok: true, title: "URL Wins", desc: "url-body"})
	})

	t.Run("prefix and ports", func(t *testing.T) {
		if got := canonRef(t, advPage); got != advPage {
			t.Fatalf("page is not canonical: %s", got)
		}
		slashEnc := canonRef(t, "https://careers.example.com/jobs/abc%2fextra")
		slashDec := canonRef(t, "https://careers.example.com/jobs/abc/extra")
		if slashEnc == advPage || slashEnc == slashDec {
			t.Fatalf("%%2f collapsed distinct paths: encoded %q decoded %q page %q", slashEnc, slashDec, advPage)
		}
		dotEnc := canonRef(t, "https://careers.example.com/jobs/%2e%2e/abc")
		dotUp := canonRef(t, "https://careers.example.com/abc")
		if dotEnc == advPage || dotEnc == dotUp {
			t.Fatalf("%%2e%%2e collapsed distinct paths: %q vs %q page %q", dotEnc, dotUp, advPage)
		}
		dotTail := canonRef(t, "https://careers.example.com/jobs/abc/%2e%2e")
		if dotTail == advPage || dotTail == canonRef(t, "https://careers.example.com/jobs") {
			t.Fatalf("trailing %%2e%%2e collapsed: %q", dotTail)
		}
		no := []string{
			"https://careers.example.com/jobs/a",
			"https://careers.example.com/jobs/abc/extra",
			"https://careers.example.com/jobs/abc2",
			"https://careers.example.com/jobs/abc%2fextra",
			"https://careers.example.com/jobs/%2e%2e/abc",
			"https://careers.example.com/jobs/abc/%2e%2e",
			"https://careers.example.com:8443/jobs/abc",
			"http://careers.example.com:443/jobs/abc",
			"https://careers.example.com:443/jobs/abc",
			"https://other.example.com/jobs/abc",
		}
		for _, ref := range no {
			t.Run("no "+ref, func(t *testing.T) {
				assertJob(t, advPage, []string{
					jpObj(`"title":"No","description":"no-body","url":` + strconv.Quote(ref)),
				}, jobWant{})
			})
		}
		yes := []string{
			"https://user:pw@careers.example.com/jobs/abc",
			"http://careers.example.com:80/jobs/abc",
			"http://Careers.Example.com/jobs/abc/",
			"http://careers.example.com/jobs/abc#apply",
			"https://careers.example.com/jobs/abc%2f",
		}
		for _, ref := range yes {
			if canonRef(t, ref) != advPage {
				t.Fatalf("canon %q = %q, want %q", ref, canonRef(t, ref), advPage)
			}
			t.Run("yes "+ref, func(t *testing.T) {
				assertJob(t, advPage, []string{
					`[` + other + `,` + jpObj(`"title":"Yes","description":"yes-body","url":`+strconv.Quote(ref)) + `]`,
				}, jobWant{ok: true, title: "Yes", desc: "yes-body"})
			})
		}
	})

	t.Run("unicode query percent", func(t *testing.T) {
		uni := canonRef(t, "https://bücher.example/jobs/abc")
		puny := canonRef(t, "https://xn--bcher-kva.example/jobs/abc")
		if uni == puny {
			t.Fatalf("CanonicalURL folded punycode %q and unicode %q", puny, uni)
		}
		if uni != "https://b%C3%BCcher.example/jobs/abc" {
			t.Fatalf("unicode host canon %q", uni)
		}
		assertJob(t, uni, []string{
			jpObj(`"title":"Puny","description":"puny-body","url":"https://xn--bcher-kva.example/jobs/abc"`),
		}, jobWant{})
		assertJob(t, uni, []string{
			jpObj(`"title":"Unicode","description":"uni-body","url":"https://bücher.example/jobs/abc"`),
		}, jobWant{ok: true, title: "Unicode", desc: "uni-body"})

		// %63 is a different canonical path than "c": RawPath keeps the escape.
		pct := canonRef(t, "https://careers.example.com/jobs/ab%63")
		if pct == advPage {
			t.Fatalf("%%63 folded onto %s", advPage)
		}
		assertJob(t, advPage, []string{
			jpObj(`"title":"Pct","description":"pct-body","url":"https://careers.example.com/jobs/ab%63"`),
		}, jobWant{})

		assertJob(t, advPage, []string{
			jpObj(`"title":"UTM","description":"utm-body","url":"https://careers.example.com/jobs/abc?UTM_source=newsletter"`),
		}, jobWant{ok: true, title: "UTM", desc: "utm-body"})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Bare UTM","description":"bare-utm-body","url":"https://careers.example.com/jobs/abc?utm=1"`),
		}, jobWant{})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Both","description":"both-body","url":"https://careers.example.com/jobs/abc?UTM_source=newsletter&utm=1"`),
		}, jobWant{})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Ref","description":"ref-body","url":"https://careers.example.com/jobs/abc?utm_source=newsletter&ref=1"`),
		}, jobWant{})
	})

	t.Run("document order", func(t *testing.T) {
		live := jpObj(`"title":"Live Match","description":"live-body","url":"` + advPage + `","validThrough":"` + advLive + `"`)
		assertJob(t, advPage, []string{`[` + deadMatch + `,` + other + `,` + live + `]`}, jobWant{ok: true, title: "Live Match", desc: "live-body"})
		assertJob(t, advPage, []string{`[` + live + `,` + other + `,` + deadMatch + `]`}, jobWant{ok: true, title: "Live Match", desc: "live-body"})
		first := jpObj(`"title":"First Live","description":"first-body","url":"` + advPage + `"`)
		second := jpObj(`"title":"Second Live","description":"second-body","url":"` + advPage + `/"`)
		assertJob(t, advPage, []string{`[` + second + `,` + first + `]`}, jobWant{ok: true, title: "Second Live", desc: "second-body"})
		assertJob(t, advPage, []string{`[` + first + `,` + second + `]`}, jobWant{ok: true, title: "First Live", desc: "first-body"})

		// Child key is first. The parent object is still recorded before it is walked.
		parentDeadChildLive := `{"mainEntity":` + jpObj(`"title":"Child","description":"child-body","url":"`+advPage+`","validThrough":"`+advLive+`"`) +
			`,"@type":"JobPosting","title":"Parent","description":"parent-body","url":"` + advPage + `","validThrough":"` + advDead + `"}`
		assertJob(t, advPage, []string{parentDeadChildLive}, jobWant{ok: true, title: "Child", desc: "child-body"})
		parentLiveChildLive := `{"mainEntity":` + jpObj(`"title":"Child","description":"child-body","url":"`+advPage+`"`) +
			`,"@type":"JobPosting","title":"Parent","description":"parent-body","url":"` + advPage + `","validThrough":"` + advLive + `"}`
		assertJob(t, advPage, []string{parentLiveChildLive}, jobWant{ok: true, title: "Parent", desc: "parent-body"})
	})

	t.Run("context graph depth", func(t *testing.T) {
		// @context names this page; mainEntity names another. Context is not read,
		// and the other url must not supply the title either.
		ctx := `{"@context":` + jpObj(`"title":"Context","description":"ctx-body","url":"`+advPage+`"`) +
			`,"mainEntity":` + jpObj(`"title":"Other","description":"other-body","url":"https://careers.example.com/jobs/other-9"`) + `}`
		assertJob(t, advPage, []string{ctx}, jobWant{})
		ctxArr := `{"@context":[` + jpObj(`"title":"Context","description":"ctx-body","url":"`+advPage+`"`) +
			`],"mainEntity":` + jpObj(`"title":"Main","description":"main-body","url":"`+advPage+`"`) + `}`
		assertJob(t, advPage, []string{ctxArr}, jobWant{ok: true, title: "Main", desc: "main-body"})
		graph := `{"@type":"WebPage","mainEntity":{"@graph":[{"@type":"Organization","name":"Acme"},` +
			jpObj(`"title":"Graph Inner","description":"graph-body","url":"`+advPage+`"`) + `]}}`
		assertJob(t, advPage, []string{graph}, jobWant{ok: true, title: "Graph Inner", desc: "graph-body"})

		// Depth: root 0, mainEntity 1, items array 2, element 3, g 4, @graph array 5, posting 6.
		atCap := `{"mainEntity":{"items":[{"g":{"@graph":[` +
			jpObj(`"title":"At Cap","description":"cap-body","url":"`+advPage+`"`) + `]}}]}}`
		assertJob(t, advPage, []string{atCap}, jobWant{ok: true, title: "At Cap", desc: "cap-body"})
		// The same shape one object deeper: the posting is at depth 7 and unread.
		over := `{"mainEntity":{"items":[{"g":{"@graph":[{"inner":` +
			jpObj(`"title":"Over Cap","description":"over-body","url":"`+advPage+`"`) + `}]}}]}}`
		assertJob(t, advPage, []string{over}, jobWant{})
	})

	t.Run("bare nodes", func(t *testing.T) {
		assertJob(t, advPage, []string{
			jpObj(`"title":"Only Bare","description":"bare-body"`),
		}, jobWant{ok: true, title: "Only Bare", desc: "bare-body"})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Expired Bare","description":"expired-bare","validThrough":"` + advDead + `"`),
		}, jobWant{ok: true, title: "Expired Bare", desc: "expired-bare", expired: true})
		assertJob(t, advPage, []string{
			`[` + jpObj(`"title":"One","description":"one-body"`) + `,` + jpObj(`"title":"Two","description":"two-body"`) + `]`,
		}, jobWant{})
		assertJob(t, advPage, []string{
			`[` + jpObj(`"title":"Bare","description":"bare-body"`) + `,` + jpObj(`"title":"Other","description":"other-body","url":"https://careers.example.com/jobs/other-9"`) + `]`,
		}, jobWant{})
	})

	t.Run("sameAs and spaces", func(t *testing.T) {
		assertJob(t, advPage, []string{
			jpObj(`"title":"Same","description":"same-body","url":"https://example.org/no","sameAs":"` + advPage + `"`),
		}, jobWant{ok: true, title: "Same", desc: "same-body"})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Empty URL","description":"empty-body","url":"","sameAs":"` + advPage + `"`),
		}, jobWant{ok: true, title: "Empty URL", desc: "empty-body"})
		// Spaces are not a ref, so the only such node is the bare posting.
		assertJob(t, advPage, []string{
			jpObj(`"title":"Spaces","description":"space-body","url":"   "`),
		}, jobWant{ok: true, title: "Spaces", desc: "space-body"})
		// Two nodes and a spaces url: not a match, and not the single bare node.
		assertJob(t, advPage, []string{
			`[` + jpObj(`"title":"Spaces","description":"space-body","url":"   "`) + `,` + jpObj(`"title":"Also","description":"also-body"`) + `]`,
		}, jobWant{})
		assertJob(t, advPage, []string{
			jpObj(`"title":"Spaces Same","description":"spaces-same","url":"   ","sameAs":"  ` + advPage + `  "`),
		}, jobWant{ok: true, title: "Spaces Same", desc: "spaces-same"})
	})

	t.Run("empty page url", func(t *testing.T) {
		assertJob(t, "", []string{
			jpObj(`"title":"Named","description":"named-body","url":"` + advPage + `"`),
		}, jobWant{})
		assertJob(t, "", []string{
			jpObj(`"title":"Bare","description":"bare-body"`),
		}, jobWant{ok: true, title: "Bare", desc: "bare-body"})
	})

	t.Run("every match expired", func(t *testing.T) {
		laterDead := jpObj(`"title":"Later Dead","description":"later-body","url":"` + advPage + `","validThrough":"` + advDead + `"`)
		assertJob(t, advPage, []string{`[` + deadMatch + `,` + other + `,` + laterDead + `]`}, jobWant{
			ok: true, title: "Dead Match", desc: "dead-body", expired: true,
		})
	})
}

func TestJSONLDAdversarialProperty(t *testing.T) {
	const page = "https://jobs.example/roles/abc"
	if c, err := posting.CanonicalURL(page); err != nil || c != page {
		t.Fatalf("property page %q err %v", c, err)
	}
	rng := rand.New(rand.NewSource(20261026))
	const iters = 200
	for i := 0; i < iters; i++ {
		g := &ldGen{rng: rng, page: page, id: i, budget: 40}
		raw := g.value(0, false)
		complete := true
		switch rng.Intn(5) {
		case 0:
			if len(raw) > 0 {
				raw = raw[:rng.Intn(len(raw))]
			}
			complete = false
		case 1:
			raw += "\x00"
			complete = false
		}
		blocks := []string{raw}
		if rng.Intn(2) == 0 {
			blocks = []string{`{"@type":"JobPosting"`, raw}
		}
		if complete && len(raw) > 1 && rng.Intn(2) == 0 {
			blocks = append(blocks, raw[:len(raw)/2])
		}
		jp, ok := findJobPosting(page, blocks, fixedNow)
		if !complete {
			if ok && !titleFrom(g.nodes, jp) && (jp.title != "" || jp.description != "") {
				t.Fatalf("iter %d truncated block produced unknown text %+v from %s", i, jp, clip(raw, 400))
			}
			continue
		}
		checkGenerated(t, i, page, g.nodes, raw, jp, ok)
	}
}

func titleFrom(nodes []placed, jp jobPosting) bool {
	for _, n := range nodes {
		if n.title == jp.title && n.desc == jp.description {
			return true
		}
	}
	return jp.title == "" && jp.description == ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type placed struct {
	title string
	desc  string
	vt    string
	refs  []string
	depth int
	hide  bool
}

func checkGenerated(t *testing.T, iter int, page string, nodes []placed, raw string, jp jobPosting, ok bool) {
	t.Helper()
	var matching, bare []placed
	others := 0
	for _, n := range nodes {
		if n.hide || n.depth > maxJSONLDDepth {
			continue
		}
		if len(n.refs) == 0 {
			bare = append(bare, n)
			continue
		}
		if refsNamePage(page, n.refs) {
			matching = append(matching, n)
			continue
		}
		others++
	}
	show := clip(raw, 400)
	if len(matching) > 0 {
		firstLive := -1
		for i, n := range matching {
			if !vtExpired(n.vt) {
				firstLive = i
				break
			}
		}
		if firstLive >= 0 {
			want := matching[firstLive]
			if !ok || jp.expired(fixedNow) || jp.title != want.title || jp.description != want.desc {
				t.Fatalf("iter %d live match: got ok=%v %+v, want %q %q\n%s", iter, ok, jp, want.title, want.desc, show)
			}
			return
		}
		want := matching[0]
		if !ok || !jp.expired(fixedNow) || jp.title != want.title || jp.description != want.desc {
			t.Fatalf("iter %d all matches expired: got ok=%v %+v, want expired %q\n%s", iter, ok, jp, want.title, show)
		}
		return
	}
	if len(bare) == 1 && others == 0 {
		want := bare[0]
		if !ok || jp.title != want.title || jp.description != want.desc || jp.expired(fixedNow) != vtExpired(want.vt) {
			t.Fatalf("iter %d single bare: got ok=%v %+v, want %q expired %v\n%s", iter, ok, jp, want.title, vtExpired(want.vt), show)
		}
		return
	}
	if ok {
		t.Fatalf("iter %d selected %+v with no page match (bare %d others %d)\n%s", iter, jp, len(bare), others, show)
	}
}

func refsNamePage(page string, refs []string) bool {
	for _, ref := range refs {
		if page != "" && samePage(page, ref) {
			return true
		}
	}
	return false
}

func vtExpired(vt string) bool {
	tm, dateOnly := parseDate(vt)
	if dateOnly {
		tm = tm.AddDate(0, 0, 1)
	}
	return (jobPosting{validThrough: tm}).expired(fixedNow)
}

// ldGen builds one JSON value and records JobPostings in walk order.
// Strings stay under 200 bytes. Nesting stops at depth 8.
type ldGen struct {
	rng    *rand.Rand
	page   string
	id     int
	seq    int
	budget int
	nodes  []placed
}

func (g *ldGen) value(depth int, hide bool) string {
	if depth >= 8 || g.budget <= 0 {
		return g.scalar()
	}
	g.budget--
	switch g.rng.Intn(5) {
	case 0:
		return g.scalar()
	case 1:
		return g.array(depth, hide)
	default:
		return g.object(depth, hide)
	}
}

func (g *ldGen) scalar() string {
	switch g.rng.Intn(5) {
	case 0:
		return "null"
	case 1:
		return "true"
	case 2:
		return strconv.Itoa(g.rng.Intn(50))
	case 3:
		n := 1 + g.rng.Intn(len(g.page))
		return strconv.Quote(capBytes(g.page[:n], 200))
	default:
		return strconv.Quote("x")
	}
}

func (g *ldGen) array(depth int, hide bool) string {
	n := g.rng.Intn(3)
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = g.value(depth+1, hide)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (g *ldGen) object(depth int, hide bool) string {
	type kv struct{ k, v string }
	var fields []kv
	makeJP := g.rng.Intn(3) != 0
	var rec placed
	if makeJP {
		rec.title = fmt.Sprintf("T%d-%d", g.id, g.seq)
		rec.desc = fmt.Sprintf("D%d-%d", g.id, g.seq)
		g.seq++
		rec.depth = depth
		rec.hide = hide
		isJP := true
		fields = append(fields, kv{"@type", strconv.Quote("JobPosting")})
		if g.rng.Intn(5) == 0 {
			second := "Organization"
			if g.rng.Intn(2) == 0 {
				second = "JobPosting"
			}
			fields = append(fields, kv{"@type", strconv.Quote(second)})
			isJP = second == "JobPosting"
		}
		fields = append(fields, kv{"title", strconv.Quote(rec.title)}, kv{"description", strconv.Quote(rec.desc)})
		if g.rng.Intn(4) != 0 {
			raw, refs := g.ref()
			fields = append(fields, kv{"url", raw})
			kept := refs
			if g.rng.Intn(4) == 0 {
				raw, refs = g.ref()
				fields = append(fields, kv{"url", raw})
				kept = refs
			}
			rec.refs = append(rec.refs, kept...)
		}
		if g.rng.Intn(3) == 0 {
			raw, refs := g.ref()
			fields = append(fields, kv{"sameAs", raw})
			rec.refs = append(rec.refs, refs...)
		}
		switch g.rng.Intn(3) {
		case 0:
			rec.vt = advDead
			fields = append(fields, kv{"validThrough", strconv.Quote(rec.vt)})
		case 1:
			rec.vt = advLive
			fields = append(fields, kv{"validThrough", strconv.Quote(rec.vt)})
		}
		if isJP {
			// Record before nested values so the order matches the walk.
			g.nodes = append(g.nodes, rec)
		}
	} else if g.rng.Intn(2) == 0 {
		fields = append(fields, kv{"@type", strconv.Quote("WebPage")})
	}
	if depth < 7 && g.rng.Intn(3) != 0 {
		keys := []string{"mainEntity", "@graph", "@context", "nest"}
		k := keys[g.rng.Intn(len(keys))]
		child := g.value(depth+1, hide || k == "@context")
		fields = append(fields, kv{k, child})
		if g.rng.Intn(4) == 0 {
			k2 := keys[g.rng.Intn(len(keys))]
			if k2 != k {
				child2 := g.value(depth+1, hide || k2 == "@context")
				fields = append(fields, kv{k2, child2})
			}
		}
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(f.k))
		b.WriteByte(':')
		b.WriteString(f.v)
	}
	b.WriteByte('}')
	return b.String()
}

func (g *ldGen) ref() (string, []string) {
	page := g.page
	switch g.rng.Intn(12) {
	case 0:
		return "1", nil
	case 1:
		return "true", nil
	case 2:
		return "null", nil
	case 3:
		return "{}", nil
	case 4:
		return "[1,2]", nil
	case 5:
		return "[" + strconv.Quote(page) + ",1]", []string{page}
	case 6:
		p := page[:1+g.rng.Intn(len(page))]
		if len(p) == len(page) && len(p) > 1 {
			p = p[:len(p)-1]
		}
		return strconv.Quote(capBytes(p, 200)), []string{p}
	case 7:
		p := capBytes(page+"/extra", 200)
		return strconv.Quote(p), []string{p}
	case 8:
		p := capBytes(page+"%2fextra", 200)
		return strconv.Quote(p), []string{p}
	case 9:
		p := capBytes(page+"%2e%2e", 200)
		return strconv.Quote(p), []string{p}
	case 10:
		return strconv.Quote("  " + page + "  "), []string{"  " + page + "  "}
	default:
		return strconv.Quote(page), []string{page}
	}
}

func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for s != "" && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func FuzzFindJobPosting(f *testing.F) {
	f.Add("")
	f.Add("{")
	f.Add(`{"@type":"JobPosting","title":"T","url":"https://jobs.example/roles/abc"}`)
	f.Add(`{"@graph":[{"@type":"JobPosting","title":"N"}]}`)
	f.Fuzz(func(t *testing.T, block string) {
		if len(block) > 200 {
			block = block[:200]
		}
		const page = "https://jobs.example/roles/abc"
		_, _ = findJobPosting(page, []string{block}, fixedNow)
		_, _ = findJobPosting("", []string{block, `{"@type":"JobPosting","title":"Bare"}`}, fixedNow)
	})
}
