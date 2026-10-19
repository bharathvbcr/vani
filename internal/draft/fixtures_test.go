package draft

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	vposting "github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/ashby"
	"github.com/bharathvbcr/Vani/internal/sources/greenhouse"
	"github.com/bharathvbcr/Vani/internal/sources/lever"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// realPostings reads the real postings in the source fixtures through each
// source's own parser, keyed "Company | Title".
func realPostings(t *testing.T) map[string]string {
	t.Helper()
	read := func(name string) []byte {
		raw, err := os.ReadFile("../sources/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	const wdSite = "https://nvidia.wd5.myworkdayjobs.com/wday/cxs/nvidia/NVIDIAExternalCareerSite"
	type fixture struct {
		replies map[string]srctest.Reply
		src     func(*srctest.Doer) sources.Source
		board   sources.Board
	}
	ok := func(raw []byte) srctest.Reply { return srctest.Reply{Status: 200, Body: raw} }
	out := map[string]string{}
	for _, f := range []fixture{
		{map[string]srctest.Reply{"https://boards-api.greenhouse.io/v1/boards/gitlab/jobs?content=true": ok(read("greenhouse/testdata/board.json"))},
			func(d *srctest.Doer) sources.Source { return greenhouse.New(d) },
			sources.Board{ATS: vposting.Greenhouse, Token: "gitlab", Company: "GitLab"}},
		{map[string]srctest.Reply{"https://api.lever.co/v0/postings/spotify?mode=json": ok(read("lever/testdata/board.json"))},
			func(d *srctest.Doer) sources.Source { return lever.New(d) },
			sources.Board{ATS: vposting.Lever, Token: "spotify", Company: "Spotify"}},
		{map[string]srctest.Reply{"https://api.ashbyhq.com/posting-api/job-board/ashby?includeCompensation=true": ok(read("ashby/testdata/board.json"))},
			func(d *srctest.Doer) sources.Source { return ashby.New(d) },
			sources.Board{ATS: vposting.Ashby, Token: "ashby", Company: "Ashby"}},
		{map[string]srctest.Reply{
			wdSite + "/jobs": ok(read("workday/testdata/list.json")),
			wdSite + "/job/US-CA-Santa-Clara/NVIDIA-2027-Ignite-Internships--Hardware-Engineering_JR2027047": ok(read("workday/testdata/detail1.json")),
			wdSite + "/job/India-Bengaluru/CAD-Engineer_JR2014480-1":                                         ok(read("workday/testdata/detail2.json")),
		}, func(d *srctest.Doer) sources.Source { return workday.New(d) },
			sources.Board{ATS: vposting.Workday, Token: "nvidia/NVIDIAExternalCareerSite", Company: "NVIDIA", Host: "nvidia.wd5.myworkdayjobs.com"}},
	} {
		ps, err := f.src(srctest.New(f.replies)).List(context.Background(), f.board)
		if err != nil {
			t.Fatalf("%s: %v", f.board.Company, err)
		}
		for _, p := range ps {
			out[p.Company+" | "+p.Title] = p.Description
		}
	}
	return out
}

func anthropicPosting(t *testing.T) Job {
	t.Helper()
	raw, err := os.ReadFile("testdata/anthropic-4981828008.json")
	if err != nil {
		t.Fatal(err)
	}
	var j Job
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func headings(desc string) string {
	var hs []string
	for _, s := range sections(desc) {
		if s.heading != "" {
			hs = append(hs, s.kind+":"+s.heading)
		}
	}
	return strings.Join(hs, " | ")
}

// Every heading in the real postings is read, and no body line is taken for one.
func TestRealPostingsAreSectioned(t *testing.T) {
	want := map[string]string{
		"GitLab | Associate Renewals Manager":                             "role:Associate Renewals Manager Responsibilities | role:Associate Renewals Manager Requirements | drop:How GitLab Supports Full-Time Employees",
		"GitLab | Engineering Manager, Build":                             "role:Summary: | role:Key Responsibilities: | role:What you'll bring: | drop:How GitLab Supports Full-Time Employees",
		"Spotify | Client Partner, Emerging & Scaled":                     "role:What You'll Do | role:Who You Are | drop:Where You'll Be",
		"Spotify | Talent Relations Manager":                              "role:What You'll Do | role:Who You Are | drop:Where You'll Be",
		"Spotify | Associate – Audiobook Licensing & Author Partnerships": "role:What You'll Do | role:Who You Are | drop:Where You'll Be",
		"Ashby | Mid-Market Solutions Engineering Manager":                "company:WHY LEAD SE AT ASHBY? | role:WHAT YOU’LL DO | role:YOU COULD BE A GREAT FIT IF: | role:BONUS POINTS FOR | role:REASONS YOU SHOULDN’T APPLY",
		"Ashby | GTM Systems Analyst":                                     "role:THE ROLE | role:WHY THIS ROLE MATTERS NOW | role:WHAT YOU'LL DO | role:WHAT WE'RE LOOKING FOR | role:NICE TO HAVE | role:THIS ROLE MAY NOT BE A FIT IF: | drop:INTERVIEW PROCESS | company:ABOUT ASHBY | drop:BENEFITS",
		"NVIDIA | NVIDIA 2027 Ignite Internships: Hardware Engineering":   "",
		"NVIDIA | CAD Engineer":                                           "role:What you’ll be doing:",
	}
	got := realPostings(t)
	if len(got) != len(want) {
		t.Fatalf("read %d real postings, want %d: %v", len(got), len(want), keys(got))
	}
	for name, w := range want {
		desc, ok := got[name]
		if !ok {
			t.Errorf("no real posting %q", name)
			continue
		}
		if h := headings(desc); h != w {
			t.Errorf("%s:\n got  %s\n want %s", name, h, w)
		}
	}
	a := anthropicPosting(t)
	if h, w := headings(a.Description), "company:About Anthropic | role:About the role | role:Responsibilities | role:You may be a good fit if you | role:Strong candidates may also have | role:Representative projects | drop:The annual compensation range for this role is listed below. | drop:Annual Salary: | drop:Logistics | company:How we're different | company:Come work with us!"; h != w {
		t.Errorf("Anthropic:\n got  %s\n want %s", h, w)
	}
}

// anthropicKB is a fictional profile for the Anthropic posting: role matches,
// one mission-only match, and bullets that only the posting's boilerplate matches.
func anthropicKB() []Evidence {
	return []Evidence{
		{Title: "Software Engineer", Org: "Example Labs", Dates: "2023-02 to present", Bullets: []string{
			"Built a React and TypeScript dashboard that researchers use daily to launch, monitor and compare training runs",
			"Designed a labeling interface for collecting human feedback on model outputs, used by 30 annotators",
			"Managed the office move and benefits enrollment for a 40-person company",
		}},
		{Title: "Backend Engineer", Org: "Sample Analytics", Dates: "2020-06 to 2023-01", Bullets: []string{
			"Shipped a sandboxed Python execution service for running untrusted notebook code",
			"Wrote the compensation and salary band calculator used by the finance team",
		}},
		{Title: "Volunteer", Org: "Community Reading Group", Dates: "2022", Bullets: []string{
			"Organised a reading group on AI safety and interpretability papers",
		}},
	}
}

// TestAnthropicMotivationPrompt pins the before/after report: for "Why
// Anthropic?" the evidence is the three role-matching bullets, and the bullets
// only the posting's pay and logistics text matched are not sent. The reading
// group shares one word ("safety") with the role and none usable with the
// mission prose, so it is not sent either: evidence that connects only to the
// company's mission is a known limit of word matching. Set VANI_PROMPT_OUT to
// a file path to write the rendered prompt there.
func TestAnthropicMotivationPrompt(t *testing.T) {
	system, user := draftPrompt(t, anthropicKB(), "Software engineer roles building tools and infrastructure for ML researchers and engineers",
		Request{Question: "Why Anthropic?", Job: anthropicPosting(t)})
	trusted, _ := frame(t, user)
	for _, b := range []string{"React and TypeScript dashboard", "labeling interface for collecting human feedback", "sandboxed Python execution service"} {
		if !strings.Contains(trusted, b) {
			t.Errorf("evidence lacks %q", b)
		}
	}
	if strings.Contains(trusted, "reading group") {
		t.Error("the mission-only bullet was sent; update the limit noted above")
	}
	if strings.Contains(trusted, "office move") || strings.Contains(trusted, "salary band calculator") {
		t.Error("a bullet matched only by pay or logistics text was sent")
	}
	if out := os.Getenv("VANI_PROMPT_OUT"); out != "" {
		if err := os.WriteFile(out, []byte("=== SYSTEM ===\n"+system+"\n\n=== USER ===\n"+user), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The motivation prompt for every real posting stays framed and bounded, and
// sends no section the classifier dropped.
func TestRealPostingsMotivationPrompts(t *testing.T) {
	jobs := map[string]Job{"Anthropic | Software Engineer, Research Tools": anthropicPosting(t)}
	for name, desc := range realPostings(t) {
		company, title, _ := strings.Cut(name, " | ")
		jobs[name] = Job{Company: company, Title: title, Description: desc}
	}
	for name, j := range jobs {
		_, user := draftPrompt(t, motivationKB(), "Software roles building tools for researchers", Request{Question: "Why " + j.Company + "?", Job: j})
		trusted, inside := frame(t, user)
		if strings.Contains(trusted, j.Company) {
			t.Errorf("%s: the company name sits outside the frame", name)
		}
		if runes(inside) > MaxDescriptionRunes+1000 {
			t.Errorf("%s: frame holds %d runes", name, runes(inside))
		}
		for _, s := range sections(j.Description) {
			if s.kind == kindDrop && s.body != "" && strings.Contains(inside, s.body) {
				t.Errorf("%s: dropped section %q was sent", name, s.heading)
			}
		}
		hasCompany := strings.Contains(inside, "Posting, about the company:")
		if says := strings.Contains(trusted, "no section about the company"); says == hasCompany {
			t.Errorf("%s: company block %v but no-company note %v", name, hasCompany, says)
		}
	}
	_, user := draftPrompt(t, nil, "", Request{Question: "Why Anthropic?", Job: anthropicPosting(t)})
	_, inside := frame(t, user)
	for _, s := range []string{"Visa sponsorship", "$300,000", "Your safety matters", "On Target Earnings"} {
		if strings.Contains(inside, s) {
			t.Errorf("Anthropic: %q was sent", s)
		}
	}
	for _, s := range []string{"How we're different", "Representative projects", "experiment orchestration"} {
		if !strings.Contains(inside, s) {
			t.Errorf("Anthropic: %q is missing", s)
		}
	}
}
