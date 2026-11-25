package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestLooksLikeShell(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{
			name: "empty",
			doc:  "",
			want: true,
		},
		{
			name: "whitespace only",
			doc:  "  \n\t ",
			want: true,
		},
		{
			name: "empty react root with noscript",
			doc:  `<!DOCTYPE html><html><head><title>Careers at Example — Join Our Mission Today and Build the Future</title><script>var a="` + strings.Repeat("x", 500) + `";</script><style>body{margin:0}` + strings.Repeat(" ", 200) + `</style></head><body><div id="root">&nbsp;  </div><noscript>You need to enable JavaScript to run this app with over 120 characters of fallback instructions and links <a href="/help">help</a>.</noscript></body></html>`,
			want: true,
		},
		{
			name: "small text without links",
			doc:  `<html><body><div class="shell">Welcome to Acme Careers.</div></body></html>`,
			want: true,
		},
		{
			name: "skip link only shell",
			doc:  `<html><body><a href="#main">Skip</a><div id="app"></div></body></html>`,
			want: true,
		},
		{
			name: "loading placeholder with header links",
			doc:  `<html><body><header><a href="/about">About</a><a href="/blog">Blog</a></header><main><p>Loading jobs...</p></main></body></html>`,
			want: true,
		},
		{
			name: "bare loading word",
			doc:  `<html><body><nav><a href="/">Home</a></nav><div id="root">Loading</div></body></html>`,
			want: true,
		},
		{
			name: "javascript required placeholder",
			doc:  `<html><body><header><a href="/">Home</a></header><main><p>JavaScript is required to view open positions at this company.</p></main></body></html>`,
			want: true,
		},
		{
			name: "nav only shell over 85 percent without main or article",
			doc:  `<html><body><header><a href="/home">Company Home</a> <a href="/products">Products</a> <a href="/pricing">Pricing</a></header><nav><a href="/about">About Us</a> <a href="/team">Team</a> <a href="/blog">Engineering Blog</a> <a href="/press">Press Kit</a></nav><footer>Copyright Acme Inc All Rights Reserved</footer><div id="root">Openings</div></body></html>`,
			want: true,
		},
		{
			name: "svg title spinner does not count as visible text",
			doc:  `<html><body><div id="root"><svg><title>` + strings.Repeat("Spinner ", 30) + `</title></svg></div></body></html>`,
			want: true,
		},
		{
			name: "real listing with job links",
			doc:  `<html><body><a href="/jobs/eng-1">Staff Engineer</a><a href="/jobs/ops-2">Ops Lead</a></body></html>`,
			want: false,
		},
		{
			name: "real posting with nav and main",
			doc:  `<html><body><nav><a href="/home">Home</a> <a href="/about">About Us</a> <a href="/products">Products</a> <a href="/customers">Customers</a> <a href="/pricing">Pricing</a> <a href="/docs">Documentation</a> <a href="/blog">Engineering Blog</a> <a href="/contact">Contact Sales</a></nav><main><header><h1>Staff Engineer</h1></header><p>Build Go systems.</p></main></body></html>`,
			want: false,
		},
		{
			name: "real posting with div content and small nav",
			doc:  `<html><body><nav><a href="/home">Home</a></nav><div class="posting"><p>We are seeking a Staff Distributed Systems Engineer to design high-throughput storage engines in Go and Rust, mentor engineers, and operate multi-region clusters.</p></div></body></html>`,
			want: false,
		},
		{
			name: "real warehouse job mentioning loading trucks",
			doc:  `<html><body><nav><a href="/">Home</a></nav><main><h1>Warehouse Associate</h1><p>Responsible for loading trucks, unloading pallets, verifying bills of lading, and maintaining warehouse safety across shifts.</p></main></body></html>`,
			want: false,
		},
		{
			name: "unterminated comment and script hostile input",
			doc:  `<html><body><script>unclosed` + strings.Repeat("<", 10000),
			want: true,
		},
		{
			name: "long text without links",
			doc:  `<html><body><p>` + strings.Repeat("Responsibilities include designing scalable distributed services. ", 4) + `</p></body></html>`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeShell(tc.doc); got != tc.want {
				t.Errorf("looksLikeShell = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMCPLoadPageStaticJSONLDPaysNoExtraCalls(t *testing.T) {
	html := `<html><body>` +
		`<main><h1>Staff Systems Engineer</h1><p>` + strings.Repeat("Build reliable storage systems. ", 6) + `</p></main>` +
		`<script type="application/ld+json">{"@type":"JobPosting","title":"Staff Systems Engineer","url":"` + pageA + `"}</script>` +
		`</body></html>`
	b, log := fakeLP(t, map[string]fakePage{
		pageA: {
			HTML:           html,
			StructuredData: `{"jsonLd":["{\"@type\":\"JobPosting\",\"title\":\"ShouldNotBeCalled\"}"]}`,
			Extracted:      `{"title":"ShouldNotBeCalled"}`,
		},
	}, 5*time.Second)

	pd, err := b.LoadPage(context.Background(), pageA, LoadOptions{
		WantStructuredData: true,
		ExtractSchema:      `{"title":"h1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(pd.HTML) != html || len(pd.StructuredData) != 0 || len(pd.Extracted) != 0 {
		t.Fatalf("unexpected extra artifacts: %+v", pd)
	}
	want := []string{"goto " + pageA, "html"}
	if got := toolCalls(t, log); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestMCPLoadPageSPAShellSettlesAndScrolls(t *testing.T) {
	shell := `<html><body><div id="root">Loading jobs...</div></body></html>`
	hydrated := `<html><body><main><a href="/jobs/1">Job 1</a><a href="/jobs/2">Job 2</a></main></body></html>`
	scroll1 := `<html><body><main><a href="/jobs/2">Job 2</a><a href="/jobs/3">Job 3</a></main></body></html>`
	scroll2 := `<html><body><main><a href="/jobs/3">Job 3</a><a href="/jobs/4">Job 4</a></main></body></html>`

	b, log := fakeLP(t, map[string]fakePage{
		pageA: {
			HTML:        shell,
			HydrateHTML: hydrated,
			ScrollHTMLs: []string{scroll1, scroll2, scroll2}, // 3rd step repeats scroll2 -> stop early
		},
		pageB: {
			HTML:          shell,
			HydrateHTML:   shell,
			WaitStateFail: true, // fail-soft when waitForState errors
		},
	}, 10*time.Second)

	pd, err := b.LoadPage(context.Background(), pageA, LoadOptions{ScrollSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if string(pd.HTML) != hydrated {
		t.Fatalf("HTML = %q, want %q", pd.HTML, hydrated)
	}
	if len(pd.ExtraHTML) != 2 || string(pd.ExtraHTML[0]) != scroll1 || string(pd.ExtraHTML[1]) != scroll2 {
		t.Fatalf("ExtraHTML = %q", pd.ExtraHTML)
	}
	wantCalls := []string{
		"goto " + pageA,
		"html",
		"waitForState",
		"html",
		"scroll", "waitForState", "html",
		"scroll", "waitForState", "html",
		"scroll", "waitForState", "html",
	}
	if got := toolCalls(t, log); !slices.Equal(got, wantCalls) {
		t.Fatalf("calls = %q, want %q", got, wantCalls)
	}

	// Verify scroll y arguments are step * 1200.
	var ys []float64
	for _, ev := range events(t, log) {
		if ev.Ev == "req" && ev.Tool == "scroll" {
			var a map[string]float64
			_ = json.Unmarshal(ev.Arguments, &a)
			ys = append(ys, a["y"])
		}
	}
	if !slices.Equal(ys, []float64{1200, 2400, 3600}) {
		t.Fatalf("scroll y args = %v, want [1200 2400 3600]", ys)
	}

	// waitForState tool error is fail-soft.
	pdB, err := b.LoadPage(context.Background(), pageB, LoadOptions{})
	if err != nil || string(pdB.HTML) != shell {
		t.Fatalf("fail-soft waitForState: pd=%+v err=%v", pdB, err)
	}
}

func TestMCPLoadPageWaitSelectorAndScrollFallback(t *testing.T) {
	b, log := fakeLP(t, map[string]fakePage{
		pageA: {
			HTML:             `<html><body><main>` + strings.Repeat("Role content. ", 12) + `</main></body></html>`,
			WaitSelectorFail: true, // triggers single waitForState fallback
		},
		pageB: {
			HTML:        `<html><body><a href="/jobs/1">1</a></body></html>`,
			ScrollHTMLs: []string{`<html><body><a href="/jobs/2">2</a></body></html>`},
			ScrollRPC:   -32601, // unsupported tool stops scroll loop early without failing page
		},
	}, 10*time.Second)

	if _, err := b.LoadPage(context.Background(), pageA, LoadOptions{WaitSelector: ".job-list"}); err != nil {
		t.Fatal(err)
	}
	wantA := []string{"goto " + pageA, "waitForSelector", "waitForState", "html"}
	if got := toolCalls(t, log); !slices.Equal(got, wantA) {
		t.Fatalf("calls = %q, want %q", got, wantA)
	}

	// Scroll error stops early cleanly.
	pdB, err := b.LoadPage(context.Background(), pageB, LoadOptions{ScrollSteps: 3})
	if err != nil || len(pdB.ExtraHTML) != 0 {
		t.Fatalf("scroll unsupported: pd=%+v err=%v", pdB, err)
	}

	// ScrollSteps > MaxPageScrollSteps is capped.
	var ys []float64
	pages := map[string]fakePage{}
	var snaps []string
	for i := 1; i <= sources.MaxPageScrollSteps+3; i++ {
		snaps = append(snaps, fmt.Sprintf(`<html><body><a href="/jobs/%d">J%d</a></body></html>`, i, i))
	}
	pages[pageA] = fakePage{
		HTML:        `<html><body><a href="/jobs/0">J0</a></body></html>`,
		ScrollHTMLs: snaps,
	}
	bCap, logCap := fakeLP(t, pages, 10*time.Second)
	pdCap, err := bCap.LoadPage(context.Background(), pageA, LoadOptions{ScrollSteps: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdCap.ExtraHTML) != sources.MaxPageScrollSteps {
		t.Fatalf("ExtraHTML len = %d, want %d", len(pdCap.ExtraHTML), sources.MaxPageScrollSteps)
	}
	for _, ev := range events(t, logCap) {
		if ev.Ev == "req" && ev.Tool == "scroll" {
			var a map[string]float64
			_ = json.Unmarshal(ev.Arguments, &a)
			ys = append(ys, a["y"])
		}
	}
	if len(ys) != sources.MaxPageScrollSteps {
		t.Fatalf("scroll count = %d, want %d", len(ys), sources.MaxPageScrollSteps)
	}
}

func TestMCPLoadPageStructuredDataAndExtractOrder(t *testing.T) {
	body := `<html><body><main><h1>Platform Engineer</h1><p>` + strings.Repeat("Work on storage. ", 10) + `</p></main></body></html>`
	micro := `{"microdata":[{"type":["https://schema.org/JobPosting"],"properties":{"title":["Platform Engineer"],"url":["` + pageA + `"]}}]}`
	b, log := fakeLP(t, map[string]fakePage{
		// structuredData succeeds with a JobPosting -> extract is skipped.
		pageA: {
			HTML:           body,
			StructuredData: micro,
			Extracted:      `{"title":"ShouldNotRun"}`,
		},
		// structuredData unsupported (-32601) -> extract runs and ignores null.
		pageB: {
			HTML:              body,
			StructuredDataRPC: -32601,
			Extracted:         `{"title":"Platform Engineer","location":null,"company":"  Acme Corp  "}`,
		},
		// extract returns isError -> fail-soft, no error, Extracted nil.
		pageC: {
			HTML:               body,
			StructuredDataFail: true,
			ExtractFail:        true,
		},
	}, 10*time.Second)

	pdA, err := b.LoadPage(context.Background(), pageA, LoadOptions{
		WantStructuredData: true,
		ExtractSchema:      `{"title":"h1"}`,
	})
	if err != nil || len(pdA.StructuredData) == 0 || pdA.Extracted != nil {
		t.Fatalf("pageA: pd=%+v err=%v", pdA, err)
	}
	if got := toolCalls(t, log); !slices.Equal(got, []string{"goto " + pageA, "html", "structuredData"}) {
		t.Fatalf("pageA calls = %q", got)
	}

	pdB, err := b.LoadPage(context.Background(), pageB, LoadOptions{
		WantStructuredData: true,
		ExtractSchema:      `{"company":".co","location":".loc","title":"h1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pdB.Extracted["title"] != "Platform Engineer" || pdB.Extracted["company"] != "Acme Corp" {
		t.Fatalf("pageB Extracted = %v", pdB.Extracted)
	}
	if _, hasLoc := pdB.Extracted["location"]; hasLoc {
		t.Fatalf("null location should be omitted: %v", pdB.Extracted)
	}

	pdC, err := b.LoadPage(context.Background(), pageC, LoadOptions{
		WantStructuredData: true,
		ExtractSchema:      `{"title":"h1"}`,
	})
	if err != nil || pdC.StructuredData != nil || pdC.Extracted != nil {
		t.Fatalf("pageC fail-soft: pd=%+v err=%v", pdC, err)
	}
}

func TestMCPLoadPageSizeCapAndCrashRecovery(t *testing.T) {
	body := `<html><body><main><a href="/jobs/1">1</a>` + strings.Repeat(" text", 40) + `</main></body></html>`
	b, log := fakeLP(t, map[string]fakePage{
		pageA: {HTML: body, StructuredDataSize: MaxStdout + 1},
		pageB: {HTML: body, CrashOnTool: "structuredData"},
		pageC: {HTML: body, StructuredData: `{}`},
	}, 5*time.Second)

	if _, err := b.LoadPage(context.Background(), pageA, LoadOptions{WantStructuredData: true}); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("oversized structuredData: err = %v", err)
	}
	if n := starts(t, log); n != 1 {
		t.Fatalf("oversized reply should keep process: %d starts", n)
	}

	if _, err := b.LoadPage(context.Background(), pageB, LoadOptions{WantStructuredData: true}); !errors.Is(err, ErrBrowserGone) {
		t.Fatalf("tool crash: err = %v", err)
	}
	if _, err := b.LoadPage(context.Background(), pageC, LoadOptions{WantStructuredData: true}); err != nil {
		t.Fatalf("recovery after tool crash: %v", err)
	}
	if n := starts(t, log); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
}

func TestPacedLoadPagePreservesRobotsAndInterval(t *testing.T) {
	const interval = 60 * time.Millisecond
	b, _ := fakeLP(t, map[string]fakePage{
		pageA: {HTML: `<html><body><a href="/jobs/b">b</a></body></html>`},
		pageB: {HTML: `<html><body><main><h1>Role</h1></main></body></html>`},
	}, 5*time.Second)
	p := mustPaced(t, b, interval)
	t0 := time.Now()
	if _, err := p.LoadPage(context.Background(), pageA, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.LoadPage(context.Background(), pageB, LoadOptions{WantStructuredData: true}); err != nil {
		t.Fatal(err)
	}
	// robots.txt visit + pageA visit both wait interval before the next call.
	if took := time.Since(t0); took < 2*interval {
		t.Fatalf("two paced LoadPage calls took %s, want >= %s", took, 2*interval)
	}

	// Too-long Crawl-delay still fails closed on LoadPage.
	pBad := mustRobotsPaced(t, b, robotsReply(200, "User-agent: *\nCrawl-delay: 25\n"), 0)
	if _, err := pBad.LoadPage(context.Background(), pageA, LoadOptions{}); !errors.Is(err, ErrCrawlDelayTooLong) {
		t.Fatalf("expected ErrCrawlDelayTooLong, got %v", err)
	}

	// Plain Browser fallback inside Paced.LoadPage.
	plain := &fakeBrowser{pages: map[string]string{pageA: "<p>plain</p>"}}
	pPlain := mustPaced(t, plain, 0)
	pd, err := pPlain.LoadPage(context.Background(), pageA, LoadOptions{WantStructuredData: true})
	if err != nil || string(pd.HTML) != "<p>plain</p>" {
		t.Fatalf("plain fallback: pd=%+v err=%v", pd, err)
	}
}

func TestStructuredDataParsing(t *testing.T) {
	const u = "https://careers.example.com/jobs/sys-1"
	// Dynamic JSON-LD via structuredData.
	dyn := `{"jsonLd":["{\"@type\":\"JobPosting\",\"title\":\"Systems Eng\",\"description\":\"&lt;p&gt;Kernel&lt;/p&gt;\",\"url\":\"https://careers.example.com/jobs/sys-1/\",\"validThrough\":\"2027-01-01\"}"]}`
	jp, ok := findStructuredJobPosting(u, []byte(dyn), fixedNow)
	if !ok || jp.title != "Systems Eng" || jp.description != "Kernel" || jp.expired(fixedNow) {
		t.Fatalf("dynamic JSON-LD: ok=%v %+v", ok, jp)
	}

	// Microdata JobPosting with nested Organization, Place/PostalAddress, TELECOMMUTE, and sibling URL filtering.
	micro := `{
		"microdata":[
			{
				"type":["https://schema.org/JobPosting"],
				"properties":{
					"title":["Other Role"],
					"url":["https://careers.example.com/jobs/other-9"],
					"validThrough":["2020-01-01"]
				}
			},
			{
				"type":["https://schema.org/JobPosting"],
				"id":"` + u + `",
				"properties":{
					"title":["Staff Rust Engineer"],
					"description":["<p>Build <b>fast</b> engines.</p>"],
					"datePosted":["2026-10-10"],
					"validThrough":["2026-10-18"],
					"jobLocationType":["TELECOMMUTE"],
					"hiringOrganization":[{"type":["https://schema.org/Organization"],"properties":{"name":["Acme Labs"]}}],
					"jobLocation":[{"type":["https://schema.org/Place"],"properties":{"address":[{"type":["https://schema.org/PostalAddress"],"properties":{"addressLocality":["Austin"],"addressRegion":["TX"],"addressCountry":["US"]}}]}}]
				}
			}
		]
	}`
	jp, ok = findStructuredJobPosting(u, []byte(micro), fixedNow)
	if !ok {
		t.Fatal("microdata JobPosting not matched")
	}
	if jp.title != "Staff Rust Engineer" || jp.company != "Acme Labs" ||
		jp.location != "Austin, TX, US" || !jp.remote ||
		jp.description != "Build fast engines." || jp.expired(fixedNow) {
		t.Fatalf("microdata parsed: %+v", jp)
	}

	// Expired microdata posting reports expired.
	expiredMicro := `{"microdata":[{"type":["http://schema.org/JobPosting"],"properties":{"title":["Old"],"url":["` + u + `"],"validThrough":["2026-10-17"]}}]}`
	jp, ok = findStructuredJobPosting(u, []byte(expiredMicro), fixedNow)
	if !ok || !jp.expired(fixedNow) {
		t.Fatalf("expired microdata: ok=%v expired=%v", ok, jp.expired(fixedNow))
	}

	// Non-HTTP itemid (e.g. urn:uuid) is ignored as a URL ref, so a single bare JobPosting still matches.
	bareMicro := `{"microdata":[{"type":["https://schema.org/JobPosting"],"id":"urn:uuid:1234","properties":{"title":["Bare Micro"]}}]}`
	jp, ok = findStructuredJobPosting(u, []byte(bareMicro), fixedNow)
	if !ok || jp.title != "Bare Micro" {
		t.Fatalf("bare microdata with urn id: ok=%v %+v", ok, jp)
	}

	// Build extract schema omits empty fields and quotes deterministically.
	if got := buildExtractSchema(nil); got != "" {
		t.Fatalf("nil schema = %q", got)
	}
	if got := buildExtractSchema(&sources.PageExtract{}); got != "" {
		t.Fatalf("empty schema = %q", got)
	}
	gotSchema := buildExtractSchema(&sources.PageExtract{Title: " h1 ", Location: ".loc"})
	if gotSchema != `{"location":".loc","title":"h1"}` {
		t.Fatalf("schema = %s", gotSchema)
	}
}

type fakeRichBrowser struct {
	pages map[string]PageDoc
	errs  map[string]error
	calls []string
	opts  map[string]LoadOptions
	res   int
}

func (f *fakeRichBrowser) Reset() error { f.res++; return nil }
func (f *fakeRichBrowser) Load(ctx context.Context, rawURL string) ([]byte, error) {
	pd, err := f.LoadPage(ctx, rawURL, LoadOptions{})
	return pd.HTML, err
}
func (f *fakeRichBrowser) LoadPage(ctx context.Context, rawURL string, opts LoadOptions) (PageDoc, error) {
	f.calls = append(f.calls, rawURL)
	if f.opts == nil {
		f.opts = map[string]LoadOptions{}
	}
	f.opts[rawURL] = opts
	if err := ctx.Err(); err != nil {
		return PageDoc{}, err
	}
	if err := f.errs[rawURL]; err != nil {
		return PageDoc{}, err
	}
	pd, ok := f.pages[rawURL]
	if !ok {
		return PageDoc{}, fmt.Errorf("missing page %s", rawURL)
	}
	return pd, nil
}

func TestListOwnerPaginationLoopCrossHostAndOverflow(t *testing.T) {
	// 1. Happy path: 3 pages, Prev link back to page 1, Next link also matches LinkPattern (/jobs/).
	b := board()
	b.LinkPattern = `^https://careers\.example\.com/jobs/`
	b.NextPattern = `^https://careers\.example\.com/jobs/page/\d+$`
	r := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {HTML: []byte(listing("/jobs/a-1", "/jobs/page/2"))},
		"https://careers.example.com/jobs/page/2": {HTML: []byte(listing("/jobs/b-2", "/jobs", "/jobs/page/3"))},
		"https://careers.example.com/jobs/page/3": {HTML: []byte(listing("/jobs/a-1", "/jobs/c-3", "/jobs/page/2"))},
		"https://careers.example.com/jobs/a-1":    {HTML: []byte(ldPage("A", `{"@type":"JobPosting","title":"A"}`))},
		"https://careers.example.com/jobs/b-2":    {HTML: []byte(ldPage("B", `{"@type":"JobPosting","title":"B"}`))},
		"https://careers.example.com/jobs/c-3":    {HTML: []byte(ldPage("C", `{"@type":"JobPosting","title":"C"}`))},
	}}
	got, err := newSrc(r).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3: %+v", len(got), got)
	}
	if len(r.calls) != 6 { // 3 listing pages + 3 unique postings (page/2 and page/3 NOT fetched as postings)
		t.Fatalf("calls = %q", r.calls)
	}

	// 2. Pagination cycle: page1 -> page2 -> page1 finishes cleanly in 2 fetches.
	cycle := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {HTML: []byte(listing("/jobs/a-1", "/jobs/page/2"))},
		"https://careers.example.com/jobs/page/2": {HTML: []byte(listing("/jobs/b-2", "/jobs"))},
		"https://careers.example.com/jobs/a-1":    {HTML: []byte(ldPage("A", `{"@type":"JobPosting","title":"A"}`))},
		"https://careers.example.com/jobs/b-2":    {HTML: []byte(ldPage("B", `{"@type":"JobPosting","title":"B"}`))},
	}}
	gotCycle, err := newSrc(cycle).List(context.Background(), b)
	if err != nil || len(gotCycle) != 2 {
		t.Fatalf("cycle: got %+v, err %v", gotCycle, err)
	}

	// 3. Cross-host next_pattern link refused.
	cross := board()
	cross.NextPattern = `page=\d+`
	rCross := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {HTML: []byte(listing("/jobs/a-1", "https://evil.example.com/jobs?page=2"))},
	}}
	if _, err := newSrc(rCross).List(context.Background(), cross); err == nil || !strings.Contains(err.Error(), "leaves listing host") {
		t.Fatalf("cross-host next link accepted: %v", err)
	}

	// 4. Exactly MaxListingPages (10) pages succeeds; 11th unseen next page fails cleanly.
	pages10 := map[string]PageDoc{
		"https://careers.example.com/jobs/a-1": {HTML: []byte(ldPage("A", `{"@type":"JobPosting","title":"A"}`))},
	}
	for i := 1; i <= MaxListingPages; i++ {
		u := listURL
		if i > 1 {
			u = fmt.Sprintf("%s?page=%d", listURL, i)
		}
		if i < MaxListingPages {
			pages10[u] = PageDoc{HTML: []byte(listing("/jobs/a-1", fmt.Sprintf("/jobs?page=%d", i+1)))}
		} else {
			pages10[u] = PageDoc{HTML: []byte(listing("/jobs/a-1"))}
		}
	}
	bQuery := board()
	bQuery.NextPattern = `\?page=\d+$`
	if got10, err := newSrc(&fakeRichBrowser{pages: pages10}).List(context.Background(), bQuery); err != nil || len(got10) != 1 {
		t.Fatalf("10 pages failed: %+v, %v", got10, err)
	}

	// Page 10 now links to unseen page 11 -> error, never silent truncation.
	pages10[fmt.Sprintf("%s?page=%d", listURL, MaxListingPages)] = PageDoc{
		HTML: []byte(listing("/jobs/a-1", fmt.Sprintf("/jobs?page=%d", MaxListingPages+1))),
	}
	if _, err := newSrc(&fakeRichBrowser{pages: pages10}).List(context.Background(), bQuery); err == nil || !strings.Contains(err.Error(), "exceeds 10 pages") {
		t.Fatalf("overflow should fail cleanly, got %v", err)
	}
}

func TestListVirtualScrollLinkMergingAndCaps(t *testing.T) {
	b := board()
	b.ScrollSteps = 3
	r := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {
			HTML: []byte(listing("/jobs/a-1", "/jobs/b-2")),
			ExtraHTML: [][]byte{
				// Recycled DOM drops a-1, keeps b-2, reveals c-3.
				[]byte(listing("/jobs/b-2", "/jobs/c-3")),
				// Next scroll drops b-2, reveals d-4.
				[]byte(listing("/jobs/c-3", "/jobs/d-4")),
			},
		},
		"https://careers.example.com/jobs/a-1": {HTML: []byte(ldPage("A", `{"@type":"JobPosting","title":"A"}`))},
		"https://careers.example.com/jobs/b-2": {HTML: []byte(ldPage("B", `{"@type":"JobPosting","title":"B"}`))},
		"https://careers.example.com/jobs/c-3": {HTML: []byte(ldPage("C", `{"@type":"JobPosting","title":"C"}`))},
		"https://careers.example.com/jobs/d-4": {HTML: []byte(ldPage("D", `{"@type":"JobPosting","title":"D"}`))},
	}}
	got, err := newSrc(r).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d postings, want 4: %+v", len(got), got)
	}
	if r.opts[listURL].ScrollSteps != 3 {
		t.Errorf("listing ScrollSteps = %d, want 3", r.opts[listURL].ScrollSteps)
	}
	if r.opts["https://careers.example.com/jobs/a-1"].ScrollSteps != 0 {
		t.Errorf("posting ScrollSteps should be 0, got %d", r.opts["https://careers.example.com/jobs/a-1"].ScrollSteps)
	}

	// Merged links across ExtraHTML exceeding MaxLinks fail before fetching postings.
	var halfA, halfB []string
	for i := range 30 {
		halfA = append(halfA, "/jobs/a"+strconv.Itoa(i))
	}
	for i := range 25 {
		halfB = append(halfB, "/jobs/b"+strconv.Itoa(i))
	}
	over := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {
			HTML:      []byte(listing(halfA...)),
			ExtraHTML: [][]byte{[]byte(listing(halfB...))},
		},
	}}
	if _, err := newSrc(over).List(context.Background(), b); err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("merged over-cap should fail, got %v", err)
	}
	if len(over.calls) != 1 {
		t.Errorf("should not fetch postings when merged links exceed MaxLinks: %q", over.calls)
	}

	// Truncated ExtraHTML snapshot fails closed.
	trunc := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {
			HTML:      []byte(listing("/jobs/a-1")),
			ExtraHTML: [][]byte{[]byte(`<a href="/jobs/b-2">ok</a><!-- unterminated`)},
		},
	}}
	if _, err := newSrc(trunc).List(context.Background(), b); !errors.Is(err, ErrTruncatedDocument) {
		t.Fatalf("truncated ExtraHTML: err = %v", err)
	}
}

func TestListExtractionFallbackChainAndSanitization(t *testing.T) {
	b := board()
	b.WaitSelector = ".job-body"
	b.Extract = &sources.PageExtract{
		Title:       "h1.role",
		Description: ".jd",
		Location:    ".loc",
		Company:     ".co",
	}

	r := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {HTML: []byte(listing("/jobs/inline-1", "/jobs/micro-2", "/jobs/expired-3", "/jobs/css-4", "/jobs/partial-5"))},
		"https://careers.example.com/jobs/inline-1": {
			HTML:           []byte(ldPage("Page Title", `{"@type":"JobPosting","title":"Inline Title","description":"Inline JD"}`)),
			StructuredData: []byte(`{"microdata":[{"type":["https://schema.org/JobPosting"],"properties":{"title":["Wrong"]}}]}`),
			Extracted:      map[string]string{"title": "WrongCSS"},
		},
		"https://careers.example.com/jobs/micro-2": {
			HTML:           []byte(`<html><head><title>HTML Title</title></head><body><p>Nav noise</p></body></html>`),
			StructuredData: []byte(`{"microdata":[{"type":["https://schema.org/JobPosting"],"properties":{"title":["Micro Role"],"description":["Micro JD"],"jobLocation":[{"properties":{"address":[{"properties":{"addressLocality":["Seattle"],"addressCountry":["US"]}}]}}]}}]}`),
			Extracted:      map[string]string{"title": "WrongCSS"},
		},
		"https://careers.example.com/jobs/expired-3": {
			HTML:           []byte(`<html><head><title>Expired</title></head><body><p>Body</p></body></html>`),
			StructuredData: []byte(`{"microdata":[{"type":["https://schema.org/JobPosting"],"properties":{"title":["Old"],"validThrough":["2026-10-17"]}}]}`),
		},
		"https://careers.example.com/jobs/css-4": {
			HTML: []byte(`<html><head><title>HTML Fallback</title></head><body><nav>menu</nav><p>body text</p></body></html>`),
			Extracted: map[string]string{
				"title":       "  CSS\x00  Role\x1b  ",
				"description": "Build\n\n\n\nsystems\t\there.",
				"location":    "  Remote  ",
				"company":     "CSS Co",
			},
		},
		"https://careers.example.com/jobs/partial-5": {
			HTML:      []byte(`<html><head><title>Fallback Title</title></head><body><p>Body fallback description.</p></body></html>`),
			Extracted: map[string]string{"location": "NYC"}, // description missed -> falls back to body
		},
	}}

	got, err := newSrc(r).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 { // expired-3 omitted
		t.Fatalf("got %d postings, want 4: %+v", len(got), got)
	}
	if got[0].Title != "Inline Title" || got[0].Description != "Inline JD" {
		t.Errorf("inline-1: %+v", got[0])
	}
	if got[1].Title != "Micro Role" || got[1].Location != "Seattle, US" || got[1].Description != "Micro JD" {
		t.Errorf("micro-2: %+v", got[1])
	}
	if got[2].Title != "CSS Role" || got[2].Description != "Build\n\nsystems here." ||
		got[2].Location != "Remote" || got[2].Company != "CSS Co" {
		t.Errorf("css-4: %+v", got[2])
	}
	if got[3].Title != "Fallback Title" || got[3].Location != "NYC" || got[3].Description != "Body fallback description." {
		t.Errorf("partial-5: %+v", got[3])
	}

	// Verify WaitSelector, WantStructuredData, and ExtractSchema propagated to posting loads.
	postOpts := r.opts["https://careers.example.com/jobs/css-4"]
	if postOpts.WaitSelector != ".job-body" || !postOpts.WantStructuredData || !strings.Contains(postOpts.ExtractSchema, `"description":".jd"`) {
		t.Errorf("unexpected posting LoadOptions: %+v", postOpts)
	}
}

func TestListRejectsInvalidHardeningOptionsBeforeLoading(t *testing.T) {
	r := &fakeRichBrowser{pages: map[string]PageDoc{}}
	mod := func(fn func(*sources.Board)) sources.Board { b := board(); fn(&b); return b }
	for name, b := range map[string]sources.Board{
		"blank next_pattern":    mod(func(b *sources.Board) { b.NextPattern = "  " }),
		"bad next_pattern":      mod(func(b *sources.Board) { b.NextPattern = "(" }),
		"long next_pattern":     mod(func(b *sources.Board) { b.NextPattern = strings.Repeat("a", maxNextPatternLen+1) }),
		"negative scroll_steps": mod(func(b *sources.Board) { b.ScrollSteps = -1 }),
		"over max scroll_steps": mod(func(b *sources.Board) { b.ScrollSteps = sources.MaxPageScrollSteps + 1 }),
		"blank wait_selector":   mod(func(b *sources.Board) { b.WaitSelector = " \t " }),
		"long wait_selector":    mod(func(b *sources.Board) { b.WaitSelector = strings.Repeat("a", sources.MaxSelectorLen+1) }),
		"ctrl wait_selector":    mod(func(b *sources.Board) { b.WaitSelector = ".a\nb" }),
		"empty extract":         mod(func(b *sources.Board) { b.Extract = &sources.PageExtract{} }),
		"blank extract field":   mod(func(b *sources.Board) { b.Extract = &sources.PageExtract{Title: " "} }),
		"ctrl extract field":    mod(func(b *sources.Board) { b.Extract = &sources.PageExtract{Description: ".a\x00"} }),
		"oversized extract field": mod(func(b *sources.Board) {
			b.Extract = &sources.PageExtract{Company: strings.Repeat("x", sources.MaxSelectorLen+1)}
		}),
	} {
		if _, err := newSrc(r).List(context.Background(), b); err == nil {
			t.Errorf("%s: accepted invalid board", name)
		}
	}
	if len(r.calls) != 0 || r.res != 0 {
		t.Errorf("invalid boards touched browser: calls=%v resets=%d", r.calls, r.res)
	}
}

func TestListNonJSONLDBodyFallbackPrunesBoilerplate(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>Staff Backend Engineer — Example</title></head><body>` +
		`<header><a href="/home">Home</a><a href="/about">About</a><a href="/careers">Careers</a></header>` +
		`<nav><a href="/products">Products</a><a href="/pricing">Pricing</a><a href="/docs">Docs</a><a href="/blog">Blog</a></nav>` +
		`<div class="cookie-banner"><p>Accept all cookies</p><p>Manage cookie preferences</p></div>` +
		`<main>` +
		`<h1>Staff Backend Engineer</h1>` +
		`<p>Join our core platform team to build reliable distributed storage systems and low-latency ingestion services in Go and Rust across multi-region clusters.</p>` +
		`<h2>Requirements</h2>` +
		`<ul><li>7+ years building production systems software.</li><li>Strong experience with concurrency, storage engines, and observability.</li></ul>` +
		`</main>` +
		`<aside><a href="/jobs/other-1">Related Role 1</a><a href="/jobs/other-2">Related Role 2</a></aside>` +
		`<footer><p>Copyright Example Inc. All rights reserved. Privacy Policy Terms of Service.</p></footer>` +
		`</body></html>`

	r := &fakeRichBrowser{pages: map[string]PageDoc{
		listURL: {HTML: []byte(listing("/jobs/staff-backend-1"))},
		"https://careers.example.com/jobs/staff-backend-1": {HTML: []byte(html)},
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings, want 1: %+v", len(got), got)
	}
	desc := got[0].Description
	for _, want := range []string{
		"Staff Backend Engineer",
		"Join our core platform team to build reliable distributed storage systems",
		"Requirements",
		"7+ years building production systems software.",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q:\n%s", want, desc)
		}
	}
	for _, unwanted := range []string{
		"Pricing",
		"Accept all cookies",
		"Manage cookie preferences",
		"Related Role 1",
		"Privacy Policy",
	} {
		if strings.Contains(desc, unwanted) {
			t.Errorf("description leaked boilerplate %q:\n%s", unwanted, desc)
		}
	}
}
