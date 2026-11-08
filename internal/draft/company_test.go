package draft

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

func companyReq(company string) Request {
	return Request{
		Question: "Why do you want to work here?",
		Job:      Job{Company: company, Title: "Engineer", Description: "About the role\nOwn inference latency."},
		MaxWords: 80,
	}
}

func companyDrafter(t *testing.T, p *seqProvider) *Drafter {
	t.Helper()
	d, err := New(p, Config{Model: "m", Timeout: 2 * time.Second}, kb())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func messageText(m llm.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.(llm.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func TestCoverQuestionIsAMotivationDraftInsideTheWordBounds(t *testing.T) {
	if CoverWords < MinWords || CoverWords > MaxWords {
		t.Fatalf("CoverWords %d outside [%d, %d]", CoverWords, MinWords, MaxWords)
	}
	if !Motivation(CoverQuestion) {
		t.Fatal("CoverQuestion is not a motivation question")
	}
	if !strings.Contains(CoverQuestion, "no greeting") || !strings.Contains(CoverQuestion, "three short paragraphs") {
		t.Fatalf("cover question %q", CoverQuestion)
	}
}

func TestMotivationDraftNamesTheCompanyOrIsRefusedAfterOneRepair(t *testing.T) {
	named := "I want this role at Northwind because I cut LLM inference latency 40%."
	p := &seqProvider{replies: []reply{{text: named}}}
	res, err := companyDrafter(t, p).Draft(context.Background(), companyReq("Northwind"))
	if err != nil || res.Draft != named || res.CompanyCheck != CompanyNamed || res.Turns != 1 || len(p.reqs) != 1 {
		t.Fatalf("res %+v err %v turns %d", res, err, len(p.reqs))
	}

	p = &seqProvider{replies: []reply{
		{text: "I cut LLM inference latency 40%."},
		{text: named},
	}}
	res, err = companyDrafter(t, p).Draft(context.Background(), companyReq("Northwind"))
	if err != nil || res.Draft != named || !res.Repaired || res.Turns != 2 || res.CompanyCheck != CompanyNamed || len(p.reqs) != 2 {
		t.Fatalf("repair res %+v err %v calls %d", res, err, len(p.reqs))
	}
	repair := messageText(p.reqs[1].Messages[2])
	if strings.Contains(repair, "Northwind") {
		t.Fatalf("repair instruction quoted the company: %q", repair)
	}

	p = &seqProvider{replies: []reply{{text: "I cut LLM inference latency 40%."}}}
	res, err = companyDrafter(t, p).Draft(context.Background(), companyReq("Northwind"))
	if codeOf(err) != CodeNotSpecific || res.Draft != "" || len(p.reqs) != 2 {
		t.Fatalf("refusal res %+v err %v calls %d", res, err, len(p.reqs))
	}
}

func TestEmptyCompanySkipsTheCheckAndSaysSo(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: "I cut LLM inference latency 40%."}}}
	res, err := companyDrafter(t, p).Draft(context.Background(), companyReq("  "))
	if err != nil || res.CompanyCheck != CompanySkipped || res.Draft == "" || len(p.reqs) != 1 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(p.reqs))
	}
}

func TestANonMotivationDraftDoesNotRequireTheCompanyName(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: "I cut LLM inference latency 40%."}}}
	req := Request{Question: "Describe a project where you cut latency.", Job: Job{Company: "Northwind", Title: "Engineer", Description: "About the role\nOwn inference."}, MaxWords: 80}
	res, err := companyDrafter(t, p).Draft(context.Background(), req)
	if err != nil || res.CompanyCheck != "" || len(p.reqs) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestAnUngroundedMetricIsNotRepairedIntoAPass(t *testing.T) {
	p := &seqProvider{replies: []reply{
		{text: "I improved throughput 90% at Northwind."},
		{text: "I improved throughput 90% at Northwind."},
	}}
	res, err := companyDrafter(t, p).Draft(context.Background(), companyReq("Northwind"))
	if codeOf(err) != CodeUngrounded || res.Draft != "" || len(p.reqs) != 2 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(p.reqs))
	}
}

func TestCompanyRepairSpendsTheSameBudget(t *testing.T) {
	p := &seqProvider{replies: []reply{
		{text: "I cut LLM inference latency 40%."},
		{err: budget.ErrExhausted},
	}}
	res, err := companyDrafter(t, p).Draft(context.Background(), companyReq("Northwind"))
	if codeOf(err) != CodeBudget || res.Draft != "" {
		t.Fatalf("res %+v err %v", res, err)
	}
}
