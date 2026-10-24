package draft

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func figureList(s string) string {
	var out []string
	for _, f := range figures(s) {
		out = append(out, f.String())
	}
	return strings.Join(out, " ")
}

// The figure reader sees through formatting and nothing more.
func TestFiguresNormalizeFormatting(t *testing.T) {
	for in, want := range map[string]string{
		"a 3.1x gain":              "3.1x",
		"a 3.1× gain":              "3.1x",
		"a 3.1✕ gain":              "3.1x",
		"1,000 users":              "1000",
		"1,000,000 users":          "1000000",
		"1 000 users":              "1000",
		"1,000 2,000":              "1000 2000",
		"42%.":                     "42%",
		"42 %":                     "42%",
		"42 percent more":          "42%",
		"42 per cent more":         "42%",
		"42％":                      "42%",
		"cut by 42, then 7":        "42 7",
		"1, 2, 3":                  "1 2 3",
		"2020,2021":                "2020 2021",
		"10x.":                     "10x",
		"3 x 4":                    "3 4",
		"2x4 lumber":               "2 4",
		"5xx errors":               "5",
		"v1.2.3 and 2.0":           "1.2.3 2.0",
		"H100 GPUs":                "100",
		"40ms p99":                 "40 99",
		"from 2023-01 to present":  "2023 01",
		"no figures at all":        "",
		"3.":                       "3",
		"percent 5":                "5",
		"12,34 is not grouped":     "12 34",
		"1,2345 is not grouped":    "1 2345",
		"٣ digits in other script": "٣",
	} {
		if got := figureList(in); got != want {
			t.Errorf("figures(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroundingHasMarkedAndBareFigures(t *testing.T) {
	g := newGrounding("cut latency 42% and 3.1x on v2.1 in 2023 for 1,000 users")
	for _, ok := range []string{"42%", "42", "3.1x", "3.1", "3.1×", "2.1", "2023", "1000", "1,000"} {
		if _, n := g.ungrounded("I did " + ok + "."); n != 0 {
			t.Errorf("%q should be grounded", ok)
		}
	}
	for _, bad := range []string{"43%", "4", "3.1%", "42x", "2.2", "20", "100", "10,000", "3.10"} {
		if _, n := g.ungrounded("I did " + bad + "."); n == 0 {
			t.Errorf("%q should not be grounded", bad)
		}
	}
	if _, n := newGrounding().ungrounded("no digits here"); n != 0 {
		t.Error("a draft without digits has nothing to ground")
	}
	if f, n := newGrounding().ungrounded("1 then 2 then 1"); n != 2 || f.String() != "1" {
		t.Errorf("first %v of %d distinct", f, n)
	}
}

func groundKB() []Evidence {
	return []Evidence{{Title: "ML Engineer", Org: "Acme Robotics", Dates: "2023-01 to present", Bullets: []string{
		"Cut LLM inference latency by 42% and shipped a 3.1x faster kernel serving 1,000 users on v2.1",
	}}}
}

func draftReply(t *testing.T, reply string, req Request, cfg Config) (Result, error) {
	t.Helper()
	cfg.Model = "m"
	d, err := New(&provider{text: reply}, cfg, groundKB())
	if err != nil {
		t.Fatal(err)
	}
	return d.Draft(context.Background(), req)
}

func latencyRequest() Request {
	return Request{Question: "Describe a project where you cut latency.", Job: Job{Company: "Modal", Title: "Inference Engineer II", Description: "Own LLM inference latency."}}
}

func ungroundedErr(t *testing.T, err error) *Error {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeUngrounded {
		t.Fatalf("err = %v, want code %s", err, CodeUngrounded)
	}
	return de
}

// A draft is refused when it holds a figure nothing the model saw holds, and
// accepted when every figure is there under another spelling.
func TestDraftRefusesUngroundedFigures(t *testing.T) {
	for _, reply := range []string{
		"I cut latency by 73% at Acme.",
		"I worked there for 5 years cutting latency.",
		"I cut latency 3.1% at Acme.",
		"I cut latency by 4% at Acme.", // 4 is not in 42
		"I served 10,000 users.",
		"I cut latency by 42% and then another 88%.",
	} {
		res, err := draftReply(t, reply, latencyRequest(), Config{})
		de := ungroundedErr(t, err)
		if res.Draft != "" {
			t.Errorf("%q: a refused draft carried text %q", reply, res.Draft)
		}
		if !strings.Contains(de.Error(), "not in the evidence") {
			t.Errorf("%q: error = %v", reply, de)
		}
	}
	_, err := draftReply(t, "I cut latency by 73% and 88%.", latencyRequest(), Config{})
	if msg := err.Error(); !strings.Contains(msg, `"73%"`) || !strings.Contains(msg, "1 more") {
		t.Errorf("the refusal names the first figure and counts the rest: %v", err)
	}
}

func TestDraftAcceptsFiguresInAnotherSpelling(t *testing.T) {
	for _, reply := range []string{
		"I cut latency by 42%, a 3.1× speedup.",
		"I cut latency by 42 percent with a 3.1x kernel.",
		"I served 1000 users on v2.1 starting in 2023.",
		"I served 1,000 users.",
		"Cut latency 42%.",
		"I cut latency, with no numbers at all.",
	} {
		if _, err := draftReply(t, reply, latencyRequest(), Config{}); err != nil {
			t.Errorf("%q refused: %v", reply, err)
		}
	}
}

// Every other thing the model was shown grounds a figure: the question, the
// company and title, the posting excerpt, the target summary.
func TestDraftGroundsAgainstWhatWasSent(t *testing.T) {
	req := latencyRequest()
	req.Question = "Describe a project where you cut latency in under 90 days."
	req.Job.Description = "About the role\nOwn LLM inference latency for 5000 GPUs.\n"
	for _, reply := range []string{
		"I did it in 90 days.",
		"The role is a level II one.",
		"I would work on 5000 GPUs.",
		"I want a 7x outcome.",
	} {
		_, err := draftReply(t, reply, req, Config{TargetSummary: "Roles with a 7x impact"})
		if err != nil {
			t.Errorf("%q refused: %v", reply, err)
		}
	}
}

// Text the draft never receives cannot ground it: a dropped pay section, and
// the motivation notes on a question that gets no motivation block.
func TestDraftDoesNotGroundAgainstUnsentText(t *testing.T) {
	req := latencyRequest()
	req.Job.Description = "About the role\nOwn LLM inference latency.\n\nBenefits\n$300,000 salary and 15 days off\n"
	for _, s := range sections(req.Job.Description) {
		if s.heading == "Benefits" && s.kind != kindDrop {
			t.Fatalf("test assumption: the Benefits section is %q, not dropped", s.kind)
		}
	}
	for _, reply := range []string{"I want the $300,000 salary.", "I want 15 days off."} {
		if _, err := draftReply(t, reply, req, Config{}); err == nil {
			t.Errorf("%q: a figure from a section never sent was accepted", reply)
		} else {
			ungroundedErr(t, err)
		}
	}
	_, err := draftReply(t, "I care about 7 things.", latencyRequest(), Config{MotivationNotes: "I care about 7 things"})
	ungroundedErr(t, err)
	// The same notes do ground a motivation answer, where they are sent.
	motive := Request{Question: "Why Modal?", Job: latencyRequest().Job}
	if _, err := draftReply(t, "I care about 7 things.", motive, Config{MotivationNotes: "I care about 7 things"}); err != nil {
		t.Errorf("notes sent with a motivation question must ground it: %v", err)
	}
}

// Only the evidence picked for this question counts: a figure in a bullet the
// model was not shown is not grounded.
func TestDraftDoesNotGroundAgainstEvidenceNotSent(t *testing.T) {
	kb := append(groundKB(), Evidence{Title: "Barista", Org: "Cafe", Dates: "2019", Bullets: []string{"Made 9000 coffees"}})
	d, err := New(&provider{text: "I made 9000 coffees."}, Config{Model: "m"}, kb)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Draft(context.Background(), latencyRequest())
	ungroundedErr(t, err)
}

func TestUngroundedMessageIsBounded(t *testing.T) {
	huge := strings.Repeat("9", 4000)
	_, err := draftReply(t, "I did "+huge+" things.", latencyRequest(), Config{})
	de := ungroundedErr(t, err)
	if len(de.Error()) > 400 || !strings.Contains(de.Error(), "...") {
		t.Errorf("an unbounded figure reached the message (%d bytes)", len(de.Error()))
	}
}

// FromResolved sends a bullet only under the entry a selection lists it for.
func TestFromResolvedKeepsOnlyEligibleBullets(t *testing.T) {
	r := kbsync.Resolved{Blocks: []kbsync.Block{
		{ID: "a", Title: "ML Engineer", Org: "Acme", Start: "2023-01", Bullets: []kbsync.BlockBullet{
			{RkbID: "ok", Canonical: "Eligible one"}, {RkbID: "old", Canonical: "Superseded wording"}}},
		{ID: "b", Title: "RA", Org: "Uni", Start: "2021-06", Bullets: []kbsync.BlockBullet{
			{RkbID: "ok", Canonical: "Eligible one"}, // a shared bullet, listed only under a
			{RkbID: "only-b", Canonical: "Under b"}}},
		{ID: "c", Title: "Nothing listed", Org: "X", Start: "2020", Bullets: []kbsync.BlockBullet{{RkbID: "zz", Canonical: "Not selected"}}},
	}}
	el := kbsync.Eligibility{
		Bullets:      map[string]bool{"ok": true, "only-b": true},
		EntryBullets: map[string][]string{"a": {"ok"}, "b": {"only-b"}},
	}
	ev := FromResolved(r, el)
	if len(ev) != 2 || fmt.Sprint(ev[0].Bullets) != "[Eligible one]" || fmt.Sprint(ev[1].Bullets) != "[Under b]" {
		t.Fatalf("evidence = %+v", ev)
	}
	if got := FromResolved(r, kbsync.Eligibility{}); len(got) != 0 {
		t.Errorf("a zero Eligibility allows nothing, got %+v", got)
	}
}
