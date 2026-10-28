package draft

import (
	"context"
	"strings"
	"testing"
)

// Claims about the seeker (a credential, years of experience) are grounded only
// by text the seeker owns: the evidence, the target summary and, for a motivation
// question, their notes. The posting, the question, the company and the title are
// shown to the model but describe the job, so they ground the figures a draft may
// echo ("a team of 40", "24/7") and its acronyms, and nothing about the seeker.

// draftWithKB runs one draft over a chosen evidence list and fails the test when
// the evidence was not the part of the turn under test (none was sent).
func draftWithKB(t *testing.T, reply string, req Request, cfg Config, kb []Evidence, wantEvidence bool) (Result, error) {
	t.Helper()
	cfg.Model = "m"
	d, err := New(&provider{text: reply}, cfg, kb)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Draft(context.Background(), req)
	if err == nil && wantEvidence && res.EvidenceItems == 0 {
		t.Fatalf("test assumption: no evidence was sent for %q", req.Question)
	}
	return res, err
}

func latencyKB(title, org, dates string, bullets ...string) []Evidence {
	return []Evidence{{Title: title, Org: org, Dates: dates, Bullets: bullets}}
}

// A posting describes the job: "PhD preferred" and "5 years of experience" are
// what the employer wants, not what the seeker has.
func TestPostingGroundsNothingAboutTheSeeker(t *testing.T) {
	req := latencyRequest()
	req.Job.Description = "About the role\nOwn LLM inference latency. A PhD is preferred; five years of experience; AWS certification.\n"
	for _, reply := range []string{
		"I hold a PhD.",
		"I have a Ph.D. in physics.",
		"I have five years of experience.",
		"I have 5 years of experience.",
		"I have 5+ years of experience.",
		"I am AWS Certified.",
		"I am certified.",
	} {
		res, err := draftReply(t, reply, req, Config{})
		de := ungroundedErr(t, err)
		if res.Draft != "" {
			t.Errorf("%q: a refused draft carried text", reply)
		}
		if msg := de.Error(); !strings.Contains(msg, "not in the evidence, the target text or the seeker's notes") || strings.Contains(msg, "the posting") {
			t.Errorf("%q: the refusal must name the seeker's own text, not the posting: %v", reply, msg)
		}
	}
	// A posting section that is never sent grounds nothing, as before.
	req.Job.Description = "About the role\nOwn LLM inference latency.\n\nBenefits\nA PhD stipend and AWS credits\n"
	for _, reply := range []string{"I hold a PhD.", "I used AWS."} {
		_, err := draftReply(t, reply, req, Config{})
		ungroundedErr(t, err)
	}
}

func TestQuestionGroundsNoClaimAboutTheSeeker(t *testing.T) {
	for _, tc := range []struct{ question, reply string }{
		{"Describe a project where you cut latency. Do you have a PhD?", "I have a PhD."},
		{"Describe a project where you cut latency. Do you hold any certification?", "I do hold a certification."},
		{"Describe a project where you cut latency. Do you have five years of experience?", "I have five years of experience."},
		{"Describe a project where you cut latency. Do you have a master's degree?", "I have a master's degree."},
	} {
		req := latencyRequest()
		req.Question = tc.question
		_, err := draftReply(t, tc.reply, req, Config{})
		ungroundedErr(t, err)
	}
}

func TestTitleAndCompanyGroundNoClaimAboutTheSeeker(t *testing.T) {
	req := latencyRequest()
	req.Job.Title = "PhD Research Intern"
	_, err := draftReply(t, "I hold a PhD.", req, Config{})
	ungroundedErr(t, err)

	req = latencyRequest()
	req.Job.Company = "Certified Payments"
	_, err = draftReply(t, "I am certified.", req, Config{})
	ungroundedErr(t, err)

	req = latencyRequest()
	req.Job.Title = "Senior Engineer (7 years experience)"
	_, err = draftReply(t, "I have 7 years of experience.", req, Config{})
	ungroundedErr(t, err)
}

// The seeker's own text grounds claims about the seeker.
func TestSeekerOwnedTextGroundsClaimsAboutTheSeeker(t *testing.T) {
	req := latencyRequest()
	for name, tc := range map[string]struct {
		kb    []Evidence
		cfg   Config
		reply string
	}{
		"a bullet that states years": {
			latencyKB("ML Engineer", "Acme", "2019 to 2024", "Led inference latency work for five years"),
			Config{}, "I have five years of experience cutting latency."},
		"digits in a bullet, spelled in the draft": {
			latencyKB("ML Engineer", "Acme", "2019 to 2024", "Led inference latency work for 5 years"),
			Config{}, "I have five years of experience."},
		"a hyphenated bullet": {
			latencyKB("ML Engineer", "Acme", "2019 to 2024", "Led a 3-year inference latency effort"),
			Config{}, "I led a 3 year latency effort."},
		"dates state years": {
			latencyKB("ML Engineer", "Acme", "2019 to 2024", "Cut inference latency"),
			Config{}, "From 2019 to 2024 I cut latency at Acme."},
		"a PhD in the evidence title": {
			latencyKB("PhD in physics", "State University", "2015", "Studied inference latency of detectors"),
			Config{}, "I hold a PhD in physics."},
		"a doctorate in a bullet": {
			latencyKB("Researcher", "Lab", "2018", "Earned a doctorate while cutting inference latency"),
			Config{}, "I hold a PhD."},
		// The target summary is the seeker's own text, though it names roles they want
		// and not a degree they hold. A summary that says "PhD-level roles" therefore
		// grounds "I hold a PhD": a residual the owner accepted, pinned here so a change
		// to it is a choice.
		"the target summary": {
			latencyKB("ML Engineer", "Acme", "2023", "Cut inference latency"),
			Config{TargetSummary: "PhD-level research roles"}, "I hold a PhD."},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := draftWithKB(t, tc.reply, req, tc.cfg, tc.kb, true); err != nil {
				t.Errorf("%q refused: %v", tc.reply, err)
			}
		})
	}
}

// The notes the seeker wrote ground a motivation answer, and only that one.
func TestMotivationNotesGroundClaimsAboutTheSeeker(t *testing.T) {
	notes := Config{MotivationNotes: "I hold a PhD and have ten years in ML"}
	motive := Request{Question: "Why Modal?", Job: latencyRequest().Job}
	if _, err := draftReply(t, "I hold a PhD and ten years of experience.", motive, notes); err != nil {
		t.Errorf("notes sent with a motivation question must ground it: %v", err)
	}
	for _, reply := range []string{"I hold a PhD.", "I have ten years of experience."} {
		_, err := draftReply(t, reply, latencyRequest(), notes)
		ungroundedErr(t, err)
	}
}

// Negation is not parsed: a draft that says it does not hold a credential is
// refused like one that says it does, when the seeker's own text lacks it. The
// owner accepted refusing over guessing at the sense of a sentence.
func TestNegatedCredentialIsStillRefused(t *testing.T) {
	req := latencyRequest()
	req.Question = "Describe a project where you cut latency. Do you have a PhD?"
	_, err := draftReply(t, "I do not have a PhD, but I have cut latency.", req, Config{})
	ungroundedErr(t, err)
}

// What a draft may echo from the posting stays grounded by it: counts of the
// job's own things, codes, and the tools it names.
func TestPostingStillGroundsWhatADraftMayEcho(t *testing.T) {
	req := latencyRequest()
	req.Job.Description = "About the role\nOwn LLM inference latency on a team of 40. SOC2 compliance, 24/7 on-call, Kubernetes and AWS.\n"
	for _, reply := range []string{
		"I would join a team of 40.",
		"I would help with SOC2.",
		"I can cover 24/7 on-call.",
		"I would work on LLM inference on AWS.",
	} {
		if _, err := draftReply(t, reply, req, Config{}); err != nil {
			t.Errorf("%q refused: %v", reply, err)
		}
	}
}

// A count of years is the seeker's to state, and the guard cannot tell the
// posting's own duration ("a 3-year roadmap") from a seeker's tenure, so a draft
// that echoes the posting's years is refused. The owner accepted that cost over
// letting "5 years of experience" in a posting ground the seeker's own claim;
// pinned so a change to it is a choice.
func TestPostingDurationInYearsIsRefusedAsAnEcho(t *testing.T) {
	req := latencyRequest()
	req.Job.Description = "About the role\nOwn LLM inference latency against a 3-year roadmap.\n"
	_, err := draftReply(t, "I would help deliver a 3 year roadmap.", req, Config{})
	ungroundedErr(t, err)
}

// The split in newGrounding itself.
func TestGroundingSplitsSeekerFromShown(t *testing.T) {
	both := func(shown, reply string) int { _, n := split(nil, []string{shown}).ungrounded(reply); return n }
	own := func(seeker, reply string) int { _, n := split([]string{seeker}, nil).ungrounded(reply); return n }
	for _, tc := range []struct{ text, reply string }{
		{"5 years of experience", "I have 5 years of experience."},
		{"5 years of experience", "I have five years of experience."},
		{"5+ years of experience", "I have 5 years of experience."},
		{"5 years of experience", "I have 5+ years of experience."},
		{"5 yrs", "I have 5 years."},
		{"5 years", "I have 5 yrs."},
		{"a 3-year plan", "a 3 year plan"},
		{"PhD preferred", "I hold a PhD."},
		{"a licensed engineer on staff", "I am licensed."},
		{"Google Cloud Certified preferred", "I am Google Cloud Certified."},
	} {
		if n := both(tc.text, tc.reply); n == 0 {
			t.Errorf("shown %q grounded %q", tc.text, tc.reply)
		}
		if n := own(tc.text, tc.reply); n != 0 {
			t.Errorf("seeker %q did not ground %q", tc.text, tc.reply)
		}
	}
	for _, tc := range []struct{ text, reply string }{
		{"a team of 40", "a team of 40"},
		{"on call 24/7", "on call 24/7"},
		{"SOC2 compliance", "SOC2 compliance"},
		{"40ms p99", "40 ms at p99"},
		{"12 engineers", "12 engineers"},
		{"Built on AWS and CUDA", "I used AWS and CUDA"},
		{"3 regions", "3 regions"},
		{"a 40% target", "a 40% target"},
	} {
		if n := both(tc.text, tc.reply); n != 0 {
			t.Errorf("shown %q did not ground %q", tc.text, tc.reply)
		}
	}
	// Only the word year is the seeker's: another word's count is the job's to state.
	if n := both("5 years", "a team of 5"); n != 0 {
		t.Error("a bare 5 is not a year count, and the posting's 5 grounds it")
	}
	if n := both("5 years", "5 days"); n == 0 {
		t.Error("5 days is not 5 years")
	}
}
